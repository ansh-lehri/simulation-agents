package generator

import (
	"math"
	"math/rand/v2"

	"simulation-agents/agent"
)

// This file holds the frozen position-management traits drawn in the same
// pass as the perception latents: the exit-profile (mode selection plus
// four numeric dials) and the max-hold tick cap.

// drawExitProfile samples the endogenous agent's position-exit dial set.
// The mode is skill-coupled (high-skill agents concentrate around the
// level mode, low-skill around the distance mode) and the four numeric
// dials are free draws; the target percent is derived as stop percent
// times a reward-to-risk multiplier so target > stop is guaranteed by
// construction.
func drawExitProfile(rng *rand.Rand, params GenParams, skill float64) agent.ExitProfile {
	mode := drawExitMode(rng, params.ExitModeAnchors, params.ExitModeSelectionTau, skill)
	stopPct := uniformInRange(rng, params.ExitStopPctRange.Low, params.ExitStopPctRange.High)
	rewardRisk := uniformInRange(rng, params.ExitRewardRiskRange.Low, params.ExitRewardRiskRange.High)
	stopBuffer := uniformInRange(rng, params.ExitStopBufferRange.Low, params.ExitStopBufferRange.High)
	targetBuffer := uniformInRange(rng, params.ExitTargetBufferRange.Low, params.ExitTargetBufferRange.High)

	return agent.ExitProfile{
		Mode:         mode,
		StopPct:      stopPct,
		TargetPct:    stopPct * rewardRisk,
		StopBuffer:   stopBuffer,
		TargetBuffer: targetBuffer,
	}
}

// drawExitMode samples one mode name against the skill-anchor kernel.
// Scoring: each mode's score is a Gaussian in (skill − anchor) with
// width tau; probabilities are the normalized scores; the mode is
// SAMPLED (not argmax) so same-skill agents do not collapse onto one
// mode. Argmax would turn the mode choice into a step function on skill
// and destroy the "two mid-skill agents, different exits" texture the
// draw is supposed to produce.
//
// Iteration order over the anchors map is forced to the sorted key
// order so a fixed seed produces an identical outcome run-to-run; Go's
// native map iteration is randomized and would silently break
// determinism.
func drawExitMode(rng *rand.Rand, anchors map[string]float64, tau, skill float64) string {
	if len(anchors) == 0 {
		return ""
	}
	sortedModeNames := sortedStringKeys(anchors)

	scores := make([]float64, len(sortedModeNames))
	totalScore := 0.0
	for index, modeName := range sortedModeNames {
		anchor := anchors[modeName]
		gap := skill - anchor
		score := math.Exp(-(gap * gap) / (2 * tau * tau))
		scores[index] = score
		totalScore += score
	}

	threshold := rng.Float64() * totalScore
	cumulative := 0.0
	for index, score := range scores {
		cumulative += score
		if threshold < cumulative {
			return sortedModeNames[index]
		}
	}
	// Floating-point rounding on the last-bin boundary can leave
	// threshold a hair above cumulative; return the last mode name as
	// the deterministic fallback.
	return sortedModeNames[len(sortedModeNames)-1]
}

// drawMaxHold places an integer tick cap inside the configured range
// using the shared reactivity rule: jumpy (high reactivity) agents land
// on short holds, deliberate (low reactivity) agents on long holds. The
// jitter width is a fraction of the range so the same jitter dial reads
// consistently across differently-scaled ranges. The result is rounded
// and then clamped so a rounding excursion cannot escape the box.
func drawMaxHold(rng *rand.Rand, intRange IntRange, reactivity, jitterFraction float64) int {
	centered := centerFromReactivity(rng, float64(intRange.Low), float64(intRange.High), reactivity, jitterFraction)
	rounded := int(math.Round(centered))
	return clampInt(rounded, intRange.Low, intRange.High)
}
