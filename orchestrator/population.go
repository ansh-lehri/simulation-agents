// Package orchestrator sequences the bring-up spine: receive the inbound
// trigger, assemble a Population through the generator, open broker
// accounts for the newly minted agents, resolve each agent's session
// trading capital, and hand off to the asynchronous tail that persists,
// snapshots, enlists, and spawns the per-exchange run goroutine.
//
// This file pins the one shared value type the orchestrator surfaces to
// its callers (Population) and the session-type constants the bring-up
// trigger dispatches on. The actual Population shape lives in the
// generator subpackage because the generator is the type's producer; the
// orchestrator aliases it so consumers can refer to it by the
// orchestrator's own package name without an awkward import indirection.
package orchestrator

import (
	"simulation-agents/agent"
	"simulation-agents/orchestrator/generator"
)

// SessionType names the two bring-up dispatch branches. The string
// values intentionally match the agent-package population labels so a
// single string flows end-to-end: a bring-up trigger with
// SessionType="canonical" assembles a canonical population and persists
// each minted agent with Population="canonical" on its durable row.
const (
	// SessionTypeCanonical names the long-lived population: generated
	// once per country and evolved across sessions. Revive-first then
	// mint the shortfall. Exactly one canonical member carries the
	// static tipper flag.
	SessionTypeCanonical = agent.PopulationCanonical

	// SessionTypeOnDemand names the per-session leased population:
	// agents are leased from a per-country pool, their traits are
	// redrawn fresh for scenario coverage, and the shortfall is minted.
	// The on-demand tipper is a runtime assignment, not a frozen trait.
	SessionTypeOnDemand = agent.PopulationOnDemand
)

// Population is the orchestrator-visible name for the generator's output
// shape. Aliased (not re-wrapped) because every downstream step —
// account-open, capital resolve, persistence, runtime snapshot, lifecycle
// handoff — treats the two slices as a single logical set that the
// generator built.
type Population = generator.Population
