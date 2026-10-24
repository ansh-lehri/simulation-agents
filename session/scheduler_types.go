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

	// instruments is the session's instrument list in the same stable
	// order as workingSet.instruments, copied onto this per-tick view
	// so decide()'s per-instrument traversal has a deterministic
	// sequence to range over. Perceive consumes agentRNG on every
	// instrument it visits, so ranging over a map instead (candles,
	// quote, or present — all map-keyed) would let Go's randomized map
	// iteration order change the agent's RNG draw sequence from run to
	// run under an identical seed, breaking reproducibility.
	instruments []string
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
	decisionParams   decision.Params
	timeframes       []string
}

// decide is the per-agent Tier-4 seam: the I/O orchestrator that binds
// 4a (perceive) → the root flat/holding fork (4b decision.Decide / 4d
// decision.Manage) → 4c (decision.Execute) into the one gated tree the
// Tier-3 seam allows to emit at most one order per agent per tick. All
// I/O — the one observe() read, the Redis mirrors — happens here; every
// computation it calls into is a pure decision-package function.
//
// Three leaves return no punch: an observe() error (the hot-path rule —
// any error skips this tick's action entirely, no partial traversal);
// zero candidates after a full traversal (every instrument coasted,
// skipped, held, or waited); and a resolved winner that 4c declines
// (iteration 1 emits nothing and relies on next-tick retry — no
// fallback to the next-best candidate).
func decide(input decideInput) (core.OrderPunchRequest, bool) {
	if len(input.timeframes) == 0 {
		return core.OrderPunchRequest{}, false
	}
	obs, err := input.observe()
	if err != nil {
		return core.OrderPunchRequest{}, false
	}

	tf := input.timeframes[0]
	acc := decision.NewAccessor(
		input.market.candles,
		input.market.quote,
		input.market.present,
		input.tick,
	)

	candidates := make([]decision.InstrumentCandidate, 0, len(input.market.instruments))
	for _, inst := range input.market.instruments {
		// if !acc.Present(inst) {
		// 	continue
		// }
		// Present(inst) only checks for a live WS quote. On a freshly
		// constructed market (on-demand), no quote exists until a
		// trade prints, but no trade can print if every instrument is
		// skipped before anyone considers it — a permanent deadlock.
		// CurrentPrice falls back to the last candle close when no
		// quote is present, so gate on "any price at all" instead.
		if _, priceOK := acc.CurrentPrice(inst, tf); !priceOK {
			continue
		}

		mem := input.agentState.perception[inst]
		if mem == nil {
			mem = &instrumentMemory{lastCommitTick: decision.NoPriorCommit}
			input.agentState.perception[inst] = mem
		}
		// 4a runs first and unconditionally for every present
		// instrument, flat or holding: a held instrument's reversal
		// and level-mode exit checks need THIS tick's bundle, not a
		// stale one from whenever it last ran perception.
		if decision.ShouldReperceive(input.agentState.traits, acc, inst, tf, mem.refs) {
			bundle, refs := decision.Perceive(
				input.agentState.traits, acc, inst, tf,
				input.agentState.agentRNG, input.perceptionParams,
			)
			mem.lastBundle = &bundle
			mem.refs = refs
			_ = input.agentState.memoryHandle.SetPerceptionRefs(inst, refs)
		}
		bundle := decision.PerceptionBundle{}
		if mem.lastBundle != nil {
			bundle = *mem.lastBundle
		}

		// The one fork: holding -> Manage (4d), flat -> Decide (4b),
		// mutually exclusive, decided off the single shared observe()
		// read from stage 0.
		position, holding := decision.PositionFor(obs, inst)

		var outcome decision.NodeOutcome
		if holding {
			currentPrice, priceOK := acc.CurrentPrice(inst, tf)
			if !priceOK {
				// No derivable price this tick even though a quote key
				// is present (e.g. no trade has ever printed and no
				// candle exists yet) — 4d has nothing to mark or
				// evaluate triggers against, so this instrument sits
				// out this tick. Perception above already ran.
				continue
			}
			outcome = decision.Manage(decision.ManageInput{
				Position:        position,
				Bundle:          &bundle,
				PositionContext: mem.positionContext,
				Traits:          input.agentState.traits,
				CurrentPrice:    currentPrice,
				Tick:            input.tick,
				Roles:           decision.TraitChannelRoles,
				RNG:             input.agentState.agentRNG,
				Params:          input.decisionParams,
			})
		} else {
			outcome = decision.Decide(decision.DecideInput{
				Bundle:              bundle,
				Traits:              input.agentState.traits,
				Roles:               decision.TraitChannelRoles,
				CurrentTick:         input.tick.TickIndex,
				SessionProgress:     acc.SessionProgress(),
				StandingOpinion:     mem.standingOpinion,
				StandingStance:      mem.standingStance,
				LastDecidedReadings: mem.lastDecidedReadings,
				PendingWatch:        mem.pendingWatch,
				WaitStreak:          mem.waitStreak,
				LastCommitTick:      mem.lastCommitTick,
				RNG:                 input.agentState.agentRNG,
				Params:              input.decisionParams,
			})
		}

		// Assessment applies now, for every traversed instrument, win
		// or lose — never deferred to after resolution.
		applyAssessment(mem, outcome.Assessment)
		mirrorAssessment(input.agentState.memoryHandle, inst, outcome.Assessment)

		if outcome.Candidate != nil {
			candidates = append(candidates, decision.InstrumentCandidate{Instrument: inst, Outcome: outcome})
		}
	}

	if len(candidates) == 0 {
		return core.OrderPunchRequest{}, false
	}
	winner := decision.ResolveOnePunch(candidates, input.agentState.agentRNG)

	req, ok := decision.Execute(
		*winner.Outcome.Candidate,
		decision.AmbientInputs{
			AccountID:      input.agentState.capitalHandle,
			Instrument:     winner.Instrument,
			Timeframe:      tf,
			TradingCapital: input.agentState.tradingCapital,
			Positions:      obs.Positions,
		},
		acc,
		input.agentState.traits,
		input.agentState.agentRNG,
		input.decisionParams,
	)
	if !ok {
		return core.OrderPunchRequest{}, false
	}

	// Commitment applies only now: resolution has already picked the
	// one instrument whose order the seam actually allows, and 4c has
	// just confirmed it is a real punch. Writing a commitment for any
	// instrument other than the winner — or before 4c confirms — would
	// make the agent believe it holds something it never actually
	// ordered.
	if winner.Outcome.Commitment != nil {
		applyCommitment(input.agentState.perception[winner.Instrument], winner.Outcome.Commitment)
		mirrorCommitment(input.agentState.memoryHandle, winner.Instrument, winner.Outcome.Commitment)
	}

	return req, true
}

// applyAssessment copies a node's reported working-memory update onto
// the agent's carried instrumentMemory for this instrument. A nil delta
// — always the case on the holding path, since 4d never reads or
// updates this bookkeeping — is a no-op: the flat-path fields stay
// exactly as they were from whenever this instrument was last flat.
func applyAssessment(mem *instrumentMemory, delta *decision.AssessmentDelta) {
	if delta == nil {
		return
	}
	mem.standingOpinion = delta.StandingOpinion
	mem.standingStance = delta.StandingStance
	mem.lastDecidedReadings = delta.LastDecidedReadings
	mem.pendingWatch = delta.PendingWatch
	mem.waitStreak = delta.WaitStreak
}

// mirrorAssessment best-effort mirrors an applied assessment to Redis,
// write-on-change only (skipped on a coast tick, per 4b's own working-
// memory-setter invariant) — the in-memory working set is always the
// live authority; Redis is a restart-recovery mirror only, so a failed
// or skipped write is never surfaced to the caller.
func mirrorAssessment(memoryHandle *RuntimeHandle, instrument string, delta *decision.AssessmentDelta) {
	if delta == nil || !delta.Changed {
		return
	}
	_ = memoryHandle.SetStandingOpinion(instrument, delta.StandingOpinion, delta.StandingStance)
	_ = memoryHandle.SetWaitStreak(instrument, delta.WaitStreak)
	_ = memoryHandle.SetLastDecidedReadings(instrument, delta.LastDecidedReadings)
	_ = memoryHandle.SetPendingWatch(instrument, storePendingWatch(delta.PendingWatch))
}

// storePendingWatch converts decision's own PendingWatch (the type the
// pure decision package returns) into store's identically-shaped
// PendingWatch (the type RuntimeStore's setter accepts) — store depends
// on neither package, so the two types stay structurally identical but
// distinct, and this is the one place that bridges them.
func storePendingWatch(pendingWatch *decision.PendingWatch) *store.PendingWatch {
	if pendingWatch == nil {
		return nil
	}
	return &store.PendingWatch{
		Direction:    pendingWatch.Direction,
		ConfirmsSeen: pendingWatch.ConfirmsSeen,
		SinceTick:    pendingWatch.SinceTick,
	}
}

// applyCommitment stamps the record of a real, just-placed order onto
// the agent's carried instrumentMemory for the winning instrument.
// Called only after one-punch resolution and only once Execute has
// confirmed a real punch, so mem is always already present — the
// instrument was visited (and its instrumentMemory constructed if
// absent) earlier in this same traversal.
func applyCommitment(mem *instrumentMemory, delta *decision.CommitmentDelta) {
	mem.lastCommitTick = delta.LastCommitTick
	positionContext := delta.PositionContext
	mem.positionContext = &positionContext
}

// mirrorCommitment best-effort mirrors an applied commitment to Redis —
// write-on-change by construction, since it is only ever called for the
// one winning instrument on a real punch, never on every tick.
func mirrorCommitment(memoryHandle *RuntimeHandle, instrument string, delta *decision.CommitmentDelta) {
	_ = memoryHandle.SetLastCommitTick(instrument, delta.LastCommitTick)
	positionContext := delta.PositionContext
	_ = memoryHandle.SetPositionContext(instrument, &positionContext)
}
