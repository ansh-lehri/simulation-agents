package decision

import (
	"math/rand/v2"

	"simulation-agents/core"
	"simulation-agents/store"
)

// AssessmentDelta is one instrument's complete, tick-updated flat-path
// working memory — what the agent now thinks, independent of whether it
// acted. Decide returns one every tick it runs, whether this tick coasted
// or re-decided; Manage never returns one, since the holding path does
// not read or update this bookkeeping at all.
type AssessmentDelta struct {
	StandingOpinion     float64
	StandingStance      string
	LastDecidedReadings map[string]float64
	PendingWatch        *PendingWatch
	WaitStreak          int

	// Changed marks whether this tick actually re-decided (a noise
	// impulse or a gate RE-DECIDE) rather than coasting. The caller
	// applies this delta to in-memory working state unconditionally
	// either way, but mirrors it to Redis only when Changed is true —
	// the write-on-change discipline 4b's own working-memory setters
	// already follow.
	Changed bool
}

// CommitmentDelta is the record that an order was actually placed this
// tick for one instrument: the tick of the commit and the resulting
// entry conditions. Applied only to the instrument that wins one-punch
// resolution, and only after Execute confirms a real punch.
type CommitmentDelta struct {
	LastCommitTick  int64
	PositionContext store.PositionContext
}

// NodeOutcome is what Decide (4b, flat) and Manage (4d, holding) each
// return for one instrument this tick. Assessment, when non-nil, is
// applied unconditionally regardless of whether Candidate is nil or
// wins resolution. Candidate, if non-nil, is parked for one-punch
// resolution. Commitment, if non-nil, is applied only to the instrument
// that wins resolution, and only once Execute confirms a real punch.
type NodeOutcome struct {
	Assessment *AssessmentDelta
	Candidate  *ExecIntent
	Commitment *CommitmentDelta
}

// InstrumentCandidate pairs one instrument with the node outcome it
// produced this tick, for one-punch resolution across instruments.
// Callers only ever construct one of these for an instrument whose
// outcome.Candidate is non-nil — ResolveOnePunch relies on that.
type InstrumentCandidate struct {
	Instrument string
	Outcome    NodeOutcome
}

// ResolveOnePunch collapses however many instruments produced a
// candidate this tick into the single order the per-agent seam allows.
// Any close candidate beats any open/add candidate outright — risk-off
// first, exiting a stopped/reversed position outranks a fresh bet,
// regardless of relative conviction. Within the winning class, higher
// ExecIntent.Conviction wins; an exact conviction tie is broken by a
// fresh agentRNG coin. candidates must be non-empty and every entry's
// Outcome.Candidate must be non-nil — the caller only calls this once
// it has at least one parked candidate.
func ResolveOnePunch(candidates []InstrumentCandidate, agentRNG *rand.Rand) InstrumentCandidate {
	closeCandidates := make([]InstrumentCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Outcome.Candidate.Action == ExecIntentActionClose {
			closeCandidates = append(closeCandidates, candidate)
		}
	}
	if len(closeCandidates) > 0 {
		return highestConviction(closeCandidates, agentRNG)
	}
	return highestConviction(candidates, agentRNG)
}

// highestConviction picks the candidate with the highest
// ExecIntent.Conviction from a single class (all closes, or — absent any
// close — all opens/adds), breaking an exact tie with a fresh agentRNG
// coin per comparison.
func highestConviction(candidates []InstrumentCandidate, agentRNG *rand.Rand) InstrumentCandidate {
	winner := candidates[0]
	for _, candidate := range candidates[1:] {
		switch {
		case candidate.Outcome.Candidate.Conviction > winner.Outcome.Candidate.Conviction:
			winner = candidate
		case candidate.Outcome.Candidate.Conviction == winner.Outcome.Candidate.Conviction && agentRNG.Float64() < 0.5:
			winner = candidate
		}
	}
	return winner
}

// PositionFor reports whether obs shows a live (non-flat) position in
// instrument. core.AccountObservation.Positions is already normalized
// non-nil by GetObservation, so the range below is always safe; a
// matching entry with Qty == 0 is treated the same as no entry at all —
// both mean flat.
func PositionFor(obs core.AccountObservation, instrument string) (core.Position, bool) {
	for _, position := range obs.Positions {
		if position.Instrument == instrument && position.Qty != 0 {
			return position, true
		}
	}
	return core.Position{}, false
}

// entryDirectionLong and entryDirectionShort are the two values stamped
// into store.PositionContext.EntryDir — a label for the position's own
// side, independent of whichever trait or impulse produced it.
const (
	entryDirectionLong  = "LONG"
	entryDirectionShort = "SHORT"
)

// entryDirectionLabel converts a signed trade direction (+1/-1) into the
// label store.PositionContext.EntryDir carries.
func entryDirectionLabel(direction float64) string {
	if direction < 0 {
		return entryDirectionShort
	}
	return entryDirectionLong
}
