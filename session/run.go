// Package session owns the per-exchange run goroutine the orchestrator
// spawns at the end of the bring-up tail. One goroutine per exchange
// id. From spawn onward the Runner owns the live session: it stamps
// each agent's entry fraction, runs the three pre-open operations
// concurrently (session install, candle-history pull, live WebSocket
// subscribe), joins them, waits for the session's start wall-clock,
// and hands off to the tick-loop scheduler.
//
// The Runner is forbidden from writing Postgres — durable identity is
// orchestrator-owned under the decide/write split. It writes Redis
// runtime state directly, caches market data in Redis, and reads
// positions / cash / P&L fresh from the broker (never stores them).
// Pre-open failures surface through a FailureReporter the orchestrator
// implements; the Runner never performs its own cleanup — that is the
// resilience layer's job.
package session

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	"simulation-agents/orchestrator/generator"
	"simulation-agents/transport"
)


// Runner is the per-exchange run goroutine body. One Runner value per
// agent process; its Run method is called from exactly one goroutine
// per bring-up spawn. Different (ctx, request) pairs may be running
// concurrently on different goroutines — the Runner holds no mutable
// per-call state and is safe for that use.
type Runner struct {
	dependencies Dependencies
}

// NewRunner validates every dependency and returns a Runner ready to
// serve bring-up spawns. Every dependency is required: a nil core
// client, runtime store, market-data store, scheduler, failure
// reporter, clock, rng source, or logger would make the first spawn
// panic deep in a call site that is harder to debug than a clear
// startup error.
func NewRunner(dependencies Dependencies) (*Runner, error) {
	if dependencies.Core == nil {
		return nil, fmt.Errorf("session: NewRunner requires a non-nil Core client")
	}
	if dependencies.Runtime == nil {
		return nil, fmt.Errorf("session: NewRunner requires a non-nil Runtime store")
	}
	if dependencies.MarketData == nil {
		return nil, fmt.Errorf("session: NewRunner requires a non-nil MarketData store")
	}
	if dependencies.Scheduler == nil {
		return nil, fmt.Errorf("session: NewRunner requires a non-nil Scheduler")
	}
	if dependencies.Failure == nil {
		return nil, fmt.Errorf("session: NewRunner requires a non-nil Failure reporter")
	}
	if dependencies.Clock == nil {
		return nil, fmt.Errorf("session: NewRunner requires a non-nil Clock")
	}
	if dependencies.RNGSource == nil {
		return nil, fmt.Errorf("session: NewRunner requires a non-nil RNG source")
	}
	if dependencies.Logger == nil {
		return nil, fmt.Errorf("session: NewRunner requires a non-nil Logger")
	}
	return &Runner{dependencies: dependencies}, nil
}

// Run is the per-exchange goroutine's body. The orchestrator spawns
// this at the end of the bring-up tail; the Runner owns the session
// from here until go-live hands off to the scheduler.
//
// Sequence:
//  1. Stamp each agent's entry fraction. Stamping happens here (not
//     in the orchestrator's tail) so a restart of the agent process
//     does not redraw it and change the entry pattern; it is also
//     fast and local, no good reason to overlap it with anything.
//  2. Install the feed agent — walk the population serially until
//     one install succeeds. The successful agent's session id is
//     the handle subscribeLive uses to open the live-feed
//     WebSocket. Serial here is correct: subscribeLive cannot start
//     without a feed id, so the pre-open latency floor is one
//     successful install.
//  3. Run the three concurrent pre-open ops:
//     - installRemainingAgents: parallel fan-out over every agent
//       that is NOT the feed agent (both the never-tried and the
//       first-pass failures; the retry gives transient failures a
//       second chance).
//     - pullHistory: paged candle pull per (instrument × TF).
//     - subscribeLive: one WS, batched subscribe over snapshot +
//       candle(tf) for every configured TF.
//  4. Join. On any fatal failure, surface via Failure.FailBringUp
//     and return — the Runner does not perform its own cleanup.
//  5. Wait until SessionStart on the injected Clock. A cancelled
//     ctx during the wait is treated as a fatal exit.
//  6. Hand off to the scheduler (stub this phase).
//  7. Report ready.
//
// Agents stay DORMANT through all of Run — none flip ACTIVE here.
// Activation is the scheduler's job inside its tick loop.
func (runner *Runner) Run(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
	curveEffective *generator.CurveEffective,
	lifecycleChannel chan<- LifecycleDecision,
) {
	runner.dependencies.Logger.Info(
		"session: Run start",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"existing_count", len(population.Existing),
		"new_count", len(population.New),
	)

	// (1) stamp entry fractions
	if err := runner.stampEntryFractionsForPopulation(ctx, request, population); err != nil {
		runner.dependencies.Failure.FailBringUp(request, err)
		return
	}

	// (2) feed agent (serial-until-first-success)
	feedSessionID, feedAgentIndex, firstPassErrors, feedErr := runner.installFeedAgent(ctx, request, population)
	if feedErr != nil {
		runner.dependencies.Failure.FailBringUp(request, feedErr)
		return
	}

	// (3) three concurrent pre-open ops
	preOpenGroup, groupCtx := errgroup.WithContext(ctx)
	preOpenGroup.Go(func() error {
		return runner.installRemainingAgents(groupCtx, request, population, feedAgentIndex, firstPassErrors)
	})
	preOpenGroup.Go(func() error {
		return runner.pullHistory(groupCtx, request)
	})
	preOpenGroup.Go(func() error {
		return runner.subscribeLive(groupCtx, request, feedSessionID)
	})
	// (4) join
	if joinErr := preOpenGroup.Wait(); joinErr != nil {
		runner.dependencies.Failure.FailBringUp(request, joinErr)
		return
	}

	// (5) go-live wait
	if waitErr := runner.dependencies.Clock.WaitUntil(ctx, request.SessionStart); waitErr != nil {
		runner.dependencies.Failure.FailBringUp(request, fmt.Errorf("session: waitUntil SessionStart: %w", waitErr))
		return
	}

	// (6) scheduler handoff; real tick loop body replaces the stub
	// without any change here.
	runner.dependencies.Scheduler.StartScheduler(ctx, request, population, curveEffective, lifecycleChannel)

	// (7) ready
	runner.dependencies.Failure.ReportReady(request)
	runner.dependencies.Logger.Info(
		"session: Run complete, session is live",
		"exchange_id", request.ExchangeID,
	)
}

// stampEntryFractionsForPopulation draws one Gaussian-process entry
// curve from a fresh seeded RNG (the session's "rhythm"), then draws
// one entry fraction per agent off that single rhythm and writes it
// to Redis. Done BEFORE the three concurrent ops because:
//   - It is cheap and local; parallelising with the Core calls
//     would add synchronization noise for no measurable gain.
//   - Writing entry fractions before the Core-side installs begin
//     means a restart of the agent process does not redraw and
//     change the entry pattern — the fraction is already in Redis
//     and is used by the tick-loop scheduler as the one-shot
//     DORMANT→ACTIVE trigger.
//
// Agents stay DORMANT after this step; the entry fraction only
// decides when the scheduler will later flip them to ACTIVE.
func (runner *Runner) stampEntryFractionsForPopulation(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
) error {
	entryRNG := runner.dependencies.RNGSource.Fresh()
	entryCurve := BuildEntryCurve(entryRNG, runner.dependencies.Params.EntryCurve)
	for _, agentRecord := range population.All() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("session: stampEntryFractions aborted: %w", ctxErr)
		}
		fraction := entryCurve.DrawEntryFraction(entryRNG)
		if err := runner.dependencies.Runtime.StampEntryTime(ctx, request.ExchangeID, agentRecord.ID, fraction); err != nil {
			return fmt.Errorf("session: StampEntryTime for %s on %s: %w", agentRecord.ID, request.ExchangeID, err)
		}
	}
	runner.dependencies.Logger.Info(
		"session: entry fractions stamped",
		"exchange_id", request.ExchangeID,
		"agent_count", len(population.All()),
	)
	return nil
}
