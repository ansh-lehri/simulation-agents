package decision

import (
	"math"
	"math/rand/v2"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/store"
)

// Exit-profile mode identifiers, drawn at generation time
// (orchestrator/generator/draw_exit_and_hold.go) into
// agent.ExitProfile.Mode. Fixed by the generator's own calibration —
// an agent.TraitVector reaching this package with any other value is
// an upstream invariant violation, not a case to handle gracefully.
const (
	exitModeDistance = "distance"
	exitModeLevel    = "level"
	exitModeHybrid   = "hybrid"
)

// ManageInput bundles everything Manage needs to decide what to do
// with an instrument the agent already holds. Built once per
// instrument by the root-dispatch fork inside session.decide — Manage
// never fetches any of this itself, the same no-I/O discipline as
// every other pure function in this package.
type ManageInput struct {
	// Position is this instrument's current holding, already resolved
	// by root-dispatch from the one per-tick observation read. Qty is
	// signed: positive for long, negative for short.
	Position core.Position

	// Bundle is this tick's already-produced perception readings for
	// this instrument — the same bundle 4a perceives for every
	// instrument, flat or holding, so the reversal check can reuse it
	// without triggering a second perceive.
	Bundle *PerceptionBundle

	// PositionContext is the frozen entry conditions stamped when this
	// position was opened or last added to. Nil is a harmless recovery
	// edge (a position exists but no stamp has landed yet) handled
	// explicitly below, never a crash.
	PositionContext *store.PositionContext

	// Traits is this agent's frozen trait vector — Exit and MaxHold
	// drive the exit check, Disposition drives the add-check's reused
	// mixer.
	Traits agent.TraitVector

	// CurrentPrice is this instrument's current price as this agent
	// itself perceives it (4a's accessor), used to mark the position
	// and to evaluate the stop/target/level triggers.
	CurrentPrice int64

	// Tick carries the current tick index the time-stop check compares
	// against this position's entry tick.
	Tick TickContext

	// Roles is the fixed channel-to-trait map the add-check's reused
	// mixer call needs, identical to the map 4b's own flat-path
	// decision passes to interpret.
	Roles map[string]TraitChannelRole

	// RNG is this agent's own per-agent randomness stream, used only
	// by the reused mixer's dominance tie-break on the add-check path.
	RNG *rand.Rand

	// Params is the global calibration surface the exit check and the
	// add-check's θ_add derivation both read from.
	Params Params
}

// Manage decides what to do with an instrument the agent already
// holds this tick: exit, add to the winning position, or hold. Returns
// a zero-value NodeOutcome (every field nil) on hold — nothing to
// apply, nothing to park. Manage never returns a non-nil Assessment:
// the holding path does not read or update the flat-path's standing-
// opinion/pending-watch/wait-streak bookkeeping, so there is nothing
// for it to report there. Pure function, same discipline as every
// other node in this package: no I/O, no Core calls, no Redis; Manage
// reads only its ManageInput and returns a value.
func Manage(input ManageInput) NodeOutcome {
	// Step 0 — defensive guard only: root-dispatch has already proved
	// this instrument is held before routing here. A zero quantity
	// reaching this function means it is not this stage's tick to act
	// on, not an error to raise.
	if input.Position.Qty == 0 {
		return NodeOutcome{}
	}
	if input.Traits.Exit == nil || input.Traits.MaxHold == nil || input.Traits.ThetaAct == nil || input.Traits.Disposition == nil {
		panic("decision: Manage called on a TraitVector missing exit_profile/max_hold/theta_act/disposition — these are generated once at mint for every endogenous agent and must never be absent while holding a position")
	}

	entryDirection := directionOf(float64(input.Position.Qty))
	entryChannel, entryTick := liveEntryChannelAndTick(input.PositionContext, input.Tick.TickIndex)

	if closeIntent, exit := checkExit(input, entryDirection, entryChannel, entryTick); exit {
		// A close never carries a commitment: positionContext is not
		// cleared here — the close may not fill this tick, and a flat
		// path's next COMMIT overwrites it on its own anyway.
		return NodeOutcome{Candidate: &closeIntent}
	}

	unrealized := unrealizedPnL(input.CurrentPrice, input.Position.WeightedAvgEntry, input.Position.Qty)
	if addIntent, addChannel, add := checkAdd(input, entryDirection, unrealized); add {
		return NodeOutcome{
			Candidate: &addIntent,
			Commitment: &CommitmentDelta{
				LastCommitTick: input.Tick.TickIndex,
				PositionContext: store.PositionContext{
					EntryDir:     entryDirectionLabel(entryDirection),
					EntryChannel: addChannel,
					EntryTick:    input.Tick.TickIndex,
				},
			},
		}
	}

	return NodeOutcome{}
}

// channelOf maps a mixer opinion's dominant trait to the structural
// channel its entry was stamped against: trend's lean channel is
// chart, mean-reversion's is range. The noise-coin impulse (4b's entry
// path only — the add-check never rolls it) reads no structure at all,
// so it maps to "noise", matching the sentinel levelsFor and
// reversalTriggered already treat as "nothing to look up."
func channelOf(dominantTrait string) string {
	switch dominantTrait {
	case traitTrend:
		return "chart"
	case traitMeanRev:
		return "range"
	default:
		return "noise"
	}
}

// ExecIntentActionAdd marks a scale-into-the-position request for an
// instrument the agent already holds and is winning on. Produced only
// by this package's add-check below — never by 4b's commit (open
// only) or 4c (which never originates an intent, only sizes one it is
// handed).
const ExecIntentActionAdd ExecIntentAction = "add"

// checkAdd decides whether to add to an already-winning position this
// tick. It reuses 4b's own interpret+mix on this tick's bundle —
// shared mechanism, not a duplicate — rather than running a second
// opinion-forming pass. An add never rolls the noise-coin bypass: it
// calls interpret+mix directly, the same signal-only path 4b's mixer
// exposes once the noise coin has already missed, since the noise
// coin is an entry-liveness impulse, not a reason to pyramid an
// already-live position.
//
// ADD fires only when ALL of: the mixer's opinion agrees with the
// position's own direction (an add never reverses), the position is
// currently in profit (add to winners, not losers), and the opinion's
// conviction clears θ_add — a deliberately higher bar than the entry
// bar θ_act, by cfg.Params.ThetaAddMargin. Any one condition failing
// is a hold, not a partial action. The second return is the channel
// the winning opinion's dominant trait reads from (chart/range) — the
// re-stamp Manage applies to positionContext on a real add.
func checkAdd(input ManageInput, entryDirection float64, unrealized int64) (ExecIntent, string, bool) {
	bundle := PerceptionBundle{}
	if input.Bundle != nil {
		bundle = *input.Bundle
	}
	verdicts := interpret(bundle, input.Traits, input.Roles, input.Params.Interpretation)
	opinion := mix(verdicts, *input.Traits.Disposition, input.RNG, input.Params.Mixer)
	if opinion == nil {
		return ExecIntent{}, "", false
	}

	thetaAdd := *input.Traits.ThetaAct + input.Params.ThetaAddMargin
	sameDirection := opinion.Direction == entryDirection
	inProfit := unrealized > 0
	convictionClearsBar := opinion.Conviction >= thetaAdd

	if !sameDirection || !inProfit || !convictionClearsBar {
		return ExecIntent{}, "", false
	}
	return ExecIntent{Action: ExecIntentActionAdd, Direction: entryDirection, Conviction: opinion.Conviction}, channelOf(opinion.DominantTrait), true
}

// checkExit evaluates this tick's exit condition. A level-mode agent
// whose structural levels aren't available this tick exits
// unconditionally, regardless of price — the Y-rule, the deliberate
// chart-blind cost of choosing a level-based exit. Otherwise, the
// four triggers run in fixed priority — stop, then signal-reversal,
// then target, then time-stop — as a tagless switch: Go evaluates
// each case top to bottom and stops at the first true one, which is
// exactly "first trigger wins, the rest are never evaluated." Every
// branch that fires produces the identical close intent; only
// opposite(entry_dir) and the fact that something fired matter
// downstream, not which condition it was.
func checkExit(input ManageInput, entryDirection float64, entryChannel string, entryTick int64) (ExecIntent, bool) {
	stop, target, abruptExit := exitPrices(
		input.Traits.Exit, input.Position.WeightedAvgEntry, entryDirection,
		entryChannel, input.Bundle, input.CurrentPrice,
	)

	switch {
	case abruptExit:
	case stopTriggered(input.CurrentPrice, stop, entryDirection):
	case reversalTriggered(entryChannel, entryDirection, input.CurrentPrice, input.Bundle):
	case targetTriggered(input.CurrentPrice, target, entryDirection):
	case timeStopTriggered(input.Tick.TickIndex, entryTick, *input.Traits.MaxHold):
	default:
		return ExecIntent{}, false
	}
	return ExecIntent{Action: ExecIntentActionClose, Direction: -entryDirection}, true
}

// exitPrices derives this tick's stop and target prices from the
// agent's frozen exit profile. distance prices off the position's own
// entry; level prices off this tick's perceived structural levels,
// falling back to distance only for hybrid and only when those levels
// aren't available — never the reverse order. A pure "level" agent
// that can't resolve levels this tick gets no stop/target at all —
// abruptExit tells the caller to close unconditionally instead.
func exitPrices(
	exitProfile *agent.ExitProfile,
	weightedAvgEntry int64,
	entryDirection float64,
	entryChannel string,
	bundle *PerceptionBundle,
	currentPrice int64,
) (stop, target int64, abruptExit bool) {
	switch exitProfile.Mode {
	case exitModeDistance:
		stop, target = distanceExitPrices(weightedAvgEntry, entryDirection, exitProfile)
		return stop, target, false
	case exitModeLevel:
		support, resistance, ok := levelsFor(entryChannel, bundle, currentPrice)
		if !ok {
			return 0, 0, true
		}
		stop, target = levelExitPrices(entryDirection, exitProfile, support, resistance)
		return stop, target, false
	case exitModeHybrid:
		if support, resistance, ok := levelsFor(entryChannel, bundle, currentPrice); ok {
			stop, target = levelExitPrices(entryDirection, exitProfile, support, resistance)
			return stop, target, false
		}
		stop, target = distanceExitPrices(weightedAvgEntry, entryDirection, exitProfile)
		return stop, target, false
	default:
		panic("decision: exitPrices called on an agent.ExitProfile with unknown Mode " + exitProfile.Mode)
	}
}

// distanceExitPrices prices stop/target as a percentage of the
// position's own entry price. TargetPct is guaranteed greater than
// StopPct at generation (TargetPct = StopPct × rewardRisk), so target
// always sits further from entry than stop without this function
// re-deriving that guarantee.
func distanceExitPrices(weightedAvgEntry int64, entryDirection float64, exitProfile *agent.ExitProfile) (stop, target int64) {
	stop = scaledTick(weightedAvgEntry, -entryDirection*exitProfile.StopPct)
	target = scaledTick(weightedAvgEntry, entryDirection*exitProfile.TargetPct)
	return stop, target
}

// levelExitPrices prices stop/target off this tick's structural
// levels. A long position stops below support and targets below
// resistance (banking profit a little early, before fully reaching
// it); a short position mirrors both onto the opposite level, per the
// same buffer-as-fraction-of-level-price shape.
func levelExitPrices(entryDirection float64, exitProfile *agent.ExitProfile, support, resistance int64) (stop, target int64) {
	stopBase, targetBase := support, resistance
	if entryDirection < 0 {
		stopBase, targetBase = resistance, support
	}
	stop = scaledTick(stopBase, -entryDirection*exitProfile.StopBuffer)
	target = scaledTick(targetBase, -entryDirection*exitProfile.TargetBuffer)
	return stop, target
}

// scaledTick multiplies an integer tick price by (1 + fraction) and
// rounds once to the nearest tick — the one int64 rounding boundary
// every exit-price calculation shares, consistent with 4a's level
// rounding (round once at the boundary, never carry floats through
// multiple steps).
func scaledTick(value int64, fraction float64) int64 {
	return int64(math.Round(float64(value) * (1 + fraction)))
}

// levelsFor resolves the structural support/resistance levels for the
// channel this position was entered on. A chart entry reads 4a's
// nearest key levels either side of the current price; a range entry
// reads the current range bounds directly; a noise entry read no
// structure at entry, so it has nothing to look up here. Any missing
// channel, unreadable reading, or (for chart) a side with no
// qualifying level at all reports ok=false.
func levelsFor(entryChannel string, bundle *PerceptionBundle, currentPrice int64) (support, resistance int64, ok bool) {
	switch entryChannel {
	case "chart":
		return chartLevels(bundle, currentPrice)
	case "range":
		return rangeLevels(bundle)
	default:
		// "noise", and the empty string the nil-positionContext
		// degrade reports, both mean no structure to look up.
		return 0, 0, false
	}
}

// chartLevels picks the nearest key level below the current price as
// support and the nearest one above as resistance. Both sides must be
// present — a one-sided level list (e.g. only resistance above, no
// support below) is not enough to price both a stop and a target.
func chartLevels(bundle *PerceptionBundle, currentPrice int64) (support, resistance int64, ok bool) {
	if bundle == nil || bundle.Chart == nil || !bundle.Chart.ReadOK {
		return 0, 0, false
	}
	haveSupport, haveResistance := false, false
	for _, level := range bundle.Chart.KeyLevels {
		switch {
		case level < currentPrice && (!haveSupport || level > support):
			support, haveSupport = level, true
		case level > currentPrice && (!haveResistance || level < resistance):
			resistance, haveResistance = level, true
		}
	}
	if !haveSupport || !haveResistance {
		return 0, 0, false
	}
	return support, resistance, true
}

// rangeLevels reads the current range bounds directly as
// support/resistance — a range reading always carries both sides when
// readable, unlike a chart's open-ended key-level list.
func rangeLevels(bundle *PerceptionBundle) (support, resistance int64, ok bool) {
	if bundle == nil || bundle.Range == nil || !bundle.Range.ReadOK {
		return 0, 0, false
	}
	return bundle.Range.RangeBounds[0], bundle.Range.RangeBounds[1], true
}

// stopTriggered reports whether price has reached or passed the stop:
// at-or-below for a long, at-or-above for a short.
func stopTriggered(currentPrice, stop int64, entryDirection float64) bool {
	if entryDirection > 0 {
		return currentPrice <= stop
	}
	return currentPrice >= stop
}

// targetTriggered reports whether price has reached or passed the
// target: at-or-above for a long, at-or-below for a short.
func targetTriggered(currentPrice, target int64, entryDirection float64) bool {
	if entryDirection > 0 {
		return currentPrice >= target
	}
	return currentPrice <= target
}

// reversalTriggered reuses THIS tick's already-produced bundle — it
// never triggers a fresh perceive. A chart entry reverses when the
// chart's direction has actively turned against entryDirection
// (DirFlat is not active opposition, so it never counts); a range
// entry reverses when price has broken clean outside the range bounds
// — the range reading carries no direction of its own, so this can
// fire even while the position is in profit, and is deliberately
// non-redundant with the stop/target checks for that reason; a noise
// entry read no signal at entry and so never reverses.
func reversalTriggered(entryChannel string, entryDirection float64, currentPrice int64, bundle *PerceptionBundle) bool {
	switch entryChannel {
	case "chart":
		if bundle == nil || bundle.Chart == nil || !bundle.Chart.ReadOK {
			return false
		}
		if entryDirection > 0 {
			return bundle.Chart.Direction == DirDown
		}
		return bundle.Chart.Direction == DirUp
	case "range":
		if bundle == nil || bundle.Range == nil || !bundle.Range.ReadOK {
			return false
		}
		low, high := bundle.Range.RangeBounds[0], bundle.Range.RangeBounds[1]
		return currentPrice < low || currentPrice > high
	default:
		// "noise" and the empty-string nil-positionContext degrade
		// both read no structure to reverse against.
		return false
	}
}

// timeStopTriggered compares ticks held against this agent's frozen
// hold cap. Strictly greater-than: a position held for exactly
// maxHold ticks has not yet overstayed it.
func timeStopTriggered(currentTick, entryTick int64, maxHold int) bool {
	return currentTick-entryTick > int64(maxHold)
}

// unrealizedPnL computes this instrument's mark-to-market profit or
// loss against the agent's own perceived current price. qty is
// signed (+long, -short), so the sign of the result is automatic — no
// long/short branch belongs here.
func unrealizedPnL(currentPrice, weightedAvgEntry, qty int64) int64 {
	return (currentPrice - weightedAvgEntry) * qty
}

// closeableQuantity is how many units of this position are not
// already sitting behind a resting order. Part of this stage's
// documented live picture, but no exit/add branch in this package
// reads it today — 4c's own close path (buildCloseOrder, execute.go)
// independently recomputes the identical value from the ambient
// position it reads, which is what actually sizes a close order.
func closeableQuantity(qty, reservedQty int64) int64 {
	return absInt64(qty) - reservedQty
}

// liveEntryChannelAndTick resolves the entry-channel and entry-tick
// the exit check needs from this instrument's stamped position
// context. A nil context is the recovery edge — a position exists but
// no stamp has been written yet, e.g. immediately after a restart —
// and degrades harmlessly: the returned channel matches none of
// "chart"/"range"/"noise", so the reversal check skips this tick, and
// the entry tick defaults to the current tick, so the time-stop reads
// zero ticks held and stays disarmed until a real stamp lands.
func liveEntryChannelAndTick(positionContext *store.PositionContext, currentTick int64) (entryChannel string, entryTick int64) {
	if positionContext == nil {
		return "", currentTick
	}
	return positionContext.EntryChannel, positionContext.EntryTick
}
