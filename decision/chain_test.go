package decision

import (
	"math/rand/v2"
	"reflect"
	"testing"

	"pgregory.net/rapid"

	"simulation-agents/agent"
)

// chainResult is the test harness's full answer for one simulated
// tick: the candidate intent (if any) plus every piece of working
// memory the caller would carry into the next tick. Mirrors the shape
// §9's control-flow pseudocode describes mutating.
type chainResult struct {
	Candidate           *ExecIntent
	PendingWatch        *PendingWatch
	WaitStreak          int
	LastDecidedReadings map[string]float64
	LastCommitTick      int64
	StandingStance      string
}

// runDecisionChain is a TEST-ONLY harness that calls noiseImpulse,
// gateDecide, mix, and commit in the order the IMPL doc's §9
// control-flow pseudocode describes — noise coin first, independent of
// gate state, then the gate, then (only on RE-DECIDE) the mixer and
// the commit gate. It exists purely so the combined behavior of the
// four pure functions built in this package can be exercised in a
// test; the real chain-stitching for production is a separate,
// later tree-assembly stage, not this one.
func runDecisionChain(
	verdicts traitVerdicts,
	traits agent.TraitVector,
	currentTick int64,
	sessionProgress float64,
	liquidity *LiquidityReading,
	currentReadings, lastDecidedReadings, sensitivities map[string]float64,
	agentRNG *rand.Rand,
	params Params,
	pendingWatch *PendingWatch,
	waitStreak int,
	lastCommitTick int64,
) chainResult {
	if impulse, hit := noiseImpulse(*traits.Disposition, agentRNG, params.Mixer); hit {
		return chainResult{
			Candidate:           &ExecIntent{Action: ExecIntentActionOpen, Direction: impulse.Direction, Conviction: impulse.Conviction},
			PendingWatch:        nil,
			WaitStreak:          waitStreak,
			LastDecidedReadings: lastDecidedReadings,
			LastCommitTick:      currentTick,
			StandingStance:      "FLAT_IDLE",
		}
	}

	switch gateDecide(pendingWatch, waitStreak, currentReadings, lastDecidedReadings, sensitivities, params.Gate) {
	case gateCoast:
		newWaitStreak := waitStreak
		standingStance := "FLAT_IDLE"
		if pendingWatch != nil {
			newWaitStreak++
			standingStance = "WAIT"
		}
		return chainResult{
			PendingWatch:        pendingWatch,
			WaitStreak:          newWaitStreak,
			LastDecidedReadings: lastDecidedReadings,
			LastCommitTick:      lastCommitTick,
			StandingStance:      standingStance,
		}

	default: // gateRedecide
		opinion := mix(verdicts, *traits.Disposition, agentRNG, params.Mixer)
		if opinion == nil {
			return chainResult{
				PendingWatch:        nil,
				WaitStreak:          0,
				LastDecidedReadings: currentReadings,
				LastCommitTick:      lastCommitTick,
				StandingStance:      "FLAT_IDLE",
			}
		}

		result := commit(*opinion, traits, currentTick, sessionProgress, liquidity, lastCommitTick, pendingWatch, params)
		newLastCommitTick := lastCommitTick
		standingStance := "FLAT_IDLE"
		var candidate *ExecIntent
		switch result.Outcome {
		case commitCommit:
			candidate = &ExecIntent{Action: ExecIntentActionOpen, Direction: result.Direction, Conviction: result.EffectiveConviction}
			newLastCommitTick = currentTick
		case commitWait:
			standingStance = "WAIT"
		case commitSkip:
			// standingStance stays FLAT_IDLE, PendingWatch stays nil (already nil in result).
		}
		return chainResult{
			Candidate:           candidate,
			PendingWatch:        result.PendingWatch,
			WaitStreak:          0,
			LastDecidedReadings: currentReadings,
			LastCommitTick:      newLastCommitTick,
			StandingStance:      standingStance,
		}
	}
}

func testChainParams() Params {
	params := testCommitParams()
	params.Mixer = testMixerParams()
	params.Gate = GateParams{WaitReassessThreshold: 5}
	return params
}

// Determinism: the full chain, run twice against byte-identical
// inputs under the same seed, must produce byte-identical results —
// both the candidate intent and every working-memory mutation. This
// sweeps a wide range of verdicts/weights/traits/wait-streaks via
// rapid so the guarantee isn't just checked against one hand-picked
// fixture.
func TestDecisionChain_DeterministicUnderFixedSeed(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		trendVerdict := rapid.Float64Range(-1, 1).Draw(rt, "trend_verdict")
		meanRevVerdict := rapid.Float64Range(-1, 1).Draw(rt, "mean_rev_verdict")
		trendWeight := rapid.Float64Range(0, 1).Draw(rt, "trend_weight")
		meanRevWeight := rapid.Float64Range(0, 1).Draw(rt, "mean_rev_weight")
		noiseWeight := rapid.Float64Range(0, 1).Draw(rt, "noise_weight")
		thetaAct := rapid.Float64Range(0, 1).Draw(rt, "theta_act")
		needsConfirm := rapid.Bool().Draw(rt, "needs_confirm")
		confirmsNeeded := rapid.IntRange(1, 5).Draw(rt, "confirms_needed")
		waitStreak := rapid.IntRange(0, 10).Draw(rt, "wait_streak")
		seed := rapid.Uint64().Draw(rt, "seed")

		traits := testTraits(thetaAct, needsConfirm, confirmsNeeded)
		traits.Disposition = &agent.Disposition{Trend: trendWeight, MeanRev: meanRevWeight, Noise: noiseWeight}
		verdicts := traitVerdicts{trend: &trendVerdict, meanRev: &meanRevVerdict}
		currentReadings := map[string]float64{"chart.direction": 0.5}
		lastDecidedReadings := map[string]float64{"chart.direction": 0.1}
		sensitivities := map[string]float64{"chart.direction": 0.1}
		params := testChainParams()

		newSeededRNG := func() *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)) }

		first := runDecisionChain(verdicts, traits, 100, 0.5, nil, currentReadings, lastDecidedReadings, sensitivities, newSeededRNG(), params, nil, waitStreak, NoPriorCommit)
		second := runDecisionChain(verdicts, traits, 100, 0.5, nil, currentReadings, lastDecidedReadings, sensitivities, newSeededRNG(), params, nil, waitStreak, NoPriorCommit)

		if !reflect.DeepEqual(first, second) {
			rt.Fatalf("non-deterministic: first=%+v second=%+v", first, second)
		}
	})
}

// Anti-deadlock property: a thin-diet agent (no chart, no range —
// both verdicts always absent) with a meaningfully noisy disposition
// must never be structurally unable to produce a candidate. Sweeping
// noise weight and sampling many independent ticks per weight proves
// the impulse mechanism actually reaches the candidate-producing
// branch — not just that the probability is nonzero on paper.
func TestDecisionChain_AntiDeadlock_NoiseProducesCandidatesWithNoVerdictsPresent(t *testing.T) {
	traits := testTraits(0.1, true, 2)
	params := testChainParams()

	rapid.Check(t, func(rt *rapid.T) {
		noiseWeight := rapid.Float64Range(0.05, 1).Draw(rt, "noise_weight")
		traits.Disposition = &agent.Disposition{Trend: 0, MeanRev: 0, Noise: noiseWeight}

		const sampleSize = 200
		candidateCount := 0
		for seed := uint64(0); seed < sampleSize; seed++ {
			agentRNG := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
			// Thin diet: no consumed fields, no chart/range verdicts at
			// all. The only possible source of a candidate this tick is
			// the noise impulse.
			result := runDecisionChain(traitVerdicts{}, traits, int64(seed), 0.5, nil, nil, nil, nil, agentRNG, params, nil, 0, NoPriorCommit)
			if result.Candidate != nil {
				candidateCount++
			}
		}

		if candidateCount == 0 {
			rt.Fatalf("noise_weight=%v: zero candidates across %d ticks with both verdicts absent — a thin-diet agent must never be structurally unable to act", noiseWeight, sampleSize)
		}
	})
}
