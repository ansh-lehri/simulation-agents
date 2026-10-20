Layer 2 — Tier 2: Bring-up (Implementation Design)

What this doc is. The build specification for bring-up — the orchestration code that sequences the Tier-1 building blocks (L2_TIER1_IMPLEMENTATION_DESIGN) to stand up a session: HandleBringUp → the 2a spine → account-open → the 2b async tail → the 2c goroutine → go-live. Point format: what it is / expected & why / cross-layer / ingress-egress / the implementation / decisions. Go. Read L2_TIER2_BRINGUP_DESIGN and the Tier-1 impl doc first.

Required companion. The scenario parameter/lifecycle manipulation that feeds generation is a required part of Tier 2, implementation-designed in L2_SCENARIOS_IMPLEMENTATION_DESIGN. This doc is its caller: Point 2 (generateOnDemand → o.scenario.Effective → EffParams into mintAgent/drawTraits) and Point 5 (curve_eff into sessions:active). "Scenarios won't be coded" means no if-else inducement — it does not mean this manipulation is optional; it is how a scenario is realized without if-else, and it runs inside Tier-2 generation.

Grounding. All from the locked 2a/2b/2c decisions (project decision log) and the Tier-1 impl blocks. Code is illustrative of shape/contract; cross-layer call wire-signatures are pinned against Core's real endpoints at build time (cross-layer boundary rule — L2 specs only its request/response). New impl choices tagged [decided here].

Scope. In: intake, spine, both generation paths, the WAL stage, account-open client (L2 side), capital resolution dispatch, continueBringUp (persist/snapshot/enlist/spawn), runAgentPopulation (entry_time, three pre-open ops, go-live), failure routing. Out: trait draw internals (→ POPULATOR/SCHEMA math + the generation-internals note below); the scenario param/lifecycle manipulation machinery that produces EffParams/curve_eff (a required Tier-2 generation companion — implementation-designed in L2_SCENARIOS_IMPLEMENTATION_DESIGN; this doc calls it at Points 2 and 5); the tick loop body (Tier 3); release/reconcile/WAL-drain execution (Tier 6).

Point 1 — Intake + the 2a spine

What it is. The async BRING_UP entry point and the synchronous spine that runs Stages A/B/C over one Population.

Expected & why. Async: validate → run spine → return once account-open completes → launch the 2b tail with go. The spine order is forced by data dependency (A→B→C). Validation covers only what 2a dispatches on; the rest is carried.

Cross-layer. Inbound from Core (network). Only Stage B touches Core.

Ingress/egress. Ingress: trigger inputs. Egress: an async ack (carrying the session handle); internal hand-off of Population to 2b.

The implementation.

go
type BringUpRequest struct {
    ExchangeID, SessionType, Country string
    Start, End time.Time
    Instruments []string
    BrokerID string
    Scenario string // optional, on-demand only; "" = none (→ Scenarios impl)
}

func (o *Orchestrator) HandleBringUp(req BringUpRequest) (BringUpAck, error) {
    if err := o.validateTrigger(req); err != nil { return BringUpAck{}, err } // bad trigger → before anything created
    rng := o.rng.Fresh()                                   // LIVE = fresh randomness (seeding is tuning-only)
    pop, err := o.bringUp(req, rng)                        // Stages A/B/C (sync)
    if err != nil { return BringUpAck{}, err }             // generation/account-open failure → surface; nothing spawned
    go o.continueBringUp(req, pop)                         // 2b/2c async tail
    return BringUpAck{ExchangeID: req.ExchangeID, Status: "accounts_ready"}, nil
}

func (o *Orchestrator) bringUp(req BringUpRequest, rng *RNG) (Population, error) {
    var pop Population
    switch req.SessionType {                               // STAGE A
    case Canonical: pop = o.gen.generateCanonical(req.Country, o.params.N, rng)
    case OnDemand:  pop = o.gen.generateOnDemand(req.Country, req.ExchangeID, o.params.N, rng, req.Scenario)
    }
    if err := o.accounts.Open(pop.New); err != nil { return Population{}, err } // STAGE B (new only)
    o.capital.Resolve(pop, req.SessionType, rng)           // STAGE C
    return pop, nil
}

Decisions. validateTrigger checks session_type (dispatch), country (keys), exchange_id (tags); start/end,instruments,broker_id carried; N is a generation param, not validated off the request. [decided here] Async return point = post-account-open (supersedes old sync lock). The async tail's failures surface via L7 poll-status, not this return. Scenario is threaded into generateOnDemand only (canonical ignores it).

Point 2 — Stage A: generation (both paths)

What it is. Assembling Population{existing, new}; canonical revive-then-mint, on-demand lease-then-mint; the WAL stage at mint. The scenario param manipulation enters here — generateOnDemand resolves the scenario to EffParams and every agent (leased-redrawn + minted) draws from it.

Expected & why. Reuse/lease first, mint only the shortfall. Canonical existing skip account-open (hold handle + persisted A); on-demand existing redraw traits + A (scenario coverage). Exactly one static is_tipper for canonical.

Cross-layer. Reads Postgres (LiveMembers/LoadMany) and Redis lease (SelectAvailable). No broker call.

The implementation.

go
func (g *Generator) generateCanonical(country string, N int, rng *RNG) Population {
    existing := g.store.LiveMembers(ctx, country, Canonical)          // reuse-index query
    var fresh []Agent
    for i := 0; i < N-len(existing); i++ { fresh = append(fresh, g.mint(rng, MintCtx{Canonical, country}, nil)) }
    ensureOneTipper(existing, fresh, rng)                             // one static is_tipper across the set
    return Population{Existing: existing, New: fresh}
}

func (g *Generator) generateOnDemand(country, ex string, N int, rng *RNG, scenario string) Population {
    eff, curveEff := g.scenario.Effective(country, scenario)         // → EffParams + CurveEff (L2_SCENARIOS_IMPLEMENTATION_DESIGN); nil when scenario==""
    g.stashCurveEff(ex, curveEff)                                    // carried to sessions:active in 2b (Point 5) → 3b
    leasedIDs := g.lease.SelectAvailable(country, N, ex)             // atomic pop+mark
    existing := g.store.LoadMany(ctx, leasedIDs)                     // reuse ACCOUNTS...
    for i := range existing { existing[i].Traits = g.drawTraits(rng, eff, MintCtx{OnDemand, country}) } // ...REDRAW traits (scenario coverage)
    var fresh []Agent
    for i := 0; i < N-len(existing); i++ { fresh = append(fresh, g.mint(rng, MintCtx{OnDemand, country}, eff)) }
    return Population{Existing: existing, New: fresh}                 // fresh enlisted into leased in 2b
}

func (g *Generator) mint(rng *RNG, ctx MintCtx, eff *EffParams) Agent {
    a := Agent{ID: newAgentID(), Population: ctx.Population, Country: ctx.Country, Origin: "mint"}
    a.Traits = g.drawTraits(rng, eff, ctx)                           // draw order kind-first; eff==nil → baseline params
    g.wal.Stage(a.ID, StageRecord{a.Traits, a.Population, a.Country, ctx.ExchangeID}) // WAL BEFORE account-open (new only)
    return a                                                         // CapitalHandle/TradingCapital filled in B/C
}

Decisions. drawTraits is the generation internal — kind-first draw order, coupled diet, the distribution samplers, perception/exit derivation from rho/phi/skill — specified by LAYER_2_AGENT_SCHEMA + LAYER_2_POPULATOR + LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION. It takes *EffParams: nil → baseline params; non-nil → scenario-shifted params produced by o.scenario.Effective — implementation-designed in L2_SCENARIOS_IMPLEMENTATION_DESIGN, a REQUIRED Tier-2 generation companion, not optional (the one scenario-aware spot inside drawTraits is the EffParams.PhiBimodal φ branch; every other trait just receives shifted numbers). On-demand leased agents call drawTraits (redraw) rather than keep loaded traits. [from the scenario lock] ensureOneTipper promotes one endogenous agent if no revived one carries the flag.

Point 3 — Stage B: account-open client (contract-only)

What it is. The one bulk network call opening broker accounts for the new set.

Expected & why. Thin payload (capital is broker-side). EnsureAccount idempotent → re-sends safe. Response carries the handle; not funded_cash — see the Tier-0 correction below.

Cross-layer. The 2a broker touch. L2 specs only request/response; how Core batches/fulfills is Core's.

The implementation.

go
func (c *AccountOpenClient) Open(newAgents []Agent) error {
    if len(newAgents) == 0 { return nil }                   // canonical with no shortfall: no-op
    req := AccountOpenRequest{Mode: c.mode, Accounts: toOpenItems(newAgents)} // [{OwnerAgent, agent_id}, ...]
    resp, err := c.core.OpenAccounts(req)                   // network bulk call to Core
    if err != nil { return err }                            // whole-pass failure → surfaced; nothing persisted yet (2b)
    for i := range newAgents {
        newAgents[i].CapitalHandle = resp.ByOwner(newAgents[i].ID).AccountID   // only account_id comes back
    }
    return nil
}

Decisions. Exact wire signature + partial-failure semantics pinned against Core's real bulk endpoint at build (default stance: fail the pass, async tail reports). A network bulk account-open on Core is a Core-side dependency (today's EnsureAccount is per-call in-process). Tier-0 correction: POST /v1/account/open returns only account_id, not funded_cash — so funded_cash is read per agent from GET /v1/account/cash (see L2_TIER0_CORE_API_CONTRACT) and fed to Stage C, not taken from this response.

Point 4 — Stage C: capital resolution (three-case dispatch)

What it is. Filling each agent's TradingCapital via the Tier-1 drawTradingCapital helper, per the three cases.

Expected & why. Draw order account-open→A. funded_cash for new agents is read from GET /v1/account/cash (Tier-0 correction); on-demand existing use default_funded_cash (skipped open); canonical existing keep persisted A.

The implementation.

go
func (r *CapitalResolver) Resolve(pop Population, st SessionType, rng *RNG) {
    for i := range pop.New {                                 // funded_cash read from /v1/account/cash per agent
        fc := r.core.GetAccountCash(pop.New[i].CapitalHandle).TradingCash
        pop.New[i].TradingCapital = drawTradingCapital(scale(pop.New[i]), fc, rng, r.p)
    }
    if st == OnDemand {
        for i := range pop.Existing {                        // leased; skipped open → default ceiling
            pop.Existing[i].TradingCapital = drawTradingCapital(scale(pop.Existing[i]), 0, rng, r.p) // 0 → default_funded_cash
        }
    }
    // canonical existing: keep persisted TradingCapital, untouched
}

Decisions. scale(a) reads a.Traits.CapitalSize. New agents' funded_cash = GET /v1/account/cash → trading_cash (Tier-0 correction, replaces "from the account-open response"). Passing fundedCash=0 for on-demand-existing deliberately triggers the helper's default_funded_cash fallback. [decided here]

Point 5 — 2b: continueBringUp (persist → snapshot → enlist → spawn)

What it is. The async tail: all L2-internal writes, then the goroutine spawn as the final handoff (Fork a).

Expected & why. Persist-before-live; thin DORMANT snapshot (entry_time deferred to 2c); on-demand enlist + runtime tipper; register + spawn. For a scenario session, the curve_eff produced in Point 2 is written into sessions:active here so 3b (and a restart) rebuilds scenario-shaped curves.

Cross-layer. None.

The implementation.

go
func (o *Orchestrator) continueBringUp(req BringUpRequest, pop Population) {
    if err := o.persistNew(pop.New); err != nil { o.failBringUp(req, err); return }  // P2
    for _, a := range pop.All() { o.rt.WriteInitialSnapshot(req.ExchangeID, a) }      // P3 {DORMANT, trading_capital}
    if req.SessionType == OnDemand {                                                  // P4
        for _, a := range pop.New { o.lease.Enlist(req.Country, a.ID, req.ExchangeID) }
        o.rt.SetTipper(req.ExchangeID, pickOne(pop.All(), o.rng).ID)                  // per-session runtime tipper
    }
    ch := make(chan LifecycleDecision, o.cfg.LifecycleBuf)                            // P5
    o.registerSession(req, ch)                                                        // sessions:active (+ curve_eff stashed in Point 2, for 3b)
    go o.runAgentPopulation(req, pop, ch)                                             // handoff → 2c
}

func (o *Orchestrator) persistNew(newAgents []Agent) error {
    for _, a := range newAgents {
        if err := o.store.InsertAgent(ctx, a); err != nil { return err } // idempotent by PK
        o.wal.Clear(a.ID)                                                 // HDEL pending_mint only AFTER durable
    }
    return nil
}

Decisions. On-demand persisted rows carry TradingCapital = NULL (toAgentRow maps on-demand A→NULL); canonical persists A. registerSession writes sessions:active including the scenario curve_eff (produced in Point 2, consumed by the 3b curve builder — L2_SCENARIOS_IMPLEMENTATION_DESIGN Point 7). Failure before spawn → failBringUp; nothing left live.

Point 6 — 2c: runAgentPopulation (pre-open → go-live)

What it is. The goroutine body: stamp entry_time → three concurrent pre-open ops → join → go-live.

Expected & why. entry_time is lifecycle (goroutine-owned). The three ops are independent → concurrent; nothing goes live until all join. Go-live at start_time; agents stay DORMANT.

Cross-layer. Three Core calls (session-load, history, WS); each specs L2's side only.

The implementation.

go
func (o *Orchestrator) runAgentPopulation(req BringUpRequest, pop Population, ch chan LifecycleDecision) {
    for _, a := range pop.All() {                                      // (1) stamp entry_time (L2-internal)
        o.rt.StampEntryTime(req.ExchangeID, a.ID, drawEntryTime(o.rng, req.Start, req.End, o.params.EntryCurve))
    }
    var g errgroup.Group                                              // (2) concurrent fan-out
    g.Go(func() error { return o.loadSessions(req, pop) })            //   session-load (bulk, Core)
    g.Go(func() error { return o.pullHistory(req) })                 //   1m history (Core GET /candles → Redis)
    g.Go(func() error { return o.subscribeLive(req) })               //   WS subscribe per exchange_id → Redis
    if err := g.Wait(); err != nil { o.failBringUp(req, err); return } // (3) join; history/WS failure fatal
    o.waitUntil(req.Start)                                            // (4) go-live at start_time
    o.startScheduler(req, pop, ch)                                    //   → Tier-3 tick loop
    o.reportReady(req)
}

func (o *Orchestrator) loadSessions(req BringUpRequest, pop Population) error {
    resp, err := o.core.LoadSessions(LoadReq{ExchangeID: req.ExchangeID, Mode: req.SessionType, Accounts: handles(pop.All())})
    if err != nil { return err }                                      // total failure → fatal
    for _, r := range resp.Results { if r.Status != Installed { o.markNotLoaded(req.ExchangeID, r.AccountID, r.Error) } } // partial OK
    return nil                                                        // L2 stores nothing else (participant_id broker-side)
}

func (o *Orchestrator) pullHistory(req BringUpRequest) error {
    market := historyMarket(req)                                      // on-demand: canonical(country); canonical: own market
    for _, inst := range req.Instruments {
        bars, err := o.core.GetCandles(CandleReq{Market: market, Instrument: inst, TF: "1m", From: req.HistFrom, To: req.Start})
        if err != nil { return err }                                  // history failure → fatal
        o.md.WriteCandles(req.ExchangeID, inst, "1m", bars)
    }
    return nil
}

func (o *Orchestrator) subscribeLive(req BringUpRequest) error {
    sub, snap, err := o.core.Subscribe(SubReq{ExchangeID: req.ExchangeID, SessionID: req.FeedSessionID, Instruments: req.Instruments})
    if err != nil { return err }                                      // subscribe failure → fatal
    o.cacheSnapshot(req.ExchangeID, snap)                             // join unblocks once snapshot cached
    go o.ingest(req.ExchangeID, sub)                                  // long-lived: candle→append, snapshot→overwrite, session→teardown hook
    return nil
}

Decisions. LoadSessions returns only per-agent success/fail; agent identity to the broker at order time = capital_handle (Tier 3). The live feed opens GET /v1/ws?session= (Tier-0): the WS binds via a session id, so L2 carries a FeedSessionID for the exchange's one feed (one WS per exchange). The subscribe batch on that one WS mixes stream kinds freely: one snapshot sub per instrument plus one candle sub per (instrument × configured timeframe). historyMarket selects the canonical source for on-demand (isolation). The WS ingest goroutine (one per exchange) outlives the join; SessionClosed handling is a Tier-6 teardown hook. Agent live transport = the human WebSocket (L2 is separate-process).

Point 7 — Failure routing

What it is. failBringUp is the single failure exit for the async tail (2b pre-spawn, 2c join).

Expected & why. Surface ready/failed via L7 poll-status; nothing left live on failure.

The implementation.

go
func (o *Orchestrator) failBringUp(req BringUpRequest, err error) {
    // mark session failed for L7 poll-status; abort-cleanup (release leased, drop partial snapshot/md,
    // deregister sessions:active, close WS) is Tier-6 teardown — invoked here, implemented there.
    o.reportFailed(req, err)
}

Decisions. Failure policy: validation → pre-creation return; generation/account-open → surface, nothing spawned; history/WS → fatal; partial session-load → proceed. Abort-cleanup mechanics = Tier 6; restart recovery (persisted rows + WAL drain + lease reconcile) covers post-partial-write crashes — no bespoke rollback here.

What Tier-2 impl guarantees
One async HandleBringUp; a data-dependency-ordered spine; {existing, new} exact; new agents opened (cap-to-A), existing reuse handles.
The WAL staged before open, cleared after durable persist → crash-recoverable mint→open→persist.
All 2b writes orchestrator-side, goroutine spawned last; goroutine wakes persisted+snapshotted.
entry_time stamped by the goroutine; three pre-open ops concurrent + joined; go-live at start_time; agents DORMANT into Tier 3.
Scenario shaping is wired into generation (Point 2) and the curve carry (Point 5), calling the required companion L2_SCENARIOS_IMPLEMENTATION_DESIGN.
Open / deferred (Tier-2 impl)
Core bulk endpoints (account-open, session-load) — Core-side dependencies; L2 payloads are settled. Per-call shapes in L2_TIER0_CORE_API_CONTRACT.
drawTraits generation internals → POPULATOR/SCHEMA/perception-generation.
The scenario param/lifecycle manipulation producing EffParams+curve_eff → L2_SCENARIOS_IMPLEMENTATION_DESIGN — a required Tier-2 generation companion (now written; not deferred, not optional), called at Points 2 and 5.
Release/reconcile/WAL-drain execution, abort-cleanup, respawn, canonical P&L→A fold → Tier 6.
Wire signatures for the Core calls → pinned at build against Core (L2_TIER0_CORE_API_CONTRACT).

End of Tier 2 implementation design. Required companion: L2_SCENARIOS_IMPLEMENTATION_DESIGN (the scenario param/lifecycle manipulation feeding generation at Point 2 and the curve carry at Point 5). Boundary contract: L2_TIER0_CORE_API_CONTRACT.