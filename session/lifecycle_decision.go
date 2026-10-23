package session

import "simulation-agents/agent"

// LifecycleDecision is sent by the tick loop's presence sweep when an
// agent reaches a transition that requires a durable write. The
// orchestrator drains the channel and applies the consequence.
//
// Decide/write split: the scheduler DECIDES the transition and sends
// this message; the orchestrator WRITES the durable consequence
// (Postgres retired_at, or the evolved trading_capital for a capital
// fold). The channel is the one seam bridging the two actors across
// the bring-up spawn boundary.
type LifecycleDecision struct {
	AgentID agent.AgentID
	Reason  string

	// NewCapital is the agent's evolved session capital, set only
	// when Reason == ReasonCapital. Zero and unused for every other
	// reason.
	NewCapital int64
}

// ReasonRetire signals that an agent has reached a for-good exit and
// its durable row should be marked retired. There is no paired Core
// account-close call — the broker has no such endpoint, so the
// orchestrator drain writes only the Postgres tombstone.
const ReasonRetire = "retire_for_good"

// ReasonCapital signals that a canonical session has closed and this
// agent's session capital has evolved off the day's realized P&L. The
// orchestrator drain writes NewCapital to the agent's persisted
// trading_capital column.
const ReasonCapital = "capital_fold"
