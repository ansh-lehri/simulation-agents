package generator

import "simulation-agents/agent"

// Population is the output of one bring-up's generation pass: two
// disjoint sets of agents the orchestrator then hands to the account-open
// step and the capital-resolver.
//
// Existing holds the identities reused from the durable store (revived
// canonical members, or leased on-demand members). These already carry
// a capital_handle and, for canonical, a persisted trading_capital that
// has evolved across sessions. They do NOT need an account opened on
// the broker again.
//
// New holds the freshly minted identities whose durable rows have not
// yet been written and whose economy accounts have not yet been opened.
// Everything in New flows through the account-open step; the trading
// capital draw against the broker's funding ceiling happens after that
// handle is set.
//
// The two slices never overlap: an agent is either revived/leased or
// freshly minted, never both.
type Population struct {
	Existing []agent.Agent
	New      []agent.Agent
}

// All returns every agent in the population in one slice, Existing
// first then New. Used by callers that need to iterate the whole set
// (initial Redis snapshot, session-load, etc.) without caring which
// half an agent came from.
func (population Population) All() []agent.Agent {
	combined := make([]agent.Agent, 0, len(population.Existing)+len(population.New))
	combined = append(combined, population.Existing...)
	combined = append(combined, population.New...)
	return combined
}
