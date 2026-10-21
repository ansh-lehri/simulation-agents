package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"simulation-agents/agent"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/session"
	"simulation-agents/store"
	"simulation-agents/transport"
)

// TestContinueBringUpCanonicalHappyPath pins the canonical tail
// shape: every new agent is persisted + WAL-cleared, every agent
// (existing + new) gets a Redis snapshot, NO lease.Enlist and NO
// SetTipper run (canonical skips P4), sessions:active is registered
// with no curve_eff, and the session runner is spawned exactly once.
func TestContinueBringUpCanonicalHappyPath(t *testing.T) {
	t.Parallel()

	fixture := newTailFixture(t)
	population := Population{
		Existing: makeAgents(2, "ex"),
		New:      makeAgents(3, "nw"),
	}
	request := canonicalTailRequest()

	fixture.orchestrator.continueBringUp(context.Background(), request, population)
	fixture.waitForRunnerToBeSpawned(t)

	if calls := fixture.agentStore.insertCount.Load(); calls != 3 {
		t.Fatalf("InsertAgent calls = %d, want 3 (one per new agent)", calls)
	}
	if calls := fixture.pendingMintWAL.clearCount.Load(); calls != 3 {
		t.Fatalf("WAL.Clear calls = %d, want 3 (one per new agent)", calls)
	}
	if calls := fixture.runtimeStore.initialSnapshotCount.Load(); calls != 5 {
		t.Fatalf("WriteInitialSnapshot calls = %d, want 5 (every agent)", calls)
	}
	if calls := fixture.leaseAllocator.enlistCount.Load(); calls != 0 {
		t.Fatalf("lease.Enlist must not run on canonical; got %d calls", calls)
	}
	if calls := fixture.runtimeStore.setTipperCount.Load(); calls != 0 {
		t.Fatalf("SetTipper must not run on canonical; got %d calls", calls)
	}
	if calls := fixture.activeSessionStore.registerCount.Load(); calls != 1 {
		t.Fatalf("Register active session = %d, want 1", calls)
	}
	registeredRecord := fixture.activeSessionStore.lastRecord()
	if registeredRecord.CurveEff != nil {
		t.Fatalf("canonical bring-up must register with CurveEff=nil; got %+v", registeredRecord.CurveEff)
	}
	if registeredRecord.SessionType != SessionTypeCanonical {
		t.Fatalf("registered record SessionType = %q, want %q",
			registeredRecord.SessionType, SessionTypeCanonical)
	}
	if calls := fixture.sessionRunner.runCount.Load(); calls != 1 {
		t.Fatalf("SessionRunner.Run calls = %d, want 1", calls)
	}
	if fixture.failure.failCount.Load() != 0 {
		t.Fatalf("FailBringUp must not be called on the happy path; got %d",
			fixture.failure.failCount.Load())
	}
}

// TestContinueBringUpOnDemandHappyPath pins the on-demand tail:
// adds lease.Enlist for every new agent AND picks one on-demand
// runtime tipper (SetTipper fires once). With a non-empty scenario
// name, the registered record carries a non-nil CurveEff resolved
// via the scenario provider.
func TestContinueBringUpOnDemandHappyPath(t *testing.T) {
	t.Parallel()

	fixture := newTailFixture(t)
	fixture.scenarioProvider.curveEffectiveToReturn = &generator.CurveEffective{
		EntryRate:   1.2,
		StepOutRate: 0.3,
		DoneRate:    0.4,
		ReentryRate: 0.5,
	}
	population := Population{
		Existing: makeAgents(2, "ex"),
		New:      makeAgents(3, "nw"),
	}
	request := canonicalTailRequest()
	request.SessionType = SessionTypeOnDemand
	request.Scenario = "polarized_optimism"

	fixture.orchestrator.continueBringUp(context.Background(), request, population)
	fixture.waitForRunnerToBeSpawned(t)

	if calls := fixture.leaseAllocator.enlistCount.Load(); calls != 3 {
		t.Fatalf("lease.Enlist calls = %d, want 3 (one per new agent)", calls)
	}
	if calls := fixture.runtimeStore.setTipperCount.Load(); calls != 1 {
		t.Fatalf("SetTipper calls = %d, want 1 (one on-demand runtime tipper)", calls)
	}
	registered := fixture.activeSessionStore.lastRecord()
	if registered.CurveEff == nil {
		t.Fatalf("on-demand+scenario bring-up must register with a non-nil CurveEff")
	}
	if registered.CurveEff.EntryRate != 1.2 {
		t.Fatalf("CurveEff.EntryRate = %v, want 1.2 (passed through from scenario provider)",
			registered.CurveEff.EntryRate)
	}
	if registered.CurveEff.ReentryRate != 0.5 {
		t.Fatalf("CurveEff.ReentryRate = %v, want 0.5", registered.CurveEff.ReentryRate)
	}
	if calls := fixture.sessionRunner.runCount.Load(); calls != 1 {
		t.Fatalf("SessionRunner.Run calls = %d, want 1", calls)
	}
}

// TestContinueBringUpOnDemandWithoutScenarioOmitsCurveEff: on-demand
// with Scenario="" skips the curve-provider call and registers with
// CurveEff=nil. The scenario provider must not be hit either.
func TestContinueBringUpOnDemandWithoutScenarioOmitsCurveEff(t *testing.T) {
	t.Parallel()

	fixture := newTailFixture(t)
	population := Population{New: makeAgents(2, "nw")}
	request := canonicalTailRequest()
	request.SessionType = SessionTypeOnDemand
	// request.Scenario is ""

	fixture.orchestrator.continueBringUp(context.Background(), request, population)
	fixture.waitForRunnerToBeSpawned(t)

	registered := fixture.activeSessionStore.lastRecord()
	if registered.CurveEff != nil {
		t.Fatalf("empty scenario must omit CurveEff; got %+v", registered.CurveEff)
	}
	if calls := fixture.scenarioProvider.effectiveCount.Load(); calls != 0 {
		t.Fatalf("empty scenario must skip the scenario provider; got %d calls", calls)
	}
}

// TestContinueBringUpP2PersistFailureAborts: InsertAgent on the
// first new agent returns an error. The tail must call FailBringUp
// and skip every subsequent step — no snapshot, no enlist, no
// register, no runner spawn.
func TestContinueBringUpP2PersistFailureAborts(t *testing.T) {
	t.Parallel()

	fixture := newTailFixture(t)
	fixture.agentStore.insertError = errors.New("db unavailable")

	request := canonicalTailRequest()
	population := Population{New: makeAgents(2, "nw")}

	fixture.orchestrator.continueBringUp(context.Background(), request, population)

	if fixture.failure.failCount.Load() != 1 {
		t.Fatalf("FailBringUp = %d, want 1", fixture.failure.failCount.Load())
	}
	if fixture.runtimeStore.initialSnapshotCount.Load() != 0 {
		t.Fatalf("WriteInitialSnapshot must not run after persist failure")
	}
	if fixture.activeSessionStore.registerCount.Load() != 0 {
		t.Fatalf("Register must not run after persist failure")
	}
	if fixture.sessionRunner.runCount.Load() != 0 {
		t.Fatalf("SessionRunner.Run must not be spawned after persist failure")
	}
	causeString := fmt.Sprintf("%v", fixture.failure.lastCause.Load())
	if !strings.Contains(causeString, "db unavailable") {
		t.Fatalf("fail cause must wrap the underlying error; got %q", causeString)
	}
}

// TestContinueBringUpP2WALClearFailureAborts: InsertAgent succeeds
// but WAL.Clear fails on the first entry. Downstream steps are
// skipped.
func TestContinueBringUpP2WALClearFailureAborts(t *testing.T) {
	t.Parallel()

	fixture := newTailFixture(t)
	fixture.pendingMintWAL.clearError = errors.New("redis down")

	request := canonicalTailRequest()
	population := Population{New: makeAgents(2, "nw")}

	fixture.orchestrator.continueBringUp(context.Background(), request, population)

	if fixture.failure.failCount.Load() != 1 {
		t.Fatalf("FailBringUp = %d, want 1", fixture.failure.failCount.Load())
	}
	if fixture.runtimeStore.initialSnapshotCount.Load() != 0 {
		t.Fatalf("WriteInitialSnapshot must not run after WAL.Clear failure")
	}
	if fixture.sessionRunner.runCount.Load() != 0 {
		t.Fatalf("SessionRunner.Run must not be spawned after WAL.Clear failure")
	}
}

// TestContinueBringUpP3SnapshotFailureAborts: InsertAgent + WAL.Clear
// succeed but WriteInitialSnapshot fails. Downstream enlist / register /
// spawn must not run.
func TestContinueBringUpP3SnapshotFailureAborts(t *testing.T) {
	t.Parallel()

	fixture := newTailFixture(t)
	fixture.runtimeStore.initialSnapshotError = errors.New("hset failed")

	request := canonicalTailRequest()
	population := Population{Existing: makeAgents(1, "ex"), New: makeAgents(2, "nw")}

	fixture.orchestrator.continueBringUp(context.Background(), request, population)

	if fixture.failure.failCount.Load() != 1 {
		t.Fatalf("FailBringUp = %d, want 1", fixture.failure.failCount.Load())
	}
	if fixture.activeSessionStore.registerCount.Load() != 0 {
		t.Fatalf("Register must not run after snapshot failure")
	}
	if fixture.sessionRunner.runCount.Load() != 0 {
		t.Fatalf("SessionRunner.Run must not spawn after snapshot failure")
	}
}

// TestContinueBringUpP4EnlistFailureAbortsOnDemand: on-demand
// bring-up, lease.Enlist fails on the first new agent. Register
// and spawn must not run.
func TestContinueBringUpP4EnlistFailureAbortsOnDemand(t *testing.T) {
	t.Parallel()

	fixture := newTailFixture(t)
	fixture.leaseAllocator.enlistError = errors.New("lease conflict")

	request := canonicalTailRequest()
	request.SessionType = SessionTypeOnDemand
	population := Population{New: makeAgents(2, "nw")}

	fixture.orchestrator.continueBringUp(context.Background(), request, population)

	if fixture.failure.failCount.Load() != 1 {
		t.Fatalf("FailBringUp = %d, want 1", fixture.failure.failCount.Load())
	}
	if fixture.activeSessionStore.registerCount.Load() != 0 {
		t.Fatalf("Register must not run after enlist failure")
	}
	if fixture.sessionRunner.runCount.Load() != 0 {
		t.Fatalf("SessionRunner.Run must not spawn after enlist failure")
	}
}

// TestContinueBringUpP4SetTipperFailureAbortsOnDemand: SetTipper
// fails on the on-demand tipper pick. Register and spawn skipped.
func TestContinueBringUpP4SetTipperFailureAbortsOnDemand(t *testing.T) {
	t.Parallel()

	fixture := newTailFixture(t)
	fixture.runtimeStore.setTipperError = errors.New("hset tipper failed")

	request := canonicalTailRequest()
	request.SessionType = SessionTypeOnDemand
	population := Population{New: makeAgents(2, "nw")}

	fixture.orchestrator.continueBringUp(context.Background(), request, population)

	if fixture.failure.failCount.Load() != 1 {
		t.Fatalf("FailBringUp = %d, want 1", fixture.failure.failCount.Load())
	}
	if fixture.activeSessionStore.registerCount.Load() != 0 {
		t.Fatalf("Register must not run after SetTipper failure")
	}
	if fixture.sessionRunner.runCount.Load() != 0 {
		t.Fatalf("SessionRunner.Run must not spawn after SetTipper failure")
	}
}

// TestContinueBringUpP5RegisterFailureAborts: Register fails →
// SessionRunner.Run must not be spawned.
func TestContinueBringUpP5RegisterFailureAborts(t *testing.T) {
	t.Parallel()

	fixture := newTailFixture(t)
	fixture.activeSessionStore.registerError = errors.New("hset active failed")

	request := canonicalTailRequest()
	population := Population{New: makeAgents(2, "nw")}

	fixture.orchestrator.continueBringUp(context.Background(), request, population)

	if fixture.failure.failCount.Load() != 1 {
		t.Fatalf("FailBringUp = %d, want 1", fixture.failure.failCount.Load())
	}
	if fixture.sessionRunner.runCount.Load() != 0 {
		t.Fatalf("SessionRunner.Run must not spawn after Register failure; got %d",
			fixture.sessionRunner.runCount.Load())
	}
}

// TestContinueBringUpOnDemandTipperPickIsDeterministicUnderFixedSeed:
// two orchestrators built with the SAME fixed-seed RNGSource pick
// the same tipper agent for the same population. The fresh-entropy
// path cannot be asserted statically; this test pins only the
// fixed-seed replay invariant tuning runs depend on.
func TestContinueBringUpOnDemandTipperPickIsDeterministicUnderFixedSeed(t *testing.T) {
	t.Parallel()

	population := Population{
		Existing: makeAgents(3, "ex"),
		New:      makeAgents(5, "nw"),
	}
	request := canonicalTailRequest()
	request.SessionType = SessionTypeOnDemand

	runWithSeed := func() string {
		fixture := newTailFixtureWithRNG(t, NewFixedSeedSource(1234, 5678))
		fixture.orchestrator.continueBringUp(context.Background(), request, population)
		fixture.waitForRunnerToBeSpawned(t)
		return fixture.runtimeStore.lastTipperAgentID()
	}

	firstRun := runWithSeed()
	secondRun := runWithSeed()
	if firstRun == "" {
		t.Fatalf("tipper was not set on the first run")
	}
	if firstRun != secondRun {
		t.Fatalf("fixed-seed tipper pick drifted: run1=%q run2=%q", firstRun, secondRun)
	}
}

// --- fixture ------------------------------------------------------

type tailFixture struct {
	orchestrator       *Orchestrator
	agentStore         *recordingAgentStore
	runtimeStore       *recordingRuntimeStore
	leaseAllocator     *recordingLeaseAllocator
	pendingMintWAL     *recordingPendingMintWAL
	activeSessionStore *recordingActiveSessionStore
	scenarioProvider   *programmableScenarioProvider
	sessionRunner      *recordingSessionRunner
	failure            *recordingTailFailure
}

func newTailFixture(t *testing.T) *tailFixture {
	t.Helper()
	return newTailFixtureWithRNG(t, NewFixedSeedSource(1, 2))
}

func newTailFixtureWithRNG(t *testing.T, rngSource RNGSource) *tailFixture {
	t.Helper()
	logger := discardLogger()

	accountOpen, err := NewAccountOpenClient(newSpyCoreAccountClient(), logger)
	if err != nil {
		t.Fatalf("NewAccountOpenClient: %v", err)
	}
	capital, err := NewCapitalResolver(agent.CapitalParams{Floor: 1, JitterSpread: 0, DefaultFundedCash: 100_000}, logger)
	if err != nil {
		t.Fatalf("NewCapitalResolver: %v", err)
	}

	fixture := &tailFixture{
		agentStore:         &recordingAgentStore{},
		runtimeStore:       &recordingRuntimeStore{},
		leaseAllocator:     &recordingLeaseAllocator{},
		pendingMintWAL:     &recordingPendingMintWAL{},
		activeSessionStore: &recordingActiveSessionStore{},
		scenarioProvider:   &programmableScenarioProvider{},
		sessionRunner:      &recordingSessionRunner{},
		failure:            &recordingTailFailure{},
	}

	orchestrator, err := NewOrchestrator(
		buildTestGenerator(t),
		accountOpen,
		capital,
		rngSource,
		fixture.agentStore,
		fixture.runtimeStore,
		fixture.leaseAllocator,
		fixture.pendingMintWAL,
		fixture.activeSessionStore,
		fixture.scenarioProvider,
		fixture.sessionRunner,
		completeOrchestratorConfig(),
		logger,
	)
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}
	// Patch the orchestrator's failure-reporter hook so the tests
	// observe FailBringUp / ReportReady directly on the recorder
	// instead of only through the logger-based default. Same-package
	// test can set the unexported field without an exported setter.
	orchestrator.failureReporter = fixture.failure
	fixture.orchestrator = orchestrator
	return fixture
}

// waitForRunnerToBeSpawned blocks briefly for the asynchronous
// spawn of sessionRunner.Run at the end of continueBringUp. If the
// expected spawn never happens, the test's call-count assertions
// catch it; this helper just avoids racing the recorder's atomic.
func (fixture *tailFixture) waitForRunnerToBeSpawned(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if fixture.sessionRunner.runCount.Load() > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func makeAgents(count int, prefix string) []agent.Agent {
	population := make([]agent.Agent, 0, count)
	for index := 0; index < count; index++ {
		population = append(population, agent.Agent{
			ID:         agent.AgentID(fmt.Sprintf("%s-%03d", prefix, index)),
			Population: agent.PopulationCanonical,
			Country:    "IN",
		})
	}
	return population
}

func canonicalTailRequest() transport.BringUpRequest {
	return transport.BringUpRequest{
		ExchangeID:   "exch_tail",
		SessionType:  SessionTypeCanonical,
		Country:      "IN",
		SessionStart: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC),
		SessionEnd:   time.Date(2026, 10, 20, 10, 30, 0, 0, time.UTC),
		Instruments:  []string{"stock1"},
		BrokerID:     "broker-A",
	}
}

// --- recording fakes ---------------------------------------------

type recordingAgentStore struct {
	insertCount atomic.Int32
	insertError error
}

func (fake *recordingAgentStore) InsertAgent(ctx context.Context, agentRecord agent.Agent) error {
	_, _ = ctx, agentRecord
	fake.insertCount.Add(1)
	if fake.insertError != nil {
		return fake.insertError
	}
	return nil
}
func (fake *recordingAgentStore) LiveMembers(ctx context.Context, country, population string) ([]agent.Agent, error) {
	_, _, _ = ctx, country, population
	return nil, nil
}
func (fake *recordingAgentStore) LoadMany(ctx context.Context, agentIDs []agent.AgentID) ([]agent.Agent, error) {
	_, _ = ctx, agentIDs
	return nil, nil
}
func (fake *recordingAgentStore) Retire(ctx context.Context, agentID agent.AgentID, retiredAt time.Time) error {
	_, _, _ = ctx, agentID, retiredAt
	return nil
}

type recordingRuntimeStore struct {
	initialSnapshotCount atomic.Int32
	initialSnapshotError error
	setTipperCount       atomic.Int32
	setTipperError       error

	tipperMutex   sync.Mutex
	tipperAgentID string
}

func (fake *recordingRuntimeStore) WriteInitialSnapshot(ctx context.Context, exchangeID string, agentRecord agent.Agent) error {
	_, _, _ = ctx, exchangeID, agentRecord
	fake.initialSnapshotCount.Add(1)
	if fake.initialSnapshotError != nil {
		return fake.initialSnapshotError
	}
	return nil
}
func (fake *recordingRuntimeStore) SetPresence(ctx context.Context, exchangeID string, agentID agent.AgentID, presence string) error {
	_, _, _, _ = ctx, exchangeID, agentID, presence
	return nil
}
func (fake *recordingRuntimeStore) SetTimers(ctx context.Context, exchangeID string, agentID agent.AgentID, next, idleSince int64) error {
	_, _, _, _, _ = ctx, exchangeID, agentID, next, idleSince
	return nil
}
func (fake *recordingRuntimeStore) StampEntryTime(ctx context.Context, exchangeID string, agentID agent.AgentID, entryTime float64) error {
	_, _, _, _ = ctx, exchangeID, agentID, entryTime
	return nil
}
func (fake *recordingRuntimeStore) SetTipper(ctx context.Context, exchangeID string, agentID agent.AgentID) error {
	_ = ctx
	_ = exchangeID
	fake.setTipperCount.Add(1)
	if fake.setTipperError != nil {
		return fake.setTipperError
	}
	fake.tipperMutex.Lock()
	fake.tipperAgentID = string(agentID)
	fake.tipperMutex.Unlock()
	return nil
}

func (fake *recordingRuntimeStore) SetStandingOpinion(_ context.Context, _ string, _ agent.AgentID, _ float64, _ string) error {
	return nil
}
func (fake *recordingRuntimeStore) SetWaitStreak(_ context.Context, _ string, _ agent.AgentID, _ int) error {
	return nil
}
func (fake *recordingRuntimeStore) SetPositionContext(_ context.Context, _ string, _ agent.AgentID, _ *store.PositionContext) error {
	return nil
}
func (fake *recordingRuntimeStore) SetTipFollowState(_ context.Context, _ string, _ agent.AgentID, _ *store.Tracking, _ string, _, _ int64) error {
	return nil
}
func (fake *recordingRuntimeStore) SetPerceptionRefs(_ context.Context, _ string, _ agent.AgentID, _ string, _ store.PerceptionRefs) error {
	return nil
}

func (fake *recordingRuntimeStore) lastTipperAgentID() string {
	fake.tipperMutex.Lock()
	defer fake.tipperMutex.Unlock()
	return fake.tipperAgentID
}

type recordingLeaseAllocator struct {
	enlistCount atomic.Int32
	enlistError error
}

func (fake *recordingLeaseAllocator) SelectAvailable(ctx context.Context, country string, count int, exchangeID string) ([]agent.AgentID, error) {
	_, _, _, _ = ctx, country, count, exchangeID
	return nil, nil
}
func (fake *recordingLeaseAllocator) Enlist(ctx context.Context, country string, agentID agent.AgentID, exchangeID string) error {
	_, _, _, _ = ctx, country, agentID, exchangeID
	fake.enlistCount.Add(1)
	if fake.enlistError != nil {
		return fake.enlistError
	}
	return nil
}
func (fake *recordingLeaseAllocator) Release(ctx context.Context, country string, agentIDs []agent.AgentID) error {
	_, _, _ = ctx, country, agentIDs
	return nil
}
func (fake *recordingLeaseAllocator) Reconcile(ctx context.Context, country string, liveExchanges map[string]bool, members []agent.AgentID) error {
	_, _, _, _ = ctx, country, liveExchanges, members
	return nil
}

type recordingPendingMintWAL struct {
	clearCount atomic.Int32
	clearError error
}

func (fake *recordingPendingMintWAL) Stage(ctx context.Context, agentID agent.AgentID, record store.StageRecord) error {
	_, _, _ = ctx, agentID, record
	return nil
}
func (fake *recordingPendingMintWAL) Clear(ctx context.Context, agentID agent.AgentID) error {
	_, _ = ctx, agentID
	fake.clearCount.Add(1)
	if fake.clearError != nil {
		return fake.clearError
	}
	return nil
}
func (fake *recordingPendingMintWAL) Drain(ctx context.Context) ([]store.StageRecord, error) {
	_ = ctx
	return nil, nil
}

type recordingActiveSessionStore struct {
	registerCount atomic.Int32
	registerError error

	recordMutex    sync.Mutex
	registeredLast store.ActiveSessionRecord
}

func (fake *recordingActiveSessionStore) Register(ctx context.Context, exchangeID string, record store.ActiveSessionRecord) error {
	_, _ = ctx, exchangeID
	fake.registerCount.Add(1)
	if fake.registerError != nil {
		return fake.registerError
	}
	fake.recordMutex.Lock()
	fake.registeredLast = record
	fake.recordMutex.Unlock()
	return nil
}
func (fake *recordingActiveSessionStore) lastRecord() store.ActiveSessionRecord {
	fake.recordMutex.Lock()
	defer fake.recordMutex.Unlock()
	return fake.registeredLast
}

type programmableScenarioProvider struct {
	effectiveCount         atomic.Int32
	effectiveParamsToReturn *generator.EffectiveParams
	curveEffectiveToReturn  *generator.CurveEffective
}

func (fake *programmableScenarioProvider) Effective(country, scenarioName string) (*generator.EffectiveParams, *generator.CurveEffective) {
	_, _ = country, scenarioName
	fake.effectiveCount.Add(1)
	return fake.effectiveParamsToReturn, fake.curveEffectiveToReturn
}

type recordingSessionRunner struct {
	runCount atomic.Int32
}

func (fake *recordingSessionRunner) Run(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
	curveEffective *generator.CurveEffective,
	lifecycleChannel chan<- session.LifecycleDecision,
) {
	_, _, _, _, _ = ctx, request, population, curveEffective, lifecycleChannel
	fake.runCount.Add(1)
}

// recordingTailFailure is a FailureReporter that only exists to
// let the continueBringUp tests observe FailBringUp without going
// through the orchestrator's logger-only methods. It is substituted
// into the orchestrator via a test-only setter.
type recordingTailFailure struct {
	failCount  atomic.Int32
	readyCount atomic.Int32
	lastCause  atomic.Value
}

func (fake *recordingTailFailure) FailBringUp(request transport.BringUpRequest, cause error) {
	_ = request
	fake.failCount.Add(1)
	fake.lastCause.Store(fmt.Sprintf("%v", cause))
}
func (fake *recordingTailFailure) ReportReady(request transport.BringUpRequest) {
	_ = request
	fake.readyCount.Add(1)
}
