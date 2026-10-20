package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// TestActivateDormantOnEntryTime: a DORMANT agent whose normalized entry
// threshold has been reached must flip to ACTIVE and appear in the ACTIVE
// subset. The entry curve is the sole activation gate — agents don't trade
// until their entry time.
func TestActivateDormantOnEntryTime(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-entry")
	agentRecord := &workingAgent{
		presence:     store.PresenceDormant,
		entryTime:    0.5,
		memoryHandle: blankMemoryHandle(),
	}
	ws.agents[agentID] = agentRecord

	// nowNorm = 0.5 exactly at the entry threshold (condition is >=).
	sched.activateDormantAgents(context.Background(), ws, fixedNow, 0.5)

	if agentRecord.presence != store.PresenceActive {
		t.Fatalf("presence = %q after entry time reached, want %q",
			agentRecord.presence, store.PresenceActive)
	}
	if _, inActive := ws.active[agentID]; !inActive {
		t.Fatal("agent not in ACTIVE subset after activation")
	}
}

// TestActivateDormantBelowEntryThresholdSkipped: a DORMANT agent whose
// entry time has not yet been reached must remain DORMANT. Premature
// activation would make the agent trade before it "arrives" in the session.
func TestActivateDormantBelowEntryThresholdSkipped(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-not-yet")
	agentRecord := &workingAgent{
		presence:     store.PresenceDormant,
		entryTime:    0.8,
		memoryHandle: blankMemoryHandle(),
	}
	ws.agents[agentID] = agentRecord

	sched.activateDormantAgents(context.Background(), ws, fixedNow, 0.5)

	if agentRecord.presence != store.PresenceDormant {
		t.Fatalf("presence = %q before entry time, want %q (must stay DORMANT)",
			agentRecord.presence, store.PresenceDormant)
	}
	if _, inActive := ws.active[agentID]; inActive {
		t.Fatal("agent appeared in ACTIVE subset before its entry time")
	}
}

// TestCeilingHookIsNoOp: all N DORMANT agents at or past their entry time
// must activate. The ceiling hook is a no-op in the prototype (ceiling = N).
func TestCeilingHookIsNoOp(t *testing.T) {
	t.Parallel()

	const agentCount = 5
	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	for i := 0; i < agentCount; i++ {
		id := agent.AgentID(agent.AgentID("agent-ceiling-" + string(rune('0'+i))))
		ws.agents[id] = &workingAgent{
			presence:     store.PresenceDormant,
			entryTime:    0.0,
			memoryHandle: blankMemoryHandle(),
		}
	}

	sched.activateDormantAgents(context.Background(), ws, fixedNow, 1.0)

	if got := len(ws.active); got != agentCount {
		t.Fatalf("active count = %d after ceiling-hook sweep, want %d (ceiling = N, no blocking)",
			got, agentCount)
	}
}

// TestActivateWritesPresenceAndTimers: activation must write SetPresence
// (ACTIVE) and SetTimers to Redis so a restarted process can rebuild the
// in-memory state without losing the agent from the ACTIVE set.
func TestActivateWritesPresenceAndTimers(t *testing.T) {
	t.Parallel()

	recorder := &recordingPresenceStore{}
	sched := newTestLiveScheduler(t, stubCoreClient{}, recorder, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-activate-write")
	ws.agents[agentID] = &workingAgent{
		presence:     store.PresenceDormant,
		entryTime:    0.0,
		memoryHandle: newRuntimeHandle(recorder, context.Background(), ws.exchangeID, agentID),
	}

	sched.activateDormantAgents(context.Background(), ws, fixedNow, 1.0)

	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if len(recorder.presenceCalls) != 1 || recorder.presenceCalls[0].presence != store.PresenceActive {
		t.Fatalf("SetPresence calls = %v, want one call with %q", recorder.presenceCalls, store.PresenceActive)
	}
	if len(recorder.timerCalls) != 1 {
		t.Fatalf("SetTimers calls = %d, want 1 on activation", len(recorder.timerCalls))
	}
}

// TestActiveAgentDoneBeforeStepOut: when both done and step-out probabilities
// are 1.0, the agent must go DONE — done supersedes step-out because an agent
// deciding to leave for good near session close should not temporarily idle.
func TestActiveAgentDoneBeforeStepOut(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-done-priority")
	agentRecord := &workingAgent{
		presence:      store.PresenceActive,
		nextCheckTime: 0,              // already due
		lastCheckTime: 0,              // delta = fixedNow.Unix() → large → pDone=1.0
		memoryHandle:  blankMemoryHandle(),
	}
	ws.agents[agentID] = agentRecord
	ws.active[agentID] = agentRecord

	// Both done and step-out fire with certainty; done must win.
	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 1.0 },
		stepOutRate: func(_ float64) float64 { return 1.0 },
		reentryProb: func(_ float64) float64 { return 0 },
		giveUpProb:  func(_ float64) float64 { return 0 },
	}

	sched.checkActiveAgents(context.Background(), ws, curves, fixedNow.Unix(), 0.9)

	if agentRecord.presence != store.PresenceDone {
		t.Fatalf("presence = %q with pDone=1 and pStepOut=1, want %q (done supersedes step-out)",
			agentRecord.presence, store.PresenceDone)
	}
	if _, inActive := ws.active[agentID]; inActive {
		t.Fatal("DONE agent still in ACTIVE subset")
	}
}

// TestActiveAgentStepsOutToIdle: when the done probability is 0 and the
// step-out probability is 1.0, the agent must go IDLE and have idleSince
// set to the current check wall-clock.
func TestActiveAgentStepsOutToIdle(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-step-out")
	agentRecord := &workingAgent{
		presence:      store.PresenceActive,
		nextCheckTime: 0,
		lastCheckTime: 0,
		memoryHandle:  blankMemoryHandle(),
	}
	ws.agents[agentID] = agentRecord
	ws.active[agentID] = agentRecord

	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 0 },
		stepOutRate: func(_ float64) float64 { return 1.0 },
		reentryProb: func(_ float64) float64 { return 0 },
		giveUpProb:  func(_ float64) float64 { return 0 },
	}

	sched.checkActiveAgents(context.Background(), ws, curves, fixedNow.Unix(), 0.5)

	if agentRecord.presence != store.PresenceIdle {
		t.Fatalf("presence = %q with pDone=0 pStepOut=1, want %q",
			agentRecord.presence, store.PresenceIdle)
	}
	if agentRecord.idleSince != fixedNow.Unix() {
		t.Fatalf("idleSince = %d, want %d (nowUnix at step-out check)", agentRecord.idleSince, fixedNow.Unix())
	}
	if _, inActive := ws.active[agentID]; inActive {
		t.Fatal("IDLE agent still in ACTIVE subset after step-out")
	}
}

// TestActiveAgentStaysActiveRearms: when both done and step-out probabilities
// are 0, the agent stays ACTIVE and nextCheckTime advances to after the
// current check — it will not be checked again until the new interval elapses.
func TestActiveAgentStaysActiveRearms(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-stay-active")
	agentRecord := &workingAgent{
		presence:      store.PresenceActive,
		nextCheckTime: 0,
		lastCheckTime: 0,
		memoryHandle:  blankMemoryHandle(),
	}
	ws.agents[agentID] = agentRecord
	ws.active[agentID] = agentRecord

	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 0 },
		stepOutRate: func(_ float64) float64 { return 0 },
		reentryProb: func(_ float64) float64 { return 0 },
		giveUpProb:  func(_ float64) float64 { return 0 },
	}

	sched.checkActiveAgents(context.Background(), ws, curves, fixedNow.Unix(), 0.3)

	if agentRecord.presence != store.PresenceActive {
		t.Fatalf("presence = %q with pDone=0 pStepOut=0, want %q (must stay ACTIVE)",
			agentRecord.presence, store.PresenceActive)
	}
	if agentRecord.nextCheckTime <= fixedNow.Unix() {
		t.Fatalf("nextCheckTime = %d not rearmed past now = %d", agentRecord.nextCheckTime, fixedNow.Unix())
	}
}

// TestActiveAgentSkippedBeforeCheckTime: an ACTIVE agent whose nextCheckTime
// has not yet elapsed must not have its lifecycle rolls run. The rolls
// consume the session RNG; firing them too early also corrupts the RNG sequence.
func TestActiveAgentSkippedBeforeCheckTime(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-not-due")
	agentRecord := &workingAgent{
		presence:      store.PresenceActive,
		nextCheckTime: fixedNow.Unix() + 9999, // well in the future
		lastCheckTime: 0,
		memoryHandle:  blankMemoryHandle(),
	}
	ws.agents[agentID] = agentRecord
	ws.active[agentID] = agentRecord

	// Curves that would definitely fire if reached.
	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 1.0 },
		stepOutRate: func(_ float64) float64 { return 1.0 },
		reentryProb: func(_ float64) float64 { return 0 },
		giveUpProb:  func(_ float64) float64 { return 0 },
	}

	sched.checkActiveAgents(context.Background(), ws, curves, fixedNow.Unix(), 0.5)

	if agentRecord.presence != store.PresenceActive {
		t.Fatalf("presence changed to %q for agent not yet due; nextCheckTime > now must prevent the check",
			agentRecord.presence)
	}
}

// TestStepOutWritesPresenceAndTimers: stepping out from ACTIVE to IDLE must
// write both SetPresence (IDLE) and SetTimers (idleSince + next check) so
// Redis tracks how long the agent has been idle for re-entry and give-up rolls.
func TestStepOutWritesPresenceAndTimers(t *testing.T) {
	t.Parallel()

	recorder := &recordingPresenceStore{}
	sched := newTestLiveScheduler(t, stubCoreClient{}, recorder, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-step-out-write")
	agentRecord := &workingAgent{
		presence:      store.PresenceActive,
		nextCheckTime: 0,
		lastCheckTime: 0,
		memoryHandle:  newRuntimeHandle(recorder, context.Background(), ws.exchangeID, agentID),
	}
	ws.agents[agentID] = agentRecord
	ws.active[agentID] = agentRecord

	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 0 },
		stepOutRate: func(_ float64) float64 { return 1.0 },
		reentryProb: func(_ float64) float64 { return 0 },
		giveUpProb:  func(_ float64) float64 { return 0 },
	}

	sched.checkActiveAgents(context.Background(), ws, curves, fixedNow.Unix(), 0.5)

	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if len(recorder.presenceCalls) != 1 || recorder.presenceCalls[0].presence != store.PresenceIdle {
		t.Fatalf("SetPresence calls = %v, want one call with %q", recorder.presenceCalls, store.PresenceIdle)
	}
	if len(recorder.timerCalls) != 1 {
		t.Fatalf("SetTimers calls = %d on step-out, want 1", len(recorder.timerCalls))
	}
}

// TestDoneWritesPresenceOnly: transitioning to DONE must write SetPresence but
// NOT SetTimers — a DONE agent has no future check, so writing timers is
// wasteful and would write stale values.
func TestDoneWritesPresenceOnly(t *testing.T) {
	t.Parallel()

	recorder := &recordingPresenceStore{}
	sched := newTestLiveScheduler(t, stubCoreClient{}, recorder, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-done-write")
	agentRecord := &workingAgent{
		presence:      store.PresenceActive,
		nextCheckTime: 0,
		lastCheckTime: 0,
		memoryHandle:  newRuntimeHandle(recorder, context.Background(), ws.exchangeID, agentID),
	}
	ws.agents[agentID] = agentRecord
	ws.active[agentID] = agentRecord

	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 1.0 },
		stepOutRate: func(_ float64) float64 { return 0 },
		reentryProb: func(_ float64) float64 { return 0 },
		giveUpProb:  func(_ float64) float64 { return 0 },
	}

	sched.checkActiveAgents(context.Background(), ws, curves, fixedNow.Unix(), 0.9)

	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if len(recorder.presenceCalls) != 1 || recorder.presenceCalls[0].presence != store.PresenceDone {
		t.Fatalf("SetPresence calls = %v, want one call with %q", recorder.presenceCalls, store.PresenceDone)
	}
	if len(recorder.timerCalls) != 0 {
		t.Fatalf("SetTimers calls = %d on DONE transition, want 0 (DONE agents have no future check)",
			len(recorder.timerCalls))
	}
}

// TestIdleAgentReentersBeforeGiveUp: when both re-entry and give-up
// probabilities are 1.0, the agent must go ACTIVE — re-entry runs first
// and takes priority over give-up when both fire simultaneously.
func TestIdleAgentReentersBeforeGiveUp(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-reentry-priority")
	agentRecord := &workingAgent{
		presence:      store.PresenceIdle,
		nextCheckTime: 0,
		lastCheckTime: 0,
		idleSince:     0,
		memoryHandle:  blankMemoryHandle(),
	}
	ws.agents[agentID] = agentRecord

	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 0 },
		stepOutRate: func(_ float64) float64 { return 0 },
		reentryProb: func(_ float64) float64 { return 1.0 },
		giveUpProb:  func(_ float64) float64 { return 1.0 },
	}

	sched.checkIdleAgents(context.Background(), ws, curves, fixedNow.Unix(), nil)

	if agentRecord.presence != store.PresenceActive {
		t.Fatalf("presence = %q with pReentry=1 pGiveUp=1, want %q (re-entry runs first)",
			agentRecord.presence, store.PresenceActive)
	}
	if _, inActive := ws.active[agentID]; !inActive {
		t.Fatal("re-entered agent not in ACTIVE subset")
	}
}

// TestIdleAgentGivesUpToDone: when re-entry probability is 0 and give-up
// is 1.0, the agent must go DONE — idling too long past the typical break
// converts to a done-for-session rather than a permanent idle.
func TestIdleAgentGivesUpToDone(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-give-up")
	agentRecord := &workingAgent{
		presence:      store.PresenceIdle,
		nextCheckTime: 0,
		lastCheckTime: 0,
		idleSince:     0,
		memoryHandle:  blankMemoryHandle(),
	}
	ws.agents[agentID] = agentRecord

	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 0 },
		stepOutRate: func(_ float64) float64 { return 0 },
		reentryProb: func(_ float64) float64 { return 0 },
		giveUpProb:  func(_ float64) float64 { return 1.0 },
	}

	sched.checkIdleAgents(context.Background(), ws, curves, fixedNow.Unix(), nil)

	if agentRecord.presence != store.PresenceDone {
		t.Fatalf("presence = %q with pReentry=0 pGiveUp=1, want %q",
			agentRecord.presence, store.PresenceDone)
	}
	if _, inActive := ws.active[agentID]; inActive {
		t.Fatal("DONE agent (from give-up) appeared in ACTIVE subset")
	}
}

// TestIdleAgentStaysIdleRearms: when both re-entry and give-up are 0, the
// agent stays IDLE and nextCheckTime advances so it will be checked again
// after the next check interval.
func TestIdleAgentStaysIdleRearms(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-stay-idle")
	agentRecord := &workingAgent{
		presence:      store.PresenceIdle,
		nextCheckTime: 0,
		lastCheckTime: 0,
		idleSince:     fixedNow.Unix() - 100,
		memoryHandle:  blankMemoryHandle(),
	}
	ws.agents[agentID] = agentRecord

	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 0 },
		stepOutRate: func(_ float64) float64 { return 0 },
		reentryProb: func(_ float64) float64 { return 0 },
		giveUpProb:  func(_ float64) float64 { return 0 },
	}

	sched.checkIdleAgents(context.Background(), ws, curves, fixedNow.Unix(), nil)

	if agentRecord.presence != store.PresenceIdle {
		t.Fatalf("presence = %q with pReentry=0 pGiveUp=0, want %q (must stay IDLE)",
			agentRecord.presence, store.PresenceIdle)
	}
	if agentRecord.nextCheckTime <= fixedNow.Unix() {
		t.Fatalf("nextCheckTime = %d not rearmed past now = %d", agentRecord.nextCheckTime, fixedNow.Unix())
	}
}

// TestIdleAgentSkippedBeforeCheckTime: an IDLE agent whose nextCheckTime
// has not elapsed must not be checked. Premature checks would skew the
// idle-duration probability curves.
func TestIdleAgentSkippedBeforeCheckTime(t *testing.T) {
	t.Parallel()

	sched := newTestLiveScheduler(t, stubCoreClient{}, stubRuntimeStore{}, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-idle-not-due")
	agentRecord := &workingAgent{
		presence:      store.PresenceIdle,
		nextCheckTime: fixedNow.Unix() + 9999,
		lastCheckTime: 0,
		idleSince:     0,
		memoryHandle:  blankMemoryHandle(),
	}
	ws.agents[agentID] = agentRecord

	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 0 },
		stepOutRate: func(_ float64) float64 { return 0 },
		reentryProb: func(_ float64) float64 { return 1.0 },
		giveUpProb:  func(_ float64) float64 { return 1.0 },
	}

	sched.checkIdleAgents(context.Background(), ws, curves, fixedNow.Unix(), nil)

	if agentRecord.presence != store.PresenceIdle {
		t.Fatalf("presence = %q for IDLE agent not yet due; must stay IDLE", agentRecord.presence)
	}
}

// TestIdleReentryWritesPresenceAndTimers: re-entry from IDLE to ACTIVE must
// write both SetPresence (ACTIVE) and SetTimers (clears idleSince, sets next
// check) — the agent is back in the active set and needs its timer re-armed.
func TestIdleReentryWritesPresenceAndTimers(t *testing.T) {
	t.Parallel()

	recorder := &recordingPresenceStore{}
	sched := newTestLiveScheduler(t, stubCoreClient{}, recorder, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-reentry-write")
	agentRecord := &workingAgent{
		presence:      store.PresenceIdle,
		nextCheckTime: 0,
		lastCheckTime: 0,
		idleSince:     0,
		memoryHandle:  newRuntimeHandle(recorder, context.Background(), ws.exchangeID, agentID),
	}
	ws.agents[agentID] = agentRecord

	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 0 },
		stepOutRate: func(_ float64) float64 { return 0 },
		reentryProb: func(_ float64) float64 { return 1.0 },
		giveUpProb:  func(_ float64) float64 { return 0 },
	}

	sched.checkIdleAgents(context.Background(), ws, curves, fixedNow.Unix(), nil)

	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if len(recorder.presenceCalls) != 1 || recorder.presenceCalls[0].presence != store.PresenceActive {
		t.Fatalf("SetPresence calls = %v, want one call with %q", recorder.presenceCalls, store.PresenceActive)
	}
	if len(recorder.timerCalls) != 1 {
		t.Fatalf("SetTimers calls = %d on re-entry, want 1", len(recorder.timerCalls))
	}
	if recorder.timerCalls[0].idleSince != 0 {
		t.Fatalf("SetTimers idleSince = %d on re-entry, want 0 (idleSince cleared)", recorder.timerCalls[0].idleSince)
	}
}

// TestGiveUpDoneWritesPresenceOnly: give-up (IDLE→DONE) must write
// SetPresence but not SetTimers — same rule as the active-done path.
func TestGiveUpDoneWritesPresenceOnly(t *testing.T) {
	t.Parallel()

	recorder := &recordingPresenceStore{}
	sched := newTestLiveScheduler(t, stubCoreClient{}, recorder, stubMarketDataStore{})
	ws := newPresenceWorkingSet()

	const agentID = agent.AgentID("agent-giveup-write")
	agentRecord := &workingAgent{
		presence:      store.PresenceIdle,
		nextCheckTime: 0,
		lastCheckTime: 0,
		idleSince:     0,
		memoryHandle:  newRuntimeHandle(recorder, context.Background(), ws.exchangeID, agentID),
	}
	ws.agents[agentID] = agentRecord

	curves := &lifecycleCurves{
		doneRate:    func(_ float64) float64 { return 0 },
		stepOutRate: func(_ float64) float64 { return 0 },
		reentryProb: func(_ float64) float64 { return 0 },
		giveUpProb:  func(_ float64) float64 { return 1.0 },
	}

	sched.checkIdleAgents(context.Background(), ws, curves, fixedNow.Unix(), nil)

	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if len(recorder.presenceCalls) != 1 || recorder.presenceCalls[0].presence != store.PresenceDone {
		t.Fatalf("SetPresence calls = %v, want one call with %q (give-up → DONE)", recorder.presenceCalls, store.PresenceDone)
	}
	if len(recorder.timerCalls) != 0 {
		t.Fatalf("SetTimers calls = %d on give-up DONE, want 0", len(recorder.timerCalls))
	}
}

// TestNormalizedSessionTimeAtOpen: at session open (now == sessionStart),
// the normalized time must be exactly 0.0.
func TestNormalizedSessionTimeAtOpen(t *testing.T) {
	t.Parallel()

	ws := newPresenceWorkingSet()
	result := normalizedSessionTime(ws.sessionStart, ws)
	if result != 0.0 {
		t.Fatalf("normalizedSessionTime at open = %v, want 0.0", result)
	}
}

// TestNormalizedSessionTimeAtClose: at session close (now == sessionEnd),
// the normalized time must be exactly 1.0.
func TestNormalizedSessionTimeAtClose(t *testing.T) {
	t.Parallel()

	ws := newPresenceWorkingSet()
	result := normalizedSessionTime(ws.sessionEnd, ws)
	if result != 1.0 {
		t.Fatalf("normalizedSessionTime at close = %v, want 1.0", result)
	}
}

// TestClamp01Boundaries: clamp01 must enforce [0, 1] exactly.
func TestClamp01Boundaries(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input float64
		want  float64
	}{
		{-1.0, 0.0},
		{0.0, 0.0},
		{0.5, 0.5},
		{1.0, 1.0},
		{2.0, 1.0},
		{1e18, 1.0},
	}
	for _, tc := range cases {
		got := clamp01(tc.input)
		if got != tc.want {
			t.Fatalf("clamp01(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// --- fixtures and recording fakes

// fixedNow is a stable reference time for presence tests. Its Unix value
// (~1 000 000) acts as the Δ in active-check tests when lastCheckTime=0:
// even a small rate ×  1 000 000 clamps to 1.0, so tests can use rate=1.0
// to force a firing result without depending on a precise RNG value.
var fixedNow = time.Unix(1_000_000, 0)

// newPresenceWorkingSet builds a minimal working set for presence tests,
// with a seeded RNG and a 1-hour session window.
func newPresenceWorkingSet() *workingSet {
	base := time.Unix(900_000, 0)
	return &workingSet{
		agents:       make(map[agent.AgentID]*workingAgent),
		active:       make(map[agent.AgentID]*workingAgent),
		instruments:  []string{"STOCK1"},
		sessionStart: base,
		sessionEnd:   base.Add(time.Hour),
		sessionRNG:   seededRNG(),
		exchangeID:   "exch-presence",
	}
}

// recordingPresenceStore embeds stubRuntimeStore so all RuntimeStore
// methods are satisfied and records only the SetPresence / SetTimers calls
// that the presence sweep makes.
type recordingPresenceStore struct {
	stubRuntimeStore
	mu            sync.Mutex
	presenceCalls []presenceWriteCall
	timerCalls    []timerWriteCall
}

type presenceWriteCall struct {
	agentID  agent.AgentID
	presence string
}

type timerWriteCall struct {
	agentID       agent.AgentID
	nextCheckTime int64
	idleSince     int64
}

func (s *recordingPresenceStore) SetPresence(_ context.Context, _ string, agentID agent.AgentID, presence string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presenceCalls = append(s.presenceCalls, presenceWriteCall{agentID, presence})
	return nil
}

func (s *recordingPresenceStore) SetTimers(_ context.Context, _ string, agentID agent.AgentID, nextCheckTime, idleSince int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timerCalls = append(s.timerCalls, timerWriteCall{agentID, nextCheckTime, idleSince})
	return nil
}
