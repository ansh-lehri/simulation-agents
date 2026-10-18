package orchestrator

import (
	"context"
	"fmt"
	"log/slog"

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
// loops per agent. The shape of the public method (one call per
// bring-up over the full new-agent slice) is designed so swapping in a
// bulk Core endpoint later is a single-method change here.
type AccountOpenClient struct {
	coreClient CoreAccountClient
	logger     *slog.Logger
}

// NewAccountOpenClient wires an AccountOpenClient from its dependencies.
// Both are required; a nil Core client would make the first bring-up
// panic in a far-away stack frame.
func NewAccountOpenClient(coreClient CoreAccountClient, logger *slog.Logger) (*AccountOpenClient, error) {
	if coreClient == nil {
		return nil, fmt.Errorf("orchestrator: coreClient is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("orchestrator: logger is required")
	}
	return &AccountOpenClient{coreClient: coreClient, logger: logger}, nil
}

// Open runs the account-open pass over the newly minted agents in a
// bring-up: first open an account for each (filling CapitalHandle),
// then read each account's funded cash (filling FundedCash). An empty
// slice is a no-op — canonical bring-ups with no shortfall produce zero
// new agents and must not emit a Core call.
//
// Partial-failure stance: fail the whole pass on the first error. The
// orchestrator's async tail is never spawned on a failed pass, so no
// durable row and no runtime snapshot is produced for the half-opened
// agents. Core's OpenAccount is get-or-create, so a retried bring-up
// re-opens idempotently.
//
// The two loops run sequentially (opens, then cash reads) because
// GetAccountCash needs the handle each OpenAccount returns. A future
// Core bulk endpoint would fold both into one request; today the shape
// of this method is already that of a bulk call — one input slice, one
// error return.
func (client *AccountOpenClient) Open(ctx context.Context, newAgents []agent.Agent) error {
	if len(newAgents) == 0 {
		return nil
	}
	client.logger.Info(
		"account-open pass start",
		"new_agent_count", len(newAgents),
	)

	for index := range newAgents {
		accountHandle, err := client.coreClient.OpenAccount(ctx, string(newAgents[index].ID))
		if err != nil {
			return fmt.Errorf(
				"orchestrator: open account for %s: %w", newAgents[index].ID, err,
			)
		}
		newAgents[index].CapitalHandle = accountHandle
	}

	for index := range newAgents {
		cash, err := client.coreClient.GetAccountCash(ctx, newAgents[index].CapitalHandle)
		if err != nil {
			return fmt.Errorf(
				"orchestrator: read account cash for %s: %w", newAgents[index].ID, err,
			)
		}
		newAgents[index].FundedCash = cash.TradingCash
	}

	client.logger.Info(
		"account-open pass end",
		"new_agent_count", len(newAgents),
	)
	return nil
}
