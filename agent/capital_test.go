package agent_test

import (
	"math/rand/v2"
	"testing"

	"pgregory.net/rapid"

	"simulation-agents/agent"
)

// newSeededRand builds a *rand.Rand from a single uint64 seed so property
// tests get both reproducible randomness and independent streams. The two
// PCG seed words are derived from the same seed by xoring with a fixed
// splitting constant so a single uint64 fully determines the stream.
func newSeededRand(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
}

// For any valid inputs with Floor <= ceiling, the drawn value is always
// within [Floor, ceiling]. The three fundedCash regimes (zero, negative,
// positive) all exercise the same bound.
func TestDrawTradingCapital_BoundsInvariant(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		floor := rapid.Int64Range(1, 1_000_000).Draw(rt, "floor")
		defaultFunded := rapid.Int64Range(floor, 10_000_000).Draw(rt, "default_funded")
		spread := rapid.Float64Range(0, 0.9).Draw(rt, "spread")
		capitalSize := rapid.Int64Range(1, 10_000_000).Draw(rt, "capital_size")

		var fundedCash int64
		switch rapid.IntRange(0, 2).Draw(rt, "funded_case") {
		case 0:
			fundedCash = 0
		case 1:
			fundedCash = -rapid.Int64Range(1, 1_000_000).Draw(rt, "neg_funded")
		case 2:
			fundedCash = rapid.Int64Range(floor, 10_000_000).Draw(rt, "pos_funded")
		}

		seed := rapid.Uint64().Draw(rt, "seed")

		params := agent.CapitalParams{
			Floor:             floor,
			JitterSpread:      spread,
			DefaultFundedCash: defaultFunded,
		}
		drawn := agent.DrawTradingCapital(capitalSize, fundedCash, newSeededRand(seed), params)

		ceiling := fundedCash
		if ceiling <= 0 {
			ceiling = defaultFunded
		}

		if drawn < floor {
			rt.Fatalf("drawn=%d below floor=%d (capitalSize=%d, spread=%f)", drawn, floor, capitalSize, spread)
		}
		if drawn > ceiling {
			rt.Fatalf("drawn=%d above ceiling=%d (fundedCash=%d, default=%d)", drawn, ceiling, fundedCash, defaultFunded)
		}
	})
}

// A non-positive fundedCash must force the fallback ceiling to
// DefaultFundedCash, regardless of how large the scale is.
func TestDrawTradingCapital_FallbackCeilingWhenFundedCashNonPositive(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		floor := rapid.Int64Range(1, 1_000_000).Draw(rt, "floor")
		defaultFunded := rapid.Int64Range(floor, 10_000_000).Draw(rt, "default_funded")
		spread := rapid.Float64Range(0, 0.9).Draw(rt, "spread")
		capitalSize := rapid.Int64Range(1, 10_000_000_000).Draw(rt, "capital_size")
		fundedCash := -rapid.Int64Range(0, 1_000_000).Draw(rt, "non_positive_funded")
		seed := rapid.Uint64().Draw(rt, "seed")

		params := agent.CapitalParams{Floor: floor, JitterSpread: spread, DefaultFundedCash: defaultFunded}
		drawn := agent.DrawTradingCapital(capitalSize, fundedCash, newSeededRand(seed), params)

		if drawn > defaultFunded {
			rt.Fatalf("drawn=%d exceeds fallback ceiling=%d (fundedCash=%d)", drawn, defaultFunded, fundedCash)
		}
	})
}

// Determinism: identical seed and inputs must produce identical output
// across independent rng instances (and runs).
func TestDrawTradingCapital_Deterministic(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		floor := rapid.Int64Range(1, 1_000_000).Draw(rt, "floor")
		defaultFunded := rapid.Int64Range(floor, 10_000_000).Draw(rt, "default_funded")
		spread := rapid.Float64Range(0, 0.9).Draw(rt, "spread")
		capitalSize := rapid.Int64Range(1, 10_000_000).Draw(rt, "capital_size")
		fundedCash := rapid.Int64Range(floor, 10_000_000).Draw(rt, "funded_cash")
		seed := rapid.Uint64().Draw(rt, "seed")

		params := agent.CapitalParams{Floor: floor, JitterSpread: spread, DefaultFundedCash: defaultFunded}
		first := agent.DrawTradingCapital(capitalSize, fundedCash, newSeededRand(seed), params)
		second := agent.DrawTradingCapital(capitalSize, fundedCash, newSeededRand(seed), params)

		if first != second {
			rt.Fatalf("non-deterministic: first=%d second=%d (seed=%d)", first, second, seed)
		}
	})
}

// Zero spread disables jitter: the output must be exactly the
// capital scale clamped into [Floor, ceiling], independent of the rng.
func TestDrawTradingCapital_ZeroSpreadIsPureClamp(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		floor := rapid.Int64Range(1, 1_000_000).Draw(rt, "floor")
		defaultFunded := rapid.Int64Range(floor, 10_000_000).Draw(rt, "default_funded")
		capitalSize := rapid.Int64Range(1, 10_000_000).Draw(rt, "capital_size")
		fundedCash := rapid.Int64Range(floor, 10_000_000).Draw(rt, "funded_cash")

		params := agent.CapitalParams{Floor: floor, JitterSpread: 0, DefaultFundedCash: defaultFunded}
		drawn := agent.DrawTradingCapital(capitalSize, fundedCash, newSeededRand(42), params)

		expected := capitalSize
		if expected < floor {
			expected = floor
		}
		if expected > fundedCash {
			expected = fundedCash
		}
		if drawn != expected {
			rt.Fatalf("zero-spread output not pure clamp: drawn=%d expected=%d", drawn, expected)
		}
	})
}
