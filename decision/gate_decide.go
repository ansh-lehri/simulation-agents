package decision

import "math"

// gateOutcome is what gateDecide tells its caller to do with one
// agent's flat/waiting instrument this tick: reuse the standing stance
// untouched (gateCoast) or run the mixer and commit gate fresh
// (gateRedecide). gateDecide only decides; it never mutates waitStreak
// or lastDecidedReadings itself — those updates (increment on coast,
// reset on redecide) are the caller's job, the same way the 4a gate's
// coast-vs-recompute branch is wired by session, not by decision.
type gateOutcome int8

const (
	gateCoast gateOutcome = iota
	gateRedecide
)

// gateDecide is the re-decide gate for one agent's one instrument this
// tick. It keys off perception-OUTPUT change — how much this agent's
// own readings moved since its own last decided snapshot — never off
// raw market change (that is 4a's gate, deliberately a different
// signal so the two gates desync).
//
// A waiter (pendingWatch != nil) coasts by default: a quiet
// consolidation is exactly the confirmation a waiter is hoping to see,
// and reading it as "nothing changed" would freeze every confirmation
// path. It only gets forced to reassess once it has coasted past
// WaitReassessThreshold consecutive ticks. A flat-idle agent
// (pendingWatch == nil) instead compares each field it actually
// consumes against its own per-field sensitivity; any one field moving
// past its threshold is enough to redecide — this mirrors 4a's "any
// on-channel trips, recompute the whole bundle" rule, applied here to
// readings instead of raw market data.
func gateDecide(
	pendingWatch *PendingWatch,
	waitStreak int,
	currentReadings, lastDecidedReadings map[string]float64,
	sensitivities map[string]float64,
	params GateParams,
) gateOutcome {
	if pendingWatch != nil {
		if waitStreak > params.WaitReassessThreshold {
			return gateRedecide
		}
		return gateCoast
	}

	// No decided reading at all yet — this agent has never run the
	// mixer for this instrument, so there is nothing to compare
	// staleness against. Mirrors 4a's "empty refs always recompute"
	// rule for the same reason: a first-ever decide can't coast on a
	// baseline that doesn't exist.
	if len(lastDecidedReadings) == 0 {
		return gateRedecide
	}

	for field, threshold := range sensitivities {
		// A missing or non-positive sensitivity disables that field's
		// staleness check on its own — it never forces a redecide by
		// itself, the same safe-fallback convention 4a's gate uses.
		if threshold <= 0 {
			continue
		}
		currentValue, hasCurrentValue := currentReadings[field]
		if !hasCurrentValue {
			// This field isn't available this tick (its channel went
			// unreadable, say) — nothing to compare, so it cannot be
			// the reason to redecide.
			continue
		}
		lastValue, hasLastValue := lastDecidedReadings[field]
		if !hasLastValue {
			return gateRedecide
		}
		if math.Abs(currentValue-lastValue) > threshold {
			return gateRedecide
		}
	}
	return gateCoast
}
