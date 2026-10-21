package generator

import (
	"math"
	"math/rand/v2"
)

// drawThetaAct places the commit-bar trait inside the configured range using
// aggressiveness as the driver. Aggressive agents (high aggressiveness) land
// near the low end of the range (low bar — they commit readily); cautious
// agents land near the high end (high bar — they need stronger conviction).
// The (1−aggressiveness) direction is identical to the (1−reactivity) rule
// used for perception fields; only the driver axis differs.
func drawThetaAct(rng *rand.Rand, aggressiveness float64, params GenParams) float64 {
	return centerFromReactivity(
		rng,
		params.ThetaActRange.Low,
		params.ThetaActRange.High,
		aggressiveness,
		params.SigmaIdio,
	)
}

// drawNeedsConfirm flips the immediate-vs-waiter coin. P(immediate) equals
// aggressiveness directly — an agent with aggressiveness=0.9 acts immediately
// 90% of the time. The result is the needs_confirm flag: false means the
// agent commits without waiting for confirmations, true means it waits.
// No range param is needed; the probability is the driver value itself.
func drawNeedsConfirm(rng *rand.Rand, aggressiveness float64) bool {
	immediate := rng.Float64() < aggressiveness
	return !immediate
}

// drawConfirmsNeeded places the confirmation-count trait inside the configured
// integer range using aggressiveness as the driver. Aggressive agents land near
// the low end (fewer confirmations required); cautious agents land near the
// high end (more patience). The draw is unconditional — it runs even when
// needs_confirm is false so the RNG stream length does not depend on the
// outcome of a prior draw.
func drawConfirmsNeeded(rng *rand.Rand, aggressiveness float64, params GenParams) int {
	centered := centerFromReactivity(
		rng,
		float64(params.ConfirmsNeededRange.Low),
		float64(params.ConfirmsNeededRange.High),
		aggressiveness,
		params.SigmaIdio,
	)
	rounded := int(math.Round(centered))
	return clampInt(rounded, params.ConfirmsNeededRange.Low, params.ConfirmsNeededRange.High)
}

// drawCommitGateTraits runs all three commit-gate draws in order and returns
// their values. The caller (the tail of the endogenous draw sequence) assigns
// the results to the trait vector; keeping the three draws together here makes
// the ordering explicit and keeps draw_traits.go uncluttered.
func drawCommitGateTraits(rng *rand.Rand, aggressiveness float64, params GenParams) (thetaAct float64, needsConfirm bool, confirmsNeeded int) {
	thetaAct = drawThetaAct(rng, aggressiveness, params)
	needsConfirm = drawNeedsConfirm(rng, aggressiveness)
	confirmsNeeded = drawConfirmsNeeded(rng, aggressiveness, params)
	return
}
