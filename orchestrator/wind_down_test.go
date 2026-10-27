package orchestrator

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"simulation-agents/agent"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/session"
	"simulation-agents/transport"
)

// --- fixture --------------------------------------------------------

// windDownFixture wires an Orchestrator with the stub tail (generator,
// account-open, capital, rng — none of which WindDown touches) plus
// purpose-built recording fakes for the four store surfaces WindDown
// does touch.
type windDownFixture struct {
	orchestrator       *Orchestrator
	agentStore         *recordingWindDownAgentStore
	runtimeStore       *recordingWindDownRuntimeStore
	activeSessionStore *recordingWindDownActiveSessionStore
	leaseAllocator     *recordingWindDownLeaseAllocator
	sequencer          *callSequencer
}

func newWindDownFixture(t *testing.T) *windDownFixture {
	t.Helper()
	logger := discardLogger()
	accountOpen, err := NewAccountOpenClient(newSpyCoreAccountClient(), 10, logger)
	if err != nil {
		t.Fatalf("NewAccountOpenClient: %v", err)
	}
	capital, err := NewCapitalResolver(agent.CapitalParams{DefaultFundedCash: 100_000}, logger)
	if err != nil {
		t.Fatalf("NewCapitalResolver: %v", err)
	}

	orch, err := newOrchestratorWithStubTail(
		buildTestGenerator(t), accountOpen, capital, NewFixedSeedSource(1, 2),
		completeOrchestratorConfig(), logger,
	)
	if err != nil {
		t.Fatalf("newOrchestratorWithStubTail: %v", err)
	}

	sequencer := &callSequencer{}
	fixture := &windDownFixture{
		orchestrator:       orch,
		agentStore:         newRecordingWindDownAgentStore(),
		runtimeStore:       newRecordingWindDownRuntimeStore(sequencer),
		activeSessionStore: newRecordingWindDownActiveSessionStore(sequencer),
		leaseAllocator:     newRecordingWindDownLeaseAllocator(),
		sequencer:          sequencer,
	}
	// Same-package field overwrite: the stub tail above gives a
	// structurally-valid Orchestrator; WindDown tests only care about
	// these four surfaces, so swap exactly those in.
	orch.agentStore = fixture.agentStore
	orch.runtimeStore = fixture.runtimeStore
	orch.activeSessionStore = fixture.activeSessionStore
	orch.leaseAllocator = fixture.leaseAllocator
	return fixture
}

// closedChannelOf builds an already-closed LifecycleDecision channel
// carrying exactly the given decisions — standing in for a per-exchange
// goroutine that has already fully exited and emitted its results.
func closedChannelOf(decisions ...session.LifecycleDecision) <-chan session.LifecycleDecision {
	ch := make(chan session.LifecycleDecision, len(decisions))
	for _, decision := range decisions {
		ch <- decision
	}
	close(ch)
	return ch
}

// --- tests ------------------------------------------------------------

// A second WindDown for the same exchange id must be a clean no-op:
// no panic, no error, and no further store calls (takeHandle finds
// nothing the second time).
func TestWindDown_IdempotentOnSecondCall(t *testing.T) {
	fixture := newWindDownFixture(t)
	var cancelCount int
	fixture.orchestrator.registerHandle("exch-dup", &sessionHandle{
		sessionType: SessionTypeCanonical,
		cancel:      func() { cancelCount++ },
		lifecycleCh: closedChannelOf(),
	})

	if err := fixture.orchestrator.WindDown(context.Background(), "exch-dup", "graceful"); err != nil {
		t.Fatalf("first WindDown: %v", err)
	}
	if cancelCount != 1 {
		t.Fatalf("cancel call count after first WindDown: got %d want 1", cancelCount)
	}
	deregisterCountAfterFirst := fixture.activeSessionStore.deregisterCount()

	if err := fixture.orchestrator.WindDown(context.Background(), "exch-dup", "graceful"); err != nil {
		t.Fatalf("second WindDown: %v", err)
	}
	if cancelCount != 1 {
		t.Fatalf("cancel must not fire again on a repeat WindDown: got %d calls", cancelCount)
	}
	if got := fixture.activeSessionStore.deregisterCount(); got != deregisterCountAfterFirst {
		t.Fatalf("Deregister must not be called again on a repeat WindDown: got %d want %d", got, deregisterCountAfterFirst)
	}
}

// Canonical's drain applies both RETIRE and CAPITAL decisions, and the
// assertions below only run once WindDown has returned — which only
// happens after handle.lifecycleCh has closed, never mid-drain.
func TestWindDown_CanonicalDrainAppliesBothReasons(t *testing.T) {
	fixture := newWindDownFixture(t)
	fixture.orchestrator.registerHandle("exch-canon", &sessionHandle{
		sessionType: SessionTypeCanonical,
		cancel:      func() {},
		lifecycleCh: closedChannelOf(
			session.LifecycleDecision{AgentID: "a-retire", Reason: session.ReasonRetire},
			session.LifecycleDecision{AgentID: "a-fold", Reason: session.ReasonCapital, NewCapital: 42_000},
		),
	})

	if err := fixture.orchestrator.WindDown(context.Background(), "exch-canon", "graceful"); err != nil {
		t.Fatalf("WindDown: %v", err)
	}

	if !fixture.agentStore.wasRetired("a-retire") {
		t.Error("a-retire was not passed to AgentStore.Retire")
	}
	gotCapital, ok := fixture.agentStore.updatedCapitalFor("a-fold")
	if !ok {
		t.Fatal("a-fold was not passed to AgentStore.UpdateTradingCapital")
	}
	if gotCapital != 42_000 {
		t.Errorf("UpdateTradingCapital value: got %d want 42000", gotCapital)
	}
	if fixture.agentStore.wasRetired("a-fold") {
		t.Error("a-fold must not also be retired in the same drain")
	}
	if len(fixture.leaseAllocator.releaseCalls()) != 0 {
		t.Error("canonical WindDown must never call LeaseAllocator.Release")
	}
}

// On-demand's drain is empty (a pure barrier) — no RETIRE/CAPITAL
// writes — but the on-demand-only lease release must still fire,
// returning every agent in the session's roster.
func TestWindDown_OnDemandDrainEmptyThenReleasesLease(t *testing.T) {
	fixture := newWindDownFixture(t)
	roster := Population{Existing: []agent.Agent{{ID: "a-1"}, {ID: "a-2"}}}
	fixture.orchestrator.registerHandle("exch-ondemand", &sessionHandle{
		sessionType: SessionTypeOnDemand,
		country:     "US",
		population:  roster,
		cancel:      func() {},
		lifecycleCh: closedChannelOf(), // empty: on-demand emits nothing
	})

	if err := fixture.orchestrator.WindDown(context.Background(), "exch-ondemand", "graceful"); err != nil {
		t.Fatalf("WindDown: %v", err)
	}

	if len(fixture.agentStore.retiredIDs()) != 0 {
		t.Error("on-demand WindDown must never call AgentStore.Retire")
	}
	if len(fixture.agentStore.updatedCapitals()) != 0 {
		t.Error("on-demand WindDown must never call AgentStore.UpdateTradingCapital")
	}
	releaseCalls := fixture.leaseAllocator.releaseCalls()
	if len(releaseCalls) != 1 {
		t.Fatalf("Release call count: got %d want 1", len(releaseCalls))
	}
	if releaseCalls[0].country != "US" {
		t.Errorf("Release country: got %q want %q", releaseCalls[0].country, "US")
	}
	if len(releaseCalls[0].agentIDs) != 2 {
		t.Fatalf("Release agent id count: got %d want 2", len(releaseCalls[0].agentIDs))
	}
}

// A7 ordering is fixed: RuntimeStore.DropSession must run strictly
// before ActiveSessionStore.Deregister, for both populations.
func TestWindDown_DropSessionRunsBeforeDeregister(t *testing.T) {
	for _, sessionType := range []string{SessionTypeCanonical, SessionTypeOnDemand} {
		t.Run(sessionType, func(t *testing.T) {
			fixture := newWindDownFixture(t)
			fixture.orchestrator.registerHandle("exch-order", &sessionHandle{
				sessionType: sessionType,
				country:     "US",
				cancel:      func() {},
				lifecycleCh: closedChannelOf(),
			})

			if err := fixture.orchestrator.WindDown(context.Background(), "exch-order", "graceful"); err != nil {
				t.Fatalf("WindDown: %v", err)
			}

			dropOrder := fixture.runtimeStore.dropSessionOrder()
			deregisterOrder := fixture.activeSessionStore.deregisterOrder()
			if dropOrder == 0 {
				t.Fatal("DropSession was never called")
			}
			if deregisterOrder == 0 {
				t.Fatal("Deregister was never called")
			}
			if dropOrder >= deregisterOrder {
				t.Fatalf("ordering: DropSession order=%d, Deregister order=%d — DropSession must run strictly first", dropOrder, deregisterOrder)
			}
		})
	}
}

// Regression guard for the continueBringUp spawn change: the handle it
// registers must carry a cancel that actually reaches the spawned
// goroutine, and the lifecycle channel must close once that goroutine
// returns — proving the drain barrier WindDown relies on actually
// works end to end, not just against a hand-built fixture.
func TestContinueBringUp_SpawnedHandleCancelStopsSchedulerAndClosesChannel(t *testing.T) {
	fixture := newTailFixture(t)
	blockingRunner := &blockingSessionRunner{}
	fixture.orchestrator.sessionRunner = blockingRunner

	request := canonicalTailRequest()
	population := Population{Existing: makeAgents(2, "exist")}
	fixture.orchestrator.continueBringUp(context.Background(), request, population)

	waitForCondition(t, "sessionRunner.Run to start", func() bool {
		return blockingRunner.ranCount.Load() > 0
	})

	if err := fixture.orchestrator.WindDown(context.Background(), request.ExchangeID, "graceful"); err != nil {
		t.Fatalf("WindDown: %v", err)
	}

	waitForCondition(t, "the blocking scheduler to observe cancellation", func() bool {
		return blockingRunner.sawCancel.Load()
	})
}

func waitForCondition(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- recording fakes --------------------------------------------------

// callSequencer hands out a strictly increasing call index so ordering
// tests can tell which of two recorded calls happened first without
// relying on wall-clock resolution.
type callSequencer struct {
	mu   sync.Mutex
	next int
}

func (sequencer *callSequencer) take() int {
	sequencer.mu.Lock()
	defer sequencer.mu.Unlock()
	sequencer.next++
	return sequencer.next
}

type recordingWindDownAgentStore struct {
	nopAgentStore
	mu                 sync.Mutex
	recordedRetiredIDs []agent.AgentID
	recordedCapitals   map[agent.AgentID]int64
}

func newRecordingWindDownAgentStore() *recordingWindDownAgentStore {
	return &recordingWindDownAgentStore{recordedCapitals: make(map[agent.AgentID]int64)}
}

func (fake *recordingWindDownAgentStore) Retire(ctx context.Context, agentID agent.AgentID, retiredAt time.Time) error {
	_, _ = ctx, retiredAt
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.recordedRetiredIDs = append(fake.recordedRetiredIDs, agentID)
	return nil
}

func (fake *recordingWindDownAgentStore) UpdateTradingCapital(ctx context.Context, agentID agent.AgentID, value int64) error {
	_ = ctx
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.recordedCapitals[agentID] = value
	return nil
}

func (fake *recordingWindDownAgentStore) wasRetired(agentID agent.AgentID) bool {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, id := range fake.recordedRetiredIDs {
		if id == agentID {
			return true
		}
	}
	return false
}

func (fake *recordingWindDownAgentStore) updatedCapitalFor(agentID agent.AgentID) (int64, bool) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	value, ok := fake.recordedCapitals[agentID]
	return value, ok
}

func (fake *recordingWindDownAgentStore) retiredIDs() []agent.AgentID {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]agent.AgentID(nil), fake.recordedRetiredIDs...)
}

func (fake *recordingWindDownAgentStore) updatedCapitals() map[agent.AgentID]int64 {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	copied := make(map[agent.AgentID]int64, len(fake.recordedCapitals))
	for id, value := range fake.recordedCapitals {
		copied[id] = value
	}
	return copied
}

type recordingWindDownRuntimeStore struct {
	nopRuntimeStore
	sequencer *callSequencer
	mu        sync.Mutex
	dropOrder int
}

func newRecordingWindDownRuntimeStore(sequencer *callSequencer) *recordingWindDownRuntimeStore {
	return &recordingWindDownRuntimeStore{sequencer: sequencer}
}

func (fake *recordingWindDownRuntimeStore) DropSession(ctx context.Context, exchangeID string) error {
	_, _ = ctx, exchangeID
	order := fake.sequencer.take()
	fake.mu.Lock()
	fake.dropOrder = order
	fake.mu.Unlock()
	return nil
}

func (fake *recordingWindDownRuntimeStore) dropSessionOrder() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.dropOrder
}

type recordingWindDownActiveSessionStore struct {
	nopActiveSessionStore
	sequencer     *callSequencer
	mu            sync.Mutex
	deregisterOrd int
	deregisterCnt int
}

func newRecordingWindDownActiveSessionStore(sequencer *callSequencer) *recordingWindDownActiveSessionStore {
	return &recordingWindDownActiveSessionStore{sequencer: sequencer}
}

func (fake *recordingWindDownActiveSessionStore) Deregister(ctx context.Context, exchangeID string) error {
	_ = ctx
	_ = exchangeID
	order := fake.sequencer.take()
	fake.mu.Lock()
	fake.deregisterOrd = order
	fake.deregisterCnt++
	fake.mu.Unlock()
	return nil
}

func (fake *recordingWindDownActiveSessionStore) deregisterOrder() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.deregisterOrd
}

func (fake *recordingWindDownActiveSessionStore) deregisterCount() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.deregisterCnt
}

type recordingWindDownLeaseAllocator struct {
	nopLeaseAllocator
	mu    sync.Mutex
	calls []windDownReleaseCall
}

type windDownReleaseCall struct {
	country  string
	agentIDs []agent.AgentID
}

func newRecordingWindDownLeaseAllocator() *recordingWindDownLeaseAllocator {
	return &recordingWindDownLeaseAllocator{}
}

func (fake *recordingWindDownLeaseAllocator) Release(ctx context.Context, country string, agentIDs []agent.AgentID) error {
	_ = ctx
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, windDownReleaseCall{country: country, agentIDs: agentIDs})
	return nil
}

func (fake *recordingWindDownLeaseAllocator) releaseCalls() []windDownReleaseCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]windDownReleaseCall(nil), fake.calls...)
}

// blockingSessionRunner stands in for a live per-exchange goroutine: it
// blocks on ctx.Done() exactly like the real scheduler's tick loop
// would, so a test can prove WindDown's cancel actually reaches and
// stops it.
type blockingSessionRunner struct {
	ranCount  atomic.Int32
	sawCancel atomic.Bool
}

func (fake *blockingSessionRunner) Run(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
	curveEffective *generator.CurveEffective,
	lifecycleChannel chan<- session.LifecycleDecision,
) {
	_, _, _ = request, population, curveEffective
	fake.ranCount.Add(1)
	<-ctx.Done()
	fake.sawCancel.Store(true)
}
