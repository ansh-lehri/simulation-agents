package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/store"
	"simulation-agents/transport"
)

// TestRunHappyPath pins the full-happy-path contract: entry
// fractions stamped for every agent, scheduler handed off exactly
// once with the correct arguments, ReportReady called, FailBringUp
// never called, agents still DORMANT (no SetPresence(ACTIVE)
// writes during Run).
func TestRunHappyPath(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t)
	population := makeOrderedPopulation(2, 3)
	request := runHappyBringUpRequest([]string{"stock1"})
	lifecycleChannel := make(chan LifecycleDecision, 1)

	fixture.clock.release()
	fixture.runner.Run(context.Background(), request, population, nil, lifecycleChannel)

	if fixture.failure.failCount.Load() != 0 {
		cause := fixture.failure.lastCause.Load()
		t.Fatalf("FailBringUp must not be called on the happy path; got %d calls (last cause=%v)",
			fixture.failure.failCount.Load(), cause)
	}
	if fixture.failure.readyCount.Load() != 1 {
		t.Fatalf("ReportReady should be called exactly once; got %d", fixture.failure.readyCount.Load())
	}
	if fixture.scheduler.callCount.Load() != 1 {
		t.Fatalf("StartScheduler should be called exactly once; got %d", fixture.scheduler.callCount.Load())
	}
	if fixture.scheduler.observedExchangeID != request.ExchangeID {
		t.Fatalf("StartScheduler ExchangeID = %q, want %q",
			fixture.scheduler.observedExchangeID, request.ExchangeID)
	}
	if int(fixture.runtime.stampCount.Load()) != len(population.All()) {
		t.Fatalf("StampEntryTime count = %d, want %d (one per agent)",
			fixture.runtime.stampCount.Load(), len(population.All()))
	}
	if presenceCount := fixture.runtime.setPresenceCount.Load(); presenceCount != 0 {
		t.Fatalf("agents must stay DORMANT through Run; SetPresence called %d times", presenceCount)
	}
}

// TestRunStartsSchedulerOnlyAfterWaitUntilReturns: the scheduler
// is NOT started before the Clock.WaitUntil returns. The
// controllable clock holds Run inside WaitUntil until the test
// explicitly releases it; before release, the scheduler call
// count must be zero.
func TestRunStartsSchedulerOnlyAfterWaitUntilReturns(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t)
	population := makeOrderedPopulation(1, 1)
	request := runHappyBringUpRequest([]string{"stock1"})

	runReturned := make(chan struct{})
	go func() {
		fixture.runner.Run(context.Background(), request, population, nil, make(chan LifecycleDecision, 1))
		close(runReturned)
	}()

	// Wait until Run has reached WaitUntil (fixture.clock records
	// the entry). The scheduler must not have been called yet.
	select {
	case <-fixture.clock.waitUntilEntered:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not reach WaitUntil within 2s")
	}
	if calls := fixture.scheduler.callCount.Load(); calls != 0 {
		t.Fatalf("StartScheduler must not be called before WaitUntil returns; got %d calls", calls)
	}

	// Release the clock; Run should now call the scheduler and
	// ReportReady, then return.
	fixture.clock.release()
	select {
	case <-runReturned:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not complete within 2s after clock release")
	}
	if calls := fixture.scheduler.callCount.Load(); calls != 1 {
		t.Fatalf("StartScheduler should be called exactly once after clock release; got %d", calls)
	}
	if calls := fixture.failure.readyCount.Load(); calls != 1 {
		t.Fatalf("ReportReady should be called exactly once; got %d", calls)
	}
}

// TestRunFatalWhenAllAgentsFailFeedSearch: every agent install
// fails during installFeedAgent → FailBringUp is called, the
// three concurrent ops are never kicked off, and the scheduler is
// never reached.
func TestRunFatalWhenAllAgentsFailFeedSearch(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t)
	fixture.core.failEveryJoinCanonical = true
	population := makeOrderedPopulation(1, 2)
	request := runHappyBringUpRequest([]string{"stock1"})

	fixture.clock.release()
	fixture.runner.Run(context.Background(), request, population, nil, make(chan LifecycleDecision, 1))

	if fixture.failure.failCount.Load() != 1 {
		t.Fatalf("FailBringUp should be called exactly once; got %d", fixture.failure.failCount.Load())
	}
	if fixture.scheduler.callCount.Load() != 0 {
		t.Fatalf("StartScheduler must not be called on feed-search failure")
	}
	if fixture.failure.readyCount.Load() != 0 {
		t.Fatalf("ReportReady must not be called on feed-search failure")
	}
	// pullHistory and subscribeLive must not have been called either —
	// the errgroup never ran.
	if fixture.core.getCandlesCount.Load() != 0 {
		t.Fatalf("GetCandles must not be hit after feed-search failure; got %d calls", fixture.core.getCandlesCount.Load())
	}
	if fixture.core.subscribeCount.Load() != 0 {
		t.Fatalf("Subscribe must not be hit after feed-search failure; got %d calls", fixture.core.subscribeCount.Load())
	}
}

// TestRunFatalWhenPullHistoryFails: pullHistory inside the
// errgroup returns an error. FailBringUp is called; the scheduler
// is never reached.
func TestRunFatalWhenPullHistoryFails(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t)
	fixture.core.getCandlesError = errors.New("history fetch failed")
	population := makeOrderedPopulation(1, 1)
	request := runHappyBringUpRequest([]string{"stock1"})

	fixture.clock.release()
	fixture.runner.Run(context.Background(), request, population, nil, make(chan LifecycleDecision, 1))

	if fixture.failure.failCount.Load() != 1 {
		t.Fatalf("FailBringUp should be called exactly once on pullHistory failure; got %d",
			fixture.failure.failCount.Load())
	}
	if fixture.scheduler.callCount.Load() != 0 {
		t.Fatalf("StartScheduler must not be called when pullHistory fails")
	}
	if fixture.failure.readyCount.Load() != 0 {
		t.Fatalf("ReportReady must not be called when pullHistory fails")
	}
}

// TestRunFatalWhenSubscribeLiveFails: subscribeLive inside the
// errgroup returns an error. Same fatal path as pullHistory.
func TestRunFatalWhenSubscribeLiveFails(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t)
	fixture.core.subscribeError = errors.New("ws dial failed")
	population := makeOrderedPopulation(1, 1)
	request := runHappyBringUpRequest([]string{"stock1"})

	fixture.clock.release()
	fixture.runner.Run(context.Background(), request, population, nil, make(chan LifecycleDecision, 1))

	if fixture.failure.failCount.Load() != 1 {
		t.Fatalf("FailBringUp should be called exactly once on subscribeLive failure; got %d",
			fixture.failure.failCount.Load())
	}
	if fixture.scheduler.callCount.Load() != 0 {
		t.Fatalf("StartScheduler must not be called when subscribeLive fails")
	}
}

// TestRunFailsOnEmptyPopulation: an empty population is an
// upstream bug (generator must produce ≥1 agent). Run surfaces
// it as a FailBringUp rather than letting it leak into
// installFeedAgent's "all failed" diagnostic, since there would
// be no agents to attribute the failure to.
func TestRunFailsOnEmptyPopulation(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t)
	fixture.clock.release()
	fixture.runner.Run(context.Background(), runHappyBringUpRequest([]string{"stock1"}), generator.Population{}, nil, make(chan LifecycleDecision, 1))

	if fixture.failure.failCount.Load() != 1 {
		t.Fatalf("FailBringUp should be called once on empty population; got %d", fixture.failure.failCount.Load())
	}
	if fixture.scheduler.callCount.Load() != 0 {
		t.Fatalf("StartScheduler must not be called on empty population")
	}
}

// TestRunPreOpenOpsRunConcurrently: with a wall-clock budget on
// each spied op, the elapsed time of Run on a happy path should
// be close to the duration of ONE op (not the sum of three). The
// margin is generous so a loaded test runner does not flake, but
// a serialised implementation (300ms sum) would clearly exceed
// the ceiling.
func TestRunPreOpenOpsRunConcurrently(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t)
	// Each parallel op sleeps for perOpBlockDuration before
	// completing. The feed-agent install is also delayed, but it
	// is strictly serial before the errgroup, so it is excluded
	// from the parallel budget.
	const perOpBlockDuration = 150 * time.Millisecond
	fixture.core.installRemainingDelay = perOpBlockDuration
	fixture.core.getCandlesDelay = perOpBlockDuration
	fixture.core.subscribeDelay = perOpBlockDuration

	population := makeOrderedPopulation(1, 1)
	request := runHappyBringUpRequest([]string{"stock1"})
	fixture.clock.release()

	startedAt := time.Now()
	fixture.runner.Run(context.Background(), request, population, nil, make(chan LifecycleDecision, 1))
	elapsed := time.Since(startedAt)

	// Parallel budget: installFeedAgent (one JoinCanonical, no
	// extra delay) + max(parallel ops) ≈ perOpBlockDuration + slack.
	// Serial budget would be 3 × perOpBlockDuration = 450ms minimum.
	ceiling := 2*perOpBlockDuration + 100*time.Millisecond
	if elapsed > ceiling {
		t.Fatalf("Run took %v; the three pre-open ops should run concurrently (ceiling %v)",
			elapsed, ceiling)
	}
	if fixture.failure.failCount.Load() != 0 {
		t.Fatalf("unexpected FailBringUp on happy path")
	}
}

// TestRunStampsEveryAgentBeforeAnyCoreCall: at the moment the
// first JoinCanonical call lands, every agent must already have
// an entry fraction in Redis. Stamp-then-install is the ordering
// Run commits to.
func TestRunStampsEveryAgentBeforeAnyCoreCall(t *testing.T) {
	t.Parallel()

	fixture := newRunFixture(t)
	population := makeOrderedPopulation(2, 3)
	// Capture the StampEntryTime count at the moment the first
	// JoinCanonical is observed. If stamping is not strictly
	// before the install, this snapshot will be less than N.
	var stampCountAtFirstJoin atomic.Int32
	var firstJoinSeen atomic.Bool
	fixture.core.beforeJoinCanonical = func() {
		if firstJoinSeen.CompareAndSwap(false, true) {
			stampCountAtFirstJoin.Store(fixture.runtime.stampCount.Load())
		}
	}

	fixture.clock.release()
	fixture.runner.Run(context.Background(), runHappyBringUpRequest([]string{"stock1"}), population, nil, make(chan LifecycleDecision, 1))

	if fixture.failure.failCount.Load() != 0 {
		t.Fatalf("unexpected FailBringUp")
	}
	if observed := int(stampCountAtFirstJoin.Load()); observed != len(population.All()) {
		t.Fatalf(
			"StampEntryTime count at first JoinCanonical = %d, want %d (every agent stamped before any install)",
			observed, len(population.All()),
		)
	}
}

// --- fixture

// runFixture bundles the recording spies the Run-body tests
// consult. Each test calls newRunFixture to get a fully-wired
// Runner + handles on the spies.
type runFixture struct {
	runner    *Runner
	core      *recordingRunCore
	runtime   *recordingRunRuntime
	scheduler *recordingRunScheduler
	failure   *recordingRunFailure
	clock     *controllableClock
}

func newRunFixture(t *testing.T) *runFixture {
	t.Helper()
	core := newRecordingRunCore()
	runtime := newRecordingRunRuntime()
	scheduler := newRecordingRunScheduler()
	failure := newRecordingRunFailure()
	clock := newControllableClock()
	runner, err := NewRunner(Dependencies{
		Core:       core,
		Runtime:    runtime,
		MarketData: newRecordingMarketDataStore(),
		Scheduler:  scheduler,
		Failure:    failure,
		Clock:      clock,
		RNGSource:  stubRNGSource{},
		Params: Params{
			EntryCurve:              DefaultEntryCurveParams(),
			LoadSessionsConcurrency: 4,
			HistoryLookback:         60 * time.Minute,
			HistoryTimeframes:       []string{core.timeframe()},
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return &runFixture{
		runner:    runner,
		core:      core,
		runtime:   runtime,
		scheduler: scheduler,
		failure:   failure,
		clock:     clock,
	}
}

func runHappyBringUpRequest(instruments []string) transport.BringUpRequest {
	return transport.BringUpRequest{
		ExchangeID:   "exch_live",
		SessionType:  agent.PopulationCanonical,
		Country:      "IN",
		SessionStart: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC),
		SessionEnd:   time.Date(2026, 10, 20, 10, 30, 0, 0, time.UTC),
		Instruments:  instruments,
	}
}

// --- recording spies

// recordingRunCore is a CoreClient that supports every happy op
// Run exercises: JoinCanonical, GetCandles, Subscribe. Each method
// has programmable error knobs and optional per-call delays so a
// test can block them to measure wall-clock concurrency. The
// shape is distinct from the Stage-C programmableCoreClient
// because this one needs to serve all four knobs at once.
type recordingRunCore struct {
	joinCanonicalCount atomic.Int32
	getCandlesCount    atomic.Int32
	subscribeCount     atomic.Int32

	beforeJoinCanonical func() // optional hook

	failEveryJoinCanonical bool
	getCandlesError        error
	subscribeError         error

	installRemainingDelay time.Duration
	getCandlesDelay       time.Duration
	subscribeDelay        time.Duration

	connectionsMutex sync.Mutex
	connections      []*fakeWSConnection
}

func newRecordingRunCore() *recordingRunCore {
	return &recordingRunCore{}
}

// timeframe is the single configured timeframe this fixture uses.
// Making it a method keeps the string in one place and lets the
// fake return matching candle pages below.
func (fake *recordingRunCore) timeframe() string {
	return core.TimeframeOneMinute
}

func (fake *recordingRunCore) JoinCanonical(ctx context.Context, agentUserID, marketTag string) (core.CanonicalSessionJoin, error) {
	_ = ctx
	_ = marketTag
	callOrdinal := fake.joinCanonicalCount.Add(1)
	if fake.beforeJoinCanonical != nil {
		fake.beforeJoinCanonical()
	}
	if fake.failEveryJoinCanonical {
		return core.CanonicalSessionJoin{}, errors.New("install failed (test)")
	}
	// Only the parallel-remaining calls carry the install delay;
	// the first call (feed-agent search) returns promptly so the
	// pre-open latency floor stays low.
	if callOrdinal > 1 && fake.installRemainingDelay > 0 {
		select {
		case <-time.After(fake.installRemainingDelay):
		case <-ctx.Done():
			return core.CanonicalSessionJoin{}, ctx.Err()
		}
	}
	return core.CanonicalSessionJoin{SessionID: "sess-" + agentUserID, Status: "active"}, nil
}

func (fake *recordingRunCore) GetCandles(ctx context.Context, request core.CandlesRequest) (core.CandlesPage, error) {
	_ = request
	fake.getCandlesCount.Add(1)
	if fake.getCandlesError != nil {
		return core.CandlesPage{}, fake.getCandlesError
	}
	if fake.getCandlesDelay > 0 {
		select {
		case <-time.After(fake.getCandlesDelay):
		case <-ctx.Done():
			return core.CandlesPage{}, ctx.Err()
		}
	}
	return core.CandlesPage{
		Timezone: "UTC",
		Candles: []core.Candle{{
			StartNaiveLocal: "2026-10-20T09:30:00",
			EndNaiveLocal:   "2026-10-20T09:31:00",
			Open:            100, High: 101, Low: 99, Close: 100, Volume: 1000,
		}},
	}, nil
}

func (fake *recordingRunCore) Subscribe(
	ctx context.Context,
	sessionID string,
	subscriptions []core.MarketDataSubscription,
) (WSConnection, core.ConnectedFrame, error) {
	_ = sessionID
	_ = subscriptions
	fake.subscribeCount.Add(1)
	if fake.subscribeError != nil {
		return nil, core.ConnectedFrame{}, fake.subscribeError
	}
	if fake.subscribeDelay > 0 {
		select {
		case <-time.After(fake.subscribeDelay):
		case <-ctx.Done():
			return nil, core.ConnectedFrame{}, ctx.Err()
		}
	}
	connection := newFakeWSConnection()
	fake.connectionsMutex.Lock()
	fake.connections = append(fake.connections, connection)
	fake.connectionsMutex.Unlock()
	return connection, core.ConnectedFrame{Market: "IN", Timezone: "UTC"}, nil
}

func (fake *recordingRunCore) GetObservation(_ context.Context, _ string) (core.AccountObservation, error) {
	return core.AccountObservation{}, nil
}

func (fake *recordingRunCore) PunchOrder(_ context.Context, _ core.OrderPunchRequest) error {
	return nil
}

// recordingRunRuntime captures StampEntryTime, SetPresence, and
// MarkSessionLoadFailed call counts so the Run-body tests can
// assert the ordering + DORMANT invariant.
type recordingRunRuntime struct {
	stampCount         atomic.Int32
	setPresenceCount   atomic.Int32
	markFailedCount    atomic.Int32
}

func newRecordingRunRuntime() *recordingRunRuntime {
	return &recordingRunRuntime{}
}

func (fake *recordingRunRuntime) WriteInitialSnapshot(ctx context.Context, exchangeID string, agentRecord agent.Agent) error {
	_ = ctx
	_ = exchangeID
	_ = agentRecord
	return nil
}

func (fake *recordingRunRuntime) SetPresence(ctx context.Context, exchangeID string, agentID agent.AgentID, presence string) error {
	_ = ctx
	_ = exchangeID
	_ = agentID
	_ = presence
	fake.setPresenceCount.Add(1)
	return nil
}

func (fake *recordingRunRuntime) SetTimers(ctx context.Context, exchangeID string, agentID agent.AgentID, nextCheckTime, idleSince int64) error {
	_ = ctx
	_ = exchangeID
	_ = agentID
	_ = nextCheckTime
	_ = idleSince
	return nil
}

func (fake *recordingRunRuntime) StampEntryTime(ctx context.Context, exchangeID string, agentID agent.AgentID, entryTime float64) error {
	_ = ctx
	_ = exchangeID
	_ = agentID
	_ = entryTime
	fake.stampCount.Add(1)
	return nil
}

func (fake *recordingRunRuntime) SetTipper(ctx context.Context, exchangeID string, agentID agent.AgentID) error {
	_ = ctx
	_ = exchangeID
	_ = agentID
	return nil
}

func (fake *recordingRunRuntime) MarkSessionLoadFailed(ctx context.Context, exchangeID string, agentID agent.AgentID, reason string) error {
	_ = ctx
	_ = exchangeID
	_ = agentID
	_ = reason
	fake.markFailedCount.Add(1)
	return nil
}

func (fake *recordingRunRuntime) SetStandingOpinion(_ context.Context, _ string, _ agent.AgentID, _ float64, _ string) error {
	return nil
}
func (fake *recordingRunRuntime) SetWaitStreak(_ context.Context, _ string, _ agent.AgentID, _ int) error {
	return nil
}
func (fake *recordingRunRuntime) SetPositionContext(_ context.Context, _ string, _ agent.AgentID, _ *store.PositionContext) error {
	return nil
}
func (fake *recordingRunRuntime) SetTipFollowState(_ context.Context, _ string, _ agent.AgentID, _ *store.Tracking, _ string, _, _ int64) error {
	return nil
}
func (fake *recordingRunRuntime) SetPerceptionRefs(_ context.Context, _ string, _ agent.AgentID, _ store.PerceptionRefs) error {
	return nil
}

// Compile-time witness: the recording store satisfies both the
// base store surface and session's narrow extension.
var _ RuntimeStore = (*recordingRunRuntime)(nil)
var _ store.RuntimeStore = (*recordingRunRuntime)(nil)

// recordingRunScheduler captures the single StartScheduler call
// Run makes on the happy path, so the tests can assert on
// arguments as well as call count.
type recordingRunScheduler struct {
	callCount         atomic.Int32
	observedExchangeID string
}

func newRecordingRunScheduler() *recordingRunScheduler {
	return &recordingRunScheduler{}
}

func (fake *recordingRunScheduler) StartScheduler(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
	curveEffective *generator.CurveEffective,
	lifecycleChannel chan<- LifecycleDecision,
) {
	_, _, _ = ctx, population, curveEffective
	_ = lifecycleChannel
	fake.callCount.Add(1)
	fake.observedExchangeID = request.ExchangeID
}

// recordingRunFailure captures FailBringUp and ReportReady.
type recordingRunFailure struct {
	failCount  atomic.Int32
	readyCount atomic.Int32
	lastCause  atomic.Value
}

func newRecordingRunFailure() *recordingRunFailure {
	return &recordingRunFailure{}
}

func (fake *recordingRunFailure) FailBringUp(request transport.BringUpRequest, cause error) {
	_ = request
	fake.failCount.Add(1)
	fake.lastCause.Store(fmt.Sprintf("%v", cause))
}

func (fake *recordingRunFailure) ReportReady(request transport.BringUpRequest) {
	_ = request
	fake.readyCount.Add(1)
}

// controllableClock lets a test drive the WaitUntil step: the
// clock blocks inside WaitUntil until the test calls release,
// and it signals via waitUntilEntered when Run has reached the
// wait so the test can assert "scheduler not called yet".
type controllableClock struct {
	waitUntilEntered chan time.Time
	releaseChannel   chan struct{}
	releaseOnce      sync.Once
}

func newControllableClock() *controllableClock {
	return &controllableClock{
		waitUntilEntered: make(chan time.Time, 1),
		releaseChannel:   make(chan struct{}),
	}
}

func (clock *controllableClock) Now() time.Time {
	return time.Time{}
}

func (clock *controllableClock) WaitUntil(ctx context.Context, deadline time.Time) error {
	select {
	case clock.waitUntilEntered <- deadline:
	default:
	}
	select {
	case <-clock.releaseChannel:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (clock *controllableClock) release() {
	clock.releaseOnce.Do(func() { close(clock.releaseChannel) })
}

// Keep the unused-import guards quiet. These vars pin the few
// cross-file identifiers this file needs so a careful refactor
// elsewhere does not accidentally drop them.
var _ = rand.NewPCG
var _ = io.Discard
