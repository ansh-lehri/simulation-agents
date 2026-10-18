package generator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// ---- Test fakes --------------------------------------------------

// fakeAgentStore satisfies AgentStoreReader for the generator tests. It
// serves preloaded live members and preloaded load-many results, and
// can be configured to return errors on either side so error paths are
// covered.
type fakeAgentStore struct {
	liveMembers    []agent.Agent
	liveMembersErr error

	loadManyByID  map[agent.AgentID]agent.Agent
	loadManyErr   error
	loadManyCalls [][]agent.AgentID
}

func (store *fakeAgentStore) LiveMembers(ctx context.Context, country, population string) ([]agent.Agent, error) {
	_, _, _ = ctx, country, population
	if store.liveMembersErr != nil {
		return nil, store.liveMembersErr
	}
	// Return a defensive copy so callers do not mutate the backing slice.
	copyOut := make([]agent.Agent, len(store.liveMembers))
	copy(copyOut, store.liveMembers)
	return copyOut, nil
}

func (store *fakeAgentStore) LoadMany(ctx context.Context, agentIDs []agent.AgentID) ([]agent.Agent, error) {
	_ = ctx
	store.loadManyCalls = append(store.loadManyCalls, append([]agent.AgentID(nil), agentIDs...))
	if store.loadManyErr != nil {
		return nil, store.loadManyErr
	}
	out := make([]agent.Agent, 0, len(agentIDs))
	for _, requestedID := range agentIDs {
		if loadedAgent, found := store.loadManyByID[requestedID]; found {
			out = append(out, loadedAgent)
		}
	}
	return out, nil
}

// fakeLeaseAllocator satisfies LeaseSelector.
type fakeLeaseAllocator struct {
	availableIDs       []agent.AgentID
	selectAvailableErr error

	selectCalls []selectCall
}

type selectCall struct {
	country    string
	count      int
	exchangeID string
}

func (lease *fakeLeaseAllocator) SelectAvailable(ctx context.Context, country string, count int, exchangeID string) ([]agent.AgentID, error) {
	_ = ctx
	lease.selectCalls = append(lease.selectCalls, selectCall{country, count, exchangeID})
	if lease.selectAvailableErr != nil {
		return nil, lease.selectAvailableErr
	}
	returnedCount := min(count, len(lease.availableIDs))
	returned := append([]agent.AgentID(nil), lease.availableIDs[:returnedCount]...)
	return returned, nil
}

// fakeWAL satisfies WALStager with a deterministic staged-records map.
// Stage is called from the mint loop which is single-goroutine, but the
// struct is mutex-protected so a future refactor cannot silently
// introduce a race.
type fakeWAL struct {
	mutex         sync.Mutex
	stagedByID    map[agent.AgentID]store.StageRecord
	stagedInOrder []agent.AgentID
	stageErr      error
}

func newFakeWAL() *fakeWAL {
	return &fakeWAL{stagedByID: map[agent.AgentID]store.StageRecord{}}
}

func (wal *fakeWAL) Stage(ctx context.Context, agentID agent.AgentID, record store.StageRecord) error {
	_ = ctx
	wal.mutex.Lock()
	defer wal.mutex.Unlock()
	if wal.stageErr != nil {
		return wal.stageErr
	}
	wal.stagedByID[agentID] = record
	wal.stagedInOrder = append(wal.stagedInOrder, agentID)
	return nil
}

func (wal *fakeWAL) stageCount() int {
	wal.mutex.Lock()
	defer wal.mutex.Unlock()
	return len(wal.stagedInOrder)
}

// ---- Harness ------------------------------------------------------

// counterIDMaker returns a deterministic agent-id source for tests.
// Production builds inject a crypto/rand UUID source; a counter here
// keeps golden-adjacent tests stable.
func counterIDMaker(prefix string) func() agent.AgentID {
	counter := 0
	return func() agent.AgentID {
		counter++
		return agent.AgentID(fmt.Sprintf("%s-%06d", prefix, counter))
	}
}

// fixedClock returns a constant time so TraitsUpdatedAt does not drift
// run-to-run in tests.
func fixedClock() func() time.Time {
	t := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type generatorHarness struct {
	agentStore *fakeAgentStore
	lease      *fakeLeaseAllocator
	wal        *fakeWAL
	scenario   ScenarioProvider
	generator  *Generator
}

func newGeneratorHarness(t *testing.T) *generatorHarness {
	t.Helper()
	fakeStore := &fakeAgentStore{loadManyByID: map[agent.AgentID]agent.Agent{}}
	fakeLease := &fakeLeaseAllocator{}
	fakeWAL := newFakeWAL()
	g, err := NewGenerator(
		fakeStore,
		fakeLease,
		fakeWAL,
		NoOpScenarioProvider{},
		completeGenParams(),
		counterIDMaker("mint"),
		fixedClock(),
		discardLogger(),
	)
	if err != nil {
		t.Fatalf("NewGenerator: %v", err)
	}
	return &generatorHarness{
		agentStore: fakeStore,
		lease:      fakeLease,
		wal:        fakeWAL,
		scenario:   NoOpScenarioProvider{},
		generator:  g,
	}
}

// ---- NewGenerator -------------------------------------------------

func TestNewGeneratorRejectsNilDependencies(t *testing.T) {
	params := completeGenParams()
	ids := counterIDMaker("x")
	clock := fixedClock()
	logger := discardLogger()
	fakeStore := &fakeAgentStore{loadManyByID: map[agent.AgentID]agent.Agent{}}
	fakeLease := &fakeLeaseAllocator{}
	fakeWAL := newFakeWAL()

	nilCases := []struct {
		name string
		call func() error
	}{
		{"nil agentStore", func() error {
			_, err := NewGenerator(nil, fakeLease, fakeWAL, NoOpScenarioProvider{}, params, ids, clock, logger)
			return err
		}},
		{"nil leaseAllocator", func() error {
			_, err := NewGenerator(fakeStore, nil, fakeWAL, NoOpScenarioProvider{}, params, ids, clock, logger)
			return err
		}},
		{"nil walStore", func() error {
			_, err := NewGenerator(fakeStore, fakeLease, nil, NoOpScenarioProvider{}, params, ids, clock, logger)
			return err
		}},
		{"nil scenarioProvider", func() error {
			_, err := NewGenerator(fakeStore, fakeLease, fakeWAL, nil, params, ids, clock, logger)
			return err
		}},
		{"nil newAgentID", func() error {
			_, err := NewGenerator(fakeStore, fakeLease, fakeWAL, NoOpScenarioProvider{}, params, nil, clock, logger)
			return err
		}},
		{"nil clock", func() error {
			_, err := NewGenerator(fakeStore, fakeLease, fakeWAL, NoOpScenarioProvider{}, params, ids, nil, logger)
			return err
		}},
		{"nil logger", func() error {
			_, err := NewGenerator(fakeStore, fakeLease, fakeWAL, NoOpScenarioProvider{}, params, ids, clock, nil)
			return err
		}},
	}
	for _, testCase := range nilCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.call(); err == nil {
				t.Fatalf("expected error for %s, got nil", testCase.name)
			}
		})
	}
}

func TestNewGeneratorRejectsInvalidParams(t *testing.T) {
	broken := completeGenParams()
	broken.HerdProbability = 1.5
	_, err := NewGenerator(
		&fakeAgentStore{loadManyByID: map[agent.AgentID]agent.Agent{}},
		&fakeLeaseAllocator{},
		newFakeWAL(),
		NoOpScenarioProvider{},
		broken,
		counterIDMaker("x"),
		fixedClock(),
		discardLogger(),
	)
	if err == nil {
		t.Fatalf("expected validation error, got nil")
	}
	if !strings.Contains(err.Error(), "HerdProbability") {
		t.Fatalf("expected error to mention HerdProbability, got %q", err.Error())
	}
}

// ---- GenerateCanonical --------------------------------------------

func TestGenerateCanonicalMintsFullShortfallWhenStoreEmpty(t *testing.T) {
	harness := newGeneratorHarness(t)

	population, err := harness.generator.GenerateCanonical(
		context.Background(), "IN", 10, seededRNG(1, 101), "exch-A",
	)
	if err != nil {
		t.Fatalf("GenerateCanonical: %v", err)
	}
	if len(population.Existing) != 0 {
		t.Fatalf("expected 0 existing, got %d", len(population.Existing))
	}
	if len(population.New) != 10 {
		t.Fatalf("expected 10 minted, got %d", len(population.New))
	}
	if harness.wal.stageCount() != 10 {
		t.Fatalf("expected 10 WAL stages, got %d", harness.wal.stageCount())
	}
	for _, mintedAgent := range population.New {
		if mintedAgent.Population != agent.PopulationCanonical {
			t.Fatalf("minted agent %s has population %q, want canonical", mintedAgent.ID, mintedAgent.Population)
		}
		if mintedAgent.Origin != agent.OriginMint {
			t.Fatalf("minted agent %s has origin %q, want mint", mintedAgent.ID, mintedAgent.Origin)
		}
		if mintedAgent.Country != "IN" {
			t.Fatalf("minted agent %s has country %q, want IN", mintedAgent.ID, mintedAgent.Country)
		}
		if mintedAgent.TraitsVersion != 1 {
			t.Fatalf("minted agent %s has traits_version %d, want 1", mintedAgent.ID, mintedAgent.TraitsVersion)
		}
		if _, staged := harness.wal.stagedByID[mintedAgent.ID]; !staged {
			t.Fatalf("minted agent %s not staged in WAL", mintedAgent.ID)
		}
	}
}

func TestGenerateCanonicalReuseFirstMintsOnlyShortfall(t *testing.T) {
	harness := newGeneratorHarness(t)
	harness.agentStore.liveMembers = []agent.Agent{
		{ID: "revived-1", Population: agent.PopulationCanonical, Country: "IN", TradingCapital: 50_000},
		{ID: "revived-2", Population: agent.PopulationCanonical, Country: "IN", TradingCapital: 50_000},
		{ID: "revived-3", Population: agent.PopulationCanonical, Country: "IN", TradingCapital: 50_000},
	}

	population, err := harness.generator.GenerateCanonical(
		context.Background(), "IN", 10, seededRNG(2, 102), "exch-A",
	)
	if err != nil {
		t.Fatalf("GenerateCanonical: %v", err)
	}
	if len(population.Existing) != 3 {
		t.Fatalf("expected 3 existing, got %d", len(population.Existing))
	}
	if len(population.New) != 7 {
		t.Fatalf("expected 7 minted, got %d", len(population.New))
	}
	if harness.wal.stageCount() != 7 {
		t.Fatalf("expected 7 WAL stages (minted only), got %d", harness.wal.stageCount())
	}
}

func TestGenerateCanonicalReuseFullAlreadyPopulatedMintsNothing(t *testing.T) {
	harness := newGeneratorHarness(t)
	harness.agentStore.liveMembers = []agent.Agent{
		{ID: "a", Population: agent.PopulationCanonical, Country: "IN"},
		{ID: "b", Population: agent.PopulationCanonical, Country: "IN"},
		{ID: "c", Population: agent.PopulationCanonical, Country: "IN"},
	}

	population, err := harness.generator.GenerateCanonical(
		context.Background(), "IN", 3, seededRNG(3, 103), "exch-A",
	)
	if err != nil {
		t.Fatalf("GenerateCanonical: %v", err)
	}
	if len(population.New) != 0 {
		t.Fatalf("expected 0 minted, got %d", len(population.New))
	}
	if harness.wal.stageCount() != 0 {
		t.Fatalf("expected 0 WAL stages, got %d", harness.wal.stageCount())
	}
}

func TestGenerateCanonicalReuseOverPopulatedMintsNothingAndKeepsAllExisting(t *testing.T) {
	harness := newGeneratorHarness(t)
	// Five live members, population size target 3 — the generator must
	// not retire or drop anybody, just skip minting.
	harness.agentStore.liveMembers = []agent.Agent{
		{ID: "a", Population: agent.PopulationCanonical, Country: "IN"},
		{ID: "b", Population: agent.PopulationCanonical, Country: "IN"},
		{ID: "c", Population: agent.PopulationCanonical, Country: "IN"},
		{ID: "d", Population: agent.PopulationCanonical, Country: "IN"},
		{ID: "e", Population: agent.PopulationCanonical, Country: "IN"},
	}

	population, err := harness.generator.GenerateCanonical(
		context.Background(), "IN", 3, seededRNG(4, 104), "exch-A",
	)
	if err != nil {
		t.Fatalf("GenerateCanonical: %v", err)
	}
	if len(population.Existing) != 5 {
		t.Fatalf("expected 5 existing (kept), got %d", len(population.Existing))
	}
	if len(population.New) != 0 {
		t.Fatalf("expected 0 minted, got %d", len(population.New))
	}
}

func TestGenerateCanonicalEnsureOneTipperPromotesMintedWhenNoRevivedCarrier(t *testing.T) {
	harness := newGeneratorHarness(t)

	population, err := harness.generator.GenerateCanonical(
		context.Background(), "IN", 10, seededRNG(5, 105), "exch-A",
	)
	if err != nil {
		t.Fatalf("GenerateCanonical: %v", err)
	}

	tipperCount := 0
	tipperIsEndogenous := false
	for _, mintedAgent := range population.New {
		if mintedAgent.Traits.IsTipper {
			tipperCount++
			if mintedAgent.Traits.AgentKind == agent.AgentKindEndogenous {
				tipperIsEndogenous = true
			}
		}
	}
	if tipperCount != 1 {
		t.Fatalf("expected exactly one tipper after promotion, got %d", tipperCount)
	}
	if !tipperIsEndogenous {
		t.Fatalf("promoted tipper is not an endogenous agent")
	}
}

func TestGenerateCanonicalEnsureOneTipperPassesThroughWhenRevivedCarrierExists(t *testing.T) {
	harness := newGeneratorHarness(t)

	// A revived tipper already in the store. The ensureOneTipper step
	// must NOT flip a minted agent on top; the revived carrier keeps
	// the role.
	revivedTipper := agent.Agent{
		ID:         "revived-tipper",
		Population: agent.PopulationCanonical,
		Country:    "IN",
		Traits:     agent.TraitVector{AgentKind: agent.AgentKindEndogenous, IsTipper: true},
	}
	harness.agentStore.liveMembers = []agent.Agent{revivedTipper}

	population, err := harness.generator.GenerateCanonical(
		context.Background(), "IN", 5, seededRNG(6, 106), "exch-A",
	)
	if err != nil {
		t.Fatalf("GenerateCanonical: %v", err)
	}

	mintedTippers := 0
	for _, mintedAgent := range population.New {
		if mintedAgent.Traits.IsTipper {
			mintedTippers++
		}
	}
	if mintedTippers != 0 {
		t.Fatalf("expected zero minted tippers when a revived carrier exists, got %d", mintedTippers)
	}
	// Revived carrier is still a tipper (we never touch its trait map).
	if !population.Existing[0].Traits.IsTipper {
		t.Fatalf("revived tipper lost its flag")
	}
}

func TestGenerateCanonicalIsDeterministic(t *testing.T) {
	first := newGeneratorHarness(t)
	second := newGeneratorHarness(t)

	firstPopulation, err := first.generator.GenerateCanonical(
		context.Background(), "IN", 10, seededRNG(7, 107), "exch-A",
	)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}
	secondPopulation, err := second.generator.GenerateCanonical(
		context.Background(), "IN", 10, seededRNG(7, 107), "exch-A",
	)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}

	if !reflect.DeepEqual(firstPopulation, secondPopulation) {
		t.Fatalf("determinism broken under identical seed+inputs:\nfirst = %+v\nsecond = %+v",
			firstPopulation, secondPopulation)
	}
}

func TestGenerateCanonicalSurfacesStoreError(t *testing.T) {
	harness := newGeneratorHarness(t)
	liveErr := errors.New("database unreachable")
	harness.agentStore.liveMembersErr = liveErr

	_, err := harness.generator.GenerateCanonical(
		context.Background(), "IN", 10, seededRNG(8, 108), "exch-A",
	)
	if err == nil || !errors.Is(err, liveErr) {
		t.Fatalf("expected wrapped LiveMembers error, got %v", err)
	}
	if harness.wal.stageCount() != 0 {
		t.Fatalf("WAL staged entries on a failed generation: %d", harness.wal.stageCount())
	}
}

func TestGenerateCanonicalSurfacesWALError(t *testing.T) {
	harness := newGeneratorHarness(t)
	stageErr := errors.New("redis down")
	harness.wal.stageErr = stageErr

	_, err := harness.generator.GenerateCanonical(
		context.Background(), "IN", 10, seededRNG(9, 109), "exch-A",
	)
	if err == nil || !errors.Is(err, stageErr) {
		t.Fatalf("expected wrapped Stage error, got %v", err)
	}
}

// ---- GenerateOnDemand ---------------------------------------------

func TestGenerateOnDemandLeasesAndMintsShortfall(t *testing.T) {
	harness := newGeneratorHarness(t)
	leasedIDs := []agent.AgentID{"lease-1", "lease-2", "lease-3"}
	harness.lease.availableIDs = leasedIDs
	for _, leaseID := range leasedIDs {
		harness.agentStore.loadManyByID[leaseID] = agent.Agent{
			ID:            leaseID,
			Population:    agent.PopulationOnDemand,
			Country:       "IN",
			CapitalHandle: "handle-" + string(leaseID),
			Traits:        agent.TraitVector{AgentKind: agent.AgentKindEndogenous, CapitalSize: 999_999},
		}
	}

	population, err := harness.generator.GenerateOnDemand(
		context.Background(), "IN", "exch-OD", 10, seededRNG(11, 111), "",
	)
	if err != nil {
		t.Fatalf("GenerateOnDemand: %v", err)
	}
	if len(population.Existing) != 3 {
		t.Fatalf("expected 3 existing (leased+loaded), got %d", len(population.Existing))
	}
	if len(population.New) != 7 {
		t.Fatalf("expected 7 minted (shortfall), got %d", len(population.New))
	}
	if harness.wal.stageCount() != 7 {
		t.Fatalf("expected 7 WAL stages (minted only), got %d", harness.wal.stageCount())
	}
	if len(harness.lease.selectCalls) != 1 {
		t.Fatalf("expected exactly one SelectAvailable call, got %d", len(harness.lease.selectCalls))
	}
	call := harness.lease.selectCalls[0]
	if call.country != "IN" || call.count != 10 || call.exchangeID != "exch-OD" {
		t.Fatalf("SelectAvailable called with %+v", call)
	}
}

func TestGenerateOnDemandRedrawsExistingTraits(t *testing.T) {
	harness := newGeneratorHarness(t)
	// Sentinel capital size on loaded rows — the generator must redraw
	// traits for leased agents so this value will NOT survive.
	const sentinel = int64(0x7F7F7F7F)
	leasedIDs := []agent.AgentID{"lease-1", "lease-2"}
	harness.lease.availableIDs = leasedIDs
	for _, leaseID := range leasedIDs {
		harness.agentStore.loadManyByID[leaseID] = agent.Agent{
			ID:         leaseID,
			Population: agent.PopulationOnDemand,
			Country:    "IN",
			Traits:     agent.TraitVector{CapitalSize: sentinel},
		}
	}

	population, err := harness.generator.GenerateOnDemand(
		context.Background(), "IN", "exch-OD", 2, seededRNG(12, 112), "",
	)
	if err != nil {
		t.Fatalf("GenerateOnDemand: %v", err)
	}
	for _, existingAgent := range population.Existing {
		if existingAgent.Traits.CapitalSize == sentinel {
			t.Fatalf("leased agent %s kept pre-load CapitalSize sentinel — traits were not redrawn",
				existingAgent.ID)
		}
	}
}

func TestGenerateOnDemandSkipsLeasedIDWithoutDurableRow(t *testing.T) {
	harness := newGeneratorHarness(t)
	harness.lease.availableIDs = []agent.AgentID{"lease-with-row", "lease-orphan"}
	harness.agentStore.loadManyByID["lease-with-row"] = agent.Agent{
		ID: "lease-with-row", Population: agent.PopulationOnDemand, Country: "IN",
	}
	// "lease-orphan" has no durable row — simulates a membership-vs-lease
	// drift (reconciled in a later phase).

	population, err := harness.generator.GenerateOnDemand(
		context.Background(), "IN", "exch-OD", 3, seededRNG(13, 113), "",
	)
	if err != nil {
		t.Fatalf("GenerateOnDemand: %v", err)
	}
	if len(population.Existing) != 1 {
		t.Fatalf("expected 1 existing (orphan skipped), got %d", len(population.Existing))
	}
	// Shortfall absorbs the orphan: with populationSize=3 and 1 loaded,
	// we mint 2.
	if len(population.New) != 2 {
		t.Fatalf("expected 2 minted, got %d", len(population.New))
	}
}

func TestGenerateOnDemandNeverSetsIsTipperOnTraits(t *testing.T) {
	harness := newGeneratorHarness(t)
	harness.lease.availableIDs = []agent.AgentID{"lease-1"}
	harness.agentStore.loadManyByID["lease-1"] = agent.Agent{
		ID: "lease-1", Population: agent.PopulationOnDemand, Country: "IN",
	}

	population, err := harness.generator.GenerateOnDemand(
		context.Background(), "IN", "exch-OD", 5, seededRNG(14, 114), "",
	)
	if err != nil {
		t.Fatalf("GenerateOnDemand: %v", err)
	}
	for _, a := range population.All() {
		if a.Traits.IsTipper {
			t.Fatalf("on-demand population carried IsTipper on frozen traits for %s", a.ID)
		}
	}
}

func TestGenerateOnDemandIsDeterministic(t *testing.T) {
	build := func() *generatorHarness {
		harness := newGeneratorHarness(t)
		harness.lease.availableIDs = []agent.AgentID{"lease-1", "lease-2", "lease-3"}
		for _, leaseID := range harness.lease.availableIDs {
			harness.agentStore.loadManyByID[leaseID] = agent.Agent{
				ID: leaseID, Population: agent.PopulationOnDemand, Country: "IN",
			}
		}
		return harness
	}
	first := build()
	second := build()

	firstPopulation, err := first.generator.GenerateOnDemand(
		context.Background(), "IN", "exch-OD", 10, seededRNG(15, 115), "",
	)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	secondPopulation, err := second.generator.GenerateOnDemand(
		context.Background(), "IN", "exch-OD", 10, seededRNG(15, 115), "",
	)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !reflect.DeepEqual(firstPopulation, secondPopulation) {
		t.Fatalf("on-demand determinism broken:\nfirst=%+v\nsecond=%+v",
			firstPopulation, secondPopulation)
	}
}

func TestGenerateOnDemandBaselineParity(t *testing.T) {
	// NoOpScenarioProvider returns (nil, nil) for any input, so the
	// draw must be identical whichever scenario string is passed.
	build := func() *generatorHarness { return newGeneratorHarness(t) }

	first := build()
	second := build()

	withEmptyScenario, err := first.generator.GenerateOnDemand(
		context.Background(), "IN", "exch-OD", 5, seededRNG(16, 116), "",
	)
	if err != nil {
		t.Fatalf("empty scenario: %v", err)
	}
	withNamedScenario, err := second.generator.GenerateOnDemand(
		context.Background(), "IN", "exch-OD", 5, seededRNG(16, 116), "any_name_at_all",
	)
	if err != nil {
		t.Fatalf("named scenario: %v", err)
	}
	if !reflect.DeepEqual(withEmptyScenario, withNamedScenario) {
		t.Fatalf("NoOp scenario provider changed output for a named scenario:\nempty=%+v\nnamed=%+v",
			withEmptyScenario, withNamedScenario)
	}
}

func TestGenerateOnDemandSurfacesLeaseError(t *testing.T) {
	harness := newGeneratorHarness(t)
	leaseErr := errors.New("lua returned nil")
	harness.lease.selectAvailableErr = leaseErr

	_, err := harness.generator.GenerateOnDemand(
		context.Background(), "IN", "exch-OD", 5, seededRNG(17, 117), "",
	)
	if err == nil || !errors.Is(err, leaseErr) {
		t.Fatalf("expected wrapped SelectAvailable error, got %v", err)
	}
	if harness.wal.stageCount() != 0 {
		t.Fatalf("WAL staged entries on a failed lease: %d", harness.wal.stageCount())
	}
}

func TestGenerateOnDemandSurfacesStoreLoadError(t *testing.T) {
	harness := newGeneratorHarness(t)
	harness.lease.availableIDs = []agent.AgentID{"lease-1"}
	loadErr := errors.New("row scan failed")
	harness.agentStore.loadManyErr = loadErr

	_, err := harness.generator.GenerateOnDemand(
		context.Background(), "IN", "exch-OD", 5, seededRNG(18, 118), "",
	)
	if err == nil || !errors.Is(err, loadErr) {
		t.Fatalf("expected wrapped LoadMany error, got %v", err)
	}
}

// ---- Mint-level invariant: WAL stage BEFORE return ----------------

func TestMintStagesWALBeforeReturningAgent(t *testing.T) {
	harness := newGeneratorHarness(t)
	// If Stage errors, mint returns an error — and the returned agent
	// (zero value) is useless. The contract is that a successful mint
	// return implies a successful stage; this test asserts the
	// contrapositive — a staging failure prevents any agent slot from
	// being filled downstream.
	harness.wal.stageErr = errors.New("staging unavailable")

	population, err := harness.generator.GenerateCanonical(
		context.Background(), "IN", 5, seededRNG(19, 119), "exch-A",
	)
	if err == nil {
		t.Fatalf("expected error on WAL stage failure, got nil")
	}
	if len(population.Existing) != 0 || len(population.New) != 0 {
		t.Fatalf("partial population returned on stage failure: %+v", population)
	}
}

func TestMintPopulatesWALRecordWithContextFields(t *testing.T) {
	harness := newGeneratorHarness(t)

	population, err := harness.generator.GenerateOnDemand(
		context.Background(), "IN", "exch-OD-xyz", 3, seededRNG(20, 120), "",
	)
	if err != nil {
		t.Fatalf("GenerateOnDemand: %v", err)
	}
	for _, mintedAgent := range population.New {
		stagedRecord, present := harness.wal.stagedByID[mintedAgent.ID]
		if !present {
			t.Fatalf("minted agent %s not staged", mintedAgent.ID)
		}
		if stagedRecord.Population != agent.PopulationOnDemand {
			t.Fatalf("staged record for %s has population %q", mintedAgent.ID, stagedRecord.Population)
		}
		if stagedRecord.Country != "IN" {
			t.Fatalf("staged record for %s has country %q", mintedAgent.ID, stagedRecord.Country)
		}
		if stagedRecord.ExchangeID != "exch-OD-xyz" {
			t.Fatalf("staged record for %s has exchange %q", mintedAgent.ID, stagedRecord.ExchangeID)
		}
		if stagedRecord.Traits.AgentKind == "" {
			t.Fatalf("staged record for %s has empty traits.AgentKind", mintedAgent.ID)
		}
	}
}

// ---- ensureOneTipper edge coverage --------------------------------

func TestEnsureOneTipperSkipsFlipWhenAllMintedAreHerd(t *testing.T) {
	// Craft a minted-slice where every agent is herd. ensureOneTipper
	// must leave every IsTipper false and log a warning (we don't
	// assert the log output here; the behaviour is "do not flip, do
	// not panic").
	existing := []agent.Agent(nil)
	minted := []agent.Agent{
		{Traits: agent.TraitVector{AgentKind: agent.AgentKindHerd}},
		{Traits: agent.TraitVector{AgentKind: agent.AgentKindHerd}},
	}
	ensureOneTipper(existing, minted, seededRNG(21, 121), discardLogger())
	for index, mintedAgent := range minted {
		if mintedAgent.Traits.IsTipper {
			t.Fatalf("herd-only pool got a tipper flip at index %d", index)
		}
	}
}

func TestEnsureOneTipperIgnoresExtraRevivedCarriers(t *testing.T) {
	// If somehow two revived agents carry the flag (data corruption),
	// ensureOneTipper does not demote either — the in-memory trait
	// vectors are treated as immutable. Both stay flagged; the
	// invariant is enforced at persist / load time, not here.
	existing := []agent.Agent{
		{ID: "a", Traits: agent.TraitVector{AgentKind: agent.AgentKindEndogenous, IsTipper: true}},
		{ID: "b", Traits: agent.TraitVector{AgentKind: agent.AgentKindEndogenous, IsTipper: true}},
	}
	minted := []agent.Agent{
		{Traits: agent.TraitVector{AgentKind: agent.AgentKindEndogenous}},
	}
	ensureOneTipper(existing, minted, seededRNG(22, 122), discardLogger())
	tipperCount := 0
	for _, revivedAgent := range existing {
		if revivedAgent.Traits.IsTipper {
			tipperCount++
		}
	}
	for _, mintedAgent := range minted {
		if mintedAgent.Traits.IsTipper {
			tipperCount++
		}
	}
	if tipperCount != 2 {
		t.Fatalf("ensureOneTipper mutated an existing carrier; tipperCount=%d", tipperCount)
	}
}

// ---- Population.All ------------------------------------------------

func TestPopulationAllConcatenatesExistingBeforeNew(t *testing.T) {
	population := Population{
		Existing: []agent.Agent{{ID: "e1"}, {ID: "e2"}},
		New:      []agent.Agent{{ID: "n1"}, {ID: "n2"}, {ID: "n3"}},
	}
	all := population.All()
	wantIDs := []agent.AgentID{"e1", "e2", "n1", "n2", "n3"}
	if len(all) != len(wantIDs) {
		t.Fatalf("All() length = %d, want %d", len(all), len(wantIDs))
	}
	for index, wantID := range wantIDs {
		if all[index].ID != wantID {
			t.Fatalf("All()[%d] = %q, want %q", index, all[index].ID, wantID)
		}
	}
}
