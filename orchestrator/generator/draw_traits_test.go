package generator

import (
	"math"
	"reflect"
	"testing"

	"simulation-agents/agent"
)

// TestDrawTraitVectorIsDeterministic pins the standing invariant for the
// whole trait draw: a fixed seed replays an identical trait vector. The
// test runs the draw twice from the same seed over the same validated
// params and asserts byte-identical outputs. If a future refactor
// introduces a non-deterministic map iteration or a time-based source,
// this fails loudly.
func TestDrawTraitVectorIsDeterministic(t *testing.T) {
	params := completeGenParams()

	firstPass := drawTraitVector(seededRNG(1, 2), params, nil)
	secondPass := drawTraitVector(seededRNG(1, 2), params, nil)

	if !reflect.DeepEqual(firstPass, secondPass) {
		t.Fatalf("determinism broken:\nfirst = %+v\nsecond = %+v", firstPass, secondPass)
	}
}

// TestDrawTraitVectorKindFirstGating pins the kind-first invariant: a
// herd agent carries ONLY the universal fields plus act_prob; an
// endogenous agent carries the full endogenous block (minus perception,
// which is populated in the next stage of the trait draw build).
func TestDrawTraitVectorKindFirstGating(t *testing.T) {
	params := completeGenParams()

	seenHerd := false
	seenEndogenous := false

	// Walk enough seeds to see both kinds. Herd probability is 0.1 in
	// the complete-params helper so 400 seeds covers both branches with
	// very high confidence.
	for seedCounter := uint64(0); seedCounter < 400 && !(seenHerd && seenEndogenous); seedCounter++ {
		traits := drawTraitVector(seededRNG(seedCounter, 1), params, nil)

		switch traits.AgentKind {
		case agent.AgentKindHerd:
			seenHerd = true
			if traits.ActProb == nil {
				t.Fatalf("herd agent missing act_prob (seed %d)", seedCounter)
			}
			if traits.Disposition != nil || traits.Diet != nil ||
				traits.Rho != nil || traits.Phi != nil ||
				traits.Exit != nil || traits.MaxHold != nil {
				t.Fatalf("herd agent carries endogenous-only fields (seed %d): %+v",
					seedCounter, traits)
			}
			if traits.Perception != nil {
				t.Fatalf("herd agent carries perception block (seed %d)", seedCounter)
			}
		case agent.AgentKindEndogenous:
			seenEndogenous = true
			if traits.ActProb != nil {
				t.Fatalf("endogenous agent carries herd-only act_prob (seed %d)", seedCounter)
			}
			if traits.Disposition == nil || traits.Diet == nil ||
				traits.Rho == nil || traits.Phi == nil ||
				traits.Perception == nil ||
				traits.Exit == nil || traits.MaxHold == nil {
				t.Fatalf("endogenous agent missing an endogenous field (seed %d): %+v",
					seedCounter, traits)
			}
		default:
			t.Fatalf("unknown agent kind %q (seed %d)", traits.AgentKind, seedCounter)
		}

		if traits.IsTipper {
			t.Fatalf("is_tipper flag set inside the draw (seed %d) — the top-level generator owns it", seedCounter)
		}
	}

	if !seenHerd {
		t.Fatalf("never saw a herd agent across 400 seeds (herd probability misconfigured?)")
	}
	if !seenEndogenous {
		t.Fatalf("never saw an endogenous agent across 400 seeds")
	}
}

// TestDrawTraitVectorUniversalRanges walks a batch of seeds and asserts
// every universal numeric field lands in its documented box. The test is
// cheap and catches off-by-one swaps on any Beta argument.
func TestDrawTraitVectorUniversalRanges(t *testing.T) {
	params := completeGenParams()

	for seedCounter := uint64(0); seedCounter < 200; seedCounter++ {
		traits := drawTraitVector(seededRNG(seedCounter, 11), params, nil)

		assertInUnit(t, "Skill", traits.Skill, seedCounter)
		assertInUnit(t, "Aggressiveness", traits.Aggressiveness, seedCounter)
		assertInUnit(t, "PnlReactivity", traits.PnlReactivity, seedCounter)
		assertInUnit(t, "PAct", traits.PAct, seedCounter)
		if traits.CapitalSize < 1 {
			t.Fatalf("CapitalSize floor violated: got %d (seed %d)", traits.CapitalSize, seedCounter)
		}
		if traits.ActProb != nil {
			assertInUnit(t, "ActProb", *traits.ActProb, seedCounter)
		}
		if traits.Rho != nil {
			assertInUnit(t, "Rho", *traits.Rho, seedCounter)
		}
		if traits.Phi != nil && (*traits.Phi < -1 || *traits.Phi > 1) {
			t.Fatalf("Phi out of [-1,1]: got %v (seed %d)", *traits.Phi, seedCounter)
		}
	}
}

func assertInUnit(t *testing.T, name string, value float64, seedWord uint64) {
	t.Helper()
	if value < 0 || value > 1 {
		t.Fatalf("%s out of [0,1]: got %v (seed %d)", name, value, seedWord)
	}
}

// TestBimodalShiftPulsesThroughToOptimism confirms the one scenario-aware
// branch: a non-nil BimodalOptimism on the overrides struct noticeably
// shifts the optimism distribution relative to the baseline. The test
// does not care about the exact shape, only that the override reaches
// the draw — a regression that silently drops the override would show up
// as near-identical means.
func TestBimodalShiftPulsesThroughToOptimism(t *testing.T) {
	baselineMean := meanOptimism(t, nil)
	polarizedMean := meanOptimism(t, &EffectiveParams{
		PhiBimodal: &BimodalOptimism{
			MixWeight:      0.5,
			FirstModeBeta:  BetaPair{Alpha: 8, Beta: 2}, // skews near +1
			SecondModeBeta: BetaPair{Alpha: 2, Beta: 8}, // skews near -1
		},
	})
	// The two means should both be near zero (symmetric baseline, mirror
	// polarized shift), but the per-draw variance of the polarized shift
	// is much larger. The test instead checks that the polarized spread
	// is noticeably larger than the baseline spread — the fingerprint of
	// a bimodal mixture.
	baselineSpread := spreadOptimism(t, nil)
	polarizedSpread := spreadOptimism(t, &EffectiveParams{
		PhiBimodal: &BimodalOptimism{
			MixWeight:      0.5,
			FirstModeBeta:  BetaPair{Alpha: 8, Beta: 2},
			SecondModeBeta: BetaPair{Alpha: 2, Beta: 8},
		},
	})
	if polarizedSpread <= baselineSpread {
		t.Fatalf("bimodal override did not widen optimism spread: baselineSpread=%v polarizedSpread=%v",
			baselineSpread, polarizedSpread)
	}
	// Sanity check on symmetry: neither mean is far from zero at this
	// sample size.
	if math.Abs(baselineMean) > 0.1 {
		t.Fatalf("baseline optimism mean drifted: %v", baselineMean)
	}
	if math.Abs(polarizedMean) > 0.2 {
		t.Fatalf("polarized optimism mean drifted: %v", polarizedMean)
	}
}

func meanOptimism(t *testing.T, scenarioShifts *EffectiveParams) float64 {
	t.Helper()
	params := completeGenParams()
	total, count := 0.0, 0
	for seedCounter := uint64(0); seedCounter < 2000; seedCounter++ {
		traits := drawTraitVector(seededRNG(seedCounter, 99), params, scenarioShifts)
		if traits.Phi == nil {
			continue
		}
		total += *traits.Phi
		count++
	}
	if count == 0 {
		t.Fatalf("no endogenous agents to measure")
	}
	return total / float64(count)
}

// TestShiftedParamsPullsThroughToEveryRootDraw confirms the main
// path a scenario reaches the trait draw: when the overrides carry a
// non-nil ShiftedParams, every root dial inside drawTraitVector reads
// from the shifted copy, not the baseline. The test puts an extreme
// shift onto AggressivenessBeta and asserts the population-level mean
// aggressiveness moves far away from the baseline mean. The sample
// size is large enough that a true population shift is unmistakable
// against sampling noise.
//
// This is the "every trait receives shifted numbers" guarantee the
// package relies on — a regression where drawTraitVector dropped the
// ShiftedParams rebind would show up here as near-identical means.
func TestShiftedParamsPullsThroughToEveryRootDraw(t *testing.T) {
	baselineMean := meanAggressiveness(t, nil)

	shiftedParams := completeGenParams()
	shiftedParams.AggressivenessBeta = BetaPair{Alpha: 10, Beta: 1} // mean ≈ 0.91
	polarizedMean := meanAggressiveness(t, &EffectiveParams{ShiftedParams: &shiftedParams})

	// The baseline AggressivenessBeta is symmetric (2, 2), mean = 0.5.
	// The shifted beta pulls the mean toward 0.91. A regression that
	// ignored ShiftedParams would leave polarizedMean near 0.5.
	if polarizedMean <= baselineMean+0.1 {
		t.Fatalf("ShiftedParams did not pull through: baselineMean=%v shiftedMean=%v",
			baselineMean, polarizedMean)
	}
}

// TestShiftedParamsNilRebindMatchesBaseline guards the inverse: a
// non-nil overrides pointer with a nil ShiftedParams (only PhiBimodal
// set, say) must leave the root draws at baseline — the swap only
// fires when ShiftedParams itself is non-nil.
func TestShiftedParamsNilRebindMatchesBaseline(t *testing.T) {
	baselineMean := meanAggressiveness(t, nil)
	phiOnlyMean := meanAggressiveness(t, &EffectiveParams{
		PhiBimodal: &BimodalOptimism{
			MixWeight:      0.5,
			FirstModeBeta:  BetaPair{Alpha: 8, Beta: 2},
			SecondModeBeta: BetaPair{Alpha: 2, Beta: 8},
		},
	})
	// Aggressiveness is unrelated to the bimodal override; the two
	// means must land within sampling noise of each other.
	if math.Abs(phiOnlyMean-baselineMean) > 0.03 {
		t.Fatalf("PhiBimodal-only shift leaked into aggressiveness draw: baseline=%v phiOnly=%v",
			baselineMean, phiOnlyMean)
	}
}

// meanAggressiveness returns the average aggressiveness over a large
// draw loop. Mirrors meanOptimism / spreadOptimism in shape; the
// aggressiveness axis is read from the universal block, so it is
// present on every agent regardless of kind.
func meanAggressiveness(t *testing.T, scenarioShifts *EffectiveParams) float64 {
	t.Helper()
	params := completeGenParams()
	total := 0.0
	const sampleCount = 2000
	for seedCounter := uint64(0); seedCounter < sampleCount; seedCounter++ {
		traits := drawTraitVector(seededRNG(seedCounter, 77), params, scenarioShifts)
		total += traits.Aggressiveness
	}
	return total / float64(sampleCount)
}

func spreadOptimism(t *testing.T, scenarioShifts *EffectiveParams) float64 {
	t.Helper()
	params := completeGenParams()
	samples := make([]float64, 0, 2000)
	for seedCounter := uint64(0); seedCounter < 2000; seedCounter++ {
		traits := drawTraitVector(seededRNG(seedCounter, 99), params, scenarioShifts)
		if traits.Phi != nil {
			samples = append(samples, *traits.Phi)
		}
	}
	if len(samples) == 0 {
		t.Fatalf("no endogenous agents to measure")
	}
	mean := 0.0
	for _, value := range samples {
		mean += value
	}
	mean /= float64(len(samples))
	variance := 0.0
	for _, value := range samples {
		variance += (value - mean) * (value - mean)
	}
	return math.Sqrt(variance / float64(len(samples)))
}
