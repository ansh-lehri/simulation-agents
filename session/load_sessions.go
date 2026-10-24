package session

import (
	"context"
	"fmt"
	"sync"

	"simulation-agents/agent"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/transport"
)

// loadSessions is the composite op that stage-C tests exercise: it
// walks the population serially until the first install succeeds
// (installFeedAgent), then fans out installs for everyone else in
// parallel (installRemainingAgents). Run uses the two inner helpers
// separately so it can run the parallel-install leg concurrently
// with pullHistory and subscribeLive; this wrapper is kept so a
// caller that just needs "install everyone and give me a feed id"
// has one entry point.
//
// Returns the feed session id (the session id of the agent that
// first installed successfully) or a fatal error if every agent
// failed to install. Partial-ok semantics inside: a single-agent
// failure marks the agent in Redis so the tick-loop scheduler can
// skip it later, and the rest of the bring-up proceeds.
func (runner *Runner) loadSessions(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
) (string, error) {
	feedSessionID, feedAgentIndex, feedPassErrors, feedErr := runner.installFeedAgent(ctx, request, population)
	if feedErr != nil {
		return "", feedErr
	}
	if err := runner.installRemainingAgents(ctx, request, population, feedAgentIndex, feedPassErrors); err != nil {
		return "", err
	}
	return feedSessionID, nil
}

// installFeedAgent walks the population in order and tries to
// install each agent until the first success. The agent that
// succeeds is the "feed agent" — its session id becomes the id
// subscribeLive uses to open the live-feed WebSocket. Running the
// search serially (rather than fanning out) lets Run start the
// three concurrent pre-open ops as soon as a single successful
// install exists, which with a population of thousands is
// meaningfully faster than waiting for every install to finish.
//
// Returns:
//   - feedSessionID: the session id of the agent that first
//     succeeded.
//   - feedAgentIndex: that agent's index in population.All(), so
//     installRemainingAgents can skip it.
//   - firstPassErrors: errors observed on the agents that failed
//     before the first success. installRemainingAgents retries
//     those agents in its parallel pass, so this map is only
//     consulted for logging / the "all failed" diagnostic.
//   - err: non-nil only when every agent failed; the bring-up
//     cannot proceed without a feed id.
//
// Context cancellation returns ctx.Err() wrapped so the caller
// can distinguish a cancelled walk from a legitimate all-failed
// outcome.
func (runner *Runner) installFeedAgent(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
) (string, int, map[agent.AgentID]error, error) {
	allAgents := population.All()
	if len(allAgents) == 0 {
		return "", -1, nil, fmt.Errorf("session: installFeedAgent received an empty population")
	}
	firstPassErrors := make(map[agent.AgentID]error)
	for agentIndex, agentRecord := range allAgents {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", -1, firstPassErrors, fmt.Errorf("session: installFeedAgent aborted: %w", ctxErr)
		}
		runner.dependencies.Logger.Info("REQUEST::::       ", request)
		sessionID, installErr := runner.installOneAgent(ctx, request, string(agentRecord.ID))
		if installErr != nil {
			firstPassErrors[agentRecord.ID] = installErr
			runner.dependencies.Logger.Info(
				"session: installFeedAgent attempt failed, trying next agent",
				"exchange_id", request.ExchangeID,
				"agent_id", agentRecord.ID,
				"attempt_index", agentIndex,
				"err", installErr,
			)
			continue
		}
		runner.dependencies.Logger.Info(
			"session: installFeedAgent succeeded",
			"exchange_id", request.ExchangeID,
			"feed_agent_id", agentRecord.ID,
			"feed_agent_index", agentIndex,
			"prior_failures", len(firstPassErrors),
		)
		return sessionID, agentIndex, firstPassErrors, nil
	}
	// Every agent failed the serial feed-search. Mark each in
	// Redis before returning the fatal — the resilience layer uses
	// the markers to know which installs to roll back. Marking
	// here (rather than at every failure inside the loop) keeps
	// the markers consistent with the "retry gets another chance"
	// semantics installRemainingAgents implements on the happy
	// path: a transient failure that would have been retried in
	// the parallel pass only gets marked when the whole bring-up
	// fails anyway.
	for _, agentRecord := range allAgents {
		cause, agentFailed := firstPassErrors[agentRecord.ID]
		if !agentFailed {
			continue
		}
		if markErr := runner.dependencies.Runtime.MarkSessionLoadFailed(
			ctx, request.ExchangeID, agentRecord.ID, cause.Error(),
		); markErr != nil {
			runner.dependencies.Logger.Warn(
				"session: MarkSessionLoadFailed itself failed on total feed-search failure; continuing",
				"exchange_id", request.ExchangeID,
				"agent_id", agentRecord.ID,
				"mark_err", markErr,
			)
		}
	}
	return "", -1, firstPassErrors, fmt.Errorf(
		"session: installFeedAgent: all %d agents failed to install", len(allAgents),
	)
}

// installRemainingAgents installs every agent in the population
// EXCEPT the feed agent, in parallel. "Remaining" includes both the
// never-tried agents (indices > feedAgentIndex) and the agents that
// failed during installFeedAgent's serial search (indices <
// feedAgentIndex). Retrying the previously-failed ones here folds
// the two populations into one parallel pass and gives any
// transient failure a second chance.
//
// Concurrency is bounded by Params.LoadSessionsConcurrency. A
// large population would otherwise open thousands of concurrent
// sockets to Core.
//
// Per-agent failure: MarkSessionLoadFailed via Redis + continue.
// Op-level return: always nil. One live agent is enough for a
// bring-up — the feed agent — so this pass is strictly a
// best-effort fill. The errgroup running in Run therefore does not
// abort the other two pre-open ops when every remaining install
// happens to fail.
//
// Context cancellation returns ctx.Err() wrapped — a cancelled
// bring-up should not run the marker pass for installs the caller
// no longer cares about.
func (runner *Runner) installRemainingAgents(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
	feedAgentIndex int,
	firstPassErrors map[agent.AgentID]error,
) error {
	allAgents := population.All()
	if len(allAgents) == 0 {
		return nil
	}

	concurrencyLimit := runner.dependencies.Params.LoadSessionsConcurrency
	if concurrencyLimit <= 0 {
		concurrencyLimit = 1
	}

	resultsByAgentID := &sync.Map{}
	semaphore := make(chan struct{}, concurrencyLimit)
	var fanOutWaitGroup sync.WaitGroup
	for agentIndex, agentRecord := range allAgents {
		if agentIndex == feedAgentIndex {
			continue
		}
		agentRecord := agentRecord
		fanOutWaitGroup.Add(1)
		go func() {
			defer fanOutWaitGroup.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			sessionID, installErr := runner.installOneAgent(ctx, request, string(agentRecord.ID))
			resultsByAgentID.Store(agentRecord.ID, installResult{
				sessionID:    sessionID,
				installError: installErr,
			})
		}()
	}
	fanOutWaitGroup.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("session: installRemainingAgents aborted: %w", ctxErr)
	}

	installedCount := 0
	failedCount := 0
	for agentIndex, agentRecord := range allAgents {
		if agentIndex == feedAgentIndex {
			continue
		}
		resultValue, found := resultsByAgentID.Load(agentRecord.ID)
		if !found {
			// Fan-out goroutine never stored — shouldn't happen; log
			// and treat as failure.
			runner.dependencies.Logger.Error(
				"session: installRemainingAgents result missing for agent (fan-out invariant violated)",
				"exchange_id", request.ExchangeID,
				"agent_id", agentRecord.ID,
			)
			failedCount++
			continue
		}
		result := resultValue.(installResult)
		if result.installError != nil {
			if markErr := runner.dependencies.Runtime.MarkSessionLoadFailed(
				ctx, request.ExchangeID, agentRecord.ID, result.installError.Error(),
			); markErr != nil {
				runner.dependencies.Logger.Warn(
					"session: MarkSessionLoadFailed itself failed; continuing",
					"exchange_id", request.ExchangeID,
					"agent_id", agentRecord.ID,
					"mark_err", markErr,
				)
			}
			failedCount++
			continue
		}
		installedCount++
	}
	runner.dependencies.Logger.Info(
		"session: installRemainingAgents complete",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"total_remaining", len(allAgents)-1,
		"installed", installedCount,
		"failed", failedCount,
		"first_pass_failure_count", len(firstPassErrors),
	)
	return nil
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
// collector can bucketise. Canonical installs join by market tag
// (request.Country); on-demand installs join the one already-live
// on-demand market by its exact exchange id (request.ExchangeID) —
// the market a human's StartOnDemand call already created, which L2
// must attach to rather than spin up a new one.
func (runner *Runner) installOneAgent(
	ctx context.Context,
	request transport.BringUpRequest,
	agentUserID string,
) (string, error) {
	switch request.SessionType {
	case agent.PopulationCanonical:
		join, joinErr := runner.dependencies.Core.JoinCanonical(ctx, agentUserID, request.Country)
		if joinErr != nil {
			return "", joinErr
		}
		if join.SessionID == "" {
			return "", fmt.Errorf("session: canonical install returned empty session_id")
		}
		return join.SessionID, nil
	case agent.PopulationOnDemand:
		join, joinErr := runner.dependencies.Core.JoinOnDemand(ctx, agentUserID, request.ExchangeID)
		if joinErr != nil {
			return "", joinErr
		}
		if join.SessionID == "" {
			return "", fmt.Errorf("session: on-demand install returned empty session_id")
		}
		return join.SessionID, nil
	default:
		return "", fmt.Errorf("session: installOneAgent received unknown session type %q", request.SessionType)
	}
}
