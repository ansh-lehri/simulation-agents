package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"simulation-agents/core"
	"simulation-agents/store"
)

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
// SessionClosed-style teardown hooks (write a wind-down marker,
// drain the lease, etc) are the resilience layer's job; this
// Phase-5 loop only cares about frames and clean exit.
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
		runner.handleIncomingFrame(ctx, exchangeID, marketTimezoneName, frame)
	}
}

// handleIncomingFrame routes one decoded frame to the right market-
// data write. Logging-only on ack / connected / unknown — those
// carry no cacheable state. Per-frame write failures are logged
// and swallowed; the loop never fails a session for a single bad
// market-data frame.
func (runner *Runner) handleIncomingFrame(
	ctx context.Context,
	exchangeID string,
	marketTimezoneName string,
	frame core.IncomingFrame,
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
			if ackResult.Status != core.SubscriptionAckStatusOK {
				runner.dependencies.Logger.Warn(
					"session: subscribe ack returned error",
					"exchange_id", exchangeID,
					"stream", ackResult.Stream,
					"instrument", ackResult.Instrument,
					"status", ackResult.Status,
					"error", ackResult.Error,
				)
			}
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
