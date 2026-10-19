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
// This stage ships the handoff shape only: argument signature, the
// end-to-end log line, no behavior. The real sequence (entry-fraction
// stamp, three pre-open ops with concurrent fan-out and join, a
// go-live wait on the clock, scheduler handoff, ready report) lands
// in a later stage. The signature is stable from here on: later
// stages replace the body, not the surface.
func (runner *Runner) Run(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
	lifecycleChannel chan<- LifecycleDecision,
) {
	_ = ctx
	_ = lifecycleChannel
	runner.dependencies.Logger.Info(
		"session run stub reached (next stage replaces body)",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"existing_count", len(population.Existing),
		"new_count", len(population.New),
	)
}
