package generator

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"time"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// Generator is the pure, seeded population producer. It owns the trait
// draw dials (GenParams), the scenario resolver, and the narrow seams
// through which it reaches the durable store, the on-demand lease, and
// the write-ahead log. It does NOT open broker accounts, write durable
// rows, write Redis runtime, or touch the agent lifecycle — those are
// the orchestrator's job after it receives a Population from the two
// public entry points below.
type Generator struct {
	agentStore       AgentStoreReader
	leaseAllocator   LeaseSelector
	walStore         WALStager
	scenarioProvider ScenarioProvider
	params           GenParams

	// newAgentID is the identity minter injected by the composition root.
	// Production builds pass a crypto/rand UUID source; tests pass a
	// deterministic counter so golden assertions stay stable.
	newAgentID func() agent.AgentID

	// clock stamps TraitsUpdatedAt on freshly minted agents. Injected so
	// tests can pin a fixed time and so a mint under load does not pay
	// the time.Now() syscall more than once per agent.
	clock func() time.Time

	logger *slog.Logger
}

// NewGenerator wires a Generator from its dependencies and validates the
// calibration. Validation at construction time means a bad GenParams
// fails the process at startup rather than at first mint — the usual
// trade-off we take for calibration bugs.
//
// Every dependency is required; nil arguments are rejected because the
// Generator panics on first use if any is nil, which is a worse failure
// mode than a clear construction error.
func NewGenerator(
	agentStore AgentStoreReader,
	leaseAllocator LeaseSelector,
	walStore WALStager,
	scenarioProvider ScenarioProvider,
	params GenParams,
	newAgentID func() agent.AgentID,
	clock func() time.Time,
	logger *slog.Logger,
) (*Generator, error) {
	if agentStore == nil {
		return nil, fmt.Errorf("generator: agentStore is required")
	}
	if leaseAllocator == nil {
		return nil, fmt.Errorf("generator: leaseAllocator is required")
	}
	if walStore == nil {
		return nil, fmt.Errorf("generator: walStore is required")
	}
	if scenarioProvider == nil {
		return nil, fmt.Errorf("generator: scenarioProvider is required")
	}
	if newAgentID == nil {
		return nil, fmt.Errorf("generator: newAgentID is required")
	}
	if clock == nil {
		return nil, fmt.Errorf("generator: clock is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("generator: logger is required")
	}
	if err := params.Validate(); err != nil {
		return nil, fmt.Errorf("generator: invalid params: %w", err)
	}
	return &Generator{
		agentStore:       agentStore,
		leaseAllocator:   leaseAllocator,
		walStore:         walStore,
		scenarioProvider: scenarioProvider,
		params:           params,
		newAgentID:       newAgentID,
		clock:            clock,
		logger:           logger,
	}, nil
}

// GenerateCanonical assembles a canonical population: reuse-first (load
// every live member in the country/population bucket), then mint the
// shortfall up to populationSize, then ensure exactly one of the
// endogenous members carries the static tipper flag (promote a revived
// carrier if one exists, else flip a freshly minted endogenous).
//
// The exchangeID is carried through to each minted agent's write-ahead
// log entry so a crash recovery pass can route an orphaned mint back to
// the right session.
func (generator *Generator) GenerateCanonical(
	ctx context.Context,
	country string,
	populationSize int,
	rng *rand.Rand,
	exchangeID string,
) (Population, error) {
	generator.logger.Info(
		"generation start",
		"session_type", agent.PopulationCanonical,
		"country", country,
		"population_size", populationSize,
		"exchange_id", exchangeID,
	)

	existing, err := generator.agentStore.LiveMembers(ctx, country, agent.PopulationCanonical)
	if err != nil {
		return Population{}, fmt.Errorf("generator: load live canonical members for %q: %w", country, err)
	}

	shortfall := populationSize - len(existing)
	if shortfall < 0 {
		shortfall = 0
	}

	minted := make([]agent.Agent, 0, shortfall)
	mintContext := MintContext{
		Population: agent.PopulationCanonical,
		Country:    country,
		ExchangeID: exchangeID,
	}
	for mintIndex := 0; mintIndex < shortfall; mintIndex++ {
		mintedAgent, err := generator.mint(ctx, rng, mintContext, nil)
		if err != nil {
			return Population{}, fmt.Errorf(
				"generator: mint canonical agent %d/%d: %w", mintIndex+1, shortfall, err,
			)
		}
		minted = append(minted, mintedAgent)
	}

	ensureOneTipper(existing, minted, rng, generator.logger)

	generator.logger.Info(
		"generation end",
		"session_type", agent.PopulationCanonical,
		"country", country,
		"existing_count", len(existing),
		"minted_count", len(minted),
		"exchange_id", exchangeID,
	)
	return Population{Existing: existing, New: minted}, nil
}

// GenerateOnDemand assembles an on-demand population: resolve the
// scenario to any trait-draw overrides, lease up to populationSize free
// agents from the per-country pool, load their durable rows, REDRAW
// their trait vectors fresh for scenario coverage, then mint the
// shortfall.
//
// On-demand does NOT assign a tipper here — the on-demand tipper is a
// runtime per-session flag written to Redis in a later bring-up phase,
// never persisted into the frozen trait vector.
//
// The lifecycle-curve overrides the scenario resolver returns alongside
// the trait overrides are not consumed in this phase; the caller that
// needs them (the session parameter writer) can resolve the scenario
// again rather than threading an extra return value through today.
func (generator *Generator) GenerateOnDemand(
	ctx context.Context,
	country string,
	exchangeID string,
	populationSize int,
	rng *rand.Rand,
	scenarioName string,
) (Population, error) {
	generator.logger.Info(
		"generation start",
		"session_type", agent.PopulationOnDemand,
		"country", country,
		"population_size", populationSize,
		"exchange_id", exchangeID,
		"scenario", scenarioName,
	)

	effectiveShifts, _, _ := generator.scenarioProvider.Effective(country, scenarioName)

	leasedIDs, err := generator.leaseAllocator.SelectAvailable(ctx, country, populationSize, exchangeID)
	if err != nil {
		return Population{}, fmt.Errorf("generator: select available for %q on %q: %w", country, exchangeID, err)
	}

	// Sort the leased ids so the redraw loop runs in a deterministic
	// order; both SPOP (set pop) and the store's load-many are allowed
	// to return in arbitrary order, and the trait draw's RNG stream
	// consumption must not depend on either.
	sort.Slice(leasedIDs, func(leftIndex, rightIndex int) bool {
		return leasedIDs[leftIndex] < leasedIDs[rightIndex]
	})

	loadedByID, err := generator.loadExistingAgentsByID(ctx, leasedIDs)
	if err != nil {
		return Population{}, err
	}

	existing := make([]agent.Agent, 0, len(leasedIDs))
	for _, leasedID := range leasedIDs {
		loadedAgent, found := loadedByID[leasedID]
		if !found {
			generator.logger.Warn(
				"leased id has no durable row; skipping",
				"agent_id", leasedID,
				"country", country,
				"exchange_id", exchangeID,
			)
			continue
		}
		// Scenario coverage: the ondemand design requires a fresh trait
		// draw per session for leased members so a scenario can shift
		// the population without a membership churn pass.
		loadedAgent.Traits = drawTraitVector(rng, generator.params, effectiveShifts)
		existing = append(existing, loadedAgent)
	}

	shortfall := populationSize - len(existing)
	if shortfall < 0 {
		shortfall = 0
	}

	minted := make([]agent.Agent, 0, shortfall)
	mintContext := MintContext{
		Population: agent.PopulationOnDemand,
		Country:    country,
		ExchangeID: exchangeID,
	}
	for mintIndex := 0; mintIndex < shortfall; mintIndex++ {
		mintedAgent, err := generator.mint(ctx, rng, mintContext, effectiveShifts)
		if err != nil {
			return Population{}, fmt.Errorf(
				"generator: mint ondemand agent %d/%d: %w", mintIndex+1, shortfall, err,
			)
		}
		minted = append(minted, mintedAgent)
	}

	generator.logger.Info(
		"generation end",
		"session_type", agent.PopulationOnDemand,
		"country", country,
		"existing_count", len(existing),
		"minted_count", len(minted),
		"exchange_id", exchangeID,
	)
	return Population{Existing: existing, New: minted}, nil
}

// loadExistingAgentsByID calls LoadMany and keys the result by id so
// the caller can pick agents in a deterministic (sorted) order
// regardless of the store's return order.
func (generator *Generator) loadExistingAgentsByID(
	ctx context.Context,
	leasedIDs []agent.AgentID,
) (map[agent.AgentID]agent.Agent, error) {
	if len(leasedIDs) == 0 {
		return map[agent.AgentID]agent.Agent{}, nil
	}
	loaded, err := generator.agentStore.LoadMany(ctx, leasedIDs)
	if err != nil {
		return nil, fmt.Errorf("generator: load leased ids: %w", err)
	}
	loadedByID := make(map[agent.AgentID]agent.Agent, len(loaded))
	for _, loadedAgent := range loaded {
		loadedByID[loadedAgent.ID] = loadedAgent
	}
	return loadedByID, nil
}

// mint assembles one freshly minted agent: assign an id, draw the full
// trait vector, and stage a write-ahead-log entry BEFORE returning so a
// crash between mint and account-open leaves a recoverable record of
// the orphaned mint (there is no economy account to orphan yet, because
// mint runs before account-open, but the staging contract is uniform).
//
// trading_capital is NOT written here — the session money A is drawn by
// the capital-resolver step after the broker's funding ceiling is read,
// and recovery deliberately redraws it.
func (generator *Generator) mint(
	ctx context.Context,
	rng *rand.Rand,
	mintContext MintContext,
	effectiveShifts *EffectiveParams,
) (agent.Agent, error) {
	mintedAgent := agent.Agent{
		ID:              generator.newAgentID(),
		Population:      mintContext.Population,
		Country:         mintContext.Country,
		Origin:          agent.OriginMint,
		TraitsVersion:   1,
		TraitsUpdatedAt: generator.clock(),
	}
	mintedAgent.Traits = drawTraitVector(rng, generator.params, effectiveShifts)

	stageRecord := store.StageRecord{
		Traits:     mintedAgent.Traits,
		Population: mintedAgent.Population,
		Country:    mintedAgent.Country,
		ExchangeID: mintContext.ExchangeID,
	}
	if err := generator.walStore.Stage(ctx, mintedAgent.ID, stageRecord); err != nil {
		return agent.Agent{}, fmt.Errorf("generator: stage pending mint for %s: %w", mintedAgent.ID, err)
	}
	return mintedAgent, nil
}

// ensureOneTipper enforces the canonical-only "exactly one static
// tipper" invariant. The scan is pass-first: if any revived member
// already carries the flag, we are done — a revived tipper keeps its
// identity across sessions. Otherwise one freshly minted endogenous
// agent is flipped; the pick uses the same seeded rng that drew the
// population so the choice is reproducible.
//
// Edge: if no endogenous agent is available to flip (every minted
// member is herd AND no revived carrier exists), the function logs and
// returns — the population runs without a canonical tipper rather than
// panicking. That edge requires both an unusually high herd probability
// and a small shortfall, neither of which is expected on realistic
// calibrations.
func ensureOneTipper(existing, minted []agent.Agent, rng *rand.Rand, logger *slog.Logger) {
	for _, existingAgent := range existing {
		if existingAgent.Traits.IsTipper {
			return
		}
	}
	endogenousIndices := []int{}
	for index, mintedAgent := range minted {
		if mintedAgent.Traits.AgentKind == agent.AgentKindEndogenous {
			endogenousIndices = append(endogenousIndices, index)
		}
	}
	if len(endogenousIndices) == 0 {
		logger.Warn(
			"no endogenous agent available to promote as canonical tipper",
			"existing_count", len(existing),
			"minted_count", len(minted),
		)
		return
	}
	pickedIndex := endogenousIndices[rng.IntN(len(endogenousIndices))]
	minted[pickedIndex].Traits.IsTipper = true
}
