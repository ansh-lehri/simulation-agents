package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/transport"
)

// TestLoadSessionsHappyCanonical: every agent installs successfully
// against JoinCanonical. The op returns the lowest-indexed agent's
// session id as the feed id, calls MarkSessionLoadFailed zero
// times, and reports a nil error. If any of these drifts the live
// bring-up would either pick the wrong feed id or treat a healthy
// session as degraded; both are visible-at-review failures.
func TestLoadSessionsHappyCanonical(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClient()
	fakeRuntime := newRecordingRuntimeStore()
	runner := loadSessionsTestRunner(t, fakeCore, fakeRuntime)
	population := makeOrderedPopulation(2, 3)

	feedSessionID, err := runner.loadSessions(context.Background(), canonicalBringUpRequest(), population)
	if err != nil {
		t.Fatalf("loadSessions: %v", err)
	}
	if feedSessionID != "sess-"+string(population.Existing[0].ID) {
		t.Fatalf("feed session id = %q, want lowest-indexed agent's session id %q",
			feedSessionID, "sess-"+string(population.Existing[0].ID))
	}
	if calls := fakeRuntime.markFailedCount.Load(); calls != 0 {
		t.Fatalf("MarkSessionLoadFailed must not be called on a happy path; got %d calls", calls)
	}
	if calls := fakeCore.joinCanonicalCount.Load(); int(calls) != len(population.All()) {
		t.Fatalf("JoinCanonical called %d times, want %d", calls, len(population.All()))
	}
}

// TestLoadSessionsHappyOnDemand: an on-demand bring-up dispatches every
// install through JoinOnDemand (keyed by the request's ExchangeID), not
// JoinCanonical — the per-agent join must attach to the one already-live
// on-demand market, never spin up a new one.
func TestLoadSessionsHappyOnDemand(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClient()
	fakeRuntime := newRecordingRuntimeStore()
	runner := loadSessionsTestRunner(t, fakeCore, fakeRuntime)
	population := makeOrderedPopulation(2, 3)

	feedSessionID, err := runner.loadSessions(context.Background(), onDemandBringUpRequest(), population)
	if err != nil {
		t.Fatalf("loadSessions: %v", err)
	}
	if feedSessionID != "sess-"+string(population.Existing[0].ID) {
		t.Fatalf("feed session id = %q, want lowest-indexed agent's session id %q",
			feedSessionID, "sess-"+string(population.Existing[0].ID))
	}
	if calls := fakeCore.joinOnDemandCount.Load(); int(calls) != len(population.All()) {
		t.Fatalf("JoinOnDemand called %d times, want %d", calls, len(population.All()))
	}
	if calls := fakeCore.joinCanonicalCount.Load(); calls != 0 {
		t.Fatalf("JoinCanonical called %d times, want 0 (on-demand must never use the canonical door)", calls)
	}
}

// TestLoadSessionsPartialFailureProceeds: a mix of successful and
// failing installs. The failing agents are marked via Redis, the
// successful ones install, the feed id is still the lowest-indexed
// SUCCESSFUL agent's session id (which may skip over failed earlier
// agents), and the op returns nil error.
func TestLoadSessionsPartialFailureProceeds(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClient()
	fakeRuntime := newRecordingRuntimeStore()
	runner := loadSessionsTestRunner(t, fakeCore, fakeRuntime)
	population := makeOrderedPopulation(2, 3)
	allAgents := population.All()

	// Fail the first two agents; the feed id must then come from
	// the third (lowest-indexed success).
	fakeCore.canonicalError[string(allAgents[0].ID)] = errors.New("transport blip")
	fakeCore.canonicalError[string(allAgents[1].ID)] = errors.New("transport blip")

	feedSessionID, err := runner.loadSessions(context.Background(), canonicalBringUpRequest(), population)
	if err != nil {
		t.Fatalf("loadSessions: %v", err)
	}
	expectedFeedSessionID := "sess-" + string(allAgents[2].ID)
	if feedSessionID != expectedFeedSessionID {
		t.Fatalf("feed session id = %q, want %q (lowest-indexed success)", feedSessionID, expectedFeedSessionID)
	}
	if marks := fakeRuntime.markFailedCount.Load(); marks != 2 {
		t.Fatalf("MarkSessionLoadFailed called %d times, want 2", marks)
	}
	if _, markedFirst := fakeRuntime.markFailedCalls.Load(allAgents[0].ID); !markedFirst {
		t.Fatalf("first failing agent must be marked")
	}
	if _, markedSecond := fakeRuntime.markFailedCalls.Load(allAgents[1].ID); !markedSecond {
		t.Fatalf("second failing agent must be marked")
	}
}

// TestLoadSessionsTotalFailureIsFatal: every install fails. The op
// returns a non-nil error, the feed session id is empty, and every
// agent is still marked (so the Tier-6 cleanup pass sees them).
func TestLoadSessionsTotalFailureIsFatal(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClient()
	fakeRuntime := newRecordingRuntimeStore()
	runner := loadSessionsTestRunner(t, fakeCore, fakeRuntime)
	population := makeOrderedPopulation(1, 2)
	allAgents := population.All()

	for _, agentRecord := range allAgents {
		fakeCore.canonicalError[string(agentRecord.ID)] = errors.New("every one failed")
	}

	feedSessionID, err := runner.loadSessions(context.Background(), canonicalBringUpRequest(), population)
	if err == nil {
		t.Fatalf("expected fatal error on total install failure; got nil")
	}
	if feedSessionID != "" {
		t.Fatalf("feed session id must be empty on total failure; got %q", feedSessionID)
	}
	if !strings.Contains(err.Error(), "all") {
		t.Fatalf("error must mention the all-failed condition; got %q", err.Error())
	}
	if marks := fakeRuntime.markFailedCount.Load(); int(marks) != len(allAgents) {
		t.Fatalf("every agent must be marked on total failure; got %d marks for %d agents",
			marks, len(allAgents))
	}
}

// TestLoadSessionsEmptyPopulationIsFatal: an empty population is
// an upstream bug (generator should always produce at least one
// agent). Treat as fatal rather than return an empty feed id that
// the Subscribe step would then reject with its own cryptic error.
func TestLoadSessionsEmptyPopulationIsFatal(t *testing.T) {
	t.Parallel()

	runner := loadSessionsTestRunner(t, newProgrammableCoreClient(), newRecordingRuntimeStore())
	_, err := runner.loadSessions(context.Background(), canonicalBringUpRequest(), generator.Population{})
	if err == nil {
		t.Fatalf("expected fatal error on empty population; got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error must mention empty population; got %q", err.Error())
	}
}

// TestLoadSessionsConcurrencyIsBounded: the fan-out must not exceed
// Params.LoadSessionsConcurrency at any instant. The fake records
// the peak concurrent in-flight call count via an atomic counter;
// a sync-once-guarded gate holds goroutines until the test releases
// them, so the counter stays saturated long enough to observe.
func TestLoadSessionsConcurrencyIsBounded(t *testing.T) {
	t.Parallel()

	gate := make(chan struct{})
	var gateCloseOnce sync.Once
	closeGate := func() { gateCloseOnce.Do(func() { close(gate) }) }
	t.Cleanup(closeGate)

	var inFlight atomic.Int32
	var peakInFlight atomic.Int32

	fakeCore := newProgrammableCoreClient()
	fakeCore.beforeJoinCanonical = func() {
		current := inFlight.Add(1)
		for {
			observedPeak := peakInFlight.Load()
			if current <= observedPeak {
				break
			}
			if peakInFlight.CompareAndSwap(observedPeak, current) {
				break
			}
		}
		<-gate
		inFlight.Add(-1)
	}
	fakeRuntime := newRecordingRuntimeStore()
	deps := baseLoadSessionsDependencies(fakeCore, fakeRuntime)
	deps.Params.LoadSessionsConcurrency = 3

	runner := testRunnerWithDependencies(t, deps)
	population := makeOrderedPopulation(5, 5)

	done := make(chan struct{})
	go func() {
		_, err := runner.loadSessions(context.Background(), canonicalBringUpRequest(), population)
		if err != nil {
			t.Errorf("loadSessions: %v", err)
		}
		close(done)
	}()

	// Give the fan-out a moment to saturate the semaphore, then
	// release the gate so the goroutines finish.
	waitUntilAtLeast(&inFlight, 3, time.Second)
	closeGate()

	<-done
	if peakInFlight.Load() > 3 {
		t.Fatalf("peak in-flight = %d, must not exceed LoadSessionsConcurrency=3", peakInFlight.Load())
	}
}

// TestLoadSessionsAbortsOnContextCancel: once ctx is cancelled, the
// op returns ctx.Err() wrapped — it does NOT run the mark pass for
// goroutines that returned a cancelled error. A terminated bring-up
// should not leave markers behind for installs it never cared about.
func TestLoadSessionsAbortsOnContextCancel(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClient()
	fakeCore.beforeJoinCanonical = func() {
		// Hold long enough for cancel to land before we return.
		time.Sleep(20 * time.Millisecond)
	}
	fakeRuntime := newRecordingRuntimeStore()
	runner := loadSessionsTestRunner(t, fakeCore, fakeRuntime)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.loadSessions(ctx, canonicalBringUpRequest(), makeOrderedPopulation(1, 2))
	if err == nil {
		t.Fatalf("expected error on cancelled context; got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error must wrap context.Canceled; got %v", err)
	}
}

// --- fixtures and fakes

func loadSessionsTestRunner(t *testing.T, fakeCore *programmableCoreClient, fakeRuntime *recordingRuntimeStore) *Runner {
	t.Helper()
	return testRunnerWithDependencies(t, baseLoadSessionsDependencies(fakeCore, fakeRuntime))
}

func testRunnerWithDependencies(t *testing.T, deps Dependencies) *Runner {
	t.Helper()
	runner, err := NewRunner(deps)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

func baseLoadSessionsDependencies(coreClient CoreClient, runtime RuntimeStore) Dependencies {
	return Dependencies{
		Core:       coreClient,
		Runtime:    runtime,
		MarketData: stubMarketDataStore{},
		Scheduler:  stubScheduler{},
		Failure:    stubFailureReporter{},
		Clock:      stubClock{},
		RNGSource:  stubRNGSource{},
		Params: Params{
			EntryCurve:              DefaultEntryCurveParams(),
			LoadSessionsConcurrency: 4,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func canonicalBringUpRequest() transport.BringUpRequest {
	return transport.BringUpRequest{
		ExchangeID:   "exch_canon",
		SessionType:  agent.PopulationCanonical,
		Country:      "IN",
		SessionStart: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC),
		SessionEnd:   time.Date(2026, 10, 20, 10, 30, 0, 0, time.UTC),
		Instruments:  []string{"stock1"},
	}
}

func onDemandBringUpRequest() transport.BringUpRequest {
	return transport.BringUpRequest{
		ExchangeID:   "od:exch_1",
		SessionType:  agent.PopulationOnDemand,
		Country:      "IN",
		SessionStart: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC),
		SessionEnd:   time.Date(2026, 10, 20, 10, 30, 0, 0, time.UTC),
		Instruments:  []string{"stock1"},
	}
}

func makeOrderedPopulation(existingCount, newCount int) generator.Population {
	population := generator.Population{}
	for existingIndex := 0; existingIndex < existingCount; existingIndex++ {
		population.Existing = append(population.Existing, agent.Agent{
			ID: agent.AgentID(fmt.Sprintf("agent-ex-%03d", existingIndex)),
		})
	}
	for newIndex := 0; newIndex < newCount; newIndex++ {
		population.New = append(population.New, agent.Agent{
			ID: agent.AgentID(fmt.Sprintf("agent-nw-%03d", newIndex)),
		})
	}
	return population
}

// programmableCoreClient lets each test program per-agent responses
// and per-agent errors for the canonical session-join call. Methods
// that are not used by loadSessions are present only to satisfy the
// CoreClient interface and return the zero value.
type programmableCoreClient struct {
	canonicalResponse map[string]core.CanonicalSessionJoin
	canonicalError    map[string]error

	// beforeJoinCanonical runs synchronously at the top of
	// JoinCanonical. Tests use it to drive concurrency peaks and
	// context-cancel timing.
	beforeJoinCanonical func()

	joinCanonicalCount atomic.Int32
	joinOnDemandCount  atomic.Int32
}

func newProgrammableCoreClient() *programmableCoreClient {
	return &programmableCoreClient{
		canonicalResponse: make(map[string]core.CanonicalSessionJoin),
		canonicalError:    make(map[string]error),
	}
}

func (fake *programmableCoreClient) JoinCanonical(ctx context.Context, agentUserID, marketTag string) (core.CanonicalSessionJoin, error) {
	_ = ctx
	_ = marketTag
	fake.joinCanonicalCount.Add(1)
	if fake.beforeJoinCanonical != nil {
		fake.beforeJoinCanonical()
	}
	if err, programmed := fake.canonicalError[agentUserID]; programmed {
		return core.CanonicalSessionJoin{}, err
	}
	if response, programmed := fake.canonicalResponse[agentUserID]; programmed {
		return response, nil
	}
	return core.CanonicalSessionJoin{SessionID: "sess-" + agentUserID, Status: "active"}, nil
}

func (fake *programmableCoreClient) JoinOnDemand(ctx context.Context, agentUserID, exchangeID string) (core.OnDemandSessionJoin, error) {
	_ = ctx
	_ = exchangeID
	fake.joinOnDemandCount.Add(1)
	if err, programmed := fake.canonicalError[agentUserID]; programmed {
		return core.OnDemandSessionJoin{}, err
	}
	if response, programmed := fake.canonicalResponse[agentUserID]; programmed {
		return core.OnDemandSessionJoin{SessionID: response.SessionID, ParticipantID: response.ParticipantID, Status: response.Status}, nil
	}
	return core.OnDemandSessionJoin{SessionID: "sess-" + agentUserID, Status: "active"}, nil
}

func (fake *programmableCoreClient) GetCandles(ctx context.Context, request core.CandlesRequest) (core.CandlesPage, error) {
	_ = ctx
	_ = request
	return core.CandlesPage{}, nil
}

func (fake *programmableCoreClient) Subscribe(
	ctx context.Context,
	sessionID string,
	subscriptions []core.MarketDataSubscription,
) (WSConnection, core.ConnectedFrame, error) {
	_ = ctx
	_ = sessionID
	_ = subscriptions
	return nil, core.ConnectedFrame{}, nil
}

func (fake *programmableCoreClient) GetObservation(_ context.Context, _ string) (core.AccountObservation, error) {
	return core.AccountObservation{}, nil
}

func (fake *programmableCoreClient) PunchOrder(_ context.Context, _ core.OrderPunchRequest) error {
	return nil
}

func (fake *programmableCoreClient) GetAccountCash(_ context.Context, _ string) (core.AccountCash, error) {
	return core.AccountCash{}, nil
}

// recordingRuntimeStore captures MarkSessionLoadFailed calls and
// counts them. All the other RuntimeStore methods delegate to the
// zero-value stub so this fake does not have to re-implement them.
type recordingRuntimeStore struct {
	stubRuntimeStore
	markFailedCalls sync.Map
	markFailedCount atomic.Int32
	markFailedError error
}

func newRecordingRuntimeStore() *recordingRuntimeStore {
	return &recordingRuntimeStore{}
}

func (fake *recordingRuntimeStore) MarkSessionLoadFailed(ctx context.Context, exchangeID string, agentID agent.AgentID, reason string) error {
	_ = ctx
	_ = exchangeID
	fake.markFailedCount.Add(1)
	if fake.markFailedError != nil {
		return fake.markFailedError
	}
	fake.markFailedCalls.Store(agentID, reason)
	return nil
}

// --- small helpers for the concurrency test

func waitUntilAtLeast(counter *atomic.Int32, target int32, maximum time.Duration) {
	deadline := time.Now().Add(maximum)
	for time.Now().Before(deadline) {
		if counter.Load() >= target {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}
