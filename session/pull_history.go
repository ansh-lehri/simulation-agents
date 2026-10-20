package session

import (
	"context"
	"fmt"
	"time"

	"simulation-agents/core"
	"simulation-agents/store"
	"simulation-agents/transport"
)

// coreNaiveLocalFormat is the exact time layout Core's candle
// endpoints accept and emit on the naive-local fields: four-digit
// year, two-digit everything else, no zone. Callers hold the
// time-to-string discipline; this package never prepends a zone.
const coreNaiveLocalFormat = "2006-01-02T15:04:05"

// pullHistory runs the pre-open candle-history pull for every
// (instrument, timeframe) pair in the request. For each pair it
// pages GET /v1/candles from (SessionStart − HistoryLookback) to
// SessionStart, follows NextCursor until the final page, converts
// the naive-local-timed bars into the store's unix-millis shape,
// and writes the window via MarketData.WriteCandles with replace
// semantics keyed per (exchange_id, instrument, timeframe).
//
// Which timeframes get pulled is a per-bring-up config dial
// (Params.HistoryTimeframes). An agent that reads a higher
// timeframe at tick time hits the pre-warmed Redis cache instead
// of a broker round-trip; a timeframe that is NOT in the configured
// list stays a broker-side concern (L2 does no local rollup).
//
// Fatal on the first error. Agents cannot run their perception
// against an incomplete history window, and partial-ok semantics
// would silently corrupt the pre-open seed; the design makes
// history a fatal pre-open op by name.
//
// Market source selection: canonical sessions pull from their own
// market tag; on-demand sessions pull from the canonical market
// tag for the same country, because the broker does not persist
// on-demand candles — the history seed for an on-demand instance
// is the canonical market's immutable past.
func (runner *Runner) pullHistory(
	ctx context.Context,
	request transport.BringUpRequest,
) error {
	historyLookback := runner.dependencies.Params.HistoryLookback
	if historyLookback <= 0 {
		return fmt.Errorf("session: pullHistory requires Params.HistoryLookback > 0, got %v", historyLookback)
	}
	timeframes := runner.dependencies.Params.HistoryTimeframes
	if len(timeframes) == 0 {
		return fmt.Errorf("session: pullHistory requires Params.HistoryTimeframes to be non-empty")
	}
	for _, timeframe := range timeframes {
		if !isAcceptedHistoryTimeframe(timeframe) {
			return fmt.Errorf(
				"session: pullHistory: timeframe %q is not one of {%q,%q,%q,%q}",
				timeframe,
				core.TimeframeOneMinute, core.TimeframeFiveMinute,
				core.TimeframeThirtyMinute, core.TimeframeSixtyMinute,
			)
		}
	}
	if len(request.Instruments) == 0 {
		runner.dependencies.Logger.Warn(
			"session: pullHistory called with no instruments; nothing to pull",
			"exchange_id", request.ExchangeID,
		)
		return nil
	}

	historyMarketTag := historyMarketTagFromRequest(request)
	fromNaiveLocal := request.SessionStart.Add(-historyLookback).Format(coreNaiveLocalFormat)
	toNaiveLocal := request.SessionStart.Format(coreNaiveLocalFormat)

	// Loop order (timeframe outer, instrument inner) keeps the per-
	// timeframe writes contiguous in the log, which is easier for an
	// operator to read on a slow pull. Correctness is independent of
	// the order; a transient failure on any pair aborts the whole op.
	for _, timeframe := range timeframes {
		for _, instrument := range request.Instruments {
			if err := runner.pullOneInstrumentHistory(
				ctx,
				request.ExchangeID,
				historyMarketTag,
				instrument,
				timeframe,
				fromNaiveLocal,
				toNaiveLocal,
			); err != nil {
				return fmt.Errorf(
					"session: pullHistory for instrument %q timeframe %q: %w",
					instrument, timeframe, err,
				)
			}
		}
	}

	runner.dependencies.Logger.Info(
		"session: pullHistory complete",
		"exchange_id", request.ExchangeID,
		"source_market", historyMarketTag,
		"instrument_count", len(request.Instruments),
		"timeframes", timeframes,
		"from", fromNaiveLocal,
		"to", toNaiveLocal,
	)
	return nil
}

// isAcceptedHistoryTimeframe returns true if the configured
// timeframe is one of the four Core-accepted values. Factored out
// so a bad composition-root entry fails loudly at the top of
// pullHistory rather than at the first Core call.
func isAcceptedHistoryTimeframe(timeframe string) bool {
	switch timeframe {
	case core.TimeframeOneMinute,
		core.TimeframeFiveMinute,
		core.TimeframeThirtyMinute,
		core.TimeframeSixtyMinute:
		return true
	default:
		return false
	}
}

// pullOneInstrumentHistory pages through the window for one
// (instrument, timeframe) pair, converts the bars, and writes them
// under the bring-up's own exchange id so canonical and on-demand
// instances stay cleanly namespaced in Redis.
func (runner *Runner) pullOneInstrumentHistory(
	ctx context.Context,
	exchangeID string,
	marketTag string,
	instrument string,
	timeframe string,
	fromNaiveLocal string,
	toNaiveLocal string,
) error {
	var accumulatedBars []store.Candle
	cursor := ""
	marketTimezoneName := ""
	for {
		candlesRequest := core.CandlesRequest{
			Market:         marketTag,
			Instrument:     instrument,
			Timeframe:      timeframe,
			FromNaiveLocal: fromNaiveLocal,
			ToNaiveLocal:   toNaiveLocal,
			Cursor:         cursor,
		}
		page, pageErr := runner.dependencies.Core.GetCandles(ctx, candlesRequest)
		if pageErr != nil {
			return pageErr
		}
		if marketTimezoneName == "" {
			marketTimezoneName = page.Timezone
		}
		for barIndex, bar := range page.Candles {
			convertedBar, convErr := convertCandleFromCoreToStore(bar, marketTimezoneName)
			if convErr != nil {
				return fmt.Errorf("convert candle %d on page cursor=%q: %w", barIndex, cursor, convErr)
			}
			accumulatedBars = append(accumulatedBars, convertedBar)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return runner.dependencies.MarketData.WriteCandles(
		ctx, exchangeID, instrument, timeframe, accumulatedBars,
	)
}

// historyMarketTagFromRequest returns the market tag to query for
// the history pull. The convention is one canonical market per
// country. On-demand sessions reuse the canonical tag because the
// broker never persists on-demand candles — the history seed is
// the canonical past regardless of which exchange_id namespace the
// bars land under in Redis.
func historyMarketTagFromRequest(request transport.BringUpRequest) string {
	return request.Country
}

// convertCandleFromCoreToStore maps one page row (naive-local
// timestamps + integer prices and volume) into the store's
// unix-millis shape so the market-data cache can serve a single
// consistent representation to the tick loop regardless of where a
// bar originated. Returning an error on an unparseable timezone or
// naive-local string surfaces malformed Core responses up through
// the fatal pre-open path.
func convertCandleFromCoreToStore(bar core.Candle, marketTimezoneName string) (store.Candle, error) {
	if marketTimezoneName == "" {
		return store.Candle{}, fmt.Errorf("empty market timezone on page envelope")
	}
	marketLocation, locErr := time.LoadLocation(marketTimezoneName)
	if locErr != nil {
		return store.Candle{}, fmt.Errorf("load timezone %q: %w", marketTimezoneName, locErr)
	}
	startInstant, startErr := time.ParseInLocation(coreNaiveLocalFormat, bar.StartNaiveLocal, marketLocation)
	if startErr != nil {
		return store.Candle{}, fmt.Errorf("parse start %q in %q: %w", bar.StartNaiveLocal, marketTimezoneName, startErr)
	}
	endInstant, endErr := time.ParseInLocation(coreNaiveLocalFormat, bar.EndNaiveLocal, marketLocation)
	if endErr != nil {
		return store.Candle{}, fmt.Errorf("parse end %q in %q: %w", bar.EndNaiveLocal, marketTimezoneName, endErr)
	}
	return store.Candle{
		OpenTime:  startInstant.UnixMilli(),
		Open:      bar.Open,
		High:      bar.High,
		Low:       bar.Low,
		Close:     bar.Close,
		Volume:    bar.Volume,
		CloseTime: endInstant.UnixMilli(),
	}, nil
}
