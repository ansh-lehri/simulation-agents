package orchestrator

import (
	"context"
	"fmt"
	"log/slog"

	"simulation-agents/orchestrator/generator"
	"simulation-agents/transport"
)

// Orchestrator is the actor that runs the bring-up spine: receive the
// inbound trigger, assemble a Population through the generator, open
// broker accounts for the newly minted agents, resolve each agent's
// session trading capital, and hand off to the asynchronous tail that
// persists, snapshots, and spawns the per-exchange run goroutine.
//
// This file wires the construction and the inbound-seam surface; the
// account-open and capital-resolver dependencies land as the next
// implementation step, and the bring-up spine wires them together after
// that. For now BringUp validates the request, logs the acceptance, and
// returns the stub ack transport callers already observe. WindDown is
// also a logged stub; the real teardown path is a later phase.
type Orchestrator struct {
	generator *generator.Generator
	rngSource RNGSource
	config    OrchestratorConfig
	logger    *slog.Logger
}

// NewOrchestrator wires an Orchestrator from its dependencies and
// validates the config. Every dependency is required: a nil generator,
// rng source, or logger would make the first bring-up panic deep in a
// call site that is harder to debug than a clear startup error.
func NewOrchestrator(
	populationGenerator *generator.Generator,
	rngSource RNGSource,
	config OrchestratorConfig,
	logger *slog.Logger,
) (*Orchestrator, error) {
	if populationGenerator == nil {
		return nil, fmt.Errorf("orchestrator: generator is required")
	}
	if rngSource == nil {
		return nil, fmt.Errorf("orchestrator: rngSource is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("orchestrator: logger is required")
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("orchestrator: invalid config: %w", err)
	}
	return &Orchestrator{
		generator: populationGenerator,
		rngSource: rngSource,
		config:    config,
		logger:    logger,
	}, nil
}

// BringUp is the inbound seam the transport layer calls. The signature
// matches transport.AgentOrchestrator so the real orchestrator slots in
// behind the server with no caller change.
//
// At this implementation step, BringUp validates the request, logs the
// acceptance, and returns the accounts-ready ack. The three-stage spine
// (assemble identities, open accounts, resolve capital) is wired as the
// next step; the asynchronous persist-snapshot-spawn tail is a later
// phase. Until the spine lands, a BringUp acknowledges and returns
// without producing any downstream side effects.
func (orchestrator *Orchestrator) BringUp(ctx context.Context, request transport.BringUpRequest) (transport.BringUpAck, error) {
	_ = ctx
	if err := validateTrigger(request); err != nil {
		return transport.BringUpAck{}, err
	}
	if request.SessionType == SessionTypeCanonical && request.Scenario != "" {
		// Scenarios are honored only on on-demand sessions by design —
		// canonical runs the baseline draw regardless. Log the carry so
		// a confused operator sees that their scenario had no effect.
		orchestrator.logger.Warn(
			"ignoring scenario on canonical session",
			"exchange_id", request.ExchangeID,
			"scenario", request.Scenario,
		)
	}
	orchestrator.logger.Info(
		"bring-up accepted",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"country", request.Country,
		"instrument_count", len(request.Instruments),
		"scenario", request.Scenario,
	)
	// Spine wires in the next step. Current acceptance is a no-op; the
	// returned ack still matches the transport contract.
	return transport.BringUpAck{
		ExchangeID: request.ExchangeID,
		Status:     transport.StatusAccountsReady,
	}, nil
}

// WindDown is the inbound teardown seam, matching
// transport.AgentOrchestrator. The real teardown path (lease release,
// WAL drain of orphan mints, snapshot drop, deregister session, close
// WS) is a later phase; this stub acknowledges and returns.
func (orchestrator *Orchestrator) WindDown(ctx context.Context, exchangeID string, teardownMode string) error {
	_ = ctx
	orchestrator.logger.Info(
		"wind-down accepted",
		"exchange_id", exchangeID,
		"teardown_mode", teardownMode,
	)
	return nil
}
