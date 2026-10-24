package session

import (
	"context"
	"fmt"

	"simulation-agents/core"
	"simulation-agents/transport"
)

// subscribeLive runs the pre-open live-feed subscribe op. It opens
// one WebSocket per exchange and sends a single batched subscribe
// frame that mixes stream kinds: one snapshot sub per instrument,
// plus one candle sub per (instrument × configured timeframe)
// where the timeframe list is the same per-bring-up dial the
// history pull uses. Core's live-feed server accepts the mixed
// batch on one WS and fans each sub out individually.
//
// Fatal on subscribe failure — a bring-up with no live feed cannot
// deliver market data to the tick loop. Per-sub ack errors are not
// fatal here: an unknown instrument may clear once it has printed
// on the tape, and the ingest loop will see the ack as a frame
// when it drains the socket.
//
// Two contexts, deliberately not the same one: joinCtx scopes only
// the synchronous Subscribe dial — it is the errgroup-derived context
// shared with the other two pre-open ops, and is cancelled the moment
// Run's preOpenGroup.Wait() returns (i.e. as soon as all three
// pre-open ops finish, usually under a second in). sessionCtx is
// Run's own long-lived context and is what the spawned ingest
// goroutine binds to, so ingest outlives the join and keeps draining
// the socket into Redis for the life of the session — only exiting
// when sessionCtx itself is cancelled (teardown, failure, or Run
// returning) or the connection errors. Binding ingest to joinCtx
// instead was the bug: it killed the live feed seconds into every
// session, right as the pre-open join completed. The connection is
// closed from inside ingest on exit so the pooled socket is released
// promptly.
func (runner *Runner) subscribeLive(
	joinCtx context.Context,
	sessionCtx context.Context,
	request transport.BringUpRequest,
	feedSessionID string,
) error {
	if feedSessionID == "" {
		return fmt.Errorf("session: subscribeLive requires a non-empty feed session id")
	}
	if len(request.Instruments) == 0 {
		return fmt.Errorf("session: subscribeLive requires at least one instrument on the request")
	}
	configuredTimeframes := runner.dependencies.Params.HistoryTimeframes
	if len(configuredTimeframes) == 0 {
		return fmt.Errorf("session: subscribeLive requires Params.HistoryTimeframes to be non-empty")
	}
	for _, timeframe := range configuredTimeframes {
		if !isAcceptedHistoryTimeframe(timeframe) {
			return fmt.Errorf(
				"session: subscribeLive: timeframe %q is not one of {%q,%q,%q,%q}",
				timeframe,
				core.TimeframeOneMinute, core.TimeframeFiveMinute,
				core.TimeframeThirtyMinute, core.TimeframeSixtyMinute,
			)
		}
	}

	subscriptions := buildLiveSubscriptionList(request.Instruments, configuredTimeframes)

	connection, connectedFrame, subscribeErr := runner.dependencies.Core.Subscribe(joinCtx, feedSessionID, subscriptions)
	if subscribeErr != nil {
		return fmt.Errorf("session: Subscribe on exchange %q: %w", request.ExchangeID, subscribeErr)
	}

	runner.dependencies.Logger.Info(
		"session: live feed subscribed",
		"exchange_id", request.ExchangeID,
		"market", connectedFrame.Market,
		"timezone", connectedFrame.Timezone,
		"subscription_count", len(subscriptions),
		"configured_timeframes", configuredTimeframes,
	)

	// The ingest goroutine owns the connection from here. It closes
	// the socket on exit so the Runner does not have to track it.
	// Bound to sessionCtx (not joinCtx) so it survives past the
	// pre-open join and keeps running for the life of the session.
	go runner.runIngest(sessionCtx, request.ExchangeID, connectedFrame.Timezone, connection)
	return nil
}

// buildLiveSubscriptionList composes the batched subscribe frame
// Core will see on the one WS. The batch mixes stream kinds freely:
// one snapshot sub per instrument (whole-picture re-emits that
// drive the tick loop's quote view) plus one candle sub per
// (instrument × configured timeframe) so every configured timeframe
// receives finalized bars on the same connection. Core publishes
// only finalized bars on the candle stream — the forming bar's
// price movement rides the snapshot's last_price.
//
// Order within the batch: all snapshot subs first (grouped by
// instrument), then all candle subs (grouped by timeframe, then
// instrument). The order is stable so a replay of the same bring-up
// config dials with the same subscribe frame.
func buildLiveSubscriptionList(instruments []string, timeframes []string) []core.MarketDataSubscription {
	totalSubs := len(instruments) * (1 + len(timeframes))
	subscriptions := make([]core.MarketDataSubscription, 0, totalSubs)
	for _, instrument := range instruments {
		subscriptions = append(subscriptions, core.MarketDataSubscription{
			Stream:     core.MarketDataStreamSnapshot,
			Instrument: instrument,
		})
	}
	for _, timeframe := range timeframes {
		for _, instrument := range instruments {
			subscriptions = append(subscriptions, core.MarketDataSubscription{
				Stream:     core.MarketDataStreamCandle,
				Instrument: instrument,
				Timeframe:  timeframe,
			})
		}
	}
	return subscriptions
}
