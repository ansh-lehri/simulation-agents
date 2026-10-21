package decision

import (
	"math"
	"math/rand/v2"
	"sort"

	"simulation-agents/agent"
)

// PerceptionParams holds the two global calibration dials shared across all
// perception channels. Composition-root concern: the values are calibration
// placeholders, not fixed design decisions.
type PerceptionParams struct {
	// SigmaPerc is the skill-noise standard deviation at skill = 0. An agent
	// with skill = 1 gets zero noise; skill = 0 gets the full-width noise.
	// Global across all channels; per-channel variation is a deferred dial.
	SigmaPerc float64

	// MASaturationMultiple is the scalar m that converts a moving-average
	// spread into a [0,1] strength reading: rawStrength = |spread|/(m×flatBand).
	// Larger m requires a wider spread before strength saturates at 1.
	MASaturationMultiple float64
}

// biasedAndNoisy applies the per-agent frozen bias then a fresh normal jitter
// to a raw continuous magnitude. The three-step order — raw → +bias → +noise
// → (caller clamps) — matches the spec.
//
// Bias is looked up from traits.Perception.Bias by biasKey; absent keys
// yield 0 (no lean, noise only). Skill-noise width shrinks toward 0 as
// skill → 1. Only magnitude/score fields pass through here; levels and
// pure facts (key_levels, phase) must not.
func biasedAndNoisy(raw float64, biasKey string, traits agent.TraitVector, agentRNG *rand.Rand, params PerceptionParams) float64 {
	bias := 0.0
	if traits.Perception != nil {
		bias = traits.Perception.Bias[biasKey]
	}
	sigma := params.SigmaPerc * (1 - traits.Skill)
	noise := 0.0
	if sigma > 0 {
		noise = agentRNG.NormFloat64() * sigma
	}
	return raw + bias + noise
}

// directionFromSpread classifies a continuous signed spread into a Direction
// using a symmetric flat band. Values within ±flatBand are DirFlat.
func directionFromSpread(spread, flatBand float64) Direction {
	if spread > flatBand {
		return DirUp
	}
	if spread < -flatBand {
		return DirDown
	}
	return DirFlat
}

// clamp01 clamps v to [0, 1].
func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

// clampSymmetric clamps v to [−1, +1].
func clampSymmetric(v float64) float64 {
	return math.Max(-1, math.Min(1, v))
}

// pathEfficiency computes |displacement| / total_path_length over a close
// series, yielding 1.0 for a straight move and ~0 for maximum chop.
// Returns 1.0 when path length is zero (perfectly still = perfectly clean).
func pathEfficiency(closes []float64) float64 {
	if len(closes) < 2 {
		return 1.0
	}
	displacement := math.Abs(closes[len(closes)-1] - closes[0])
	totalPath := 0.0
	for i := 1; i < len(closes); i++ {
		totalPath += math.Abs(closes[i] - closes[i-1])
	}
	if totalPath == 0 {
		return 1.0
	}
	return clamp01(displacement / totalPath)
}

// trailingRunFraction counts consecutive trailing elements in a boolean
// series that are true, normalized by the total series length.
func trailingRunFraction(matches []bool) float64 {
	if len(matches) == 0 {
		return 0
	}
	count := 0
	for i := len(matches) - 1; i >= 0 && matches[i]; i-- {
		count++
	}
	return clamp01(float64(count) / float64(len(matches)))
}

// medianFloat64 returns the median of a float64 slice without modifying
// the input. Returns 0 for empty input.
func medianFloat64(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	scratch := make([]float64, len(vals))
	copy(scratch, vals)
	sort.Float64s(scratch)
	mid := len(scratch) / 2
	if len(scratch)%2 == 0 {
		return (scratch[mid-1] + scratch[mid]) / 2
	}
	return scratch[mid]
}
