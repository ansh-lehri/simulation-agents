package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"golang.org/x/sync/errgroup"

	"simulation-agents/agent"
	"simulation-agents/core"
)

// CoreAccountClient is the narrow slice of the Core client the
// account-open step calls. Defined here (not on the core side) so this
// package's tests inject a small spy without pulling the whole Core
// client, and so a reader can tell at a glance exactly which Core calls
// the bring-up spine makes. The real *core.Client satisfies this
// interface structurally.
type CoreAccountClient interface {
	// OpenAccount is the per-agent account-open on Core. The Core
	// endpoint is get-or-create keyed on the agent id, so re-sends are
	// safe; the returned string is the opaque account handle.
	OpenAccount(ctx context.Context, agentUserID string) (string, error)

	// GetAccountCash is the per-account funded-cash read. The returned
	// AccountCash.TradingCash is the ceiling the capital-resolver step
	// bounds each new agent's trading capital against.
	GetAccountCash(ctx context.Context, accountID string) (core.AccountCash, error)
}

// AccountOpenClient opens Core accounts for the freshly minted agents
// in a bring-up and reads each resulting account's funded cash so the
// capital-resolver can draw the session trading capital against a real
// broker ceiling.
//
// There is no bulk account-open endpoint on Core today, so this client
// fans out one goroutine per agent, bounded by concurrency. Each
// goroutine runs OpenAccount then GetAccountCash sequentially for its
// agent — the cash read requires the handle — but all agents run
// concurrently, reducing Stage B from O(N × RTT) to O(ceil(N/concurrency) × RTT).
type AccountOpenClient struct {
	coreClient  CoreAccountClient
	concurrency int
	logger      *slog.Logger
}

// NewAccountOpenClient wires an AccountOpenClient from its dependencies.
// concurrency caps the parallel fan-out; it must be positive (comes from
// OrchestratorConfig.AccountOpenConcurrency).
func NewAccountOpenClient(coreClient CoreAccountClient, concurrency int, logger *slog.Logger) (*AccountOpenClient, error) {
	if coreClient == nil {
		return nil, fmt.Errorf("orchestrator: coreClient is required")
	}
	if concurrency <= 0 {
		return nil, fmt.Errorf("orchestrator: concurrency %d must be positive", concurrency)
	}
	if logger == nil {
		return nil, fmt.Errorf("orchestrator: logger is required")
	}
	return &AccountOpenClient{coreClient: coreClient, concurrency: concurrency, logger: logger}, nil
}

// Open runs the account-open pass over the newly minted agents in a
// bring-up. An empty slice is a no-op — canonical bring-ups with no
// shortfall produce zero new agents and must not emit a Core call.
//
// Each agent's OpenAccount + GetAccountCash pair runs sequentially within
// its goroutine (cash read needs the handle), but all agents run in
// parallel bounded by the client's concurrency ceiling. The first error
// from any goroutine cancels the rest and is returned; Core's OpenAccount
// is get-or-create, so a retried bring-up re-opens idempotently.
func (client *AccountOpenClient) Open(ctx context.Context, newAgents []agent.Agent) error {
	if len(newAgents) == 0 {
		return nil
	}
	client.logger.Info(
		"account-open pass start",
		"new_agent_count", len(newAgents),
		"concurrency", client.concurrency,
	)

	sem := make(chan struct{}, client.concurrency)
	var mu sync.Mutex
	// Use a plain errgroup (no derived context) so that one goroutine
	// returning an error does NOT cancel the context of the other
	// in-flight HTTP calls. errgroup.WithContext would cancel egCtx on
	// the first failure, which tears down every live HTTP connection and
	// floods the market-simulator with "context canceled" DB errors.
	// The parent ctx is passed directly so process-level cancellation
	// (BringUpTimeout, shutdown) still propagates correctly.
	var eg errgroup.Group

	for index := range newAgents {
		index := index
		eg.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			handle, err := client.coreClient.OpenAccount(ctx, string(newAgents[index].ID))
			if err != nil {
				return fmt.Errorf(
					"orchestrator: open account for %s: %w", newAgents[index].ID, err,
				)
			}

			cash, err := client.coreClient.GetAccountCash(ctx, handle)
			if err != nil {
				return fmt.Errorf(
					"orchestrator: read account cash for %s: %w", newAgents[index].ID, err,
				)
			}

			mu.Lock()
			newAgents[index].CapitalHandle = handle
			newAgents[index].FundedCash = cash.TradingCash
			mu.Unlock()
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	client.logger.Info(
		"account-open pass end",
		"new_agent_count", len(newAgents),
	)
	return nil
}
