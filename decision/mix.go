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

// noiseImpulse rolls one agent's own per-tick noise coin. It is a
// standalone step, independent of gateDecide and mix, meant to be
// called once per tick, before the gate is even consulted — the IMPL
// doc's own control-flow pseudocode is explicit that this coin fires
// "EVERY acting tick, above the gate," and that mix() (below), called
// later from inside the RE-DECIDE branch, always sees a miss. That
// guarantee only holds if the coin is drawn exactly once per tick,
// here, and never independently re-rolled inside mix() itself — see
// the comment below mix() for the full reasoning; an earlier version
// of this file had the coin inside mix(), which this replaces.
//
// A hit returns a random ±1 impulse with NoiseImpulseConviction as its
// conviction, and true — telling the caller to skip gateDecide, mix,
// and commit entirely and act on the impulse directly. A miss returns
// (nil, false), and the caller proceeds to gateDecide as normal.
func noiseImpulse(disposition agent.Disposition, agentRNG *rand.Rand, params MixerParams) (*mixOpinion, bool) {
	if agentRNG.Float64() >= disposition.Noise {
		return nil, false
	}

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

// mix combines one agent's own per-trait verdicts into at most one
// opinion this tick. By the time mix is called, the noise coin
// (noiseImpulse, above) has already missed and the re-decide gate has
// already said RE-DECIDE — mix is never called on a tick that
// bypassed via noise, and never called on a COAST tick, so it has no
// bypass concept of its own. Everything here is that one agent's own:
// agentRNG is its own independent stream (used only for the dominance
// tie-break below), disposition is its own frozen weights, verdicts
// came from its own interpret() call against its own perception
// bundle.
//
// Returns nil when neither trait has a verdict (the caller treats this
// as SKIP); the one present verdict verbatim with no multiplier when
// only one trait has a verdict; or the dominant trait's verdict
// dampened/boosted by the secondary trait's agreement or disagreement
// when both are present.
func mix(verdicts traitVerdicts, disposition agent.Disposition, agentRNG *rand.Rand, params MixerParams) *mixOpinion {
	presentCount := 0
	if verdicts.trend != nil {
		presentCount++
	}
	if verdicts.meanRev != nil {
		presentCount++
	}

	switch presentCount {
	case 0:
		return nil
	case 1:
		var onlyVerdict float64
		if verdicts.trend != nil {
			onlyVerdict = *verdicts.trend
		} else {
			onlyVerdict = *verdicts.meanRev
		}
		return &mixOpinion{Direction: directionOf(onlyVerdict), Conviction: math.Abs(onlyVerdict)}
	}

	// Both traits have a verdict: the one with the larger disposition
	// weight leads. A tie is broken by a fresh coin rather than an
	// arbitrary fixed preference for one trait over the other. As with
	// the noise-impulse direction above, the spec says "ties broken by
	// seeded coin" without pinning a split; 0.5 is my own unbiased
	// reading, not a spec-stated value, and is a placeholder likely to
	// change once calibration has an opinion on whether trend or
	// mean-reversion should lead a tie.
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

	return &mixOpinion{Direction: direction, Conviction: conviction}
}

// Superseded: mix() originally owned the noise coin directly and
// returned a bypass bool, matching decision_compute/CLAUDE.md's
// compressed public-surface bullet literally: "mix(verdicts, weights,
// agentRNG) (opinion, bypass bool) — noise coin FIRST ... skips
// gate+4b-2 entirely".
//
// That shape conflicted with the IMPL doc's own §9 control-flow
// pseudocode, which draws the coin as a standalone top-level step
// BEFORE gateDecide runs ("EVERY acting tick, above the gate") and
// annotates mix()'s call site inside the RE-DECIDE branch as
// "bypass=false here" — stated as a guaranteed fact, not a
// probability. That guarantee can only hold if the coin is drawn
// exactly once per tick and is never independently re-rolled inside
// mix() itself: under this old shape, calling mix() a second time
// could still roll a fresh hit even though the top-level check already
// missed, which would make "bypass=false here" false some of the
// time. Resolved in favor of §9's literal pseudocode — the coin is now
// the standalone noiseImpulse function above, called once, before
// gateDecide; mix() keeps only the dominant+secondary combine and no
// longer has a bypass concept at all.
//
// Kept here, commented out, instead of deleted, so the prior shape and
// the reasoning for abandoning it both stay visible in the diff.
//
// func mix(verdicts traitVerdicts, disposition agent.Disposition, agentRNG *rand.Rand, params MixerParams) (*mixOpinion, bool) {
// 	if agentRNG.Float64() < disposition.Noise {
// 		direction := 1.0
// 		if agentRNG.Float64() < 0.5 {
// 			direction = -1.0
// 		}
// 		return &mixOpinion{Direction: direction, Conviction: params.NoiseImpulseConviction}, true
// 	}
//
// 	presentCount := 0
// 	if verdicts.trend != nil {
// 		presentCount++
// 	}
// 	if verdicts.meanRev != nil {
// 		presentCount++
// 	}
//
// 	switch presentCount {
// 	case 0:
// 		return nil, false
// 	case 1:
// 		var onlyVerdict float64
// 		if verdicts.trend != nil {
// 			onlyVerdict = *verdicts.trend
// 		} else {
// 			onlyVerdict = *verdicts.meanRev
// 		}
// 		return &mixOpinion{Direction: directionOf(onlyVerdict), Conviction: math.Abs(onlyVerdict)}, false
// 	}
//
// 	trendIsDominant := disposition.Trend > disposition.MeanRev
// 	if disposition.Trend == disposition.MeanRev {
// 		trendIsDominant = agentRNG.Float64() < 0.5
// 	}
//
// 	dominantVerdict, secondaryVerdict := *verdicts.trend, *verdicts.meanRev
// 	secondaryWeight := disposition.MeanRev
// 	if !trendIsDominant {
// 		dominantVerdict, secondaryVerdict = *verdicts.meanRev, *verdicts.trend
// 		secondaryWeight = disposition.Trend
// 	}
//
// 	direction := directionOf(dominantVerdict)
// 	conviction := math.Abs(dominantVerdict)
//
// 	align := 1.0
// 	if directionOf(secondaryVerdict) != direction {
// 		align = -1.0
// 	}
// 	conviction = clamp01(conviction * (1 + align*secondaryWeight*math.Abs(secondaryVerdict)*params.SecondarySensitivity))
//
// 	return &mixOpinion{Direction: direction, Conviction: conviction}, false
// }
