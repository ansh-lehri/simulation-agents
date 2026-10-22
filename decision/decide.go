package decision

import (
	"math/rand/v2"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// readingFieldChartDirection, readingFieldChartStrength,
// readingFieldRangePosition, and readingFieldRangeStretch name the four
// consumed directional fields the re-decide gate compares against —
// the same field keys the generator uses to key
// agent.PerceptionProfile.DecideSensitivity (orchestrator/generator's
// own directionalFields list), reproduced here because that list is
// unexported in a package decision does not depend on.
const (
	readingFieldChartDirection = "chart.direction"
	readingFieldChartStrength  = "chart.strength"
	readingFieldRangePosition  = "range.position_in_range"
	readingFieldRangeStretch   = "range.stretch"
)

// currentReadings extracts this tick's consumed-directional-field
// values from a perception bundle, keyed the same way as
// DecideSensitivity/lastDecidedReadings, for the re-decide gate to
// compare against. A field is omitted (not zero) when its channel is
// diet-off or unreadable this tick — gateDecide already treats a
// missing current value as "nothing to compare, cannot be the reason
// to redecide," so omitting here is the correct absent-not-neutral
// behavior, not a shortcut.
func currentReadings(bundle PerceptionBundle) map[string]float64 {
	readings := map[string]float64{}
	if bundle.Chart != nil && bundle.Chart.ReadOK {
		readings[readingFieldChartDirection] = float64(bundle.Chart.Direction)
		readings[readingFieldChartStrength] = bundle.Chart.Strength
	}
	if bundle.Range != nil && bundle.Range.ReadOK {
		readings[readingFieldRangePosition] = bundle.Range.PositionInRange
		readings[readingFieldRangeStretch] = bundle.Range.Stretch
	}
	return readings
}

// DecideInput bundles everything Decide needs to decide what a flat
// instrument's agent wants to do this tick. Built once per instrument
// by the traversal inside session.decide — Decide never fetches any of
// this itself, the same no-I/O discipline as every other pure function
// in this package.
type DecideInput struct {
	// Bundle is this tick's already-produced perception readings for
	// this instrument.
	Bundle PerceptionBundle

	// Traits is this agent's frozen trait vector.
	Traits agent.TraitVector

	// Roles is the fixed channel-to-trait map interpret needs.
	Roles map[string]TraitChannelRole

	// CurrentTick is the current tick index — read by the cooldown
	// shave and stamped into a fresh commitment on COMMIT.
	CurrentTick int64

	// SessionProgress is this tick's normalized session time in
	// [0,1], read by the time-of-session commit shave.
	SessionProgress float64

	// StandingOpinion, StandingStance, LastDecidedReadings,
	// PendingWatch, WaitStreak, and LastCommitTick are this
	// instrument's current carried working memory. Decide reads them
	// and echoes each forward — updated or unchanged — into the
	// returned AssessmentDelta/CommitmentDelta; it never mutates them
	// itself.
	StandingOpinion     float64
	StandingStance      string
	LastDecidedReadings map[string]float64
	PendingWatch        *PendingWatch
	WaitStreak          int
	LastCommitTick      int64

	// RNG is this agent's own per-agent randomness stream.
	RNG *rand.Rand

	// Params is the global calibration surface the gate, mixer, and
	// commit-gate all read from.
	Params Params
}

// Decide decides what a flat instrument's agent wants to do this tick:
// commit to a fresh entry, keep watching, or coast/skip. Pure function,
// same discipline as every other node in this package: no I/O, no Core
// calls, no Redis; Decide reads only its DecideInput and returns a
// value — it is the production chain-stitch (noise coin → gate → mix →
// commit) the four pure files in this package were each built to be
// called by, not a fifth building block alongside them.
func Decide(input DecideInput) NodeOutcome {
	if input.Traits.Disposition == nil || input.Traits.Perception == nil {
		panic("decision: Decide called on a TraitVector missing disposition/perception_profile — these are generated once at mint for every endogenous agent and must never be absent while flat")
	}

	readings := currentReadings(input.Bundle)

	if impulse, hit := noiseImpulse(*input.Traits.Disposition, input.RNG, input.Params.Mixer); hit {
		return NodeOutcome{
			Assessment: &AssessmentDelta{
				StandingOpinion:     impulse.Direction * impulse.Conviction,
				StandingStance:      standingStanceFlatIdle,
				LastDecidedReadings: input.LastDecidedReadings,
				WaitStreak:          input.WaitStreak,
				Changed:             true,
			},
			Candidate: &ExecIntent{Action: ExecIntentActionOpen, Direction: impulse.Direction, Conviction: impulse.Conviction},
			Commitment: &CommitmentDelta{
				LastCommitTick: input.CurrentTick,
				PositionContext: store.PositionContext{
					EntryDir:     entryDirectionLabel(impulse.Direction),
					EntryChannel: channelOf(impulse.DominantTrait),
					EntryTick:    input.CurrentTick,
				},
			},
		}
	}

	sensitivities := map[string]float64{}
	if input.Traits.Perception.DecideSensitivity != nil {
		sensitivities = input.Traits.Perception.DecideSensitivity
	}

	if gateDecide(input.PendingWatch, input.WaitStreak, readings, input.LastDecidedReadings, sensitivities, input.Params.Gate) == gateCoast {
		newWaitStreak := input.WaitStreak
		standingStance := standingStanceFlatIdle
		if input.PendingWatch != nil {
			newWaitStreak++
			standingStance = standingStanceWait
		}
		return NodeOutcome{
			Assessment: &AssessmentDelta{
				StandingOpinion:     input.StandingOpinion,
				StandingStance:      standingStance,
				LastDecidedReadings: input.LastDecidedReadings,
				PendingWatch:        input.PendingWatch,
				WaitStreak:          newWaitStreak,
				Changed:             false,
			},
		}
	}

	verdicts := interpret(input.Bundle, input.Traits, input.Roles, input.Params.Interpretation)
	opinion := mix(verdicts, *input.Traits.Disposition, input.RNG, input.Params.Mixer)
	if opinion == nil {
		return NodeOutcome{
			Assessment: &AssessmentDelta{
				StandingStance:      standingStanceFlatIdle,
				LastDecidedReadings: readings,
				Changed:             true,
			},
		}
	}

	result := commit(*opinion, input.Traits, input.CurrentTick, input.SessionProgress, input.Bundle.Liquidity, input.LastCommitTick, input.PendingWatch, input.Params)

	standingStance := standingStanceFlatIdle
	if result.Outcome == commitWait {
		standingStance = standingStanceWait
	}
	outcome := NodeOutcome{
		Assessment: &AssessmentDelta{
			StandingOpinion:     opinion.Direction * opinion.Conviction,
			StandingStance:      standingStance,
			LastDecidedReadings: readings,
			PendingWatch:        result.PendingWatch,
			Changed:             true,
		},
	}
	if result.Outcome == commitCommit {
		outcome.Candidate = &ExecIntent{Action: ExecIntentActionOpen, Direction: result.Direction, Conviction: result.EffectiveConviction}
		outcome.Commitment = &CommitmentDelta{
			LastCommitTick: input.CurrentTick,
			PositionContext: store.PositionContext{
				EntryDir:     entryDirectionLabel(result.Direction),
				EntryChannel: channelOf(opinion.DominantTrait),
				EntryTick:    input.CurrentTick,
			},
		}
	}
	return outcome
}

// standingStanceFlatIdle and standingStanceWait are the two values
// instrumentMemory.standingStance (session package) carries — mirrors
// pendingWatch's presence for a quick read without dereferencing it.
const (
	standingStanceFlatIdle = "FLAT_IDLE"
	standingStanceWait     = "WAIT"
)
