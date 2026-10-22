package decision

import (
	"math/rand/v2"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/store"
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
// holds this tick: exit, add to the winning position, or hold. A
// false second return means hold — emit nothing. Pure function, same
// discipline as every other node in this package: no I/O, no Core
// calls, no Redis; Manage reads only its ManageInput and returns a
// value.
func Manage(input ManageInput) (ExecIntent, bool) {
	// Step 0 — defensive guard only: root-dispatch has already proved
	// this instrument is held before routing here. A zero quantity
	// reaching this function means it is not this stage's tick to act
	// on, not an error to raise.
	if input.Position.Qty == 0 {
		return ExecIntent{}, false
	}

	// The exit check (stop/reversal/target/time-stop) and the
	// add-check land in later stages of this build; they will read
	// this position's live picture (entry direction, unrealized P&L,
	// closeable quantity, and the entry channel/tick resolved below)
	// to decide close/add/hold.
	return ExecIntent{}, false
}

// unrealizedPnL computes this instrument's mark-to-market profit or
// loss against the agent's own perceived current price. qty is
// signed (+long, -short), so the sign of the result is automatic — no
// long/short branch belongs here.
func unrealizedPnL(currentPrice, weightedAvgEntry, qty int64) int64 {
	return (currentPrice - weightedAvgEntry) * qty
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
