package agent

import "math/rand/v2"

// CapitalParams holds the three dials DrawTradingCapital reads. Values
// are calibration — never fixed in this package.
type CapitalParams struct {
	Floor             int64   // lower bound on the drawn session money
	JitterSpread      float64 // multiplier is drawn uniformly from [1-JitterSpread, 1+JitterSpread]; zero disables jitter
	DefaultFundedCash int64   // fallback ceiling when the broker's funded_cash is unknown or non-positive
}

// DrawTradingCapital produces an agent's session money from its frozen
// capital scale and the broker's funding ceiling. The result is
// deterministic for a given rng state and params.
//
// Shape: trading_capital = clamp(capitalSize * jitter, Floor, ceiling),
// where ceiling is fundedCash when positive and DefaultFundedCash when
// the broker response omitted or zeroed funded_cash.
//
// Exported because the generator phase lives in a sibling package and
// calls this as its shared capital primitive — the three-case dispatch
// (new / existing-reused / revived) wraps it there.
func DrawTradingCapital(capitalSize int64, fundedCash int64, rng *rand.Rand, params CapitalParams) int64 {
	ceiling := fundedCash
	if ceiling <= 0 {
		ceiling = params.DefaultFundedCash
	}
	scaled := int64(float64(capitalSize) * drawJitter(rng, params.JitterSpread))
	return clamp(scaled, params.Floor, ceiling)
}

// drawJitter samples a uniform multiplier in [1-spread, 1+spread]. A
// non-positive spread disables jitter and returns exactly 1, which makes
// DrawTradingCapital a pure clamp of the scale — useful for calibration
// runs where jitter noise would mask the dial's effect.
func drawJitter(rng *rand.Rand, spread float64) float64 {
	if spread <= 0 {
		return 1
	}
	return 1 - spread + rng.Float64()*2*spread
}

// clamp constrains value to [low, high]. If a mis-calibrated caller
// passes low > high, the result pins to low — the floor is treated as
// the stronger invariant (an agent below the floor has no runway at all,
// whereas an agent slightly above the ceiling just over-funds).
func clamp(value, low, high int64) int64 {
	return max(low, min(high, value))
}
