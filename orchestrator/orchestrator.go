package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"

	"simulation-agents/orchestrator/generator"
	"simulation-agents/session"
	"simulation-agents/store"
	"simulation-agents/transport"
)

// PopulationGenerator is the orchestrator's view of the trait-draw
// engine. Defined as an interface so tests can inject a fake generator
// and so the orchestrator's dependency surface is spelled out at the
// point of use. The real *generator.Generator satisfies this
// structurally; the compile-time witness in handle_bringup_test.go
// catches signature drift at build time.
type PopulationGenerator interface {
	GenerateCanonical(
		ctx context.Context,
		country string,
		populationSize int,
		rng *rand.Rand,
		exchangeID string,
	) (generator.Population, error)

	GenerateOnDemand(
		ctx context.Context,
		country string,
		exchangeID string,
		populationSize int,
		rng *rand.Rand,
		scenarioName string,
	) (generator.Population, error)
}

// Orchestrator is the actor that runs the bring-up spine: receive the
// inbound trigger, assemble a Population through the generator, open
// broker accounts for the newly minted agents, resolve each agent's
// session trading capital, and hand off to the asynchronous tail that
// persists, snapshots, enlists, registers, and spawns the per-
// exchange run goroutine.
type Orchestrator struct {
	generator      PopulationGenerator
	accountOpen    *AccountOpenClient
	capital        *CapitalResolver
	rngSource      RNGSource

	// Persistence surfaces the bring-up tail (continueBringUp)
	// writes to. Each is a narrow store interface so a composition
	// root can swap in a different backend without touching this
	// package.
	agentStore         store.AgentStore
	runtimeStore       store.RuntimeStore
	leaseAllocator     store.LeaseAllocator
	pendingMintWAL     store.PendingMintWAL
	activeSessionStore store.ActiveSessionStore

	// scenarioProvider resolves on-demand scenarios the second time
	// in the tail — once for the generator at Stage A, again here
	// at Stage P5 to obtain the lifecycle curve_eff that rides into
	// sessions:active. Both calls hit the same engine; the extra
	// resolution avoids threading curveEff through the whole spine.
	scenarioProvider generator.ScenarioProvider

	// sessionRunner is the per-exchange run goroutine the tail
	// spawns at the end of Stage P5. Held as an interface so a
	// spy can replace it in orchestrator tests.
	sessionRunner SessionRunner

	// failureReporter is the surface the tail routes fatal
	// errors through. Defaults to the orchestrator itself (its
	// own FailBringUp / ReportReady methods satisfy the
	// FailureReporter interface, verified at build by the
	// compile-time witness in failure.go). Tests swap this field
	// to a recording spy via replaceFailureReporterForTest.
	failureReporter session.FailureReporter

	config         OrchestratorConfig
	logger         *slog.Logger
	validScenarios map[string]struct{}
}

// NewOrchestrator wires an Orchestrator from its dependencies and
// validates the config. Every dependency is required: a nil in any
// of them would make the first bring-up panic deep in a call site
// that is harder to debug than a clear startup error.
func NewOrchestrator(
	populationGenerator PopulationGenerator,
	accountOpenClient *AccountOpenClient,
	capitalResolver *CapitalResolver,
	rngSource RNGSource,
	agentStore store.AgentStore,
	runtimeStore store.RuntimeStore,
	leaseAllocator store.LeaseAllocator,
	pendingMintWAL store.PendingMintWAL,
	activeSessionStore store.ActiveSessionStore,
	scenarioProvider generator.ScenarioProvider,
	sessionRunner SessionRunner,
	config OrchestratorConfig,
	logger *slog.Logger,
) (*Orchestrator, error) {
	if populationGenerator == nil {
		return nil, fmt.Errorf("orchestrator: generator is required")
	}
	if accountOpenClient == nil {
		return nil, fmt.Errorf("orchestrator: accountOpenClient is required")
	}
	if capitalResolver == nil {
		return nil, fmt.Errorf("orchestrator: capitalResolver is required")
	}
	if rngSource == nil {
		return nil, fmt.Errorf("orchestrator: rngSource is required")
	}
	if agentStore == nil {
		return nil, fmt.Errorf("orchestrator: agentStore is required")
	}
	if runtimeStore == nil {
		return nil, fmt.Errorf("orchestrator: runtimeStore is required")
	}
	if leaseAllocator == nil {
		return nil, fmt.Errorf("orchestrator: leaseAllocator is required")
	}
	if pendingMintWAL == nil {
		return nil, fmt.Errorf("orchestrator: pendingMintWAL is required")
	}
	if activeSessionStore == nil {
		return nil, fmt.Errorf("orchestrator: activeSessionStore is required")
	}
	if scenarioProvider == nil {
		return nil, fmt.Errorf("orchestrator: scenarioProvider is required")
	}
	if sessionRunner == nil {
		return nil, fmt.Errorf("orchestrator: sessionRunner is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("orchestrator: logger is required")
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("orchestrator: invalid config: %w", err)
	}
	validScenarios := make(map[string]struct{}, len(config.ValidScenarioNames))
	for _, scenarioName := range config.ValidScenarioNames {
		validScenarios[scenarioName] = struct{}{}
	}
	built := &Orchestrator{
		generator:          populationGenerator,
		accountOpen:        accountOpenClient,
		capital:            capitalResolver,
		rngSource:          rngSource,
		agentStore:         agentStore,
		runtimeStore:       runtimeStore,
		leaseAllocator:     leaseAllocator,
		pendingMintWAL:     pendingMintWAL,
		activeSessionStore: activeSessionStore,
		scenarioProvider:   scenarioProvider,
		sessionRunner:      sessionRunner,
		config:             config,
		logger:             logger,
		validScenarios:     validScenarios,
	}
	// Self-reference: the orchestrator's own FailBringUp and
	// ReportReady methods satisfy session.FailureReporter. Routing
	// the tail's failure calls through this field (rather than
	// directly on the receiver) is the single-line hook tests use
	// to intercept the two methods without rewiring the orchestrator.
	built.failureReporter = built
	return built, nil
}

// BringUp is the inbound seam the transport layer calls. The signature
// matches transport.AgentOrchestrator so the real orchestrator slots in
// behind the server with no caller change.
//
// Flow: validate the trigger, warn on carried-but-unused fields, draw a
// fresh RNG from the configured source, run the three-stage synchronous
// spine (assemble identities, open broker accounts, resolve capital),
// and spawn the asynchronous tail that persists / snapshots / spawns
// the per-exchange run goroutine. The ack returns once the spine
// completes; the tail runs under a background-rooted context so it is
// not cancelled when the inbound caller is unblocked.
func (orchestrator *Orchestrator) BringUp(
	ctx context.Context,
	request transport.BringUpRequest,
) (transport.BringUpAck, error) {
	if err := validateTrigger(request); err != nil {
		return transport.BringUpAck{}, err
	}
	// Reject an unknown scenario name at intake rather than silently
	// resolving to baseline inside the spine. A silent fallback is
	// the exact failure mode scenarios must avoid — an operator who
	// typed a bad name must see the rejection, not a confused run.
	if err := validateScenarioName(request.Scenario, orchestrator.validScenarios); err != nil {
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
		"bring-up spine start",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"country", request.Country,
		"instrument_count", len(request.Instruments),
		"scenario", request.Scenario,
	)

	freshRNG := orchestrator.rngSource.Fresh()
	population, err := orchestrator.runBringUpSpine(ctx, request, freshRNG)
	if err != nil {
		orchestrator.logger.Error(
			"bring-up spine failed",
			"exchange_id", request.ExchangeID,
			"error", err,
		)
		return transport.BringUpAck{}, err
	}

	// Detach the tail from the request's context. The caller is unblocked
	// once the ack returns, and the transport layer may cancel the
	// request context immediately after; the tail must survive that.
	go orchestrator.continueBringUp(context.Background(), request, population)

	orchestrator.logger.Info(
		"bring-up spine complete, accounts ready",
		"exchange_id", request.ExchangeID,
		"existing_count", len(population.Existing),
		"new_count", len(population.New),
	)
	return transport.BringUpAck{
		ExchangeID: request.ExchangeID,
		Status:     transport.StatusAccountsReady,
	}, nil
}

// runBringUpSpine runs the three-stage synchronous spine over one
// Population. The order is forced by data dependency: Stage A produces
// identities, Stage B needs identities to open broker accounts and
// yields FundedCash, Stage C needs FundedCash to bound the new agents'
// trading capital draw. Each stage wraps its error with its own name so
// a bring-up failure surfaces which stage failed without having to
// reason about the call stack.
func (orchestrator *Orchestrator) runBringUpSpine(
	ctx context.Context,
	request transport.BringUpRequest,
	rng *rand.Rand,
) (generator.Population, error) {
	var population generator.Population
	var generationErr error

	// Stage A — assemble identities.
	switch request.SessionType {
	case SessionTypeCanonical:
		population, generationErr = orchestrator.generator.GenerateCanonical(
			ctx,
			request.Country,
			orchestrator.config.PopulationSize,
			rng,
			request.ExchangeID,
		)
	case SessionTypeOnDemand:
		population, generationErr = orchestrator.generator.GenerateOnDemand(
			ctx,
			request.Country,
			request.ExchangeID,
			orchestrator.config.PopulationSize,
			rng,
			request.Scenario,
		)
	default:
		// Unreachable: validateTrigger has already rejected any other
		// session type. Returning an explicit error here beats a silent
		// zero population in the event validateTrigger is ever
		// weakened.
		return generator.Population{}, fmt.Errorf(
			"orchestrator: unexpected session type %q reached the spine",
			request.SessionType,
		)
	}
	if generationErr != nil {
		return generator.Population{}, fmt.Errorf("stage A (generation): %w", generationErr)
	}

	// Stage B — bulk account-open for the new agents.
	if err := orchestrator.accountOpen.Open(ctx, population.New); err != nil {
		return generator.Population{}, fmt.Errorf("stage B (account open): %w", err)
	}

	// Stage C — resolve session trading capital under the three-case
	// dispatch. Pure in-memory step, cannot fail.
	orchestrator.capital.Resolve(&population, request.SessionType, rng)

	return population, nil
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

// Compile-time witness: the real generator satisfies the orchestrator's
// view of it. Signature drift would fail at build rather than at first
// bring-up.
var _ PopulationGenerator = (*generator.Generator)(nil)
