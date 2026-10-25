package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"simulation-agents/core"
	"simulation-agents/store"
)

const (
	// defaultSubscribeMaxRetries is used when Params.SubscribeMaxRetries is
	// zero. 12 attempts gives ~60 s of retry window at the default interval,
	// enough for the liquidity seeder to place its first orders and tape-learn
	// the instrument regardless of pre-open delay.
	defaultSubscribeMaxRetries = 12

	// defaultSubscribeRetryInterval is the wait between consecutive retries
	// when Params.SubscribeRetryInterval is zero.
	defaultSubscribeRetryInterval = 5 * time.Second

	// retryWriteBuffer is the depth of the channel from retry goroutines to
	// the sole-writer goroutine. Sized to hold one retry burst per instrument
	// without blocking the sleeping retry goroutines.
	retryWriteBuffer = 32
)

// subscribeRetryKey identifies one subscription slot for retry tracking.
type subscribeRetryKey struct {
	stream     string
	instrument string
	timeframe  string
}

// subscribeRetryState tracks how many times each subscription slot has been
// retried. Not goroutine-safe — only the ingest goroutine mutates it.
type subscribeRetryState struct {
	counts   map[subscribeRetryKey]int
	maxTries int
	interval time.Duration
}

func newSubscribeRetryState(maxRetries int, interval time.Duration) *subscribeRetryState {
	if maxRetries <= 0 {
		maxRetries = defaultSubscribeMaxRetries
	}
	if interval <= 0 {
		interval = defaultSubscribeRetryInterval
	}
	return &subscribeRetryState{
		counts:   make(map[subscribeRetryKey]int),
		maxTries: maxRetries,
		interval: interval,
	}
}

// runIngest is the long-lived WebSocket consumer. One goroutine per
// exchange. Its job is simple: block on ReadFrame, route each
// decoded frame to the right market-data write, and exit cleanly
// when ctx is cancelled or the socket closes.
//
// Error handling policy inside the loop: per-frame failures are
// logged and skipped — a bad snapshot does not stop a session, and
// retrying a frame that already came off the wire makes no sense.
// Only a terminal ReadFrame error (ctx done, socket EOF) exits the
// loop. The ingest closes the connection on exit so the socket is
// released even on the ctx-cancel path.
//
// Retry policy for "unknown instrument" acks: the exchange does not
// tape-learn an instrument until the first quote or trade arrives,
// which only happens after the first order. The liquidity seeder places
// its first orders at SessionStart; agents subscribe during pre-open,
// before that point. A dedicated sole-writer goroutine re-sends the
// subscribe frame for each failed slot after SubscribeRetryInterval,
// up to SubscribeMaxRetries times. On exhaustion the slot is abandoned
// and an error is logged — the session continues without live quotes
// for that instrument.
func (runner *Runner) runIngest(
	ctx context.Context,
	exchangeID string,
	marketTimezoneName string,
	connection WSConnection,
) {
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			runner.dependencies.Logger.Warn(
				"session: WebSocket close returned an error",
				"exchange_id", exchangeID,
				"close_err", closeErr,
			)
		}
	}()

	// Sole-writer goroutine. All writes to the socket — retry subscribe
	// frames — are funnelled here so no two goroutines write concurrently
	// (gorilla: one concurrent writer per connection). writeCh is never
	// closed; the goroutine exits on ctx.Done().
	writeCh := make(chan []core.MarketDataSubscription, retryWriteBuffer)
	go runner.runIngestWriter(ctx, exchangeID, connection, writeCh)

	retryState := newSubscribeRetryState(
		runner.dependencies.Params.SubscribeMaxRetries,
		runner.dependencies.Params.SubscribeRetryInterval,
	)

	for {
		frame, readErr := connection.ReadFrame(ctx)
		if readErr != nil {
			switch {
			case errors.Is(readErr, context.Canceled), errors.Is(readErr, context.DeadlineExceeded):
				runner.dependencies.Logger.Info(
					"session: ingest exiting on context cancellation",
					"exchange_id", exchangeID,
				)
			default:
				runner.dependencies.Logger.Info(
					"session: ingest exiting on socket read error",
					"exchange_id", exchangeID,
					"err", readErr,
				)
			}
			return
		}
		runner.handleIncomingFrame(ctx, exchangeID, marketTimezoneName, frame, writeCh, retryState)
	}
}

// runIngestWriter is the sole-writer goroutine for the ingest connection.
// It drains writeCh and calls WriteSubscribe for each batch. Exits when
// ctx is done or a write fails (the socket is in a bad state; the reader
// will see the error on the next ReadFrame and exit too).
func (runner *Runner) runIngestWriter(
	ctx context.Context,
	exchangeID string,
	connection WSConnection,
	writeCh <-chan []core.MarketDataSubscription,
) {
	for {
		select {
		case subs := <-writeCh:
			if err := connection.WriteSubscribe(subs); err != nil {
				runner.dependencies.Logger.Warn(
					"session: ingest retry write failed, writer exiting",
					"exchange_id", exchangeID,
					"err", err,
				)
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// handleIncomingFrame routes one decoded frame to the right market-
// data write. Logging-only on connected / unknown — those carry no
// cacheable state. Per-frame write failures are logged and swallowed;
// the loop never fails a session for a single bad market-data frame.
// Ack frames with "unknown instrument" errors schedule a retry via
// the retry state and the sole-writer channel.
func (runner *Runner) handleIncomingFrame(
	ctx context.Context,
	exchangeID string,
	marketTimezoneName string,
	frame core.IncomingFrame,
	writeCh chan<- []core.MarketDataSubscription,
	retryState *subscribeRetryState,
) {
	switch frame.Kind {
	case core.IncomingFrameKindSnapshot:
		if frame.Snapshot == nil {
			return
		}
		quote := convertSnapshotToStoreQuote(frame.Snapshot.Data, runner.dependencies.Clock.Now())
		if writeErr := runner.dependencies.MarketData.WriteQuote(
			ctx, exchangeID, frame.Snapshot.Instrument, quote,
		); writeErr != nil {
			runner.dependencies.Logger.Warn(
				"session: WriteQuote failed",
				"exchange_id", exchangeID,
				"instrument", frame.Snapshot.Instrument,
				"err", writeErr,
			)
		}
	case core.IncomingFrameKindCandle:
		if frame.Candle == nil {
			return
		}
		candle, convErr := convertStreamBarToStoreCandle(frame.Candle.Bar, marketTimezoneName)
		if convErr != nil {
			runner.dependencies.Logger.Warn(
				"session: candle frame conversion failed",
				"exchange_id", exchangeID,
				"instrument", frame.Candle.Instrument,
				"err", convErr,
			)
			return
		}
		if writeErr := runner.dependencies.MarketData.AppendCandle(
			ctx, exchangeID, frame.Candle.Instrument, frame.Candle.Timeframe, candle,
		); writeErr != nil {
			runner.dependencies.Logger.Warn(
				"session: AppendCandle failed",
				"exchange_id", exchangeID,
				"instrument", frame.Candle.Instrument,
				"timeframe", frame.Candle.Timeframe,
				"err", writeErr,
			)
		}
	case core.IncomingFrameKindAck:
		if frame.Ack == nil {
			return
		}
		for _, ackResult := range frame.Ack.Results {
			if ackResult.Status == core.SubscriptionAckStatusOK {
				runner.dependencies.Logger.Info(
					"session: subscription ack ok",
					"exchange_id", exchangeID,
					"stream", ackResult.Stream,
					"instrument", ackResult.Instrument,
					"timeframe", ackResult.Timeframe,
				)
				continue
			}
			if ackResult.Error != "unknown instrument" {
				runner.dependencies.Logger.Warn(
					"session: subscribe ack returned error",
					"exchange_id", exchangeID,
					"stream", ackResult.Stream,
					"instrument", ackResult.Instrument,
					"status", ackResult.Status,
					"error", ackResult.Error,
				)
				continue
			}
			// "unknown instrument": the instrument is not yet tape-learned.
			// Schedule a retry if we have attempts remaining.
			key := subscribeRetryKey{
				stream:     ackResult.Stream,
				instrument: ackResult.Instrument,
				timeframe:  ackResult.Timeframe,
			}
			retryState.counts[key]++
			attempt := retryState.counts[key]

			if attempt > retryState.maxTries {
				runner.dependencies.Logger.Error(
					"session: subscribe retry exhausted, giving up on instrument",
					"exchange_id", exchangeID,
					"stream", ackResult.Stream,
					"instrument", ackResult.Instrument,
					"timeframe", ackResult.Timeframe,
					"attempts", attempt,
					"max_retries", retryState.maxTries,
				)
				continue
			}

			runner.dependencies.Logger.Info(
				"session: subscription unknown instrument, scheduling retry",
				"exchange_id", exchangeID,
				"stream", ackResult.Stream,
				"instrument", ackResult.Instrument,
				"timeframe", ackResult.Timeframe,
				"attempt", attempt,
				"max_retries", retryState.maxTries,
				"retry_in", retryState.interval,
			)

			retrySub := core.MarketDataSubscription{
				Stream:     ackResult.Stream,
				Instrument: ackResult.Instrument,
				Timeframe:  ackResult.Timeframe,
			}
			retryInterval := retryState.interval
			go func(sub core.MarketDataSubscription) {
				timer := time.NewTimer(retryInterval)
				defer timer.Stop()
				select {
				case <-timer.C:
					select {
					case writeCh <- []core.MarketDataSubscription{sub}:
					case <-ctx.Done():
					}
				case <-ctx.Done():
				}
			}(retrySub)

		}
	case core.IncomingFrameKindConnected:
		// Core re-emits connected only on the initial handshake,
		// which happens inside Subscribe. A connected frame here
		// would be unusual but is harmless — log and continue.
		runner.dependencies.Logger.Info(
			"session: ingest observed unexpected connected frame",
			"exchange_id", exchangeID,
		)
	case core.IncomingFrameKindUnknown:
		// Forward-compat: Core may add new frame kinds. Log the
		// envelope so an operator sees it and ship a decoder later.
		runner.dependencies.Logger.Info(
			"session: ingest observed unknown frame kind",
			"exchange_id", exchangeID,
		)
	default:
		runner.dependencies.Logger.Info(
			"session: ingest ignoring frame with no recognised kind",
			"exchange_id", exchangeID,
			"kind", frame.Kind,
		)
	}
}

// convertSnapshotToStoreQuote collapses the whole-picture snapshot
// into the two-sided quote the cache stores. All gate flags (HasBid,
// HasAsk, HasLast) are forwarded from the source — a side with no
// resting liquidity is written with HasBid/HasAsk=false so accessors
// downstream correctly report no two-sided book. Depth, Phase, and
// LastPrice are copied unconditionally so every derived signal
// (Spread, Imbalance, DepthQty, Phase, CurrentPrice) reads live state.
func convertSnapshotToStoreQuote(
	snapshotData core.SnapshotFrameData,
	observedAt time.Time,
) store.Quote {
	quote := store.Quote{
		Timestamp: observedAt.UnixMilli(),
		Phase:     snapshotData.Phase,
		HasLast:   snapshotData.HasLast,
		LastPrice: snapshotData.LastPrice,
	}
	if snapshotData.Quote.HasBid {
		quote.HasBid = true
		quote.BidPrice = snapshotData.Quote.BestBid
		quote.BidQty = snapshotData.Quote.BidQty
	}
	if snapshotData.Quote.HasAsk {
		quote.HasAsk = true
		quote.AskPrice = snapshotData.Quote.BestAsk
		quote.AskQty = snapshotData.Quote.AskQty
	}
	if len(snapshotData.Depth.Bids) > 0 || len(snapshotData.Depth.Asks) > 0 {
		bids := make([]store.QuoteLevel, len(snapshotData.Depth.Bids))
		for depthIndex, depthLevel := range snapshotData.Depth.Bids {
			bids[depthIndex] = store.QuoteLevel{Price: depthLevel.Price, Qty: depthLevel.Qty}
		}
		asks := make([]store.QuoteLevel, len(snapshotData.Depth.Asks))
		for depthIndex, depthLevel := range snapshotData.Depth.Asks {
			asks[depthIndex] = store.QuoteLevel{Price: depthLevel.Price, Qty: depthLevel.Qty}
		}
		quote.Depth = store.QuoteDepth{Bids: bids, Asks: asks}
	}
	return quote
}

// convertStreamBarToStoreCandle maps one finalized stream bar into
// the store's unix-millis candle shape. The connection's declared
// timezone (from the ConnectedFrame on the initial handshake) is
// the only correct way to interpret the naive-local start/end
// strings; the caller threads that timezone in on every call.
func convertStreamBarToStoreCandle(
	streamBar core.MarketDataBar,
	marketTimezoneName string,
) (store.Candle, error) {
	if marketTimezoneName == "" {
		return store.Candle{}, fmt.Errorf("empty market timezone for stream bar conversion")
	}
	marketLocation, locErr := time.LoadLocation(marketTimezoneName)
	if locErr != nil {
		return store.Candle{}, fmt.Errorf("load timezone %q: %w", marketTimezoneName, locErr)
	}
	startInstant, startErr := time.ParseInLocation(coreNaiveLocalFormat, streamBar.StartNaiveLocal, marketLocation)
	if startErr != nil {
		return store.Candle{}, fmt.Errorf("parse start %q in %q: %w", streamBar.StartNaiveLocal, marketTimezoneName, startErr)
	}
	endInstant, endErr := time.ParseInLocation(coreNaiveLocalFormat, streamBar.EndNaiveLocal, marketLocation)
	if endErr != nil {
		return store.Candle{}, fmt.Errorf("parse end %q in %q: %w", streamBar.EndNaiveLocal, marketTimezoneName, endErr)
	}
	return store.Candle{
		OpenTime:  startInstant.UnixMilli(),
		Open:      streamBar.Open,
		High:      streamBar.High,
		Low:       streamBar.Low,
		Close:     streamBar.Close,
		Volume:    streamBar.Volume,
		CloseTime: endInstant.UnixMilli(),
	}, nil
}
