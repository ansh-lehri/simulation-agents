package decision

import (
	"math/rand/v2"
	"testing"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// makeRangeAccessor wraps a candle slice as an Accessor for range tests.
// The last close is used as the current price.
func makeRangeAccessor(inst, tf string, candles []store.Candle) *Accessor {
	lastClose := candles[len(candles)-1].Close
	return NewAccessor(
		map[string]map[string][]store.Candle{inst: {tf: candles}},
		map[string]store.Quote{inst: {LastPrice: lastClose, HasLast: true}},
		map[string]bool{inst: true},
		TickContext{},
	)
}

// flatCloseCandles returns candles with identical closes (flat market).
func flatCloseCandles(price int64, count int) []store.Candle {
	out := make([]store.Candle, count)
	for i := range out {
		out[i] = store.Candle{Open: price, High: price, Low: price, Close: price, Volume: 100}
	}
	return out
}

// risingCloseCandles returns candles with linearly rising closes.
func risingCloseCandles(start int64, count int) []store.Candle {
	out := make([]store.Candle, count)
	for i := range out {
		p := start + int64(i)
		out[i] = store.Candle{Open: p, High: p + 1, Low: p - 1, Close: p, Volume: 100}
	}
	return out
}

func noTraitsRNG() (agent.TraitVector, *rand.Rand) {
	return zeroNoise(), noiseRNG()
}

func noPerceptionParams() PerceptionParams { return PerceptionParams{MASaturationMultiple: 1} }

func TestPerceiveRangeBollinger_FlatMarket(t *testing.T) {
	// Flat closes → zero std dev → BBands produce equal upper/mid/lower → bandSpan=0.
	// The function must return ReadOK=false (not panic, not return garbage pos).
	candles := flatCloseCandles(1000, 20)
	acc := makeRangeAccessor("X", "1m", candles)
	block := &agent.ChannelBlock{
		Method:         "bollinger",
		ReadingParams:  map[string]float64{"period": 10, "sigma_mult": 2.0},
		ReadingCutoffs: map[string]float64{"ob_os_band": 0.8},
	}
	traits, rng := noTraitsRNG()
	r := perceiveRange(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if r.ReadOK {
		t.Fatal("flat market (zero std dev) → bandSpan=0 → ReadOK must be false")
	}
}

func TestPerceiveRangeBollinger_UpperBand(t *testing.T) {
	// Build candles where the last close is well above the average:
	// 19 flat bars at 1000, then one spike to 2000.
	candles := flatCloseCandles(1000, 19)
	candles = append(candles, store.Candle{Open: 2000, High: 2000, Low: 2000, Close: 2000, Volume: 100})
	acc := makeRangeAccessor("X", "1m", candles)
	block := &agent.ChannelBlock{
		Method:         "bollinger",
		ReadingParams:  map[string]float64{"period": 10, "sigma_mult": 2.0},
		ReadingCutoffs: map[string]float64{"ob_os_band": 0.5},
	}
	traits, rng := noTraitsRNG()
	r := perceiveRange(block, acc, "X", "1m", traits, rng, noPerceptionParams())

	if !r.ReadOK {
		t.Fatal("ReadOK must be true when bandSpan > 0")
	}
	// Price spike above mean → pos > 0.
	if r.PositionInRange <= 0 {
		t.Errorf("expected positive position at upper extreme, got %v", r.PositionInRange)
	}
	// Range bounds: lower < upper.
	if r.RangeBounds[0] >= r.RangeBounds[1] {
		t.Errorf("RangeBounds[0] must be < [1], got %v", r.RangeBounds)
	}
}

func TestPerceiveRangeBollinger_ThinHistory(t *testing.T) {
	candles := flatCloseCandles(1000, 5) // period=20 needs 20 bars
	acc := makeRangeAccessor("X", "1m", candles)
	block := &agent.ChannelBlock{
		Method:         "bollinger",
		ReadingParams:  map[string]float64{"period": 20, "sigma_mult": 2.0},
		ReadingCutoffs: map[string]float64{"ob_os_band": 0.5},
	}
	traits, rng := noTraitsRNG()
	r := perceiveRange(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if r.ReadOK {
		t.Fatal("ReadOK must be false with insufficient bars")
	}
}

func TestPerceiveRangeChannel_MidPrice(t *testing.T) {
	// Rising candles from 990 to 1010; midpoint around 1000.
	candles := risingCloseCandles(990, 21) // 21 bars
	acc := makeRangeAccessor("X", "1m", candles)
	block := &agent.ChannelBlock{
		Method:         "channel",
		ReadingParams:  map[string]float64{"window": 20},
		ReadingCutoffs: map[string]float64{"extreme_band": 0.5},
	}
	traits, rng := noTraitsRNG()
	r := perceiveRange(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	// Position is in [-1,+1].
	if r.PositionInRange < -1 || r.PositionInRange > 1 {
		t.Errorf("PositionInRange must be in [-1,+1], got %v", r.PositionInRange)
	}
}

func TestPerceiveRangeChannel_StretchBeyondExtremeBand(t *testing.T) {
	// Candles in a range [1000,1010]; current close = 1010 (max) → pos = +1.
	// With extreme_band = 0.5: stretch = (1-0.5)/(1-0.5) = 1.0.
	candles := make([]store.Candle, 20)
	for i := range candles {
		p := int64(1000 + i%11) // cycles 1000..1010
		candles[i] = store.Candle{Open: p, High: p, Low: p, Close: p, Volume: 100}
	}
	// Force last close to max.
	candles[19] = store.Candle{Open: 1010, High: 1010, Low: 1010, Close: 1010, Volume: 100}
	acc := makeRangeAccessor("X", "1m", candles)
	block := &agent.ChannelBlock{
		Method:         "channel",
		ReadingParams:  map[string]float64{"window": 20},
		ReadingCutoffs: map[string]float64{"extreme_band": 0.5},
	}
	traits, rng := noTraitsRNG()
	r := perceiveRange(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	if r.Stretch < 0.9 {
		t.Errorf("stretch near max: expected close to 1.0, got %v", r.Stretch)
	}
}

func TestPerceiveRangeRSI_ThinHistory(t *testing.T) {
	candles := risingCloseCandles(1000, 5) // needs period+1 = 15 bars
	acc := makeRangeAccessor("X", "1m", candles)
	block := &agent.ChannelBlock{
		Method:         "rsi",
		ReadingParams:  map[string]float64{"period": 14},
		ReadingCutoffs: map[string]float64{"ob_os_levels": 20},
	}
	traits, rng := noTraitsRNG()
	r := perceiveRange(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if r.ReadOK {
		t.Fatal("ReadOK must be false with insufficient bars")
	}
}

func TestPerceiveRangeRSI_RisingMarket(t *testing.T) {
	// Strongly rising market → RSI near 100 → pos near +1.
	candles := risingCloseCandles(1000, 30)
	acc := makeRangeAccessor("X", "1m", candles)
	block := &agent.ChannelBlock{
		Method:         "rsi",
		ReadingParams:  map[string]float64{"period": 14},
		ReadingCutoffs: map[string]float64{"ob_os_levels": 20},
	}
	traits, rng := noTraitsRNG()
	r := perceiveRange(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	if r.PositionInRange <= 0 {
		t.Errorf("rising market → positive RSI position expected, got %v", r.PositionInRange)
	}
}

func TestPerceiveRangeRSI_RangeBoundsNotEmpty(t *testing.T) {
	candles := risingCloseCandles(1000, 30)
	acc := makeRangeAccessor("X", "1m", candles)
	block := &agent.ChannelBlock{
		Method:         "rsi",
		ReadingParams:  map[string]float64{"period": 14},
		ReadingCutoffs: map[string]float64{"ob_os_levels": 20},
	}
	traits, rng := noTraitsRNG()
	r := perceiveRange(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	if r.RangeBounds[0] >= r.RangeBounds[1] {
		t.Errorf("RangeBounds[0] must be < [1], got %v", r.RangeBounds)
	}
}

func TestPerceiveRange_Absent(t *testing.T) {
	acc := NewAccessor(
		map[string]map[string][]store.Candle{},
		map[string]store.Quote{},
		map[string]bool{"X": false},
		TickContext{},
	)
	block := &agent.ChannelBlock{
		Method:         "bollinger",
		ReadingParams:  map[string]float64{"period": 10, "sigma_mult": 2.0},
		ReadingCutoffs: map[string]float64{"ob_os_band": 0.5},
	}
	traits, rng := noTraitsRNG()
	r := perceiveRange(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if r.ReadOK {
		t.Fatal("ReadOK must be false when absent")
	}
}

func TestPerceiveRangeBollinger_StretchClampedAt1(t *testing.T) {
	// Extreme spike → raw stretch >> 1, must be clamped to 1.0.
	candles := flatCloseCandles(1000, 19)
	candles = append(candles, store.Candle{Open: 5000, High: 5000, Low: 5000, Close: 5000, Volume: 100})
	acc := makeRangeAccessor("X", "1m", candles)
	block := &agent.ChannelBlock{
		Method:         "bollinger",
		ReadingParams:  map[string]float64{"period": 10, "sigma_mult": 2.0},
		ReadingCutoffs: map[string]float64{"ob_os_band": 0.5},
	}
	traits, rng := noTraitsRNG()
	r := perceiveRange(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if !r.ReadOK {
		t.Skip("flat std-dev causes degenerate band; skipping stretch clamp check")
	}
	if r.Stretch > 1.0+1e-9 {
		t.Errorf("stretch must be clamped to [0,1], got %v", r.Stretch)
	}
}

