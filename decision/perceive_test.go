package decision

import (
	"math/rand/v2"
	"testing"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// fullPerceptionTraits builds an endogenous TraitVector with all five diet
// channels on and a complete PerceptionProfile, ready to drive all channels.
func fullPerceptionTraits() agent.TraitVector {
	return agent.TraitVector{
		AgentKind: agent.AgentKindEndogenous,
		Skill:     0.9,
		Diet: &agent.Diet{
			Chart: true, OrderBook: true, MeanReversion: true,
			Volume: true, TimeOfSession: true,
		},
		Perception: &agent.PerceptionProfile{
			Chart: &agent.ChannelBlock{
				Method:         "sma",
				ReadingParams:  map[string]float64{"fast_period": 5, "slow_period": 20},
				ReadingCutoffs: map[string]float64{"flat_band": 0.001},
			},
			Range: &agent.ChannelBlock{
				Method:         "channel",
				ReadingParams:  map[string]float64{"window": 10},
				ReadingCutoffs: map[string]float64{"extreme_band": 0.5},
			},
			Liquidity: &agent.ChannelBlock{
				ReadingParams: map[string]float64{"depth_levels": 1},
			},
			Volume: &agent.ChannelBlock{
				Method: "mean",
				ReadingParams: map[string]float64{
					"avg_window": 5, "spike_multiple": 2.0, "band_width": 0.4,
				},
			},
			TimeOfSession: &agent.ChannelBlock{},
			Bias:          map[string]float64{},
			RereadSensitivity: map[string]float64{
				"chart": 0.01, "range": 0.1, "liquidity": 0.05, "volume": 0.2,
			},
		},
	}
}

// perceptionAccessor builds an Accessor with enough candles for all channels
// (>= slow_period=20 for chart, window=10 for range, avg_window+1=6 for vol)
// and a valid quote.
func perceptionAccessor(inst, tf string, price int64) *Accessor {
	candles := make([]store.Candle, 30)
	for i := range candles {
		p := price + int64(i)*5 // gently rising so MAs spread apart
		candles[i] = store.Candle{
			Open: p, High: p + 2, Low: p - 2, Close: p, Volume: 100,
		}
	}
	quote := store.Quote{
		LastPrice: price, HasLast: true,
		BidPrice: price - 5, AskPrice: price + 5, HasBid: true, HasAsk: true,
		Phase: "open",
		Depth: store.QuoteDepth{
			Bids: []store.QuoteLevel{{Price: price - 5, Qty: 200}},
			Asks: []store.QuoteLevel{{Price: price + 5, Qty: 150}},
		},
	}
	return NewAccessor(
		map[string]map[string][]store.Candle{inst: {tf: candles}},
		map[string]store.Quote{inst: quote},
		map[string]bool{inst: true},
		TickContext{},
	)
}

func perceiveParams() PerceptionParams {
	return PerceptionParams{SigmaPerc: 0, MASaturationMultiple: 2.0}
}

// --- Core bundle shape tests ---

func TestPerceive_AllChannelsNonNil(t *testing.T) {
	traits := fullPerceptionTraits()
	acc := perceptionAccessor("X", "1m", 1000)
	bundle, _ := Perceive(traits, acc, "X", "1m", noiseRNG(), perceiveParams())

	if bundle.Chart == nil {
		t.Error("Chart must be non-nil when diet.Chart=true")
	}
	if bundle.Range == nil {
		t.Error("Range must be non-nil when diet.MeanReversion=true")
	}
	if bundle.Liquidity == nil {
		t.Error("Liquidity must be non-nil when diet.OrderBook=true")
	}
	if bundle.Volume == nil {
		t.Error("Volume must be non-nil when diet.Volume=true")
	}
	if bundle.Time == nil {
		t.Error("Time must be non-nil when diet.TimeOfSession=true")
	}
}

func TestPerceive_OffChannel_NilPointer(t *testing.T) {
	// Chart-only diet: range/liquidity/volume/time must be nil.
	traits := fullPerceptionTraits()
	traits.Diet = &agent.Diet{Chart: true}
	acc := perceptionAccessor("X", "1m", 1000)

	bundle, _ := Perceive(traits, acc, "X", "1m", noiseRNG(), perceiveParams())

	if bundle.Chart == nil {
		t.Error("Chart must be non-nil when on")
	}
	if bundle.Range != nil {
		t.Error("Range must be nil when diet.MeanReversion=false")
	}
	if bundle.Liquidity != nil {
		t.Error("Liquidity must be nil when diet.OrderBook=false")
	}
	if bundle.Volume != nil {
		t.Error("Volume must be nil when diet.Volume=false")
	}
	if bundle.Time != nil {
		t.Error("Time must be nil when diet.TimeOfSession=false")
	}
}

func TestPerceive_HerdAgent_EmptyBundle(t *testing.T) {
	// Herd agents have no diet and no perception profile.
	traits := agent.TraitVector{
		AgentKind: agent.AgentKindHerd,
		Skill:     0.5,
		Diet:      nil,
		Perception: nil,
	}
	acc := perceptionAccessor("X", "1m", 1000)
	bundle, refs := Perceive(traits, acc, "X", "1m", noiseRNG(), perceiveParams())

	if bundle.Chart != nil || bundle.Range != nil || bundle.Liquidity != nil ||
		bundle.Volume != nil || bundle.Time != nil {
		t.Error("herd agent must produce all-nil bundle")
	}
	if refs != (store.PerceptionRefs{}) {
		t.Error("herd agent must produce zero refs")
	}
}

func TestPerceive_NilDiet_EmptyBundle(t *testing.T) {
	traits := fullPerceptionTraits()
	traits.Diet = nil
	acc := perceptionAccessor("X", "1m", 1000)
	bundle, refs := Perceive(traits, acc, "X", "1m", noiseRNG(), perceiveParams())

	if bundle.Chart != nil {
		t.Error("nil diet must produce nil chart")
	}
	if refs != (store.PerceptionRefs{}) {
		t.Error("nil diet must produce zero refs")
	}
}

// --- ReadOK propagation ---

func TestPerceive_ThinHistory_ChartReadOKFalse(t *testing.T) {
	// Only 3 bars — not enough for slow_period=20.
	candles := []store.Candle{
		{Close: 1000, Volume: 100},
		{Close: 1001, Volume: 100},
		{Close: 1002, Volume: 100},
	}
	traits := fullPerceptionTraits()
	traits.Diet = &agent.Diet{Chart: true}
	acc := NewAccessor(
		map[string]map[string][]store.Candle{"X": {"1m": candles}},
		map[string]store.Quote{"X": {LastPrice: 1002, HasLast: true}},
		map[string]bool{"X": true},
		TickContext{},
	)

	bundle, _ := Perceive(traits, acc, "X", "1m", noiseRNG(), perceiveParams())

	if bundle.Chart == nil {
		t.Fatal("Chart must be non-nil (on) even when unreadable")
	}
	if bundle.Chart.ReadOK {
		t.Error("Chart.ReadOK must be false with insufficient bars")
	}
}

func TestPerceive_Absent_AllReadOKFalse(t *testing.T) {
	traits := fullPerceptionTraits()
	acc := NewAccessor(
		map[string]map[string][]store.Candle{},
		map[string]store.Quote{},
		map[string]bool{"X": false},
		TickContext{},
	)

	bundle, _ := Perceive(traits, acc, "X", "1m", noiseRNG(), perceiveParams())

	for name, ptr := range map[string]interface{ getReadOK() bool }{
		"Chart":     readOKProxy{bundle.Chart != nil && bundle.Chart.ReadOK},
		"Range":     readOKProxy{bundle.Range != nil && bundle.Range.ReadOK},
		"Liquidity": readOKProxy{bundle.Liquidity != nil && bundle.Liquidity.ReadOK},
		"Volume":    readOKProxy{bundle.Volume != nil && bundle.Volume.ReadOK},
		"Time":      readOKProxy{bundle.Time != nil && bundle.Time.ReadOK},
	} {
		if ptr.getReadOK() {
			t.Errorf("%s.ReadOK must be false when instrument is absent", name)
		}
	}
}

type readOKProxy struct{ ok bool }

func (p readOKProxy) getReadOK() bool { return p.ok }

// --- Refs correctness ---

func TestPerceive_RefsPopulated(t *testing.T) {
	traits := fullPerceptionTraits()
	acc := perceptionAccessor("X", "1m", 2000)

	_, refs := Perceive(traits, acc, "X", "1m", noiseRNG(), perceiveParams())

	if refs.LastPrice == 0 {
		t.Error("refs.LastPrice must be populated after Perceive")
	}
	if refs.LastSpread == 0 {
		t.Error("refs.LastSpread must be populated after Perceive")
	}
	if refs.LastVolume == 0 {
		t.Error("refs.LastVolume must be populated after Perceive")
	}
	if refs.LastPhase == "" {
		t.Error("refs.LastPhase must be populated after Perceive")
	}
}

func TestPerceive_RefsPrice_MatchesCurrentPrice(t *testing.T) {
	traits := fullPerceptionTraits()
	acc := perceptionAccessor("X", "1m", 3000)
	price, _ := acc.CurrentPrice("X", "1m")

	_, refs := Perceive(traits, acc, "X", "1m", noiseRNG(), perceiveParams())

	if refs.LastPrice != price {
		t.Errorf("refs.LastPrice: want %v, got %v", price, refs.LastPrice)
	}
}

func TestPerceive_RefsPhase_MatchesQuote(t *testing.T) {
	traits := fullPerceptionTraits()
	acc := perceptionAccessor("X", "1m", 1000)

	_, refs := Perceive(traits, acc, "X", "1m", noiseRNG(), perceiveParams())

	if refs.LastPhase != "open" {
		t.Errorf("refs.LastPhase: want %q, got %q", "open", refs.LastPhase)
	}
}

func TestPerceive_Determinism(t *testing.T) {
	// Same seed → identical bundle field values across two calls.
	traits := fullPerceptionTraits()
	acc := perceptionAccessor("X", "1m", 1000)

	bundle1, refs1 := Perceive(traits, acc, "X", "1m", rand.New(rand.NewPCG(7, 0)), perceiveParams())
	bundle2, refs2 := Perceive(traits, acc, "X", "1m", rand.New(rand.NewPCG(7, 0)), perceiveParams())

	if refs1 != refs2 {
		t.Errorf("refs differ across identical runs: %+v vs %+v", refs1, refs2)
	}
	if bundle1.Chart == nil || bundle2.Chart == nil {
		t.Fatal("chart must be non-nil for determinism check")
	}
	if bundle1.Chart.Direction != bundle2.Chart.Direction {
		t.Error("chart direction not deterministic")
	}
	if bundle1.Chart.Strength != bundle2.Chart.Strength {
		t.Error("chart strength not deterministic")
	}
}

func TestPerceive_RangeReadOK_SetsLastRangePos(t *testing.T) {
	traits := fullPerceptionTraits()
	traits.Diet = &agent.Diet{MeanReversion: true}
	acc := perceptionAccessor("X", "1m", 1000)

	bundle, refs := Perceive(traits, acc, "X", "1m", noiseRNG(), perceiveParams())

	if bundle.Range != nil && bundle.Range.ReadOK {
		// When range channel produces a valid reading, refs.LastRangePos is set.
		if refs.LastRangePos == 0 && bundle.Range.PositionInRange != 0 {
			t.Errorf("refs.LastRangePos not mirrored from bundle: bundle=%v refs=%v",
				bundle.Range.PositionInRange, refs.LastRangePos)
		}
	}
}

// Regression: calling Perceive twice with different RNG seeds must differ on
// at least one perturbed field when SigmaPerc is large and skill = 0. We use
// a nearly-flat market so rawStrength is not saturated and noise can move it.
func TestPerceive_NoisyAgent_ProducesVariation(t *testing.T) {
	traits := fullPerceptionTraits()
	traits.Skill = 0.0 // maximum noise

	// Nearly-flat candles — rawSpread will be tiny so rawStrength << 1 and
	// noise has room to produce distinct outputs.
	candles := make([]store.Candle, 30)
	for i := range candles {
		p := int64(1000)
		candles[i] = store.Candle{Open: p, High: p + 1, Low: p - 1, Close: p, Volume: 100}
	}
	// Last bar closes slightly higher so SMA fast > slow (just barely).
	candles[29] = store.Candle{Open: 1000, High: 1001, Low: 999, Close: 1001, Volume: 100}

	acc := NewAccessor(
		map[string]map[string][]store.Candle{"X": {"1m": candles}},
		map[string]store.Quote{"X": {
			LastPrice: 1001, HasLast: true,
			BidPrice: 996, AskPrice: 1006, HasBid: true, HasAsk: true, Phase: "open",
			Depth: store.QuoteDepth{
				Bids: []store.QuoteLevel{{Qty: 100}},
				Asks: []store.QuoteLevel{{Qty: 100}},
			},
		}},
		map[string]bool{"X": true},
		TickContext{},
	)
	noisyParams := PerceptionParams{SigmaPerc: 0.5, MASaturationMultiple: 2.0}

	// Run many (seed, seed+1) pairs; at least one must differ on strength or direction.
	varied := false
	for seed := uint64(0); seed < 50; seed++ {
		b1, _ := Perceive(traits, acc, "X", "1m", rand.New(rand.NewPCG(seed, 0)), noisyParams)
		b2, _ := Perceive(traits, acc, "X", "1m", rand.New(rand.NewPCG(seed+100, 0)), noisyParams)
		if b1.Chart == nil || b2.Chart == nil {
			continue
		}
		if !b1.Chart.ReadOK || !b2.Chart.ReadOK {
			continue
		}
		if b1.Chart.Strength != b2.Chart.Strength || b1.Chart.Direction != b2.Chart.Direction {
			varied = true
			break
		}
	}
	if !varied {
		t.Error("high-noise agent (skill=0, SigmaPerc=0.5) must produce varying chart output across different seeds")
	}
}
