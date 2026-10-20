Layer 2 — Tier 3: The Runtime Loop (Implementation Design)

What this doc is. The build specification for the runtime loop — the per-exchange goroutine's post-go-live tick. It translates L2_TIER3_RUNTIME_LOOP_DESIGN (the design, D1–D12) against the real shipped signatures read from the simulation-agents repo on 2026-10-19 (core/, store/, orchestrator/, scenario/, cmd/agentsd/). Point format matches L2_TIER2_IMPLEMENTATION_DESIGN: what it is / expected & why / cross-layer / ingress-egress / the implementation / decisions.

Grounding. Code is illustrative of shape/contract, not compile-ready Go (project rule: impl altitude = interfaces/structs/pseudo-logic/ingress-egress). Every seam below is pinned to a signature actually in the tree; where this doc's earlier Phase-3-era orientation was stale, the as-built signature wins. New impl-level choices are tagged [decided here]. (The one cross-doc inference — the rate→curve-constant binding, Point 8a — was flagged [confirm] and is now confirmed by Ansh, 2026-10-19.) The 3b transition math is reproduced verbatim from LAYER_2_LIFECYCLE (the lifecycle-math doc), with trait factors f_i = 1 and fatigue c_fat = 0 (prototype), per D7.

Scope — Tier 3 only.

In: the store/wiring prerequisites Tier 3 binds to, the handoff contract into startScheduler, the in-memory working set, the two-cadence loop, the 3a action tick (snapshot → decide → collect → emit), the dispatcher + fire-and-forget punch, the 3b presence sweep (curve builder, two clocks, verbatim rolls, write split), the LifecycleDecision channel + orchestrator drain, the degrade-not-abort failure policy.
Out — NOT this doc: the 2b/2c bodies continueBringUp / runAgentPopulation (Phase 5, the orchestrator-integration phase — Tier 3 defines the seam it is handed, never the body); the decide() internals — perception/mixer/execution/exit (Tier 4; 3a provides the seam only); the scenario machinery that produces curve_eff (the scenario/ package — already shipped; Tier 3 only consumes the four rates); RETIRE execution (square-off → quit roll → Core close), restart rebuild (the runtime reader), WS reconnect, abort-cleanup (Tier 6).

Boundary with Phase 5 (explicit). Phase 5 owns the goroutine body: it stamps entry_time, runs the pre-open ops (per-agent join = session-load, history pull, WS subscribe + the ingest goroutine), waits for go-live, reads sessions:active, and calls startScheduler(...). Tier 3 begins at that call. Tier 3 declares what startScheduler must be handed (Point 2) and the stores/wiring it binds to (Point 1); it does not spec how Phase 5 fills them.

Point 1 — Store & wiring prerequisites (the Tier-3 deltas, against real signatures)

What it is. The store additions, struct extension, and composition-root wiring the loop binds to. Nothing in Points 3–9 compiles without these. Each is pinned to the as-built signature.

Expected & why. The loop reads the market cache and own-state fresh, writes presence/working-memory to Redis, and reaches Core through one o.core. The shipped stores have the writers but not the readers the loop needs; the cached quote drops fields 3a/4a require; the orchestrator does not hold the handles the goroutine uses; several config dials do not exist.

Cross-layer. Edits one already-shipped Tier-1 struct (store.Quote) + the composition root (cmd/agentsd/main.go + the Orchestrator struct). No Core contract change.

The implementation.

(a) D4 — extend the cached quote [OK'd]. As-built store.Quote (store/market_data_store.go) carries only BidPrice/AskPrice/BidQty/AskQty/Timestamp. The WS SnapshotFrameData (core/websocket_frames.go) already carries Phase/LastPrice/HasLast/ChangePct/Quote{BestBid,BidQty,HasBid,BestAsk,AskQty,HasAsk}/Depth{Bids,Asks []MarketDataDepthLevel{Price,Qty}}. 3a's current-price read needs LastPrice/HasLast; 4a's liquidity channel needs Depth (and the sided HasBid/HasAsk); Phase is a free pass-through for 4a/4c. Grow the struct:

go
// store/market_data_store.go — EXTENDED
type Quote struct {
    BidPrice, AskPrice, BidQty, AskQty, Timestamp int64   // shipped, unchanged
    LastPrice int64  `json:"last_price"`
    HasLast   bool   `json:"has_last"`
    Phase     string `json:"phase"`
    HasBid    bool   `json:"has_bid"`   // mirror the WS top-of-book gates
    HasAsk    bool   `json:"has_ask"`
    Depth     QuoteDepth `json:"depth"`
}
type QuoteDepth struct { Bids, Asks []QuoteLevel `json:"bids"` ; ... }
type QuoteLevel struct { Price, Qty int64 }

Dependency on Phase 5 (not spec'd here): the field that fills these from the snapshot frame is the 2c ingest mapping (Phase 5's ingest goroutine). Tier 3 extends the struct + adds the reader (below); Phase 5's ingest must map snap.LastPrice/HasLast/Phase/Quote.Has*/Depth → Quote.* on WriteQuote. Flagged as a coordination point, not a Tier-3 body.

[decided here] Carry HasBid/HasAsk (the frame has them; a liquidity read needs to tell "no bid" from "bid at 0"). Drop ChangePct — derivable, unread. WriteQuote stays one SET (bigger JSON blob, same key); no schema/key change.

(b) Market-data readers (NEW — MarketDataStore has writers only). Add to the interface + RedisMarketDataStore:

go
// store/market_data_store.go — NEW on the interface
ReadCandles(ctx, exchangeID, instrument, timeframe string, limit int) ([]Candle, error) // LRANGE CandleKey, -limit..-1, json-decode each
ReadQuote  (ctx, exchangeID, instrument string)        (Quote, bool, error)              // GET QuoteKey; bool=present (key may not exist yet)

ReadCandles reads the bounded trailing window via LRANGE key -limit -1 (limit = K, Point 1e) — the candle list grows all session (AppendCandle RPUSHes), so an unbounded LRANGE 0 -1 would make a late-session tick read far more than any agent uses. A cache miss (key absent / short list) returns what exists; "not present" is a normal path (Point 10), not an error.

(c) Runtime working-memory writers + field constants (NEW). store/keys.go deliberately omits working-memory field names ("come with their writer design in a downstream stage"); RuntimeSnapshot (store/runtime_store.go) already declares every field. Add the field constants + the writers to the RuntimeStore interface + RedisRuntimeStore (thin HSETs mirroring the shipped SetPresence/SetTimers, write-on-change). These are written by decide() (Tier 4) via the handle 3a hands it — Tier 3 declares the surface, Tier 4 calls it:

go
// store/keys.go — NEW field constants (alongside FieldPresence/FieldEntryTime/…)
//   FieldStandingOpinion="standing_opinion", FieldStandingStance="standing_stance",
//   FieldWaitStreak="wait_streak", FieldLastDecidedReadings="last_decided_readings",
//   FieldPositionContext="position_context", FieldLastActedTipID="last_acted_tip_id",
//   FieldTracking="tracking", FieldMyExitLevel="my_exit_level", FieldSubmitAt="submit_at",
//   + the 4a perception-gate reference values (last-read price/spread/volume/range-pos/phase)
// store/runtime_store.go — NEW writers on the RuntimeStore interface
SetStandingOpinion(ctx, ex, id, opinion float64, stance string) error
SetWaitStreak(ctx, ex, id string, streak int) error
SetPositionContext(ctx, ex, id string, pc *PositionContext) error
SetTipFollowState(ctx, ex, id string, t *Tracking, lastActedTipID string, myExitLevel, submitAt int64) error
SetPerceptionRefs(ctx, ex, id string, refs PerceptionRefs) error   // 4a gate carried state
// NAMED-ONLY (Tier 6 restart rebuild): ReadSnapshot(ctx, ex, id) (RuntimeSnapshot, bool, error)

(d) RuntimeHandle — the bound working-memory accessor [decided here]. The shipped writers all take (ctx, exchangeID, agentID, …). Rather than have decide() juggle (ex, id) on every write, Tier 3 hands Tier 4 a small bound handle:

go
type RuntimeHandle struct { rt store.RuntimeStore; ctx context.Context; ex string; id agent.AgentID }
// methods forward to rt with ex/id bound: h.SetStandingOpinion(op, stance), h.SetWaitStreak(n),
//   h.SetPositionContext(pc), h.SetPerceptionRefs(refs), … and matching in-memory getters.

Not a shipped type — an ergonomic Tier-3 invention so the Tier-4 seam is one object. Working-memory Redis writes go direct on the goroutine (D6).

(e) CurveEffective is real (Phase-4 delta). orchestrator/generator.CurveEffective is a populated four-field struct (EntryRate/StepOutRate/DoneRate/ReentryRate float64, events/unit-time, strictly positive). The real scenario.ScenarioEngine is wired in main.go (not NoOpScenarioProvider). Effective("", "") returns (nil, nil) — baseline carries no curve_eff. Tier 3 consumes a *generator.CurveEffective handed in (Point 2); it does not import scenario/ or read sessions:active itself.

(f) D12 — orchestrator dependency injection + composition root. As-built Orchestrator (orchestrator/orchestrator.go) holds only generator/accountOpen/capital/rngSource/config/logger/validScenarios. The goroutine (a method on *Orchestrator) needs direct handles to call Core + the stores. main.go currently builds agentStore/leaseAllocator/pendingMintWAL but not the runtime or market-data Redis stores. Add:

go
// orchestrator struct — NEW fields: core *core.Client, rt store.RuntimeStore,
//   md store.MarketDataStore, agents store.AgentStore
// cmd/agentsd/main.go run() — NEW builds + inject:
//   rt := store.NewRedisRuntimeStore(redisClient); md := store.NewRedisMarketDataStore(redisClient)
//   → passed into NewOrchestrator(…)

(These handles also serve Phase 5's 2b/2c; Tier 3 only requires that they exist on the orchestrator.)

(g) New config dials on OrchestratorConfig [decided here]. As-built config has PopulationSize/LifecycleBuffer/CapitalParams/ValidScenarioNames. Add:

go
TAction        time.Duration   // action-tick floor (D1)
TPresence      time.Duration   // presence-sweep cadence; ≫ TAction (D1)
MaxLookbackK   int             // bounded candle-window size (Point 1b/5a)
PunchBuffer    int             // dispatcher channel buffer (Point 7)
LifecycleCurve LifecycleCurveParams  // Tier 3's OWN curve calibration (Point 8a)

All calibration dials, none fixed in design. LifecycleCurveParams is the Tier-3-owned baseline rates + shapes (Point 8a).

Decisions. The field-constant names and RuntimeHandle are [decided here]. The runtime reader (ReadSnapshot) is named only — Tier 6 builds it for the restart rebuild; Tier 3 never reads runtime on the live path (it builds the working set from the in-memory population handover, Point 4).

Point 2 — The handoff contract into Tier 3

What it is. What startScheduler must be handed. This is the Tier-3 ↔ Phase-5 seam.

Expected & why. Tier 3 is reached by Phase 5's runAgentPopulation calling o.startScheduler(...) on the per-exchange goroutine, after persist/snapshot/entry-stamp/pre-open/go-live. Everything Tier 3 needs must arrive in that call; Tier 3 does not read sessions:active (Phase 5 owns that read — the reader doesn't exist yet and is Phase 5's to build).

Ingress/egress. Ingress: the four inputs below. Egress: a running loop + dispatcher; the lifecycle channel feeds the orchestrator drain (Point 9).

The implementation.

go
func (o *Orchestrator) startScheduler(
    request   transport.BringUpRequest,         // clock (SessionStart/SessionEnd), Instruments, ExchangeID, SessionType
    population Population,                       // generator.Population{Existing, New}; .All() iterates both
    curveEff  *generator.CurveEffective,        // nil = baseline (Tier 3 uses its own rates); non-nil = four shifted rates
    lifecycleCh chan LifecycleDecision,          // created in 2b (buffer = OrchestratorConfig.LifecycleBuffer)
)

curveEff is the whole of the scenario carry. Per the corrected model: baseline session → curveEff == nil → 3b uses its own baseline rates (Point 8a); scenario session → curveEff != nil → 3b uses the four shifted rates. However Phase 5 obtains it (reads sessions:active, or re-resolves the scenario) is Phase 5's call.

Decisions. [decided here] Tier 3 takes curveEff as a parameter rather than reading sessions:active itself — keeps Tier 3 out of the (Phase-5-owned) session-store reader and keeps startScheduler's inputs explicit. Of the four rates, Tier 3 consumes three (StepOut/Done/Reentry); EntryRate is consumed by 2c's entry-time stamping (Phase 5), not by Tier 3 — the entry curve is realized at entry-stamp time, not in the Tier-3 curve builder.

Point 3 — startScheduler (the ignition)

What it is. Build the working set, realize the lifecycle curves once, spawn the dispatcher, enter the loop.

The implementation.

go
func (o *Orchestrator) startScheduler(request …, population …, curveEff *generator.CurveEffective, lifecycleCh …) {
    ws     := o.buildWorkingSet(request, population)                 // Point 4
    curves := o.buildLifecycleCurves(curveEff, ws.rng)              // Point 8a — baseline-or-shifted
    punchCh := make(chan core.OrderPunchRequest, o.config.PunchBuffer)
    go o.runDispatcher(request.ExchangeID, punchCh)                  // Point 7
    o.runLoop(request, ws, curves, punchCh, lifecycleCh)            // Point 5 — blocks until session end / Tier-6 teardown
}

Decisions. [decided here] startScheduler runs the loop synchronously — it is the goroutine's tail (the goroutine was spawned by 2b's go o.continueBringUp → runAgentPopulation). The only new goroutine Tier 3 spawns is the dispatcher; the 2c ingest goroutine is already running (Phase 5).

Point 4 — The in-memory working set (the goroutine's authority)

What it is. The one in-memory structure the tick runs on, built from the handover. Redis is the write-on-change mirror for restart (rebuild = Tier 6); the tick never reads Redis for agent state.

Expected & why. Per-agent frozen traits + handle + session capital + live presence/timers + the Tier-4 working-memory handle; a derived ACTIVE subset the hot path iterates, starting empty (all DORMANT), mutated only by the presence sweep (Point 8). Running the action tick before the sweep each pass means the subset never races the decide loop.

The implementation.

go
type workingAgent struct {
    traits        agent.TraitVector   // frozen (from population)
    capitalHandle string              // Core identity at order time (agent.CapitalHandle)
    capital       int64               // session A (agent.TradingCapital)
    presence      string              // store.PresenceDormant|Active|Idle|Done|Retired (starts DORMANT)
    entryTime     float64             // normalized [0,1]; stamped in 2c, read here
    nextCheckTime int64               // clock-2 re-arm (unix); set on ACTIVATE
    idleSince     int64               // stamped on step-out→IDLE; feeds re-entry/give-up
    wm            *RuntimeHandle       // Tier-4 working-memory + 4a gate-state accessor (Point 1d)
}
type workingSet struct {
    agents map[agent.AgentID]*workingAgent  // full set
    active map[agent.AgentID]*workingAgent  // derived ACTIVE subset (starts empty) — a Go map (D: see below)
    start, end time.Time                     // request.SessionStart/SessionEnd — the scheduler clock
    instruments []string
    rng *rand.Rand                           // fresh seeded (LIVE); act-coin + emit shuffle + lifecycle rolls
}

Decisions. [decided here] The ACTIVE subset is a plain Go map — Ansh confirmed the seeded/tuning mode need not reproduce the action phase agent-for-agent, so map-iteration randomness under a fixed seed is acceptable; no ordered-slice machinery. Not-loaded agents are excluded at build — agents whose pre-open per-agent join failed (Phase 5 marks them) never enter agents, so they never tick or sweep (handover §5). entryTime comes from the 2c stamp. wm binds (rt, ex, id). Positions/cash/P&L are never in workingAgent — read fresh via GetObservation inside decide() (ownership spine).

Point 5 — The loop (two cadences on one goroutine, sleep-to-next) — D1

What it is. One loop, two independent "is it due?" timers over one wall clock: TAction (market-driving) and TPresence ≫ TAction (lifecycle sweep). Run whichever is due, then sleep to the nearest next-due.

Expected & why. TAction is a floor, not a guarantee — the sleep is computed after the work, so a cheap tick paces evenly and a heavy tick clamps the sleep to 0 and chains flat-out (self-correcting). The heavy-decide() regime is fixed by the deferred DECIDE worker-pool (Point 6), not the loop. ACTION before PRESENCE when both are due → presence changes affect future ticks only → race-free active set.

The implementation.

go
func (o *Orchestrator) runLoop(request …, ws *workingSet, curves *lifecycleCurves, punchCh …, lifeCh …) {
    var lastAction, lastPresence time.Time
    for !o.sessionOver(request) {                                     // sessionOver = Tier-6/clock hook (Point 10)
        now := time.Now()
        if now.Sub(lastAction)   >= o.config.TAction   { o.runActionPhase(ws, curves, punchCh); lastAction = now }     // 3a
        if now.Sub(lastPresence) >= o.config.TPresence { o.runPresencePhase(ws, curves, lifeCh); lastPresence = now }  // 3b
        nextDue := minTime(lastAction.Add(o.config.TAction), lastPresence.Add(o.config.TPresence))
        time.Sleep(maxDur(0, nextDue.Sub(time.Now())))                // clamps to 0 under heavy load
    }
}

Decisions. [decided here] TAction/TPresence are config dials (Point 1g). sessionOver is a Tier-6/clock hook — prototype placeholder = time.Now() >= request.SessionEnd; real teardown/WindDown is Tier 6. 1 tick = TAction; the presence sweep is not a "tick."

Point 6 — 3a: the ACTION phase (the hot path, 4 steps) — D2, D3

What it is. runActionPhase — SNAPSHOT → DECIDE → COLLECT → EMIT, serial on the goroutine.

Expected & why. Read the market once per tick and share it; iterate the ACTIVE subset only; gate decide() behind the per-agent act-coin; hand the batch to the dispatcher non-blocking. The 4-step split is preserved so DECIDE can go to a worker pool later without touching SNAPSHOT/COLLECT/EMIT (deferred).

The implementation.

go
func (o *Orchestrator) runActionPhase(ws *workingSet, curves *lifecycleCurves, punchCh chan core.OrderPunchRequest) {
    view := o.snapshot(ws)                                          // 1 SNAPSHOT (Point 6a)
    batch := make([]core.OrderPunchRequest, 0, len(ws.active))
    for _, a := range ws.active {                                   // 2 DECIDE — ACTIVE subset only
        if ws.rng.Float64() >= a.traits.PAct { continue }           // per-tick act-coin (stateless, session RNG)
        in := DecideInput{ agent: a, market: view,
            observe: func() (core.AccountObservation, error) { return o.core.GetObservation(o.ctx, a.capitalHandle) } }
        if req, ok := decide(in); ok { batch = append(batch, req) } // 3 COLLECT — pure gather
    }
    ws.rng.Shuffle(len(batch), func(i, j int) { batch[i], batch[j] = batch[j], batch[i] })  // 4 EMIT — seeded shuffle
    for _, req := range batch { select { case punchCh <- req: default: o.dropPunch(req) } }  // non-blocking
}

(6a) SNAPSHOT — the shared raw read. Loads the bounded window + quote per instrument into memory. Tier 3 produces the raw holder; the interpretive read accessor is Tier 4's (LAYER_2_TIER4_4A_PERCEPTION §4 — Tier 3 does the bulk cache→memory copy, Tier 4's accessor serves per-agent last-N/range over it).

go
type snapshotView struct { candles map[string][]store.Candle; quote map[string]store.Quote; present map[string]bool }
func (o *Orchestrator) snapshot(ws *workingSet) *snapshotView {
    v := &snapshotView{…}
    for _, inst := range ws.instruments {
        v.candles[inst], _ = o.md.ReadCandles(o.ctx, ws.ex, inst, store.Timeframe1m, o.config.MaxLookbackK)  // bounded K
        q, ok, _ := o.md.ReadQuote(o.ctx, ws.ex, inst); v.quote[inst] = q; v.present[inst] = ok
    }
    return v
}

(6b) The Tier-4 seam — exactly three things.

go
type DecideInput struct {
    agent   *workingAgent                            // (1) frozen traits + RuntimeHandle (working memory + 4a gate state)
    market  *snapshotView                            // (2) shared raw window + quote (read-only this tick); Tier-4 accessor wraps it
    observe func() (core.AccountObservation, error)  // (3) bound fresh-read = o.core.GetObservation(capital_handle)
}
func decide(in DecideInput) (core.OrderPunchRequest, bool)   // Tier-4 body — 3a never looks in

Decisions. [decided here] Current price for the tick = quote.LastPrice (D4), gated by HasLast; an instrument with present==false or HasLast==false reads absent this tick (degrade, Point 10). How often decide() calls observe() and its latency is Tier-4's concern — 3a only binds the accessor. The synchronous GetObservation round-trip inside decide() is the one honest hot-path cost; the designated fix is the DECIDE worker-pool (D2 seam) — deferred, prototype runs DECIDE serial, zero agent goroutines. EMIT is select…default non-blocking; a full channel drops rather than blocks (dropPunch = log + metric; fire-and-forget means a dropped punch reconciles as "never sent" on a later GetObservation). The Tier-4 read accessor is not defined here (it's 4a's); snapshotView is the raw structure it reads over.

Point 7 — The dispatcher goroutine + fire-and-forget punch — D10, D5

What it is. One dispatcher goroutine per exchange, draining the punch channel and calling o.core.PunchOrder, ignoring the 202.

Expected & why. Punch is fire-and-forget (core.PunchOrder → POST /v1/orders → 202 {order_id} ignored; no order ids cached). A reject (409 no_session, 422, 504) is observed after the fact on a later GetObservation. L2 sends tif (D5): Tier-4's executor sets it; the client's validateOrderPunchRequest enforces the pairing (limit ⇒ tif=gtc + price>0; market ⇒ tif=ioc + price==0; never fok) — confirmed in core/order_punch.go.

The implementation.

go
func (o *Orchestrator) runDispatcher(ex string, punchCh chan core.OrderPunchRequest) {
    for req := range punchCh {                 // drains until channel closed (Tier-6 teardown)
        _ = o.core.PunchOrder(o.ctx, req)      // 202 ignored; writes NEVER retried (504 ≠ nothing happened)
    }
}
// req = core.OrderPunchRequest{AccountID: capital_handle, Instrument, Side, Type, TIF, Qty, LimitPrice} — Tier-4 fills

Decisions. The dispatcher makes no other Core call; order outcomes are read fresh via GetObservation inside decide(), never correlated here. Precise order-id correlation (A2 fallback) is recorded, not built — switch only if Tier-4 cancels/exits need it.

Point 8 — 3b: the PRESENCE phase (lifecycle sweep) — D6, D7, D8

What it is. runPresencePhase — the lifecycle sweep over the working set on TPresence: the curve builder (once, at startScheduler), the two clocks, the verbatim rolls, the write split. Runs after the action tick each pass it's due → mutates the ACTIVE subset race-free.

8a — The lifecycle curves (built once, Tier 3 OWNS the baseline)

Per the corrected model: Tier 3's curve builder owns the baseline rates (its own calibration, OrchestratorConfig.LifecycleCurve). curveEff (Point 2) overrides only when non-nil.

go
type LifecycleCurveParams struct {   // Tier-3-owned calibration (Point 1g) — baseline rates + ALL shapes
    StepOutRate, DoneRate, ReentryRate float64   // baseline heights/peaks (overridden by curveEff when present)
    TauOut, TauDone float64                       // ramp shapes — NEVER scenario-shifted
    GPSigmaSqOut, GPEllOut, GPSigmaSqDone, GPEllDone float64  // per-curve GP (RBF) shape — never shifted
    MuBreak, SigmaBreak float64                   // re-entry Gaussian — never shifted
    QMaxGiveUp, TauGiveUp float64                 // give-up — NOT in curve_eff at all
    // (EntryRate/entry-curve params live with 2c's entry stamping, not here)
}
type lifecycleCurves struct {
    lambdaOut  func(tNorm float64) float64   // s·(1−e^{−t/τ_out})·e^{g_out(t)}     (LAYER_2_LIFECYCLE §5)
    lambdaDone func(tNorm float64) float64   // D·e^{−(1−t)/τ_done}·e^{g_done(t)}   (LAYER_2_LIFECYCLE §7)
    reentry    func(d float64) float64       // p_max·exp(−(d−μ)²/2σ²)  closed-form (§6) — no realization
    giveUp     func(d float64) float64       // q_max·(1−e^{−(d−μ)/τ})  closed-form (§7) — no realization
}
func (o *Orchestrator) buildLifecycleCurves(curveEff *generator.CurveEffective, rng *rand.Rand) *lifecycleCurves {
    p := o.config.LifecycleCurve                 // baseline (Tier-3-owned)
    s, D, pMax := p.StepOutRate, p.DoneRate, p.ReentryRate
    if curveEff != nil { s, D, pMax = curveEff.StepOutRate, curveEff.DoneRate, curveEff.ReentryRate }  // scenario override
    // realize g_out, g_done GP samples (seeded, this session); bind λ_out, λ_done over {s,D} + fixed shapes;
    // bind reentry over {pMax, μ_break, σ_break}; bind giveUp over {q_max, τ_giveup} (never shifted).
}

[CONFIRMED 2026-10-19] — the rate→constant binding. The four CurveEffective rates map onto the curve constants as: EntryRate→A (entry, consumed by 2c), StepOutRate→s (step-out plateau), DoneRate→D (done height), ReentryRate→p_max (re-entry peak) — confirmed by Ansh. Give-up (q_max/τ_giveup) and all shape params are not in curve_eff, consistent with give-up being the complement mechanism.

8b — Clock 1: entry (checked every sweep) — verbatim
go
nowNorm := float64(now.Sub(ws.start)) / float64(ws.end.Sub(ws.start))   // (now−SessionStart)/(SessionEnd−SessionStart)
if a.presence == store.PresenceDormant && nowNorm >= a.entryTime && o.ceilingHook(len(ws.active)) {  // D8 no-op (=N)
    a.presence = store.PresenceActive
    a.nextCheckTime = now.Add(randDur(ws.rng, 20*time.Minute, 40*time.Minute)).Unix()
    ws.active[id] = a
    o.rt.SetPresence(o.ctx, ws.ex, id, store.PresenceActive)
    o.rt.SetTimers(o.ctx, ws.ex, id, a.nextCheckTime, 0)                 // Redis direct (D6)
}
8c — Clock 2: recurring rolls (per agent, when next_check_time ≤ now) — verbatim, f_i=1, c_fat=0

Δ = the agent's realized check interval (elapsed since its last check; nominal 20–40 min). Rate→per-check prob uses ·Δ for step-out/done; re-entry/give-up are already per-check (no Δ), per LAYER_2_LIFECYCLE §6–7.

go
// ACTIVE — roll DONE first (4a), then step-out (#2); done supersedes step-out near close
if a.presence == store.PresenceActive && now.Unix() >= a.nextCheckTime {
    pDone := clamp(curves.lambdaDone(nowNorm) * 1 * 1 * Δ, 0, 1)         // λ_done·fatigue·f_i·Δ
    if ws.rng.Float64() < pDone {
        a.presence = store.PresenceDone; delete(ws.active, id); o.rt.SetPresence(o.ctx, ws.ex, id, store.PresenceDone)
    } else {
        pOut := clamp(curves.lambdaOut(nowNorm) * 1 * Δ, 0, 1)           // λ_out·f_i·Δ
        if ws.rng.Float64() < pOut {
            a.presence = store.PresenceIdle; a.idleSince = now.Unix(); delete(ws.active, id)
            o.rt.SetPresence(o.ctx, ws.ex, id, store.PresenceIdle)
            o.rt.SetTimers(o.ctx, ws.ex, id, a.nextCheckTime, a.idleSince)
        }
    }
    a.nextCheckTime = now.Add(randDur(ws.rng, 20*time.Minute, 40*time.Minute)).Unix()
    o.rt.SetTimers(o.ctx, ws.ex, id, a.nextCheckTime, a.idleSince)       // Redis direct
}
// IDLE — roll re-entry first (#3), then give-up (4b) if not returned
if a.presence == store.PresenceIdle && now.Unix() >= a.nextCheckTime {
    d := float64(now.Unix() - a.idleSince)                              // idle_duration
    if ws.rng.Float64() < clamp(curves.reentry(d)*1, 0, 1) {           // r(d)·f_i ; NO Δ
        a.presence = store.PresenceActive; a.idleSince = 0; ws.active[id] = a
        a.nextCheckTime = now.Add(randDur(ws.rng, 20*time.Minute, 40*time.Minute)).Unix()
        o.rt.SetPresence(o.ctx, ws.ex, id, store.PresenceActive); o.rt.SetTimers(o.ctx, ws.ex, id, a.nextCheckTime, 0)
    } else if ws.rng.Float64() < clamp(curves.giveUp(d)*1, 0, 1) {     // q(d)·f_i ; NO Δ
        a.presence = store.PresenceDone; o.rt.SetPresence(o.ctx, ws.ex, id, store.PresenceDone)
    } else {
        a.nextCheckTime = now.Add(randDur(ws.rng, 20*time.Minute, 40*time.Minute)).Unix()
        o.rt.SetTimers(o.ctx, ws.ex, id, a.nextCheckTime, a.idleSince)
    }
}
8d — The write split (D6) + RETIRE
ACTIVATE / step-out→IDLE / done→DONE / give-up→DONE → in-memory + Redis direct, inline (SetPresence/SetTimers, write-on-change). Positions/P&L never snapshotted.
RETIRE (for-good) is not a per-sweep roll — it is the end-of-session quit roll (#6, LAYER_2_LIFECYCLE §9), fired once at session close. Tier 3 defines the channel + message + drain (Point 9); the roll execution (square-off → quit roll → Retire + Core close) is Tier 6. So in the prototype sweep, nothing sends RETIRE down lifeCh yet.

Decisions. [D7 carried] math verbatim, f_i=1, c_fat=0. [D8 carried] ceilingHook defaults to no-op (=N); X ≤ active ≤ N balancing + blocked-entry PARKED. [decided here] Δ = realized check interval. Mid-session join (#5), P&L-driven DONE, f_i trait factors remain out.

Point 9 — The lifecycle channel + orchestrator drain — D6

What it is. The LifecycleDecision message type + the orchestrator-side drain that applies a decision.

The implementation.

go
type LifecycleDecision struct { AgentID agent.AgentID; Reason string }   // Reason: "retire_for_good" (today the only durable route)
func (o *Orchestrator) runLifecycleDrain(lifeCh chan LifecycleDecision) {
    for d := range lifeCh {
        switch d.Reason {
        case ReasonRetire:
            _ = o.agents.Retire(o.ctx, d.AgentID, time.Now())   // Postgres retired_at (store.AgentStore.Retire — shipped)
            // Core account-close + square-off/quit-roll = TIER 6 (named here, wired there)
        }
    }
}

Decisions. [decided here] LifecycleDecision carries {agent_id, reason} only — no position/P&L payload (read fresh at close-out, Tier 6). Long-lived orchestrator goroutine over the per-session channel (buffer = LifecycleBuffer). AgentStore.Retire(ctx, id, retiredAt) is the shipped target; the Core close that pairs with it is Tier 6, so a RETIRE applied today marks Postgres only.

Point 10 — Failure policy (degrade, never abort) — D11
SNAPSHOT read miss / empty cache (present==false, short list) → that instrument reads flat/absent this tick; no crash. "not present" is a normal ReadCandles/ReadQuote path.
GetObservation error inside decide() (409 no_session, 5xx, timeout) → that agent skips/defers this tick; the client's idempotent-read retry absorbs a transient blip; a persistent no_session reconciles on a later tick.
PunchOrder error → ignored by design (fire-and-forget); reconciled on the next GetObservation. Writes never retried.
Core persistently unreachable → the tick keeps running against cached market data; sustained-outage handling is Tier 6.

No failure in a tick tears the session down; teardown is always a Tier-6 / WindDown concern (incl. sessionOver).

Ordering & concurrency invariants (LOCKED, carried from design §9)
One goroutine per exchange runs the whole tick serially — SNAPSHOT, DECIDE, COLLECT, EMIT, and the presence sweep all on it. No agent goroutines in the prototype.
ACTION before PRESENCE each pass both are due → presence changes affect future ticks only → race-free active set, no locking.
Only two other goroutines per session: 2c ingest (WS→Redis, Phase 5) and the Tier-3 dispatcher (channel→punch). The orchestrator lifecycle drain is cross-session (reads only the channel). None touch the working set.
Within-tick determinism holds; global cross-tick settlement timing is NOT bit-reproducible (async dispatcher). Accepted; strict global repro is a joint L2+Core concern, deferred.
What Tier-3 impl guarantees
The forced-delta set is complete against real signatures: D4 quote extension (+ the reader), bounded ReadCandles/ReadQuote, runtime working-memory writers + field constants + RuntimeHandle, D12 orchestrator DI + the two Redis stores built in main.go, and the new config dials.
startScheduler takes (request, population, curveEff, lifecycleCh); builds the working set (map; not-loaded excluded); realizes step-out + done curves once with Tier-3-owned baseline, overridden by curveEff when a scenario is set; spawns the dispatcher; runs the loop.
The two-cadence loop runs ACTION on TAction (a floor) and PRESENCE on TPresence, ACTION-before-PRESENCE, sleep-to-next, self-correcting under load — the DECIDE worker-pool seam preserved.
3a reads the market once per tick (bounded K), iterates the ACTIVE subset, gates decide() by the act-coin, hands shuffled punches to the dispatcher non-blocking; decide() gets the three-part seam; the punch is fire-and-forget with L2-sent tif.
3b advances presence through the two clocks with verbatim lifecycle math; ACTIVATE/IDLE/DONE write Redis direct; RETIRE plumbing defined, its roll/close execution deferred to Tier 6.
Degrade-not-abort throughout; teardown is always Tier 6.
Open / deferred / parked (Tier-3 impl)
[CONFIRMED 2026-10-19] — the four-rate → curve-constant binding (Point 8a): EntryRate→A(2c), StepOutRate→s, DoneRate→D, ReentryRate→p_max. Locked by Ansh; no longer open.
Phase-5 coordination (not Tier 3): the 2c ingest mapping must populate the new store.Quote fields; startScheduler's curveEff is supplied by Phase 5's sessions:active read.
Tier-4 internals — decide() body + the read accessor over snapshotView; theta_act/theta_add open; channel→trait map owed. 3a provides the seam only.
DECIDE worker-pool — the sole change for the heavy-decide() regime; recorded, not built.
Tier 6 — RETIRE execution, restart rebuild (runtime ReadSnapshot + reconcile), WS reconnect, abort-cleanup, the X-floor, the sessionOver/teardown hook that closes the loop + the punch channel + the lifecycle channel.
Calibration — TAction, TPresence, MaxLookbackK, PunchBuffer, the baseline rates + all curve shapes in LifecycleCurveParams, the 20–40 min bounds, Δ — all config dials, none fixed.
What Tier 3 hands onward

To Tier 4: the DecideInput seam — frozen traits + the RuntimeHandle (working memory + 4a gate state), the shared raw snapshotView + quote, and the bound GetObservation fresh-read. Tier 4 returns a core.OrderPunchRequest (or nothing); everything inside decide() — and the read accessor over snapshotView — is Tier 4.

To Tier 6 (named here, executed there): RETIRE execution; the restart rebuild (runtime ReadSnapshot + reconcile); WS reconnect; abort-cleanup; the X-floor; the sessionOver/teardown hook.

End of Tier 3 implementation design. Design source: L2_TIER3_RUNTIME_LOOP_DESIGN (D1–D12). Signatures verified against the shipped simulation-agents repo (core/store/orchestrator/scenario/cmd), 2026-10-19. Boundary contract: L2_TIER0_CORE_API_CONTRACT. Lifecycle math: LAYER_2_LIFECYCLE. Handoff from: Phase 5 (continueBringUp/runAgentPopulation).