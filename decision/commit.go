package decision

import (
	"math"

	"simulation-agents/agent"
)

// NoPriorCommit is the lastCommitTick sentinel meaning "this agent has
// never committed an order for this instrument." Tick indices are
// zero-based and strictly increasing, so a negative sentinel can never
// collide with a real tick — using 0 would wrongly look like "this
// agent's first-ever commit happened at tick zero" the very next time
// the cooldown shave runs.
const NoPriorCommit int64 = -1

// ExecIntentAction identifies what kind of trading intent a decision
// produced. The flat-agent entry decision built in this package only
// ever produces an "open" intent; the type carries an Action field
// anyway so it can be shared, unchanged, with the position-management
// stages that will introduce add/close intents later.
type ExecIntentAction string

// ExecIntentActionOpen is the only action this package ever produces:
// a brand-new position request for an instrument the agent is flat in.
const ExecIntentActionOpen ExecIntentAction = "open"

// ExecIntent is the signed trading intent handed onward to the stage
// that resolves one candidate per instrument into an actual order.
// Direction and Conviction are the only fields a downstream sizing
// stage may read — Conviction already carries every shave this
// package applied (time-of-session caution, spread thinness, commit
// cooldown), so a thin-book or back-to-back commit sizes smaller
// automatically rather than needing its own re-check downstream.
type ExecIntent struct {
	Action     ExecIntentAction
	Direction  float64 // +1 for long, -1 for short
	Conviction float64 // [0,1], already shaved
}

// PendingWatch is a waiter's in-progress confirmation count toward a
// commit. Direction is the view the waiter is trying to confirm;
// ConfirmsSeen counts how many consecutive same-direction re-decides
// have agreed so far; SinceTick records when the watch started. A nil
// *PendingWatch means the agent is not currently waiting on anything —
// it is either flat-idle or just committed/skipped. Seeing the
// opposite direction restarts the watch at ConfirmsSeen=1 rather than
// accumulating toward a confirmation count that no longer reflects the
// agent's current view.
type PendingWatch struct {
	Direction    float64
	ConfirmsSeen int
	SinceTick    int64
}

// Params is the global calibration surface this package's four
// decision functions all draw from. Every magnitude here is a
// composition-root concern — the mechanism (how a shave is shaped, how
// the mixer combines traits) is fixed in code; only these numbers are
// meant to move during calibration. Mirrors decision.PerceptionParams
// from the perception stage: pure functions take their calibration as
// an explicit argument, never read it from a package-level variable.
type Params struct {
	Mixer          MixerParams
	Interpretation InterpretationParams
	Gate           GateParams
	TimeShave      TimeShaveParams
	SpreadShave    SpreadShaveParams
	CooldownShave  CooldownShaveParams

	// ThetaAddMargin is the amount added to an agent's ThetaAct to
	// derive ThetaAdd — the (higher) bar for adding to an already-open
	// position. It is defined here, at the one place decision.Params
	// is first introduced, but nothing in this package reads it yet;
	// the add-position stage is the consumer.
	ThetaAddMargin float64
}

// MixerParams calibrates how the mixer turns per-trait verdicts and
// the noise disposition into one opinion.
type MixerParams struct {
	// SecondarySensitivity controls how strongly a secondary trait's
	// agreement or disagreement with the dominant trait dampens or
	// boosts the dominant trait's conviction. Zero means secondaries
	// are ignored entirely; larger values let a strongly-disagreeing
	// secondary pull conviction most of the way to zero (but never
	// flip its sign — that floor is enforced in code, not by this
	// value staying small).
	SecondarySensitivity float64

	// NoiseImpulseConviction is the fixed conviction assigned to a
	// noise-coin impulse. Noise reads no channel, so it has no
	// magnitude of its own to report — this is the one number that
	// lets a downstream sizing stage treat an impulse like any other
	// candidate.
	NoiseImpulseConviction float64
}

// InterpretationParams calibrates the condition-channel multipliers
// interpret applies on top of each trait's directional lean. The lean
// itself and the overall "sign(lean)·clamp(|lean|·Πmultipliers,0,1)"
// shape are fixed; only how strongly each condition nudges the
// multiplier is a calibration number.
type InterpretationParams struct {
	// TrendCleanlinessBoost scales how much a clean (low-chop) chart
	// move strengthens the trend verdict: multiplier = 1 +
	// TrendCleanlinessBoost×Chart.Cleanliness.
	TrendCleanlinessBoost float64

	// TrendVolumeConfirmBoost is the trend multiplier bonus applied
	// when the volume channel reports the move as Confirming (a real
	// price move on at-or-above-baseline volume).
	TrendVolumeConfirmBoost float64

	// TrendVolumeLowDamp is the trend multiplier penalty applied when
	// volume is thin (Level==Low) — a move on thin volume is weaker
	// evidence of a real trend.
	TrendVolumeLowDamp float64

	// MeanRevVolumeLowExtremeBoost is the mean-reversion multiplier
	// bonus applied when volume is thin AND the range reading is
	// stretched beyond its normal band — thin volume at an extreme
	// reads as exhaustion, the classic setup a fade trader wants.
	MeanRevVolumeLowExtremeBoost float64
}

// GateParams calibrates the re-decide gate.
type GateParams struct {
	// WaitReassessThreshold is how many consecutive ticks a waiter may
	// coast in WAIT before being forced to re-decide even though
	// perception output has not moved enough to trip the normal
	// staleness check. Without this floor a quiet consolidation —
	// which is exactly what a waiter is hoping to see — would read as
	// "nothing changed" and the agent would coast past its own
	// confirmation forever.
	WaitReassessThreshold int
}

// TimeShaveParams calibrates the session-time commit shave: a U-shape
// that makes an agent more cautious both right after the open and
// right before the close, trusting its own signal most through the
// middle of the session.
type TimeShaveParams struct {
	OpenDamp          float64 // depth of the open-side caution dip
	CloseDamp         float64 // depth of the close-side caution dip
	OpenRampExponent  float64 // how sharply the open-side dip fades as the session progresses
	CloseRampExponent float64 // how sharply the close-side dip grows as the session nears its end
	Floor             float64 // the multiplier never shaves below this, however early or late the tick
}

// SpreadShaveParams calibrates the order-book-thinness commit shave.
// Only applied to an agent whose diet includes the order-book channel
// — an agent that never perceives the book has no honest signal to
// shave on.
type SpreadShaveParams struct {
	SpreadSensitivity float64 // how strongly a wide spread shaves conviction
	NormalSpread      float64 // a static per-instrument "normal" spread the current spread is compared against
	Floor             float64 // the multiplier never shaves below this, however wide the spread
}

// CooldownShaveParams calibrates the post-commit cooldown shave: an
// agent that just committed is less convinced of an immediate repeat
// than one who has not acted in a while. The shave decays exponentially
// with the number of ticks elapsed since the agent's last commit.
type CooldownShaveParams struct {
	Damp  float64 // depth of the cooldown dip immediately after a commit
	Tau   float64 // how many ticks it takes the dip to decay away
	Floor float64 // the multiplier never shaves below this, however recent the last commit
}

// commitOutcome is what commit decides to do with one agent's mixer
// opinion on one instrument this tick.
type commitOutcome int8

const (
	commitSkip commitOutcome = iota
	commitWait
	commitCommit
)

// commitResult is commit's full answer: the outcome, the signed intent
// to act on (meaningful only when Outcome is commitCommit), and the
// PendingWatch the caller should carry forward into this agent's
// working memory for this instrument. PendingWatch is nil on
// commitSkip and commitCommit (nothing left to watch — either the view
// didn't clear the bar at all, or it already acted on it) and non-nil
// on commitWait (the in-progress confirmation count).
type commitResult struct {
	Outcome             commitOutcome
	Direction           float64
	EffectiveConviction float64
	PendingWatch        *PendingWatch
}

// commit turns one agent's mixer opinion on one instrument into
// COMMIT / WAIT / SKIP. opinion is taken by value, not by pointer, and
// carries only Direction and Conviction — there is no mixer-internal
// state on the type for this function to read even if it wanted to;
// conviction already bakes in however much the traits agreed or
// disagreed with each other (Option C), so commit never re-derives
// that agreement.
//
// Every input here is this one agent's own: its own ThetaAct/
// NeedsConfirm/ConfirmsNeeded traits, its own liquidity reading, its
// own lastCommitTick and pendingWatch working memory. Two agents
// sharing the identical opinion and the identical market conditions
// this tick can still land on different outcomes, because their
// traits and carried state differ.
//
// theta_act, needs_confirm, and confirms_needed are generated once at
// mint for every endogenous agent (never drawn, defaulted, or
// re-derived here) — an endogenous agent reaching this function
// without them is a broken upstream invariant, not a recoverable case,
// so this panics rather than silently synthesizing a value.
func commit(
	opinion mixOpinion,
	traits agent.TraitVector,
	currentTick int64,
	sessionProgress float64,
	liquidity *LiquidityReading,
	lastCommitTick int64,
	pendingWatch *PendingWatch,
	params Params,
) commitResult {
	if traits.ThetaAct == nil || traits.NeedsConfirm == nil || traits.ConfirmsNeeded == nil {
		panic("decision: commit called on a TraitVector missing theta_act/needs_confirm/confirms_needed — these are generated once at mint for every endogenous agent and must never be absent here")
	}

	// Step 0 — shave: each condition multiplier can shrink conviction
	// hard but never past its own floor, so a genuinely strong view
	// survives a rough environment rather than being force-zeroed by
	// one bad condition alone.
	timeMult := timeShaveMultiplier(sessionProgress, params.TimeShave)
	spreadMult := spreadShaveMultiplier(traits.Diet, liquidity, params.SpreadShave)
	cooldownMult := cooldownShaveMultiplier(currentTick, lastCommitTick, params.CooldownShave)
	effectiveConviction := opinion.Conviction * timeMult * spreadMult * cooldownMult

	// Step 1 — floor: below this agent's own commit bar, nothing below
	// survives regardless of how it got there, and any in-progress
	// watch is abandoned rather than carried forward on a view that no
	// longer clears the bar.
	if effectiveConviction < *traits.ThetaAct {
		return commitResult{Outcome: commitSkip}
	}

	// Step 2 — immediate actors commit on the spot.
	if !*traits.NeedsConfirm {
		return commitResult{
			Outcome:             commitCommit,
			Direction:           opinion.Direction,
			EffectiveConviction: effectiveConviction,
		}
	}

	// Step 2, waiter branch — accumulate confirmations toward
	// confirms_needed. A same-direction re-decide advances the count
	// without resetting how long the watch has been open; an
	// opposite-direction re-decide means the view changed mid-watch, so
	// the count restarts rather than continuing to track a view this
	// agent no longer holds.
	switch {
	case pendingWatch == nil:
		return commitResult{
			Outcome:      commitWait,
			PendingWatch: &PendingWatch{Direction: opinion.Direction, ConfirmsSeen: 1, SinceTick: currentTick},
		}
	case pendingWatch.Direction == opinion.Direction:
		confirmsSeen := pendingWatch.ConfirmsSeen + 1
		if confirmsSeen >= *traits.ConfirmsNeeded {
			return commitResult{
				Outcome:             commitCommit,
				Direction:           opinion.Direction,
				EffectiveConviction: effectiveConviction,
			}
		}
		return commitResult{
			Outcome:      commitWait,
			PendingWatch: &PendingWatch{Direction: opinion.Direction, ConfirmsSeen: confirmsSeen, SinceTick: pendingWatch.SinceTick},
		}
	default:
		return commitResult{
			Outcome:      commitWait,
			PendingWatch: &PendingWatch{Direction: opinion.Direction, ConfirmsSeen: 1, SinceTick: currentTick},
		}
	}
}

// clampFloor clamps value to [floor, 1] — the shape every commit shave
// multiplier shares: a condition can shrink conviction hard, but never
// below the floor calibration sets, and never above 1 (a shave only
// ever shrinks, it never amplifies).
func clampFloor(value, floor float64) float64 {
	return math.Max(floor, math.Min(1, value))
}

// timeShaveMultiplier is the session-time U-shape: an agent is more
// cautious both right after the open and right before the close,
// trusting its own signal most through the middle of the session.
// sessionProgress is already clamped to [0,1] by the time it gets
// here (4a's accessor guarantees that), so no further clamping is
// needed on the input itself.
func timeShaveMultiplier(sessionProgress float64, params TimeShaveParams) float64 {
	openDip := params.OpenDamp * math.Pow(1-sessionProgress, params.OpenRampExponent)
	closeDip := params.CloseDamp * math.Pow(sessionProgress, params.CloseRampExponent)
	return clampFloor(1-openDip-closeDip, params.Floor)
}

// spreadShaveMultiplier shaves conviction for a wide spread relative
// to this instrument's normal baseline — but only for an agent whose
// diet actually includes the order-book channel; an agent that never
// perceives the book has no honest signal to shave on, and neither
// does an agent whose book reading happened to be unreadable this
// tick, so both cases fall back to no shave at all rather than
// penalizing a channel the agent isn't using.
func spreadShaveMultiplier(diet *agent.Diet, liquidity *LiquidityReading, params SpreadShaveParams) float64 {
	if diet == nil || !diet.OrderBook {
		return 1
	}
	if liquidity == nil || !liquidity.ReadOK {
		return 1
	}
	if params.NormalSpread <= 0 {
		return 1
	}
	return clampFloor(1-params.SpreadSensitivity*(liquidity.Spread/params.NormalSpread), params.Floor)
}

// cooldownShaveMultiplier shaves conviction for how recently this
// agent last committed on this instrument — a back-to-back commit is
// less convincing than one with room to breathe since the last. An
// agent that has never committed before has nothing to cool down from,
// so it gets no shave at all, never a false one from comparing against
// a sentinel tick as if it were a real one.
func cooldownShaveMultiplier(currentTick, lastCommitTick int64, params CooldownShaveParams) float64 {
	if lastCommitTick == NoPriorCommit {
		return 1
	}
	if params.Tau <= 0 {
		return 1
	}
	ticksSinceLastCommit := float64(currentTick - lastCommitTick)
	return clampFloor(1-params.Damp*math.Exp(-ticksSinceLastCommit/params.Tau), params.Floor)
}
