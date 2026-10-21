package decision

import (
	"testing"
	"time"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// gateAccessor builds an Accessor for gate tests with a known price, spread,
// volume, and phase.
func gateAccessor(inst, tf string, price, bid, ask, volume int64, phase string, present bool) *Accessor {
	var quote store.Quote
	if price > 0 {
		quote.LastPrice = price
		quote.HasLast = true
	}
	if bid > 0 && ask > 0 {
		quote.BidPrice = bid
		quote.AskPrice = ask
		quote.HasBid = true
		quote.HasAsk = true
	}
	quote.Phase = phase

	candles := make([]store.Candle, 25)
	for i := range candles {
		candles[i] = store.Candle{
			Open: price, High: price + 2, Low: price - 2,
			Close: price, Volume: volume,
		}
	}

	return NewAccessor(
		map[string]map[string][]store.Candle{inst: {tf: candles}},
		map[string]store.Quote{inst: quote},
		map[string]bool{inst: present},
		TickContext{
			Now:          time.Now(),
			SessionStart: time.Now().Add(-time.Hour),
			SessionEnd:   time.Now().Add(time.Hour),
		},
	)
}

// fullDiet returns a Diet with all five channels enabled.
func fullDiet() *agent.Diet {
	return &agent.Diet{Chart: true, OrderBook: true, MeanReversion: true, Volume: true, TimeOfSession: true}
}

// sensitiveTraits builds a TraitVector with all channels at a low threshold
// so small changes trigger a reperceive.
func sensitiveTraits(threshold float64) agent.TraitVector {
	return agent.TraitVector{
		Skill: 1.0,
		Diet:  fullDiet(),
		Perception: &agent.PerceptionProfile{
			RereadSensitivity: map[string]float64{
				"chart":     threshold,
				"liquidity": threshold,
				"volume":    threshold,
				"range":     threshold,
			},
		},
	}
}

func TestShouldReperceive_EmptyRefs(t *testing.T) {
	acc := gateAccessor("X", "1m", 1000, 995, 1005, 100, "open", true)
	traits := sensitiveTraits(0.01)

	// Empty refs → always reperceive regardless of market state.
	if !ShouldReperceive(traits, acc, "X", "1m", store.PerceptionRefs{}) {
		t.Fatal("empty refs must trigger reperceive")
	}
}

func TestShouldReperceive_Absent(t *testing.T) {
	acc := gateAccessor("X", "1m", 1000, 995, 1005, 100, "open", false)
	traits := sensitiveTraits(0.001)
	refs := store.PerceptionRefs{LastPrice: 1000, LastSpread: 10, LastVolume: 100, LastPhase: "open"}

	// Absent instrument → coast (false).
	if ShouldReperceive(traits, acc, "X", "1m", refs) {
		t.Fatal("absent instrument must return false (coast)")
	}
}

func TestShouldReperceive_NoChange(t *testing.T) {
	acc := gateAccessor("X", "1m", 1000, 995, 1005, 100, "open", true)
	traits := sensitiveTraits(0.01)
	refs := store.PerceptionRefs{LastPrice: 1000, LastSpread: 10, LastVolume: 100, LastPhase: "open"}

	// Nothing changed → coast.
	if ShouldReperceive(traits, acc, "X", "1m", refs) {
		t.Fatal("no market change should coast")
	}
}

func TestShouldReperceive_PriceChange_TriggersChart(t *testing.T) {
	// Price moved 5% — above 1% chart threshold.
	acc := gateAccessor("X", "1m", 1050, 1045, 1055, 100, "open", true)
	traits := sensitiveTraits(0.01)
	refs := store.PerceptionRefs{LastPrice: 1000, LastSpread: 10, LastVolume: 100, LastPhase: "open"}

	if !ShouldReperceive(traits, acc, "X", "1m", refs) {
		t.Fatal("5% price change above 1% chart threshold must trigger reperceive")
	}
}

func TestShouldReperceive_SmallPriceChange_DoesNotTrigger(t *testing.T) {
	// Price moved 0.1% — below 1% threshold.
	acc := gateAccessor("X", "1m", 1001, 996, 1006, 100, "open", true)
	traits := sensitiveTraits(0.01)
	refs := store.PerceptionRefs{LastPrice: 1000, LastSpread: 10, LastVolume: 100, LastPhase: "open"}

	if ShouldReperceive(traits, acc, "X", "1m", refs) {
		t.Fatal("0.1% price change below 1% threshold must not trigger")
	}
}

func TestShouldReperceive_SpreadChange_TriggersLiquidity(t *testing.T) {
	// Spread doubled from 10 to 20 ticks (100% change > 1% threshold).
	acc := gateAccessor("X", "1m", 1000, 990, 1010, 100, "open", true)
	traits := sensitiveTraits(0.01)
	refs := store.PerceptionRefs{LastPrice: 1000, LastSpread: 10, LastVolume: 100, LastPhase: "open"}

	if !ShouldReperceive(traits, acc, "X", "1m", refs) {
		t.Fatal("large spread change must trigger reperceive via liquidity channel")
	}
}

func TestShouldReperceive_VolumeSpike_TriggersVolume(t *testing.T) {
	// Volume tripled (200% change > 1% threshold).
	acc := gateAccessor("X", "1m", 1000, 995, 1005, 300, "open", true)
	traits := sensitiveTraits(0.01)
	refs := store.PerceptionRefs{LastPrice: 1000, LastSpread: 10, LastVolume: 100, LastPhase: "open"}

	if !ShouldReperceive(traits, acc, "X", "1m", refs) {
		t.Fatal("3× volume spike must trigger reperceive via volume channel")
	}
}

func TestShouldReperceive_PhaseChange_TriggersTime(t *testing.T) {
	// Phase changed open → closing.
	acc := gateAccessor("X", "1m", 1000, 995, 1005, 100, "closing", true)
	traits := sensitiveTraits(0.01)
	refs := store.PerceptionRefs{LastPrice: 1000, LastSpread: 10, LastVolume: 100, LastPhase: "open"}

	if !ShouldReperceive(traits, acc, "X", "1m", refs) {
		t.Fatal("phase change must trigger reperceive via time channel")
	}
}

func TestShouldReperceive_ZeroThreshold_DoesNotTrigger(t *testing.T) {
	// Threshold = 0 → channel is effectively disabled (calibration not provided).
	acc := gateAccessor("X", "1m", 1050, 1045, 1055, 100, "open", true)
	traits := agent.TraitVector{
		Skill: 1.0,
		Diet:  &agent.Diet{Chart: true},
		Perception: &agent.PerceptionProfile{
			RereadSensitivity: map[string]float64{"chart": 0},
		},
	}
	refs := store.PerceptionRefs{LastPrice: 1000, LastSpread: 10, LastVolume: 100, LastPhase: "open"}

	if ShouldReperceive(traits, acc, "X", "1m", refs) {
		t.Fatal("zero threshold must not trigger reperceive")
	}
}

func TestShouldReperceive_NilDiet(t *testing.T) {
	acc := gateAccessor("X", "1m", 1050, 1045, 1055, 100, "open", true)
	traits := agent.TraitVector{Skill: 1.0, Diet: nil}
	refs := store.PerceptionRefs{LastPrice: 1000, LastSpread: 10}

	// nil diet → coast.
	if ShouldReperceive(traits, acc, "X", "1m", refs) {
		t.Fatal("nil diet must coast")
	}
}

func TestShouldReperceive_OffChannelIgnored(t *testing.T) {
	// Only chart channel is on. Volume spike should NOT trigger.
	acc := gateAccessor("X", "1m", 1001, 995, 1005, 300, "open", true)
	traits := agent.TraitVector{
		Skill: 1.0,
		Diet:  &agent.Diet{Chart: true}, // volume is OFF
		Perception: &agent.PerceptionProfile{
			RereadSensitivity: map[string]float64{
				"chart":  0.1,  // 0.1% price change threshold
				"volume": 0.01, // very sensitive if it were on
			},
		},
	}
	refs := store.PerceptionRefs{LastPrice: 1000, LastSpread: 10, LastVolume: 100, LastPhase: "open"}

	// Price moved 0.1%, volume 3× — only chart is checked.
	// 0.1% == 0.1% threshold is NOT greater, so should coast.
	if ShouldReperceive(traits, acc, "X", "1m", refs) {
		t.Fatal("off-channel volume spike must not trigger reperceive")
	}
}

func TestCloseSpan(t *testing.T) {
	cases := []struct {
		closes []float64
		want   float64
	}{
		{[]float64{100, 110, 90, 105}, 20},
		{[]float64{100}, 0},
		{nil, 0},
		{[]float64{50, 50, 50}, 0},
	}
	for _, c := range cases {
		got := closeSpan(c.closes)
		if got != c.want {
			t.Errorf("closeSpan(%v) = %v, want %v", c.closes, got, c.want)
		}
	}
}
