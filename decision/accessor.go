package decision

import (
	"math"
	"time"

	"simulation-agents/store"
)

// TickContext carries the timing information for the current action tick.
// Built once per tick in the action phase by the session goroutine and
// passed into NewAccessor so signal functions can compute session progress
// without any clock calls of their own.
type TickContext struct {
	Now          time.Time
	SessionStart time.Time
	SessionEnd   time.Time
	TickIndex    int64
}

// bookSide selects a side of the order book for DepthQty reads.
type bookSide int8

const (
	sideBid bookSide = iota
	sideAsk
)

// Accessor is a thin, read-only window onto one tick's in-memory market
// snapshot. Signal functions ask it for raw slices and agent-agnostic
// arithmetic; all indicator math lives in the signal functions themselves.
// Constructed once per tick, shared across all agents — it carries no
// per-agent state.
type Accessor struct {
	candles map[string]map[string][]store.Candle // instrument → timeframe → bars
	quotes  map[string]store.Quote
	present map[string]bool
	tick    TickContext
}

// NewAccessor wraps the per-tick snapshot for read-only signal access.
// The caller (the session goroutine) owns the maps and must not mutate
// them while any agent's signal functions hold this Accessor.
func NewAccessor(
	candles map[string]map[string][]store.Candle,
	quotes map[string]store.Quote,
	present map[string]bool,
	tick TickContext,
) *Accessor {
	return &Accessor{
		candles: candles,
		quotes:  quotes,
		present: present,
		tick:    tick,
	}
}

// Closes returns the last n closing prices as float64 for the given
// instrument and timeframe. If fewer than n bars are available, all
// available bars are returned. The slice is chronological (oldest first).
// Returns nil when the instrument or timeframe is absent.
func (acc *Accessor) Closes(inst, tf string, n int) []float64 {
	bars := acc.candleWindow(inst, tf, n)
	if len(bars) == 0 {
		return nil
	}
	return candlesToFloat64(bars)
}

// Highs returns the last n bar highs as float64.
func (acc *Accessor) Highs(inst, tf string, n int) []float64 {
	bars := acc.candleWindow(inst, tf, n)
	if len(bars) == 0 {
		return nil
	}
	return highsToFloat64(bars)
}

// Lows returns the last n bar lows as float64.
func (acc *Accessor) Lows(inst, tf string, n int) []float64 {
	bars := acc.candleWindow(inst, tf, n)
	if len(bars) == 0 {
		return nil
	}
	return lowsToFloat64(bars)
}

// Volumes returns the last n bar volumes as float64.
func (acc *Accessor) Volumes(inst, tf string, n int) []float64 {
	bars := acc.candleWindow(inst, tf, n)
	if len(bars) == 0 {
		return nil
	}
	return volumesToFloat64(bars)
}

// BarCount returns the number of bars available for the given instrument
// and timeframe. Signal functions gate on this before trusting go-talib
// output — go-talib fills warm-up positions with 0.0, not NaN.
func (acc *Accessor) BarCount(inst, tf string) int {
	tfMap, ok := acc.candles[inst]
	if !ok {
		return 0
	}
	return len(tfMap[tf])
}

// CurrentPrice returns the best available price for the given instrument.
// When the quote's HasLast flag is set, the quote's LastPrice is returned.
// Otherwise the most recent candle close for the given timeframe is used —
// a fallback for thin early-session periods before a trade has printed.
// The bool is false when neither source yields a price.
func (acc *Accessor) CurrentPrice(inst, tf string) (int64, bool) {
	if quote, ok := acc.quotes[inst]; ok && quote.HasLast {
		return quote.LastPrice, true
	}
	tfMap, ok := acc.candles[inst]
	if !ok {
		return 0, false
	}
	bars := tfMap[tf]
	if len(bars) == 0 {
		return 0, false
	}
	return bars[len(bars)-1].Close, true
}

// Spread returns the bid-ask spread in ticks. The bool is false when the
// quote has no valid bid or ask — signal functions must propagate this as
// ReadOK=false rather than treating a zero spread as "tight."
func (acc *Accessor) Spread(inst string) (int64, bool) {
	quote, ok := acc.quotes[inst]
	if !ok || !quote.HasBid || !quote.HasAsk {
		return 0, false
	}
	return quote.AskPrice - quote.BidPrice, true
}

// DepthQty sums the top-levels quantities on one side of the order book.
// Returns 0 when the instrument is absent or the requested side's flag is
// not set. The sum is absolute — no rolling normalization baseline.
func (acc *Accessor) DepthQty(inst string, levels int, side bookSide) int64 {
	quote, ok := acc.quotes[inst]
	if !ok {
		return 0
	}
	switch side {
	case sideBid:
		if !quote.HasBid {
			return 0
		}
		var total int64
		for depth := 0; depth < levels && depth < len(quote.Depth.Bids); depth++ {
			total += quote.Depth.Bids[depth].Qty
		}
		return total
	case sideAsk:
		if !quote.HasAsk {
			return 0
		}
		var total int64
		for depth := 0; depth < levels && depth < len(quote.Depth.Asks); depth++ {
			total += quote.Depth.Asks[depth].Qty
		}
		return total
	}
	return 0
}

// Imbalance returns (bidSum−askSum)/(bidSum+askSum) ∈ [−1,+1] over the
// top-levels book. The bool is false when either side is absent or the
// total qty is zero.
func (acc *Accessor) Imbalance(inst string, levels int) (float64, bool) {
	quote, ok := acc.quotes[inst]
	if !ok || !quote.HasBid || !quote.HasAsk {
		return 0, false
	}
	var bidSum, askSum int64
	for depth := 0; depth < levels && depth < len(quote.Depth.Bids); depth++ {
		bidSum += quote.Depth.Bids[depth].Qty
	}
	for depth := 0; depth < levels && depth < len(quote.Depth.Asks); depth++ {
		askSum += quote.Depth.Asks[depth].Qty
	}
	total := bidSum + askSum
	if total == 0 {
		return 0, false
	}
	return float64(bidSum-askSum) / float64(total), true
}

// Phase returns the exchange phase string for the given instrument.
// Returns an empty string when no quote is present.
func (acc *Accessor) Phase(inst string) string {
	if quote, ok := acc.quotes[inst]; ok {
		return quote.Phase
	}
	return ""
}

// SessionProgress returns the normalized session time: 0.0 at session start,
// 1.0 at session end, clamped to [0,1].
func (acc *Accessor) SessionProgress() float64 {
	total := acc.tick.SessionEnd.Sub(acc.tick.SessionStart).Seconds()
	if total <= 0 {
		return 0
	}
	elapsed := acc.tick.Now.Sub(acc.tick.SessionStart).Seconds()
	return math.Max(0, math.Min(1, elapsed/total))
}

// Present reports whether the given instrument has a live quote in this
// tick's snapshot. False means the ingest goroutine has not yet delivered
// a quote for this instrument; signal functions treat it as ReadOK=false.
func (acc *Accessor) Present(inst string) bool {
	return acc.present[inst]
}

// Candles returns the raw candle slice for a given instrument and timeframe.
// Used by signal functions that need direct bar access (structure pivot
// detection, confirming volume). Returns nil when absent.
func (acc *Accessor) Candles(inst, tf string) []store.Candle {
	tfMap, ok := acc.candles[inst]
	if !ok {
		return nil
	}
	return tfMap[tf]
}

// candleWindow returns the last n bars for (inst, tf). When fewer than n
// bars are available, all bars are returned. Never pads or synthesizes.
func (acc *Accessor) candleWindow(inst, tf string, n int) []store.Candle {
	tfMap, ok := acc.candles[inst]
	if !ok {
		return nil
	}
	bars := tfMap[tf]
	if n <= 0 || len(bars) == 0 {
		return nil
	}
	if n >= len(bars) {
		return bars
	}
	return bars[len(bars)-n:]
}
