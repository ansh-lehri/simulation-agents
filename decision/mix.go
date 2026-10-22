package decision

import (
	"math"
	"math/rand/v2"

	"simulation-agents/agent"
)

// mixOpinion is the mixer's output: one signed trading view and its
// conviction. commit.go is only ever allowed to read these two fields
// — this type carries nothing else, which is what structurally
// enforces "commit never re-reads mixer internals": there is simply no
// internal state left on the type for it to read. Conviction already
// bakes in however much the traits agreed or disagreed with each
// other, so nothing downstream needs to re-derive that agreement.
type mixOpinion struct {
	Direction  float64 // +1 long, -1 short
	Conviction float64 // [0,1]
}

// mix combines one agent's own per-trait verdicts and its own noise
// disposition into at most one opinion this tick. Everything here is
// that one agent's: agentRNG is its own independent stream, disposition
// is its own frozen weights, verdicts came from its own interpret()
// call against its own perception bundle. Running mix again for a
// different agent against the identical verdicts and weights can still
// land on a different opinion, because the RNG draws differ.
//
// The noise coin is checked first, before either verdict is even
// looked at: a hit returns a random impulse with bypass=true, telling
// the caller to skip the re-decide gate and the commit-gate shave
// entirely and commit the impulse as-is. A miss falls through to
// combining whatever verdicts interpret actually produced; a nil
// opinion (always paired with bypass=false) means neither trait had
// anything to say this tick — the caller treats that as SKIP.
func mix(verdicts traitVerdicts, disposition agent.Disposition, agentRNG *rand.Rand, params MixerParams) (*mixOpinion, bool) {
	if agentRNG.Float64() < disposition.Noise {
		direction := 1.0
		// The spec only says the impulse direction is "±1 by agentRNG" —
		// it does not pin a split. 0.5 is my own reading of "a coin" as
		// unbiased, not a number the spec states; this is a placeholder
		// likely to change once calibration has an opinion (e.g. a
		// per-agent or population-level directional lean on noise).
		if agentRNG.Float64() < 0.5 {
			direction = -1.0
		}
		return &mixOpinion{Direction: direction, Conviction: params.NoiseImpulseConviction}, true
	}

	presentCount := 0
	if verdicts.trend != nil {
		presentCount++
	}
	if verdicts.meanRev != nil {
		presentCount++
	}

	switch presentCount {
	case 0:
		return nil, false
	case 1:
		var onlyVerdict float64
		if verdicts.trend != nil {
			onlyVerdict = *verdicts.trend
		} else {
			onlyVerdict = *verdicts.meanRev
		}
		return &mixOpinion{Direction: directionOf(onlyVerdict), Conviction: math.Abs(onlyVerdict)}, false
	}

	// Both traits have a verdict: the one with the larger disposition
	// weight leads. A tie is broken by a fresh coin rather than an
	// arbitrary fixed preference for one trait over the other. As
	// above, the spec says "ties broken by seeded coin" without pinning
	// a split; 0.5 is my own unbiased reading, not a spec-stated value,
	// and is a placeholder likely to change once calibration has an
	// opinion on whether trend or mean-reversion should lead a tie.
	trendIsDominant := disposition.Trend > disposition.MeanRev
	if disposition.Trend == disposition.MeanRev {
		trendIsDominant = agentRNG.Float64() < 0.5
	}

	dominantVerdict, secondaryVerdict := *verdicts.trend, *verdicts.meanRev
	secondaryWeight := disposition.MeanRev
	if !trendIsDominant {
		dominantVerdict, secondaryVerdict = *verdicts.meanRev, *verdicts.trend
		secondaryWeight = disposition.Trend
	}

	direction := directionOf(dominantVerdict)
	conviction := math.Abs(dominantVerdict)

	// The secondary trait only ever scales conviction toward or away
	// from zero — align flips the sign of the scaling term, never the
	// direction itself, which stays fixed to the dominant trait's sign
	// set above.
	align := 1.0
	if directionOf(secondaryVerdict) != direction {
		align = -1.0
	}
	conviction = clamp01(conviction * (1 + align*secondaryWeight*math.Abs(secondaryVerdict)*params.SecondarySensitivity))

	return &mixOpinion{Direction: direction, Conviction: conviction}, false
}
