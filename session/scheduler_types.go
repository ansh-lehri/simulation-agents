package session

import (
	"context"
	"math/rand/v2"
	"time"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/decision"
	"simulation-agents/store"
)

// RuntimeHandle is a bound accessor for one agent's working-memory
// Redis writes. It captures the runtime store, request context,
// exchange id, and agent id once so the Tier-4 decide function
// receives one object rather than threading four arguments through
// every write call. All methods delegate to the underlying store.
type RuntimeHandle struct {
	runtimeStore store.RuntimeStore
	ctx          context.Context
	exchangeID   string
	agentID      agent.AgentID
}

func newRuntimeHandle(
	runtimeStore store.RuntimeStore,
	ctx context.Context,
	exchangeID string,
	agentID agent.AgentID,
) *RuntimeHandle {
	return &RuntimeHandle{
		runtimeStore: runtimeStore,
		ctx:          ctx,
		exchangeID:   exchangeID,
		agentID:      agentID,
	}
}

func (handle *RuntimeHandle) SetStandingOpinion(instrument string, opinion float64, stance string) error {
	return handle.runtimeStore.SetStandingOpinion(handle.ctx, handle.exchangeID, handle.agentID, instrument, opinion, stance)
}

func (handle *RuntimeHandle) SetWaitStreak(instrument string, streak int) error {
	return handle.runtimeStore.SetWaitStreak(handle.ctx, handle.exchangeID, handle.agentID, instrument, streak)
}

func (handle *RuntimeHandle) SetPendingWatch(instrument string, pendingWatch *store.PendingWatch) error {
	return handle.runtimeStore.SetPendingWatch(handle.ctx, handle.exchangeID, handle.agentID, instrument, pendingWatch)
}

func (handle *RuntimeHandle) SetLastCommitTick(instrument string, commitTick int64) error {
	return handle.runtimeStore.SetLastCommitTick(handle.ctx, handle.exchangeID, handle.agentID, instrument, commitTick)
}

func (handle *RuntimeHandle) SetLastDecidedReadings(instrument string, readings map[string]float64) error {
	return handle.runtimeStore.SetLastDecidedReadings(handle.ctx, handle.exchangeID, handle.agentID, instrument, readings)
}

func (handle *RuntimeHandle) SetPositionContext(instrument string, positionContext *store.PositionContext) error {
	return handle.runtimeStore.SetPositionContext(handle.ctx, handle.exchangeID, handle.agentID, instrument, positionContext)
}

func (handle *RuntimeHandle) SetTipFollowState(
	tracking *store.Tracking,
	lastActedTipID string,
	myExitLevel, submitAt int64,
) error {
	return handle.runtimeStore.SetTipFollowState(
		handle.ctx, handle.exchangeID, handle.agentID,
		tracking, lastActedTipID, myExitLevel, submitAt,
	)
}

func (handle *RuntimeHandle) SetPerceptionRefs(instrument string, refs store.PerceptionRefs) error {
	return handle.runtimeStore.SetPerceptionRefs(handle.ctx, handle.exchangeID, handle.agentID, instrument, refs)
}

// instrumentMemory tracks one agent's carried state for one instrument
// it watches, across both the perception stage and the decision stage.
// refs/lastBundle are perception's working memory, compared against
// current market readings to decide whether to rerun perception.
// standingOpinion/standingStance/lastDecidedReadings/pendingWatch/
// waitStreak/lastCommitTick/positionContext are the decision stage's
// working memory, read and written by whichever future seam calls
// into the decision package's gate/mixer/commit-gate chain (while
// flat) or its position-exit chain (while holding) for this agent and
// this instrument — this struct only carries the fields, it does not
// mutate them itself.
type instrumentMemory struct {
	refs       store.PerceptionRefs
	lastBundle *decision.PerceptionBundle

	standingOpinion     float64                // this instrument's last-formed signed opinion, carried while coasting
	standingStance      string                 // "FLAT_IDLE" | "WAIT" — mirrors pendingWatch's presence for quick reads
	lastDecidedReadings map[string]float64     // the reading snapshot the re-decide gate last compared against; nil until this agent's first-ever decide for this instrument
	pendingWatch        *decision.PendingWatch // nil when not waiting on a confirmation
	waitStreak          int                    // consecutive coasted WAIT ticks; anti-freeze counter for the quiet-confirm fix
	lastCommitTick      int64                  // tick of this agent's last commit on this instrument; the Go zero value (0) is NOT the "never committed" sentinel — whoever first constructs an instrumentMemory for an agent's decision stage must set this to decision.NoPriorCommit explicitly, or the very first cooldown check will misread a fresh instrument as having committed at tick zero

	positionContext *store.PositionContext // entry conditions of this instrument's currently open position, read by the position-exit stage while holding; nil while flat or before any stamp exists (e.g. just after a restart)
}

// workingAgent is one agent's in-memory state for the duration of a
// session. Built once from the population handover; mutated only
// through presence transitions in the presence sweep. Positions,
// cash, and P&L are never stored here — they are read fresh from
// Core inside decide() on demand.
type workingAgent struct {
	traits         agent.TraitVector
	capitalHandle  string // Core account id used for GetObservation and PunchOrder
	tradingCapital int64
	presence       string  // one of store.PresenceDormant / Active / Idle / Done / Retired
	entryTime      float64 // normalized [0,1]; DORMANT→ACTIVE trigger stamped by 2c
	nextCheckTime  int64   // unix seconds; re-arm point for the recurring lifecycle check
	lastCheckTime  int64   // unix seconds; previous check wall-clock, used to compute the elapsed Δ for rate→probability conversion
	idleSince      int64   // unix seconds; set on step-out→IDLE; feeds re-entry and give-up rolls
	memoryHandle   *RuntimeHandle
	agentRNG       *rand.Rand                   // independent per-agent randomness stream, seeded from the session RNG at build time
	perception     map[string]*instrumentMemory // per-instrument perception working memory; keyed by instrument symbol
}

// workingSet is the full in-memory authority for one exchange session.
// Built once from the population handover at startScheduler time; the
// tick loop runs entirely off this structure and never re-reads Redis
// for agent state on the live path. Redis receives write-on-change
// updates for restart recovery, but those writes never feed back into
// the running loop.
type workingSet struct {
	agents       map[agent.AgentID]*workingAgent
	active       map[agent.AgentID]*workingAgent // derived ACTIVE subset; starts empty (all DORMANT)
	sessionStart time.Time
	sessionEnd   time.Time
	instruments  []string
	sessionRNG   *rand.Rand // seeded per bring-up; drives act-coin, emit shuffle, and lifecycle rolls
	exchangeID   string
}

// snapshotView holds one action tick's raw market-data reads for all
// instruments in the session. Loaded once per tick via the bounded
// Redis reads and shared across every agent that ticks. The decision
// accessor wraps this for per-agent signal extraction.
type snapshotView struct {
	candles map[string]map[string][]store.Candle // instrument → timeframe → bars
	quote   map[string]store.Quote
	present map[string]bool // false when the quote key is absent in Redis
}

// lifecycleCurves holds the four realized lifecycle functions for one
// session. Built once at startScheduler from the baseline calibration
// dials, optionally overriding the three rate parameters from a
// scenario's CurveEffective. Functions are called many times through
// the session; the GP realization inside each time-based function is
// fixed at build time.
type lifecycleCurves struct {
	// stepOutRate(normalizedTime) → λ_out: step-out intensity at
	// normalized session time t ∈ [0,1].
	stepOutRate func(normalizedTime float64) float64

	// doneRate(normalizedTime) → λ_done: done-early intensity,
	// rising sharply near session close.
	doneRate func(normalizedTime float64) float64

	// reentryProb(idleDuration) → r(d): per-check re-entry
	// probability given idle duration in seconds. Gaussian peak
	// centered at the configured break point. No Δ factor.
	reentryProb func(idleDuration float64) float64

	// giveUpProb(idleDuration) → q(d): per-check give-up
	// probability, the complement mechanism that bounds maximum idle
	// duration. Sigmoid rising from zero. No Δ factor.
	giveUpProb func(idleDuration float64) float64
}

// decideInput is the seam handed to decide() for each active agent whose
// act-coin fires. The action phase assembles it; the decide() body fills
// the perception and decision logic.
// Parts: the agent's traits and working-memory handle; the shared raw
// market snapshot; a bound fresh-read accessor for the agent's live
// own-state from Core; the tick timing for building the market-data
// accessor; the two global perception calibration dials; and the ordered
// timeframe list the snapshot was built from.
type decideInput struct {
	agentState       *workingAgent
	market           *snapshotView
	observe          func() (core.AccountObservation, error)
	tick             decision.TickContext
	perceptionParams decision.PerceptionParams
	timeframes       []string
}

// decide is the per-agent decision function. For Stage 4a (perception) it
// runs the gate-and-perceive loop for each instrument in the snapshot:
// compare current market state against stored refs, re-run perception only
// when the gate signals a meaningful change, and persist the updated refs
// to Redis as a best-effort write. The order decision (4b) is not yet
// implemented — this function always returns no punch.
func decide(input decideInput) (core.OrderPunchRequest, bool) {
	if len(input.timeframes) == 0 || len(input.market.candles) == 0 {
		return core.OrderPunchRequest{}, false
	}
	tf := input.timeframes[0]
	acc := decision.NewAccessor(
		input.market.candles,
		input.market.quote,
		input.market.present,
		input.tick,
	)
	for inst := range input.market.candles {
		mem := input.agentState.perception[inst]
		if mem == nil {
			mem = &instrumentMemory{}
			input.agentState.perception[inst] = mem
		}
		if decision.ShouldReperceive(input.agentState.traits, acc, inst, tf, mem.refs) {
			bundle, refs := decision.Perceive(
				input.agentState.traits, acc, inst, tf,
				input.agentState.agentRNG, input.perceptionParams,
			)
			mem.lastBundle = &bundle
			mem.refs = refs
			_ = input.agentState.memoryHandle.SetPerceptionRefs(inst, refs)
		}
	}
	return core.OrderPunchRequest{}, false
}
