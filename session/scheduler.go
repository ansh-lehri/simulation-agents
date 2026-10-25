package session

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/decision"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/store"
	"simulation-agents/transport"
)

// LiveScheduler is the real per-session tick loop, implementing the
// Scheduler interface wired by the composition root. Tests that cover
// the pre-open path (Runner.Run) use NoOpScheduler to stay fast and
// free of the loop's wall-clock machinery.
type LiveScheduler struct {
	core       CoreClient
	runtime    RuntimeStore
	marketData MarketDataStore
	params     Params
	logger     *slog.Logger
	rngSource  RNGSource

	// decideFunc is the Tier-4 black box called for each active agent
	// whose act-coin fires. Defaults to the stub that always returns no
	// punch; replaced with the real Tier-4 body when that phase lands.
	// Injectable so tests can inject a recording version without globals.
	decideFunc func(decideInput) (core.OrderPunchRequest, bool)
}

// NewLiveScheduler constructs a LiveScheduler ready to serve
// StartScheduler calls. All parameters are required.
func NewLiveScheduler(
	coreClient CoreClient,
	runtime RuntimeStore,
	marketData MarketDataStore,
	params Params,
	logger *slog.Logger,
	rngSource RNGSource,
) *LiveScheduler {
	return &LiveScheduler{
		core:       coreClient,
		runtime:    runtime,
		marketData: marketData,
		params:     params,
		logger:     logger,
		rngSource:  rngSource,
		decideFunc: decide,
	}
}

// StartScheduler builds the in-memory working set, realizes the
// session lifecycle curves once, spawns the punch dispatcher goroutine,
// and then runs the two-cadence tick loop synchronously until the
// session ends or ctx is cancelled. This is the synchronous tail of the
// per-exchange goroutine — it blocks for the life of the session.
func (sched *LiveScheduler) StartScheduler(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
	curveEffective *generator.CurveEffective,
	lifecycleChannel chan<- LifecycleDecision,
) {
	ws := sched.buildWorkingSet(ctx, request, population)
	curves := buildLifecycleCurves(curveEffective, sched.params.LifecycleCurve, ws.sessionRNG)
	punchCh := make(chan core.OrderPunchRequest, sched.params.PunchBuffer)
	go sched.runDispatcher(ctx, request.ExchangeID, punchCh)
	sched.runLoop(ctx, request, ws, curves, punchCh, lifecycleChannel)

	if request.SessionType == agent.PopulationCanonical {
		sched.emitCanonicalCloseEffects(ctx, request, ws, lifecycleChannel)
	}
	// On-demand sessions have no close-of-session effects to compute or
	// emit (no churn model, no persisted capital to evolve) — the
	// lifecycle channel is left untouched here, and the spawn wrapper's
	// defer close(...) still fires once this function returns.
}

// emitCanonicalCloseEffects runs the canonical close-of-session quit
// roll and capital fold and sends every resulting decision on
// lifecycleChannel. Called only after runLoop above has returned.
//
// 🚩 Carried timing gap (not fixed here — see
// docs/agents/L2_TIER6_TEARDOWN_IMPL.md, the stale-cash-on-fold note):
// runLoop returns on its own session-end wall-clock check, which can
// fire a moment before the broker has actually finalized this
// session's trading cash for every account. On that narrow overlap,
// readTradingCash below returns a one-session-stale cash figure for
// an agent, so its carried capital evolves off the wrong number for
// one session. Accepted for now: the drift lands on a value that
// evolves across many sessions and self-corrects, and agents are
// internal-only, so nothing external-facing is wrong. The real fix is
// to park here until this session's own cancellation arrives (which
// is guaranteed to follow the broker's finalize) rather than firing
// off this wall-clock exit — deliberately out of scope for this stage.
func (sched *LiveScheduler) emitCanonicalCloseEffects(
	ctx context.Context,
	request transport.BringUpRequest,
	ws *workingSet,
	lifecycleChannel chan<- LifecycleDecision,
) {
	agentViews := make([]workingAgentView, 0, len(ws.agents))
	for agentID, agentRecord := range ws.agents {
		agentViews = append(agentViews, workingAgentView{
			AgentID:        agentID,
			CapitalHandle:  agentRecord.capitalHandle,
			TradingCapital: agentRecord.tradingCapital,
		})
	}

	readTradingCash := func(capitalHandle string) (int64, error) {
		accountCash, err := sched.core.GetAccountCash(ctx, capitalHandle)
		if err != nil {
			sched.logger.Warn("session: GetAccountCash failed at session close, leaving this agent's capital unchanged",
				"exchange_id", request.ExchangeID,
				"capital_handle", capitalHandle,
			)
			return 0, err
		}
		return accountCash.TradingCash, nil
	}

	decisions := computeCanonicalTeardown(
		ws.sessionRNG,
		agentViews,
		len(agentViews),
		sched.params.ExpectedQuitsPerSession,
		sched.params.BrokerDefaultFundedCash,
		readTradingCash,
	)
	for _, teardownDecision := range decisions {
		lifecycleChannel <- teardownDecision
	}
}

// buildWorkingSet constructs the in-memory working set from the bring-up
// population. Every agent starts DORMANT; the ACTIVE subset is empty
// until the presence sweep activates agents as their entry-time threshold
// is reached. The session RNG is seeded fresh here and is the sole
// source of randomness for the lifetime of this session: entry-fraction
// draws, lifecycle GP realizations, act-coin flips, emit shuffles, and
// presence rolls all draw from it.
//
// Entry fractions are drawn here from the session RNG. The pre-open
// phase wrote a separate set of fractions to Redis for restart recovery;
// on the live path these fresh draws are the working authority (a Redis
// reader for the restart rebuild is a Tier-6 concern).
func (sched *LiveScheduler) buildWorkingSet(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
) *workingSet {
	sessionRNG := sched.rngSource.Fresh()
	entryCurve := BuildEntryCurve(sessionRNG, sched.params.EntryCurve)

	allAgents := population.All()
	agents := make(map[agent.AgentID]*workingAgent, len(allAgents))
	for _, agentRecord := range allAgents {
		agentRNG := rand.New(rand.NewPCG(sessionRNG.Uint64(), sessionRNG.Uint64()))
		agents[agentRecord.ID] = &workingAgent{
			traits:         agentRecord.Traits,
			capitalHandle:  agentRecord.CapitalHandle,
			tradingCapital: agentRecord.TradingCapital,
			presence:       store.PresenceDormant,
			entryTime:      entryCurve.DrawEntryFraction(sessionRNG),
			memoryHandle:   newRuntimeHandle(sched.runtime, ctx, request.ExchangeID, agentRecord.ID),
			agentRNG:       agentRNG,
			perception:     make(map[string]*instrumentMemory),
		}
	}

	return &workingSet{
		agents:       agents,
		active:       make(map[agent.AgentID]*workingAgent),
		sessionStart: request.SessionStart,
		sessionEnd:   request.SessionEnd,
		instruments:  request.Instruments,
		sessionRNG:   sessionRNG,
		exchangeID:   request.ExchangeID,
	}
}

// runLoop is the two-cadence loop. Each iteration runs whichever
// phases are due — action first, then presence — updates each phase's
// last-run bookmark, and sleeps to the nearest next-due time. TAction
// is a floor: a heavy tick clamps the sleep to zero and the loop runs
// flat-out. The loop exits when the session's end wall-clock is
// reached or ctx is cancelled.
func (sched *LiveScheduler) runLoop(
	ctx context.Context,
	request transport.BringUpRequest,
	ws *workingSet,
	curves *lifecycleCurves,
	punchCh chan core.OrderPunchRequest,
	lifecycleChannel chan<- LifecycleDecision,
) {
	var lastAction, lastPresence time.Time
	var tickIndex int64
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if time.Now().After(request.SessionEnd) {
			return
		}

		now := time.Now()
		if now.Sub(lastAction) >= sched.params.TAction {
			tick := decision.TickContext{
				Now:          now,
				SessionStart: ws.sessionStart,
				SessionEnd:   ws.sessionEnd,
				TickIndex:    tickIndex,
			}
			sched.runActionPhase(ctx, ws, punchCh, tick)
			lastAction = now
			tickIndex++
		}
		if now.Sub(lastPresence) >= sched.params.TPresence {
			sched.runPresencePhase(ctx, ws, curves, now, lifecycleChannel)
			lastPresence = now
		}

		nextDue := earliestTime(
			lastAction.Add(sched.params.TAction),
			lastPresence.Add(sched.params.TPresence),
		)
		sleepDuration := nextDue.Sub(time.Now())
		if sleepDuration > 0 {
			select {
			case <-time.After(sleepDuration):
			case <-ctx.Done():
				return
			}
		}
	}
}

// runActionPhase is the 3a hot path: SNAPSHOT → DECIDE → COLLECT →
// EMIT. The market snapshot is read once per call and shared across
// all active agents. Each agent in the ACTIVE subset has its act-coin
// flipped; those that act are handed to decideFunc (the Tier-4 seam).
// The resulting batch is seeded-shuffled and forwarded to the dispatcher
// channel non-blocking — a full channel drops the order rather than
// stalling the tick.
func (sched *LiveScheduler) runActionPhase(ctx context.Context, ws *workingSet, punchCh chan core.OrderPunchRequest, tick decision.TickContext) {
	view := sched.snapshot(ctx, ws)
	batch := make([]core.OrderPunchRequest, 0, len(ws.active))
	actedCount := 0

	// Collect per-instrument quote diagnostics once per tick for the warn path.
	quoteDiag := make(map[string]string, len(ws.instruments))
	for _, inst := range ws.instruments {
		if q, ok := view.quote[inst]; ok {
			quoteDiag[inst+"_has_bid"] = boolStr(q.HasBid)
			quoteDiag[inst+"_has_ask"] = boolStr(q.HasAsk)
			quoteDiag[inst+"_has_last"] = boolStr(q.HasLast)
		} else {
			quoteDiag[inst+"_quote"] = "absent"
		}
	}

	for _, agentRecord := range ws.active {
		if ws.sessionRNG.Float64() >= agentRecord.traits.PAct {
			continue
		}
		actedCount++
		capitalHandle := agentRecord.capitalHandle
		input := decideInput{
			agentState: agentRecord,
			market:     view,
			observe: func() (core.AccountObservation, error) {
				return sched.core.GetObservation(ctx, capitalHandle)
			},
			tick:             tick,
			perceptionParams: sched.params.Perception,
			decisionParams:   sched.params.Decision,
			timeframes:       sched.params.ConfiguredTimeframes,
		}
		if req, punchNeeded := sched.decideFunc(input); punchNeeded {
			batch = append(batch, req)
		}
	}
	if len(ws.active) > 0 {
		sched.logger.Info("session: action tick",
			"exchange_id", ws.exchangeID,
			"tick_index", tick.TickIndex,
			"active_count", len(ws.active),
			"acted_count", actedCount,
			"punch_count", len(batch),
		)
	}
	if actedCount > 0 && len(batch) == 0 {
		hasPrice := anyInstrumentHasPrice(view, sched.params.ConfiguredTimeframes)
		if !hasPrice {
			sched.logger.Warn("session: no instrument has any price (live quote or candle) yet — agents cannot trade",
				"exchange_id", ws.exchangeID,
				"tick_index", tick.TickIndex,
				"instruments", ws.instruments,
			)
		} else {
			// Agents acted but produced nothing. Log quote flags every 10 ticks
			// so operators can see whether HasBid/HasAsk/HasLast are populated.
			if tick.TickIndex%10 == 0 {
				diagArgs := []any{
					"exchange_id", ws.exchangeID,
					"tick_index", tick.TickIndex,
					"acted_count", actedCount,
				}
				for k, v := range quoteDiag {
					diagArgs = append(diagArgs, k, v)
				}
				slog.Info("session: agents acted but no order produced — quote flag snapshot", diagArgs...)
			}
		}
	}
	ws.sessionRNG.Shuffle(len(batch), func(i, j int) { batch[i], batch[j] = batch[j], batch[i] })
	for _, req := range batch {
		select {
		case punchCh <- req:
		default:
			sched.logger.Warn("session: punch channel full, dropping order",
				"exchange_id", ws.exchangeID,
				"instrument", req.Instrument,
			)
		}
	}
}

// snapshot reads the bounded OHLCV window and latest quote for every
// session instrument into goroutine memory. Reads are amortized once
// across all active agents per tick. A missing key — an instrument
// not yet printed or whose quote has not arrived — is a normal path
// that sets present=false; agents skip that instrument this tick.
func (sched *LiveScheduler) snapshot(ctx context.Context, ws *workingSet) *snapshotView {
	view := &snapshotView{
		candles:     make(map[string]map[string][]store.Candle, len(ws.instruments)),
		quote:       make(map[string]store.Quote, len(ws.instruments)),
		present:     make(map[string]bool, len(ws.instruments)),
		instruments: ws.instruments,
	}
	for _, instrument := range ws.instruments {
		tfCandles := make(map[string][]store.Candle, len(sched.params.ConfiguredTimeframes))
		for _, tf := range sched.params.ConfiguredTimeframes {
			candles, _ := sched.marketData.ReadCandles(ctx, ws.exchangeID, instrument, tf, sched.params.MaxLookbackK)
			tfCandles[tf] = candles
		}
		view.candles[instrument] = tfCandles
		quote, present, _ := sched.marketData.ReadQuote(ctx, ws.exchangeID, instrument)
		view.quote[instrument] = quote
		view.present[instrument] = present
	}
	return view
}

// runDispatcher drains the punch channel and calls PunchOrder for each
// request, ignoring the 202 response and all errors (fire-and-forget).
// Order outcomes are reconciled via GetObservation inside decide() on a
// later tick. The dispatcher exits when the channel is closed or ctx is
// cancelled.
func (sched *LiveScheduler) runDispatcher(ctx context.Context, exchangeID string, punchCh <-chan core.OrderPunchRequest) {
	for {
		select {
		case req, open := <-punchCh:
			if !open {
				return
			}
			if err := sched.core.PunchOrder(ctx, req); err != nil {
				sched.logger.Warn("session: PunchOrder failed (fire-and-forget, not retried)",
					"exchange_id", exchangeID,
					"instrument", req.Instrument,
				)
				continue
			}
			sched.logger.Info("session: order punched",
				"exchange_id", exchangeID,
				"account_id", req.AccountID,
				"instrument", req.Instrument,
				"side", req.Side,
				"type", req.Type,
				"qty", req.Qty,
				"limit_price", req.LimitPrice,
			)
		case <-ctx.Done():
			return
		}
	}
}

// anyInstrumentHasPrice reports whether at least one instrument in the
// snapshot has a price available from either source decide() can use:
// a live quote, or (the candle fallback) a non-empty candle list on
// any of the configured timeframes. Used to distinguish "agents acted
// but genuinely found nothing worth doing this tick" (a normal, quiet
// outcome) from "agents acted but no instrument has any price at all"
// (the real deadlock — nothing to decide against, on any source).
// Mirrors decision.Accessor.CurrentPrice's two sources; checking only
// the live quote here would flag the normal case too, since the quote
// stream for a never-yet-traded instrument never arrives.
func anyInstrumentHasPrice(view *snapshotView, timeframes []string) bool {
	for _, inst := range view.instruments {
		if quote, ok := view.quote[inst]; ok && quote.HasLast {
			return true
		}
		for _, tf := range timeframes {
			if len(view.candles[inst][tf]) > 0 {
				return true
			}
		}
	}
	return false
}

// earliestTime returns whichever of two times is earlier — used to
// find the nearest next-due wakeup in the loop.
func earliestTime(firstTime, secondTime time.Time) time.Time {
	if firstTime.Before(secondTime) {
		return firstTime
	}
	return secondTime
}

// boolStr renders a bool as "true"/"false" for use in structured log
// key-value pairs where the key is dynamic (slog attribute maps do not
// accept raw bool without a type-assertion detour).
func boolStr(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
