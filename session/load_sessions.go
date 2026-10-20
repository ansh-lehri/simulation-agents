package session

import (
	"context"
	"fmt"
	"sync"

	"simulation-agents/agent"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/transport"
)

// loadSessions runs the per-agent session-install op during the
// pre-open phase. For every agent in the population it issues the
// appropriate Core join call (canonical or on-demand) and collects
// the result. The op is partial-ok by design: a single agent's
// install failure marks the agent in Redis so the tick-loop
// scheduler skips it later, and the rest of the bring-up proceeds.
// The op fails fatally only when ZERO agents install successfully —
// a session with no live members cannot run.
//
// The returned feedSessionID is the session id of the lowest-
// indexed agent that installed successfully. The exchange's one
// live-feed WebSocket binds via a session id; any installed agent's
// id works, so picking by agent order (rather than by goroutine
// completion order) makes the feed id deterministic under a fixed
// seed — a replay of the same bring-up chooses the same feed id.
//
// Concurrency is bounded by Params.LoadSessionsConcurrency. A large
// population would otherwise open hundreds of concurrent sockets to
// Core; the semaphore caps the fan-out while still parallelising
// what Core can handle in parallel.
//
// Context cancellation is treated as a hard abort: after the fan-out
// drains, a cancelled ctx returns ctx.Err() without running the
// mark-and-return pass. This keeps a terminated bring-up from
// writing per-agent markers for installs the caller no longer
// cares about.
func (runner *Runner) loadSessions(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
) (string, error) {
	allAgents := population.All()
	if len(allAgents) == 0 {
		return "", fmt.Errorf("session: loadSessions received an empty population")
	}

	// Market tag convention is one canonical market per country.
	marketTag := request.Country

	concurrencyLimit := runner.dependencies.Params.LoadSessionsConcurrency
	if concurrencyLimit <= 0 {
		concurrencyLimit = 1
	}

	resultsByAgentID := &sync.Map{}
	semaphore := make(chan struct{}, concurrencyLimit)
	var fanOutWaitGroup sync.WaitGroup
	for _, agentRecord := range allAgents {
		agentRecord := agentRecord
		fanOutWaitGroup.Add(1)
		go func() {
			defer fanOutWaitGroup.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			sessionID, installErr := runner.installOneAgent(
				ctx,
				request.SessionType,
				string(agentRecord.ID),
				marketTag,
			)
			resultsByAgentID.Store(agentRecord.ID, installResult{
				sessionID: sessionID,
				installError: installErr,
			})
		}()
	}
	fanOutWaitGroup.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", fmt.Errorf("session: loadSessions aborted: %w", ctxErr)
	}

	// Walk allAgents in order so the feed session id is picked
	// deterministically: lowest-indexed agent that installed wins.
	// Also marks every failing agent via Redis in that same order.
	feedSessionID := ""
	installedCount := 0
	for _, agentRecord := range allAgents {
		resultValue, found := resultsByAgentID.Load(agentRecord.ID)
		if !found {
			// Should not happen: every fan-out goroutine stores a
			// result before returning. Treat as a per-agent failure
			// and surface in the logs — the invariant violation is
			// worth seeing but not worth aborting the pre-open for.
			runner.dependencies.Logger.Error(
				"session: loadSessions result missing for agent (fan-out invariant violated)",
				"exchange_id", request.ExchangeID,
				"agent_id", agentRecord.ID,
			)
			continue
		}
		result := resultValue.(installResult)
		if result.installError != nil {
			if markErr := runner.dependencies.Runtime.MarkSessionLoadFailed(
				ctx, request.ExchangeID, agentRecord.ID, result.installError.Error(),
			); markErr != nil {
				// Marker-write failure is a Redis issue the next
				// phase will retry; we do not abort the whole
				// bring-up for one unmarked agent. Log and move on.
				runner.dependencies.Logger.Warn(
					"session: MarkSessionLoadFailed itself failed; continuing",
					"exchange_id", request.ExchangeID,
					"agent_id", agentRecord.ID,
					"mark_err", markErr,
				)
			}
			continue
		}
		installedCount++
		if feedSessionID == "" {
			feedSessionID = result.sessionID
		}
	}

	if installedCount == 0 {
		return "", fmt.Errorf("session: loadSessions: all %d agents failed to install", len(allAgents))
	}

	runner.dependencies.Logger.Info(
		"session: loadSessions complete",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"total_agents", len(allAgents),
		"installed", installedCount,
		"failed", len(allAgents)-installedCount,
	)
	return feedSessionID, nil
}

// installResult carries one agent's per-fan-out outcome from the
// goroutine that executed it to the post-wait collector. Private
// to this file because no other op has the same shape.
type installResult struct {
	sessionID    string
	installError error
}

// installOneAgent dispatches on session type and translates the Core
// response into a plain (session id, error) pair the fan-out
// collector can bucketise. Only the canonical branch is live in
// this stage; the on-demand branch is deferred — the on-demand join
// endpoint on the Core client is not part of this stage's scope,
// so a bring-up with SessionType=ondemand returns a clear error
// per agent and the whole op then surfaces its "total failure"
// fatal to the caller.
func (runner *Runner) installOneAgent(
	ctx context.Context,
	sessionType string,
	agentUserID string,
	marketTag string,
) (string, error) {
	switch sessionType {
	case agent.PopulationCanonical:
		join, joinErr := runner.dependencies.Core.JoinCanonical(ctx, agentUserID, marketTag)
		if joinErr != nil {
			return "", joinErr
		}
		if join.SessionID == "" {
			return "", fmt.Errorf("session: canonical install returned empty session_id")
		}
		return join.SessionID, nil
	case agent.PopulationOnDemand:
		return "", fmt.Errorf("session: on-demand session install is not implemented in this stage")
	default:
		return "", fmt.Errorf("session: installOneAgent received unknown session type %q", sessionType)
	}
}
