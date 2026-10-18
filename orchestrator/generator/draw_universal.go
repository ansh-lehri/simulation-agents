package generator

import (
	"math"
	"math/rand/v2"

	"simulation-agents/agent"
)

// This file holds the trait draws that every agent — endogenous or herd —
// carries. Order matches the kind-first draw contract: kind is drawn first
// (so the branch that gates the rest of the vector is decided before any
// trait that depends on it), then the universal numeric axes. The
// universal numeric axes are consumed in a fixed order across every mint
// so the RNG sequence is reproducible from the seed alone.

// drawAgentKind flips a weighted coin against herdProbability. A herd
// agent follows the active tip rather than reading the market itself; an
// endogenous agent reads channels and decides on its own. The coin is the
// first per-agent draw so this branch gates every subsequent draw and
// keeps a mint's RNG consumption deterministic from the seed alone.
func drawAgentKind(rng *rand.Rand, herdProbability float64) string {
	if rng.Float64() < herdProbability {
		return agent.AgentKindHerd
	}
	return agent.AgentKindEndogenous
}

// drawSkill samples the execution-quality trait. It is the fidelity of
// carrying out whichever decision the agent reached — low skill means a
// noisy, slow entry; high skill means a clean one — and is NEVER a
// win-rate dial. The sample lands in [0, 1].
func drawSkill(rng *rand.Rand, pair BetaPair) float64 {
	return drawBeta(rng, pair.Alpha, pair.Beta)
}

// drawAggressiveness samples the market-vs-limit propensity trait: the
// taker/maker balance of the agent's orders. The sample lands in [0, 1];
// 0 agents never cross the spread, 1 agents always take.
func drawAggressiveness(rng *rand.Rand, pair BetaPair) float64 {
	return drawBeta(rng, pair.Alpha, pair.Beta)
}

// drawCapitalSize samples the frozen capital scale — the agent's bankroll
// anchor — from a log-normal distribution. Rounded to int64 coins to
// match the money type used end-to-end in this process; floored at one
// coin so a pathological log-normal tail cannot produce an unspendable
// agent. The frozen scale is a different number from the per-session
// trading capital (that one is drawn later against the broker's funding
// ceiling by the capital-resolver step).
func drawCapitalSize(rng *rand.Rand, pair LogNormalPair) int64 {
	rawScale := drawLogNormal(rng, pair.Mu, pair.Sigma)
	rounded := int64(math.Round(rawScale))
	if rounded < 1 {
		return 1
	}
	return rounded
}

// drawPnlReactivity samples the per-agent sensitivity dial that governs
// how strongly the agent's behaviour shifts as its own running profit and
// loss moves. The sample lands in [0, 1]: 0 is steady/disciplined, 1 is
// maximally reactive (panic/capitulation or revenge/tilt at the
// extremes). The live profit-and-loss is read fresh from the broker at
// run time; only the sensitivity is frozen here.
func drawPnlReactivity(rng *rand.Rand, pair BetaPair) float64 {
	return drawBeta(rng, pair.Alpha, pair.Beta)
}

// drawPAct samples the per-tick act-probability trait: the probability
// that the agent produces an order on any given scheduler tick while
// active. The sample lands in [0, 1]; a slow/fast shape in the Beta
// parameters sets the population's cadence profile.
func drawPAct(rng *rand.Rand, pair BetaPair) float64 {
	return drawBeta(rng, pair.Alpha, pair.Beta)
}
