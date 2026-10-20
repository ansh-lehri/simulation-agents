package session

import "simulation-agents/agent"

// LifecycleDecision is sent by the tick loop's presence sweep when an
// agent reaches a transition that requires a durable write. The
// orchestrator drains the channel and applies the consequence.
//
// Decide/write split: the scheduler DECIDES the transition and sends
// this message; the orchestrator WRITES the durable consequence
// (Postgres retired_at today; Core account-close added in Tier 6).
// The channel is the one seam bridging the two actors across the
// bring-up spawn boundary.
type LifecycleDecision struct {
	AgentID agent.AgentID
	Reason  string
}

// ReasonRetire is the only Reason the prototype sweep emits. It
// signals that an agent has reached a for-good exit and its durable
// row should be marked retired. The actual Core account-close that
// pairs with it is wired in Tier 6; today the orchestrator drain
// writes only Postgres.
const ReasonRetire = "retire_for_good"
