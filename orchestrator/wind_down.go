package orchestrator

import (
	"context"
	"time"

	"simulation-agents/agent"
	"simulation-agents/session"
)

// sessionHandle is the orchestrator's record of one live per-exchange
// session — just enough to find it again at teardown: how to stop its
// goroutine, the receive end of its close-of-session decisions, and
// (for an on-demand session only) who to return its leased agents to.
type sessionHandle struct {
	sessionType string                           // SessionTypeCanonical | SessionTypeOnDemand
	country     string                           // the on-demand lease-pool key; unused for canonical
	population  Population                       // this session's full roster, for the on-demand lease release
	cancel      context.CancelFunc               // stops the per-exchange goroutine
	lifecycleCh <-chan session.LifecycleDecision // receive end; the spawn wrapper owns closing it
}

// registerHandle records a session's handle under its exchange id.
// Called before the per-exchange goroutine is spawned, so a WindDown
// arriving immediately after the spawn can never find the registry
// empty.
func (orchestrator *Orchestrator) registerHandle(exchangeID string, handle *sessionHandle) {
	orchestrator.sessionsMu.Lock()
	defer orchestrator.sessionsMu.Unlock()
	orchestrator.sessions[exchangeID] = handle
}

// takeHandle looks up and removes a session's handle in one locked
// step. A second WindDown for the same exchange id — or one that
// arrives after the session already tore itself down — finds nothing
// and gets a nil back, which is exactly what makes WindDown idempotent.
func (orchestrator *Orchestrator) takeHandle(exchangeID string) *sessionHandle {
	orchestrator.sessionsMu.Lock()
	defer orchestrator.sessionsMu.Unlock()
	handle, found := orchestrator.sessions[exchangeID]
	if !found {
		return nil
	}
	delete(orchestrator.sessions, exchangeID)
	return handle
}

// agentIDsFrom extracts the bare ids from a population slice — the
// shape LeaseAllocator.Release needs, independent of the richer Agent
// record the population carries everywhere else.
func agentIDsFrom(agents []agent.Agent) []agent.AgentID {
	agentIDs := make([]agent.AgentID, len(agents))
	for index, agentRecord := range agents {
		agentIDs[index] = agentRecord.ID
	}
	return agentIDs
}

// WindDown is the inbound teardown seam, matching
// transport.AgentOrchestrator. It stops the named session's goroutine,
// drains and persists whatever close-of-session decisions it emitted,
// releases its leased agents if it was an on-demand session, and wipes
// its Redis footprint — finishing with the one write that marks the
// session as fully torn down.
//
// Every step below is best-effort: teardown must make forward progress
// even if one write fails, so a failure is logged and the next step
// still runs rather than aborting the sequence. WindDown itself always
// returns nil once a handle is found — matching its fire-and-forget
// call from the conductor, which never inspects this return value.
func (orchestrator *Orchestrator) WindDown(ctx context.Context, exchangeID, teardownMode string) error {
	handle := orchestrator.takeHandle(exchangeID)
	if handle == nil {
		// Idempotent: a repeat call, or one for a session this process
		// never registered (or already tore down), is a clean no-op.
		orchestrator.logger.Info("wind-down: no live session found, treating as already torn down",
			"exchange_id", exchangeID,
			"teardown_mode", teardownMode,
		)
		return nil
	}

	// Signals the per-exchange goroutine's tick-top cancel check; the
	// goroutine stops producing orders, runs its own close-of-session
	// compute (canonical only), and closes lifecycleCh when it has
	// fully exited.
	handle.cancel()

	// Drains until the close — not a fixed count, not a timeout — so
	// this blocks exactly until the goroutine has provably exited
	// before any of the Redis cleanup below runs.
	for lifecycleDecision := range handle.lifecycleCh {
		switch lifecycleDecision.Reason {
		case session.ReasonRetire:
			if err := orchestrator.agentStore.Retire(ctx, lifecycleDecision.AgentID, time.Now().UTC()); err != nil {
				orchestrator.logger.Error("wind-down: Retire failed",
					"exchange_id", exchangeID,
					"agent_id", lifecycleDecision.AgentID,
					"error", err,
				)
			}
		case session.ReasonCapital:
			if err := orchestrator.agentStore.UpdateTradingCapital(ctx, lifecycleDecision.AgentID, lifecycleDecision.NewCapital); err != nil {
				orchestrator.logger.Error("wind-down: UpdateTradingCapital failed",
					"exchange_id", exchangeID,
					"agent_id", lifecycleDecision.AgentID,
					"error", err,
				)
			}
		}
	}

	if handle.sessionType == SessionTypeOnDemand {
		agentIDs := agentIDsFrom(handle.population.All())
		if err := orchestrator.leaseAllocator.Release(ctx, handle.country, agentIDs); err != nil {
			orchestrator.logger.Error("wind-down: lease Release failed",
				"exchange_id", exchangeID,
				"country", handle.country,
				"error", err,
			)
		}
	}

	if err := orchestrator.runtimeStore.DropSession(ctx, exchangeID); err != nil {
		orchestrator.logger.Error("wind-down: DropSession failed",
			"exchange_id", exchangeID,
			"error", err,
		)
	}
	// Deregister is the commit point: it must run last, after every
	// durable write and every other Redis cleanup above, so the
	// session is only marked dead once everything else has landed.
	if err := orchestrator.activeSessionStore.Deregister(ctx, exchangeID); err != nil {
		orchestrator.logger.Error("wind-down: Deregister failed",
			"exchange_id", exchangeID,
			"error", err,
		)
	}

	orchestrator.logger.Info("wind-down complete",
		"exchange_id", exchangeID,
		"session_type", handle.sessionType,
		"teardown_mode", teardownMode,
	)
	return nil
}
