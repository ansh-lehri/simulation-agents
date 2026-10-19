package session

// LifecycleDecision is the message shape the tick loop will emit on
// the lifecycle channel the orchestrator creates at spawn time. Its
// field set lands in a later phase alongside the lifecycle presence
// transitions it describes; this file pins the type name so the
// handoff signature (Run and Scheduler.StartScheduler both carry a
// chan<- LifecycleDecision) is stable from this stage onward.
//
// Decide/write split: the Runner — through the scheduler — DECIDES a
// presence transition and sends the decision on this channel. The
// orchestrator reads the channel and WRITES the durable consequence.
// The channel is the one seam that bridges the two actors across the
// bring-up spawn boundary.
type LifecycleDecision struct {
	// Fields land in the lifecycle-presence phase. Keeping the
	// struct empty for now avoids locking a shape before the
	// transition set is designed; the type's presence is enough to
	// stabilize the handoff signatures.
}
