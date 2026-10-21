package session

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/decision"
	"simulation-agents/store"
)

// TestBuildWorkingSetPopulatesAllAgents: every agent in the population
// (both existing and new) must appear in the working set. A missing
// agent would never tick and never be activated — a silent population
// undercount the action and presence loops would never detect.
func TestBuildWorkingSetPopulatesAllAgents(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	population := makeOrderedPopulation(2, 3)
	ws := sched.buildWorkingSet(context.Background(), canonicalBringUpRequest(), population)

	want := len(population.All())
	if got := len(ws.agents); got != want {
		t.Fatalf("working set has %d agents, want %d (all of existing + new)", got, want)
	}
}

// TestBuildWorkingSetStartsAllDormant: the working set must initialize
// every agent to DORMANT with an empty ACTIVE subset. Activating agents
// at build time would fire their first tick before the session clock
// has been checked — the presence sweep is the only correct activation path.
func TestBuildWorkingSetStartsAllDormant(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := sched.buildWorkingSet(context.Background(), canonicalBringUpRequest(), makeOrderedPopulation(2, 2))

	if len(ws.active) != 0 {
		t.Fatalf("ACTIVE subset must be empty at build time; got %d entries", len(ws.active))
	}
	for agentID, agentRecord := range ws.agents {
		if agentRecord.presence != store.PresenceDormant {
			t.Fatalf("agent %s has presence %q at build time, want %q",
				agentID, agentRecord.presence, store.PresenceDormant)
		}
	}
}

// TestBuildWorkingSetEntryTimeInRange: every agent's entry fraction
// must be in [0, 1]. An out-of-range fraction would trigger an
// ACTIVATE check for a normalized time outside the session span.
func TestBuildWorkingSetEntryTimeInRange(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := sched.buildWorkingSet(context.Background(), canonicalBringUpRequest(), makeOrderedPopulation(3, 3))

	for agentID, agentRecord := range ws.agents {
		if agentRecord.entryTime < 0 || agentRecord.entryTime > 1 {
			t.Fatalf("agent %s entryTime %v is outside [0, 1]", agentID, agentRecord.entryTime)
		}
	}
}

// TestSnapshotReadsAllInstruments: snapshot must issue one ReadCandles
// and one ReadQuote per instrument in the working set. Skipping any
// instrument means agents that trade it read stale or absent data for
// that entire tick.
func TestSnapshotReadsAllInstruments(t *testing.T) {
	t.Parallel()

	recorder := newCountingMarketDataStore()
	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, recorder)
	instruments := []string{"AAPL", "GOOG", "MSFT"}
	ws := newTestWorkingSet("exch_snap", instruments)

	sched.snapshot(context.Background(), ws)

	if got := int(recorder.readCandlesCount.Load()); got != len(instruments) {
		t.Fatalf("ReadCandles called %d times, want %d (one per instrument)", got, len(instruments))
	}
	if got := int(recorder.readQuoteCount.Load()); got != len(instruments) {
		t.Fatalf("ReadQuote called %d times, want %d (one per instrument)", got, len(instruments))
	}
}

// TestSnapshotPassesMaxLookbackK: the bounded window limit ReadCandles
// receives must equal Params.MaxLookbackK. Passing a larger limit
// would make a late-session tick read the full accumulated history.
func TestSnapshotPassesMaxLookbackK(t *testing.T) {
	t.Parallel()

	const wantLimit = 42
	recorder := newCountingMarketDataStore()
	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, recorder)
	sched.params.MaxLookbackK = wantLimit
	ws := newTestWorkingSet("exch_limit", []string{"AAPL"})

	sched.snapshot(context.Background(), ws)

	if recorder.lastReadCandlesLimit != wantLimit {
		t.Fatalf("ReadCandles limit = %d, want %d (MaxLookbackK)", recorder.lastReadCandlesLimit, wantLimit)
	}
}

// TestActionPhaseIteratesActiveSubsetOnly: an agent in ws.agents but
// not in ws.active (DORMANT) must never have its decideFunc called.
// Only agents in the ACTIVE subset may trade.
func TestActionPhaseIteratesActiveSubsetOnly(t *testing.T) {
	t.Parallel()

	var decideCalls atomic.Int32
	recording := func(input decideInput) (core.OrderPunchRequest, bool) {
		decideCalls.Add(1)
		return core.OrderPunchRequest{}, false
	}

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	sched.decideFunc = recording

	ws := newTestWorkingSet("exch_active", []string{"STOCK1"})
	// Two ACTIVE, one DORMANT.
	ws.active[agent.AgentID("agent-active-0")] = &workingAgent{
		traits: agent.TraitVector{PAct: 1.0}, presence: store.PresenceActive,
		memoryHandle: blankMemoryHandle(),
	}
	ws.active[agent.AgentID("agent-active-1")] = &workingAgent{
		traits: agent.TraitVector{PAct: 1.0}, presence: store.PresenceActive,
		memoryHandle: blankMemoryHandle(),
	}
	ws.agents[agent.AgentID("agent-dormant-0")] = &workingAgent{
		traits: agent.TraitVector{PAct: 1.0}, presence: store.PresenceDormant,
		memoryHandle: blankMemoryHandle(),
	}

	punchCh := make(chan core.OrderPunchRequest, 16)
	sched.runActionPhase(context.Background(), ws, punchCh, decision.TickContext{})

	if got := int(decideCalls.Load()); got != 2 {
		t.Fatalf("decideFunc called %d times, want 2 (only ACTIVE agents)", got)
	}
}

// TestActionPhaseActCoinPActZeroNeverActs: when PAct=0 the act-coin
// never passes, so decideFunc is never called regardless of how many
// ACTIVE agents exist.
func TestActionPhaseActCoinPActZeroNeverActs(t *testing.T) {
	t.Parallel()

	var decideCalls atomic.Int32
	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	sched.decideFunc = func(_ decideInput) (core.OrderPunchRequest, bool) {
		decideCalls.Add(1)
		return core.OrderPunchRequest{}, false
	}

	ws := newTestWorkingSet("exch_pact0", []string{"STOCK1"})
	for i := 0; i < 5; i++ {
		id := agent.AgentID(fmt.Sprintf("agent-%03d", i))
		agentRecord := &workingAgent{
			traits: agent.TraitVector{PAct: 0.0}, presence: store.PresenceActive,
			memoryHandle: blankMemoryHandle(),
		}
		ws.active[id] = agentRecord
	}

	punchCh := make(chan core.OrderPunchRequest, 16)
	sched.runActionPhase(context.Background(), ws, punchCh, decision.TickContext{})

	if got := int(decideCalls.Load()); got != 0 {
		t.Fatalf("decideFunc called %d times with PAct=0, want 0", got)
	}
}

// TestActionPhaseActCoinPActOneAlwaysActs: when PAct=1 every ACTIVE
// agent passes the act-coin on every tick. Under the stub decide
// (returns false) no punches are produced, but decideFunc is called
// for every agent.
func TestActionPhaseActCoinPActOneAlwaysActs(t *testing.T) {
	t.Parallel()

	const agentCount = 5
	var decideCalls atomic.Int32
	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	sched.decideFunc = func(_ decideInput) (core.OrderPunchRequest, bool) {
		decideCalls.Add(1)
		return core.OrderPunchRequest{}, false
	}

	ws := newTestWorkingSet("exch_pact1", []string{"STOCK1"})
	for i := 0; i < agentCount; i++ {
		id := agent.AgentID(fmt.Sprintf("agent-%03d", i))
		ws.active[id] = &workingAgent{
			traits: agent.TraitVector{PAct: 1.0}, presence: store.PresenceActive,
			memoryHandle: blankMemoryHandle(),
		}
	}

	punchCh := make(chan core.OrderPunchRequest, 16)
	sched.runActionPhase(context.Background(), ws, punchCh, decision.TickContext{})

	if got := int(decideCalls.Load()); got != agentCount {
		t.Fatalf("decideFunc called %d times with PAct=1, want %d", got, agentCount)
	}
}

// TestActionPhaseNonBlockingOnFullChannel: when the punch channel is
// full, runActionPhase must return promptly without blocking. A
// blocking emit would stall the tick, breaking the TAction floor.
func TestActionPhaseNonBlockingOnFullChannel(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	sched.decideFunc = func(_ decideInput) (core.OrderPunchRequest, bool) {
		return core.OrderPunchRequest{Instrument: "STOCK1"}, true
	}

	ws := newTestWorkingSet("exch_nonblock", []string{"STOCK1"})
	ws.active[agent.AgentID("agent-000")] = &workingAgent{
		traits: agent.TraitVector{PAct: 1.0}, presence: store.PresenceActive,
		memoryHandle: blankMemoryHandle(),
	}

	// Channel with capacity 0 — any send that blocks would deadlock.
	punchCh := make(chan core.OrderPunchRequest, 0)

	done := make(chan struct{})
	go func() {
		sched.runActionPhase(context.Background(), ws, punchCh, decision.TickContext{})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runActionPhase blocked on a full channel; must drop and continue")
	}
}

// TestActionPhaseEmitShuffleSeeded: two action-phase runs backed by
// identical RNG state must produce the same emit order. The seeded
// shuffle is the liquidity-priority ordering; a non-deterministic
// shuffle would break regression baselines under a fixed seed.
func TestActionPhaseEmitShuffleSeeded(t *testing.T) {
	t.Parallel()

	const agentCount = 4
	instruments := []string{"STOCK0", "STOCK1", "STOCK2", "STOCK3"}

	// Each call to decideFunc returns a punch whose Instrument field
	// encodes the agent's index so the emit order can be compared.
	makeDecideFunc := func(callIndex *atomic.Int32) func(decideInput) (core.OrderPunchRequest, bool) {
		return func(_ decideInput) (core.OrderPunchRequest, bool) {
			index := int(callIndex.Add(1)) - 1
			return core.OrderPunchRequest{Instrument: instruments[index%len(instruments)]}, true
		}
	}

	runAndCollect := func(rngState uint64) []string {
		sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
		var callIndex atomic.Int32
		sched.decideFunc = makeDecideFunc(&callIndex)

		ws := newTestWorkingSet("exch_shuffle", []string{"STOCK1"})
		ws.sessionRNG = seededRNGWithOffset(rngState)
		for i := 0; i < agentCount; i++ {
			id := agent.AgentID(fmt.Sprintf("agent-%03d", i))
			ws.active[id] = &workingAgent{
				traits: agent.TraitVector{PAct: 1.0}, presence: store.PresenceActive,
				memoryHandle: blankMemoryHandle(),
			}
		}

		punchCh := make(chan core.OrderPunchRequest, 16)
		sched.runActionPhase(context.Background(), ws, punchCh, decision.TickContext{})
		close(punchCh)

		var order []string
		for req := range punchCh {
			order = append(order, req.Instrument)
		}
		return order
	}

	firstRun := runAndCollect(77)
	secondRun := runAndCollect(77)

	if len(firstRun) != len(secondRun) {
		t.Fatalf("emit count: run1=%d run2=%d — determinism broken", len(firstRun), len(secondRun))
	}
	for i := range firstRun {
		if firstRun[i] != secondRun[i] {
			t.Fatalf("emit order at %d: run1=%q run2=%q — shuffle not seeded", i, firstRun[i], secondRun[i])
		}
	}
}

// TestDispatcherForwardsPunchesToCore: the dispatcher must call
// PunchOrder once for each request placed in the punch channel.
// Fire-and-forget means errors are ignored, but the call must happen.
func TestDispatcherForwardsPunchesToCore(t *testing.T) {
	t.Parallel()

	coreClient := &recordingDispatcherCoreClient{}
	sched := newTestLiveScheduler(t, coreClient, stubRuntimeStore{}, stubMarketDataStore{})

	ctx, cancel := context.WithCancel(context.Background())
	punchCh := make(chan core.OrderPunchRequest, 8)

	done := make(chan struct{})
	go func() {
		sched.runDispatcher(ctx, "exch_dispatch", punchCh)
		close(done)
	}()

	requests := []core.OrderPunchRequest{
		{Instrument: "STOCK1", Side: "buy"},
		{Instrument: "STOCK2", Side: "sell"},
		{Instrument: "STOCK3", Side: "buy"},
	}
	for _, req := range requests {
		punchCh <- req
	}

	// Wait for all three punches to be processed, then cancel.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && int(coreClient.punchCount.Load()) < len(requests) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	if got := int(coreClient.punchCount.Load()); got != len(requests) {
		t.Fatalf("PunchOrder called %d times, want %d", got, len(requests))
	}
}

// TestDispatcherExitsOnContextCancel: the dispatcher goroutine must
// exit promptly when ctx is cancelled. A lingering dispatcher would
// block the punch channel's drain and keep the session goroutine alive
// after teardown.
func TestDispatcherExitsOnContextCancel(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ctx, cancel := context.WithCancel(context.Background())
	punchCh := make(chan core.OrderPunchRequest, 4)

	done := make(chan struct{})
	go func() {
		sched.runDispatcher(ctx, "exch_cancel", punchCh)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not exit within 1s after ctx cancel")
	}
}

// --- fixtures and fakes

func newTestLiveScheduler(t *testing.T, coreClient CoreClient, runtime RuntimeStore, marketData MarketDataStore) *LiveScheduler {
	t.Helper()
	return NewLiveScheduler(
		coreClient,
		runtime,
		marketData,
		Params{
			EntryCurve:              DefaultEntryCurveParams(),
			LifecycleCurve:          DefaultLifecycleCurveParams(),
			LoadSessionsConcurrency: 1,
			MaxLookbackK:            50,
			ConfiguredTimeframes:    []string{"1m"},
			PunchBuffer:             16,
			TAction:                 50 * time.Millisecond,
			TPresence:               5 * time.Minute,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		stubRNGSource{},
	)
}

// newTestWorkingSet builds a minimal working set wired with a seeded
// session RNG. Tests add agents directly to ws.agents and ws.active.
func newTestWorkingSet(exchangeID string, instruments []string) *workingSet {
	return &workingSet{
		agents:       make(map[agent.AgentID]*workingAgent),
		active:       make(map[agent.AgentID]*workingAgent),
		instruments:  instruments,
		sessionStart: time.Now(),
		sessionEnd:   time.Now().Add(time.Hour),
		sessionRNG:   seededRNG(),
		exchangeID:   exchangeID,
	}
}

// blankMemoryHandle returns a RuntimeHandle wired to the no-op stub
// runtime store — safe to carry in workingAgent without any real Redis.
func blankMemoryHandle() *RuntimeHandle {
	return newRuntimeHandle(stubRuntimeStore{}, context.Background(), "exch", "agent")
}

// countingMarketDataStore records how many times ReadCandles and
// ReadQuote were called and captures the last limit passed to
// ReadCandles. All other methods are stubs.
type countingMarketDataStore struct {
	stubMarketDataStore
	readCandlesCount     atomic.Int32
	readQuoteCount       atomic.Int32
	mu                   sync.Mutex
	lastReadCandlesLimit int
}

func newCountingMarketDataStore() *countingMarketDataStore {
	return &countingMarketDataStore{}
}

func (m *countingMarketDataStore) ReadCandles(_ context.Context, _, _, _ string, limit int) ([]store.Candle, error) {
	m.readCandlesCount.Add(1)
	m.mu.Lock()
	m.lastReadCandlesLimit = limit
	m.mu.Unlock()
	return nil, nil
}

func (m *countingMarketDataStore) ReadQuote(_ context.Context, _, _ string) (store.Quote, bool, error) {
	m.readQuoteCount.Add(1)
	return store.Quote{}, false, nil
}

// recordingDispatcherCoreClient counts PunchOrder calls. Other methods
// are stubs — the dispatcher only calls PunchOrder.
type recordingDispatcherCoreClient struct {
	stubCoreClient
	punchCount atomic.Int32
}

func (c *recordingDispatcherCoreClient) PunchOrder(_ context.Context, _ core.OrderPunchRequest) error {
	c.punchCount.Add(1)
	return nil
}
