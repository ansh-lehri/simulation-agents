package session

import (
	"math/rand/v2"

	"simulation-agents/agent"
)

// workingAgentView is the minimal per-agent projection the canonical
// close-of-session effects are computed over. Deliberately independent
// of workingAgent/workingSet: a plain slice of these three fields is
// all computeCanonicalTeardown needs, so a later restart-recovery path
// can rebuild the same shape from a different source (not a live
// scheduler's working set) and call the exact same function.
type workingAgentView struct {
	AgentID        agent.AgentID
	CapitalHandle  string
	TradingCapital int64
}

// computeCanonicalTeardown runs the two canonical close-of-session
// effects over one session's agents and returns the decisions to route
// over the lifecycle channel:
//
//  1. The quit-for-good roll, over every agent regardless of presence
//     state — each agent's own per-session quit probability is
//     expectedQuitsPerSession divided by populationSize, so rolling
//     the full population (not just the currently-active subset) is
//     what makes the roll average out to expectedQuitsPerSession
//     quitters across the session.
//  2. The capital fold, over every agent the roll did not just retire:
//     reads each survivor's durable post-close trading cash via
//     readTradingCash and evolves its carried trading capital by this
//     session's realized P&L (durable cash minus the broker's flat
//     per-session funding figure), clamped so it never goes negative.
//
// Pure over its inputs — no I/O happens directly here (readTradingCash
// is the caller's bound accessor, and any logging on its errors is the
// caller's concern), and nothing scheduler-internal appears in the
// signature, so a later caller can rebuild agentViews from any source
// and get the identical decision logic.
func computeCanonicalTeardown(
	sessionRNG *rand.Rand,
	agentViews []workingAgentView,
	populationSize int,
	expectedQuitsPerSession float64,
	brokerDefaultFundedCash int64,
	readTradingCash func(capitalHandle string) (int64, error),
) []LifecycleDecision {
	decisions := make([]LifecycleDecision, 0, len(agentViews))

	retiredAgentIDs := make(map[agent.AgentID]bool, len(agentViews))
	if populationSize > 0 {
		perAgentQuitProbability := clamp01(expectedQuitsPerSession / float64(populationSize))
		for _, view := range agentViews {
			if sessionRNG.Float64() >= perAgentQuitProbability {
				continue
			}
			retiredAgentIDs[view.AgentID] = true
			decisions = append(decisions, LifecycleDecision{
				AgentID: view.AgentID,
				Reason:  ReasonRetire,
			})
		}
	}

	for _, view := range agentViews {
		if retiredAgentIDs[view.AgentID] {
			continue
		}
		tradingCash, err := readTradingCash(view.CapitalHandle)
		if err != nil {
			// A missed durable-cash read leaves this agent's carried
			// capital unchanged for this session rather than blocking
			// every other agent's fold on one failed read — the drift
			// is on a value that evolves across many sessions.
			continue
		}
		evolvedCapital := view.TradingCapital + (tradingCash - brokerDefaultFundedCash)
		if evolvedCapital < 0 {
			evolvedCapital = 0
		}
		decisions = append(decisions, LifecycleDecision{
			AgentID:    view.AgentID,
			Reason:     ReasonCapital,
			NewCapital: evolvedCapital,
		})
	}

	return decisions
}
