package decision

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
