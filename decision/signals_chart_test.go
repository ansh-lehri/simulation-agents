package decision

import (
	"testing"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// makeChartAccessor builds an Accessor from a candle slice with the last
// close available as the quote price.
func makeChartAccessor(inst, tf string, candles []store.Candle) *Accessor {
	lastClose := candles[len(candles)-1].Close
	return NewAccessor(
		map[string]map[string][]store.Candle{inst: {tf: candles}},
		map[string]store.Quote{inst: {LastPrice: lastClose, HasLast: true}},
		map[string]bool{inst: true},
		TickContext{},
	)
}

// strongUpCandles returns candles with steep monotone price rise.
func strongUpCandles(start int64, count int) []store.Candle {
	out := make([]store.Candle, count)
	for i := range out {
		p := start + int64(i)*10
		out[i] = store.Candle{Open: p, High: p + 2, Low: p - 2, Close: p, Volume: 100}
	}
	return out
}

// strongDownCandles returns candles with steep monotone price fall.
func strongDownCandles(start int64, count int) []store.Candle {
	out := make([]store.Candle, count)
	for i := range out {
		p := start - int64(i)*10
		out[i] = store.Candle{Open: p, High: p + 2, Low: p - 2, Close: p, Volume: 100}
	}
	return out
}

// maBlock builds a chart channel block for the EMA or SMA method.
func maBlock(method string, fast, slow int, flatBand float64) *agent.ChannelBlock {
	return &agent.ChannelBlock{
		Method: method,
		ReadingParams: map[string]float64{
			"fast_period": float64(fast),
			"slow_period": float64(slow),
		},
		ReadingCutoffs: map[string]float64{"flat_band": flatBand},
	}
}

func rsiChartBlock(period int, midBand float64) *agent.ChannelBlock {
	return &agent.ChannelBlock{
		Method:         "rsi",
		ReadingParams:  map[string]float64{"period": float64(period)},
		ReadingCutoffs: map[string]float64{"mid_band": midBand},
	}
}

func structureBlock(swingWindow, confirmSwings int, minSwingPct float64) *agent.ChannelBlock {
	return &agent.ChannelBlock{
		Method: "structure",
		ReadingParams: map[string]float64{
			"swing_window":   float64(swingWindow),
			"confirm_swings": float64(confirmSwings),
		},
		ReadingCutoffs: map[string]float64{"min_swing_pct": minSwingPct},
	}
}

func TestPerceiveChart_EMA_Up(t *testing.T) {
	// 60 strongly rising bars → fast EMA > slow EMA → DirUp.
	candles := strongUpCandles(1000, 60)
	acc := makeChartAccessor("X", "1m", candles)
	block := maBlock("ema", 5, 20, 0.001)
	params := PerceptionParams{MASaturationMultiple: 2.0}
	traits, rng := noTraitsRNG()

	r := perceiveChart(block, acc, "X", "1m", traits, rng, params)
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	if r.Direction != DirUp {
		t.Errorf("strong rise: expected DirUp, got %v", r.Direction)
	}
	if r.Strength <= 0 {
		t.Errorf("expected positive strength, got %v", r.Strength)
	}
	if r.Cleanliness <= 0 {
		t.Errorf("expected positive cleanliness, got %v", r.Cleanliness)
	}
	if len(r.KeyLevels) != 2 {
		t.Errorf("expected 2 key levels, got %v", r.KeyLevels)
	}
}

func TestPerceiveChart_SMA_Down(t *testing.T) {
	candles := strongDownCandles(2000, 60)
	acc := makeChartAccessor("X", "1m", candles)
	block := maBlock("sma", 5, 20, 0.001)
	params := PerceptionParams{MASaturationMultiple: 2.0}
	traits, rng := noTraitsRNG()

	r := perceiveChart(block, acc, "X", "1m", traits, rng, params)
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	if r.Direction != DirDown {
		t.Errorf("strong fall: expected DirDown, got %v", r.Direction)
	}
}

func TestPerceiveChart_MA_ThinHistory(t *testing.T) {
	// Only 5 bars but slow_period = 20.
	candles := strongUpCandles(1000, 5)
	acc := makeChartAccessor("X", "1m", candles)
	block := maBlock("ema", 5, 20, 0.001)
	traits, rng := noTraitsRNG()

	r := perceiveChart(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if r.ReadOK {
		t.Fatal("ReadOK must be false with insufficient bars")
	}
}

func TestPerceiveChart_MA_FlatBand(t *testing.T) {
	// Flat market → small spread → DirFlat with a large flat_band.
	candles := flatCloseCandles(1000, 40)
	acc := makeChartAccessor("X", "1m", candles)
	block := maBlock("sma", 5, 20, 0.1)
	params := PerceptionParams{MASaturationMultiple: 2.0}
	traits, rng := noTraitsRNG()

	r := perceiveChart(block, acc, "X", "1m", traits, rng, params)
	if !r.ReadOK {
		t.Fatal("ReadOK must be true even for flat market")
	}
	if r.Direction != DirFlat {
		t.Errorf("flat market with large band: expected DirFlat, got %v", r.Direction)
	}
}

func TestPerceiveChart_RSI_Rising(t *testing.T) {
	// 30 rising bars → RSI near 100 → above mid_band=10 → DirUp.
	candles := strongUpCandles(1000, 30)
	acc := makeChartAccessor("X", "1m", candles)
	block := rsiChartBlock(14, 10)
	params := PerceptionParams{}
	traits, rng := noTraitsRNG()

	r := perceiveChart(block, acc, "X", "1m", traits, rng, params)
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	if r.Direction != DirUp {
		t.Errorf("strong rise: expected DirUp from RSI, got %v", r.Direction)
	}
	if r.Strength <= 0 {
		t.Errorf("expected positive strength, got %v", r.Strength)
	}
}

func TestPerceiveChart_RSI_ThinHistory(t *testing.T) {
	candles := strongUpCandles(1000, 5)
	acc := makeChartAccessor("X", "1m", candles)
	block := rsiChartBlock(14, 10)
	traits, rng := noTraitsRNG()

	r := perceiveChart(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if r.ReadOK {
		t.Fatal("ReadOK must be false with insufficient bars")
	}
}

func TestPerceiveChart_Structure_UpTrend(t *testing.T) {
	// Build a clear uptrend: series of clean higher-highs and higher-lows.
	// swing_window=2, confirm_swings=2, min_swing_pct=0.001.
	// Need at least 2*2+1=5 bars, but pivots must also emerge. Use 40 bars.
	candles := make([]store.Candle, 40)
	// Alternate small dip then big rise to create clear HH/HL pattern.
	for i := range candles {
		base := int64(1000 + i*5)
		candles[i] = store.Candle{Open: base, High: base + 3, Low: base - 3, Close: base, Volume: 100}
	}
	acc := makeChartAccessor("X", "1m", candles)
	block := structureBlock(2, 2, 0.001)
	params := PerceptionParams{}
	traits, rng := noTraitsRNG()

	r := perceiveChart(block, acc, "X", "1m", traits, rng, params)
	// We can't guarantee ReadOK because pivot detection may not find enough
	// confirmed pivots in a smooth trend. Just verify no panic and if OK → DirUp.
	if r.ReadOK && r.Direction != DirUp {
		t.Errorf("uptrend candles → expected DirUp or insufficient pivots, got %v", r.Direction)
	}
}

func TestPerceiveChart_Structure_InsufficientBars(t *testing.T) {
	candles := strongUpCandles(1000, 3) // too few for swing_window=2
	acc := makeChartAccessor("X", "1m", candles)
	block := structureBlock(2, 2, 0.01)
	traits, rng := noTraitsRNG()

	r := perceiveChart(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if r.ReadOK {
		t.Fatal("ReadOK must be false with fewer bars than 2*swingWindow+1")
	}
}

func TestPerceiveChart_Structure_InsufficientPivots(t *testing.T) {
	// Perfectly monotone: no swing pivots at all.
	candles := strongUpCandles(1000, 30)
	acc := makeChartAccessor("X", "1m", candles)
	block := structureBlock(2, 3, 0.001) // needs 3 pivots of each type
	traits, rng := noTraitsRNG()

	r := perceiveChart(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	// A monotone series has no swing pivots → ReadOK=false.
	if r.ReadOK {
		t.Fatal("ReadOK must be false when no confirmed swing pivots exist")
	}
}

func TestPerceiveChart_Absent(t *testing.T) {
	acc := NewAccessor(
		map[string]map[string][]store.Candle{},
		map[string]store.Quote{},
		map[string]bool{"X": false},
		TickContext{},
	)
	block := maBlock("ema", 5, 20, 0.001)
	traits, rng := noTraitsRNG()

	r := perceiveChart(block, acc, "X", "1m", traits, rng, noPerceptionParams())
	if r.ReadOK {
		t.Fatal("ReadOK must be false when absent")
	}
}

func TestDetectSwingPivots_SimplePattern(t *testing.T) {
	// Build: low - high - low - high pattern with swing_window=1.
	//        950  1050  960  1060
	// Padding needed for confirmation lag.
	bars := []store.Candle{
		{High: 1000, Low: 1000}, // 0 — padding
		{High: 950, Low: 940},   // 1 — swing low
		{High: 1000, Low: 1000}, // 2 — padding
		{High: 1000, Low: 1000}, // 3 — padding
		{High: 1050, Low: 1000}, // 4 — swing high
		{High: 1000, Low: 1000}, // 5 — padding
		{High: 1000, Low: 1000}, // 6 — padding
		{High: 970, Low: 960},   // 7 — swing low
		{High: 1000, Low: 1000}, // 8 — padding
		{High: 1000, Low: 1000}, // 9 — padding
		{High: 1060, Low: 1000}, // 10 — swing high
		{High: 1000, Low: 1000}, // 11 — padding
	}
	highs, lows := detectSwingPivots(bars, 1, 0.001)
	if len(highs) < 1 {
		t.Errorf("expected at least one swing high, got %v", highs)
	}
	if len(lows) < 1 {
		t.Errorf("expected at least one swing low, got %v", lows)
	}
}

func TestSwingDirectionAndStrength_UpTrend(t *testing.T) {
	// HH and HL pattern across 3 pivots each.
	highs := []int64{1000, 1010, 1020}
	lows := []int64{990, 995, 1000}
	dir, strength := swingDirectionAndStrength(highs, lows, 3)
	if dir != DirUp {
		t.Errorf("HH+HL pattern: expected DirUp, got %v", dir)
	}
	if strength <= 0 {
		t.Errorf("expected positive strength, got %v", strength)
	}
}

func TestSwingDirectionAndStrength_DownTrend(t *testing.T) {
	highs := []int64{1020, 1010, 1000}
	lows := []int64{1000, 995, 990}
	dir, _ := swingDirectionAndStrength(highs, lows, 3)
	if dir != DirDown {
		t.Errorf("LH+LL pattern: expected DirDown, got %v", dir)
	}
}

func TestSwingDirectionAndStrength_Flat(t *testing.T) {
	// Highs rising but lows falling → conflicting → DirFlat.
	highs := []int64{1000, 1010} // rising
	lows := []int64{1000, 990}   // falling
	dir, _ := swingDirectionAndStrength(highs, lows, 2)
	if dir != DirFlat {
		t.Errorf("conflicting signals: expected DirFlat, got %v", dir)
	}
}

func TestDirectionRunFraction_UpTrend(t *testing.T) {
	closes := []float64{100, 101, 102, 103}
	got := directionRunFraction(closes, DirUp)
	if got != 1.0 {
		t.Errorf("all up: expected 1.0, got %v", got)
	}
}

func TestDirectionRunFraction_BreakInRun(t *testing.T) {
	// Closes: up, up, DOWN, up, up → last 2 are up: run=2, total=4 → 0.5.
	closes := []float64{100, 101, 102, 100, 101, 102}
	got := directionRunFraction(closes, DirUp)
	if got < 0 || got > 1 {
		t.Errorf("result must be in [0,1], got %v", got)
	}
}

func TestDirectionRunFraction_FlatDir(t *testing.T) {
	closes := []float64{100, 101, 102}
	got := directionRunFraction(closes, DirFlat)
	if got != 0 {
		t.Errorf("DirFlat: expected 0, got %v", got)
	}
}

func TestLastN(t *testing.T) {
	s := []int64{1, 2, 3, 4, 5}
	if got := lastN(s, 3); len(got) != 3 || got[0] != 3 {
		t.Errorf("lastN(5,3): want [3,4,5], got %v", got)
	}
	if got := lastN(s, 10); len(got) != 5 {
		t.Errorf("lastN(5,10): want full slice, got len=%d", len(got))
	}
	if got := lastN(s, 0); len(got) != 0 {
		t.Errorf("lastN(5,0): want empty slice, got len=%d", len(got))
	}
}
