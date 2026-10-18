package orchestrator

import (
	"context"

	"simulation-agents/transport"
)

// This file holds stubs for the two bring-up tail methods — the
// asynchronous persist-snapshot-spawn step and the per-exchange run
// goroutine body. Both are called by name from the spine so that
// wiring is in place and the shape of the handoff is settled; their
// implementations land in the persistence+live phase.
//
// Keeping the stubs here (rather than inside orchestrator.go) makes it
// easy to see at a glance what has shipped and what has not: anything
// in this file is a placeholder, every other method is a real
// implementation.

// continueBringUp is the asynchronous tail of a bring-up: insert the
// new agents' durable rows, clear their write-ahead-log entries, write
// the initial Redis snapshot per agent, enlist the newly minted
// on-demand agents into the lease, assign the on-demand runtime tipper,
// register the session in the active-sessions map, and spawn the per-
// exchange run goroutine as the final handoff.
//
// STUB — the implementation lands in the persistence+live phase. The
// shape of the arguments is pinned here so the spine step can wire the
// call once and never change the signature again.
func (orchestrator *Orchestrator) continueBringUp(ctx context.Context, request transport.BringUpRequest, population Population) {
	_ = ctx
	orchestrator.logger.Info(
		"continueBringUp stub reached (persistence+live phase to replace)",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"existing_count", len(population.Existing),
		"new_count", len(population.New),
	)
}

// runAgentPopulation is the per-exchange run goroutine's body: stamp
// each agent's entry time, run the three pre-open operations
// concurrently (session-load, history pull, websocket subscribe), join
// them, wait for the session's start wall-clock, then start the tick
// loop that drives the active agents into the broker.
//
// STUB — the implementation lands in the persistence+live phase. One
// goroutine per exchange is the invariant the handoff is designed
// around; this stub does not spawn anything of its own.
func (orchestrator *Orchestrator) runAgentPopulation(ctx context.Context, request transport.BringUpRequest, population Population) {
	_ = ctx
	orchestrator.logger.Info(
		"runAgentPopulation stub reached (persistence+live phase to replace)",
		"exchange_id", request.ExchangeID,
		"existing_count", len(population.Existing),
		"new_count", len(population.New),
	)
}
