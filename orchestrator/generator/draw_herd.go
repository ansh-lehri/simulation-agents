package generator

import "math/rand/v2"

// This file holds the herd-only trait draws. A herd agent's whole
// behaviour is parameterized by two universal numbers (skill,
// aggressiveness, capital_size, pnl_reactivity, p_act — all drawn by
// the universal pass) plus the one herd-specific credulity dial drawn
// here.

// drawActProb samples the per-agent tip credulity in [0, 1]: 0 agents
// ignore every tip even after noticing it, 1 agents act on every tip
// they notice. It is a dedicated herd dial — NOT derived from the other
// trait axes — because "notices the tip" and "trusts the tip" are
// independent facets and sampling credulity off aggressiveness or skill
// would smuggle a hidden correlation into the follower population.
func drawActProb(rng *rand.Rand, pair BetaPair) float64 {
	return drawBeta(rng, pair.Alpha, pair.Beta)
}
