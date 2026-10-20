Layer 2 — Tier 3: The Runtime Loop (Design)

What this doc is. The design of what a session does once it is live: the per-exchange goroutine's post-go-live tick loop. It is two phases — the ACTION phase (3a), the hot path that drives the market, and the PRESENCE phase (3b), the lifecycle sweep that moves agents through DORMANT → ACTIVE → IDLE → DONE → RETIRED. It is the direct consumer of everything Tier 2 hands over at spawn, and the caller of the Tier-4 decision procedure (decide()). Read L2_OVERVIEW_AND_TOPOLOGY, L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN, L2_TIER2_BRINGUP_DESIGN, and L2_TIER0_CORE_API_CONTRACT first — this doc fills the runAgentPopulation body those leave stubbed and hands off to Tier 4.

Grounding. The 3a/3b core (5-stage tick, fire-and-forget punch, the write-target rule, the two lifecycle clocks) is locked in the project decision log. This doc is the reconciliation of that locked core against the shipped Tier 0/1/2 code (verified against the simulation-agents repo: core/, store/, agent/, orchestrator/). Where the locked design and the shipped code disagree, the delta is called out as a CHANGE with its reason — the code's real signatures win (project rule: code wins over doc). The lifecycle transition mathematics come from LAYER_2_LIFECYCLE (the lifecycle-math doc) and are reproduced unchanged; this tier only decides when the rolls are scanned and who writes the result.

Scope.

In: the scheduler entry (startScheduler), the in-memory working set, the two-cadence loop (the action-tick clock and the presence-sweep clock), the 3a action tick end-to-end (snapshot → decide → collect → emit → dispatch), the 3b presence sweep end-to-end (the two lifecycle clocks, the transition rolls, the write split), the cross-tier access map, the dispatcher goroutine, and the lifecycle→orchestrator channel.
Out: the agent decision internals inside decide() — perception/mixer/execution/exit (that is Tier 4; 3a provides the seam, never looks inside); the scenario engine that produces curve_eff (a future phase — this tier consumes a baseline curve today); teardown / square-off / account-close execution, restart rebuild of the working set, WS reconnect, abort-cleanup (all Tier 6).
1. The frame — where Tier 3 sits

Bring-up (Tier 2) ends by spawning one goroutine per exchange_id (runAgentPopulation) and handing it the session. Everything before go-live — generation, account-open, persist, snapshot, entry-time stamping, the three concurrent pre-open ops (session-load, history pull, WS subscribe) — is Tier 2. Tier 3 is the goroutine's life after go-live: the tick loop that reads the market, runs the active agents through decide(), punches their orders, and advances every agent's presence through the session.

Two facts from the topology (L2_OVERVIEW_AND_TOPOLOGY §2–3) govern every decision below:

One goroutine owns the session. All in-tick work runs serially on that single goroutine. The only other goroutines in the session are the ingest goroutine (spawned in 2c, draining the WS into Redis) and the dispatcher goroutine (spawned by Tier 3, §7). Neither shares the goroutine's agent state.
The ownership spine is absolute. L2 reads market data and own-state fresh from Core / its Redis cache; it stores no positions, cash, P&L, or order ids. Everything the tick needs about an agent's money is a fresh GetObservation read; everything about the market is the Redis snapshot the ingest goroutine maintains.
2. The whole shape
runAgentPopulation (2c) … entry_time stamped · session-load · history · WS subscribe · go-live @ SessionStart
   │
   └─► startScheduler(request, population, lifecycleCh)                        ◄── Tier 3 begins here
          │  (1) build the in-memory WORKING SET from population + sessions:active   (§3)
          │  (2) build the session LIFECYCLE CURVES once (from curve_eff|baseline)   (§6)
          │  (3) spawn the DISPATCHER goroutine (per exchange)                        (§7)
          │  (4) enter the LOOP:
          ▼
      ┌───────────────────────────────────────────────────────────────────────┐
      │ loop (one goroutine):                                                   │
      │   now := now()                                                          │
      │   if now - last_action   >= T_action   → runActionPhase()   (3a) ─┐     │
      │   if now - last_presence >= T_presence → runPresencePhase() (3b)  │     │
      │   sleep( max(0, next_due - now()) )   // sleep-to-next, §4        │     │
      └───────────────────────────────────────────────────────────────────┼─────┘
                                                                           │
      3a ACTION tick (hot path, 4 steps):                                  │
        1 SNAPSHOT  read full OHLCV + quote from Redis → goroutine memory  │
        2 DECIDE    for each ACTIVE agent: flip p_act; if acts, decide() ──┼──► Tier 4
        3 COLLECT   gather produced punches into a batch                   │
        4 EMIT      seeded-shuffle the batch → dispatcher channel ─────────┘ (non-blocking)
                                                                             │
      DISPATCHER goroutine:  drains channel → o.core.PunchOrder (fire-and-forget) ──► Core

      3b PRESENCE sweep (loose path):
        clock 1 (per sweep):   DORMANT agents: now_norm ≥ entry_time → ACTIVATE
        clock 2 (per agent, every 20–40 min): step-out / re-entry / done / give-up rolls
        writes: ACTIVATE/IDLE/DONE → Redis direct;  RETIRE(for_good) → lifecycleCh → orchestrator ──► Postgres + Core

One tick = T_action. The action phase is the unit of market-driving time; the presence sweep runs on its own far-coarser cadence T_presence and is not a "tick."

3. The in-memory working set (the goroutine's authority)

At startScheduler the goroutine builds one in-memory working set from the handover, and that set — not Redis — is the authority the tick runs on. Redis is the write-on-change mirror that exists only so a crashed-and-restarted L2 can rebuild this set (the rebuild itself is Tier 6).

Per agent the set holds: the frozen Traits (from population), the CapitalHandle, the session capital A, the current presence (starts DORMANT), the one-shot entry_time (already stamped in 2c), the recurring next_check_time / idle_since, and the slot for Tier-4 working memory (standing opinion/stance, wait-streak, position-context) that decide() owns.

The set also maintains, as a derived view, the ACTIVE subset — the agents with presence == ACTIVE. This starts empty (all agents DORMANT) and is mutated only by the presence sweep (§6). The action tick iterates only this subset, never the full population — this is the first reason the hot path stays cheap (§5).

Session-level values held once: SessionStart / SessionEnd (the scheduler clock, from the request), the instrument list, the session type, the lifecycle curves (§6), and the session RNG — the fresh seeded source the tick uses for the per-agent act-coin and the emit shuffle. (Live = fresh randomness; no seed derived from exchange_id. L2_OVERVIEW_AND_TOPOLOGY §7.)

CHANGE (working-set rebuild is a read L2 cannot do yet). The shipped RuntimeStore (store/runtime_store.go) has only write-on-change setters and WriteInitialSnapshot — no reader. Tier 3 builds the working set from the in-memory population handover, so it needs no reader on the live path. A runtime reader (load snapshot → reconcile) is required only for the restart rebuild, which is Tier 6; Tier 3 names the accessor, Tier 6 implements it.

4. The loop — two cadences on one goroutine

The goroutine runs a single loop with two independent "is it due?" timers over one wall clock:

T_action — the action interval. How often the ACTION phase runs: the market-driving cadence. A configurable duration; faster = more trading pressure/throughput, slower = calmer. (Calibration dial, not fixed.)
T_presence — the presence interval. How often the PRESENCE sweep runs to scan for lifecycle changes. Configurable and far coarser than T_action (tens of seconds vs sub-second), because presence has slack timing — an agent entering a few seconds "late" over a multi-hour / ~22,500-tick session is invisible.

Each pass reads the clock, runs whichever phase(s) are due, updates that phase's last_* bookmark, then sleeps to the nearest next-due time (sleep(max(0, min(next_action_due, next_presence_due) − now))). The sleep is computed after the work finishes, so nothing has to predict how long a tick takes:

Tick finished early (cheap decide()) → the sleep is positive → phases run evenly spaced at their intervals.
Tick ran longer than T_action (heavy decide()) → next_due is already past → the sleep clamps to 0 → the loop runs flat-out, back-to-back, self-correcting.

So T_action is a target/floor, not a guarantee. When agents are cheap it paces the market evenly; when decide() gets heavy, T_action stops binding and ticks chain as fast as the work allows. That heavy regime is not fixed by the timing loop — it is fixed by the DECIDE concurrency seam (§5). Decoupling presence onto T_presence keeps the loose-timing lifecycle work off the hot path entirely, at no extra machinery cost (it still runs between ticks on the one goroutine, so it never races the active-set).

DECISION (presence decoupled from the action tick). The locked 3b wording was "checks ride the tick." CHANGE: presence runs on its own coarse cadence (T_presence), not every action tick. Same goroutine, so the active-set mutation stays lock-free; only the scan cadence changes. The lifecycle math is untouched — the recurring rolls already carry their own interval (the 20–40 min next_check_time), independent of how often the sweep scans for due agents.

5. 3a — the ACTION phase (the hot path)

The action tick is four steps, run serially on the goroutine:

Step 1 — SNAPSHOT. Read the market once per tick into goroutine memory: the full cached OHLCV window per instrument and the latest quote. Reads the Tier-1 market-data cache (marketdata:{exchange_id}:{instrument}:candles:1m and :quote). The snapshot loads the entire OHLCV per instrument — it does not bound by any agent's lookback; a window-view accessor over that in-memory data lets each agent pull "last W candles / range" as it needs (W is the agent's concern, Tier 4). Market reads are amortized once across the whole tick, never per agent.

Current price for the tick = the quote cache's last_price. (For an instrument an agent already holds, GetObservation.Position.LTP also carries a broker mark when MarkAvailable; for a flat instrument being considered for entry, the quote cache is the only source — which is why last_price must be in the cache.)

CHANGE (the cached quote must carry last_price + depth — a forced Tier-1 edit). The WS SnapshotFrameData (core/websocket_frames.go) carries LastPrice, HasLast, Phase, top-of-book Quote{BestBid/BestAsk/Has*}, and Depth{Bids,Asks}. The shipped cached store.Quote (store/market_data_store.go) persists only BidPrice/AskPrice/BidQty/AskQty/Timestamp — it drops last_price, has_last, phase, and depth. 3a's current-price read and Tier 4's liquidity channel need those. Consequence: the cached quote shape grows (add last_price + has_last + phase + depth) and the 2c ingest mapping writes them. This edits a Tier-1 struct that already shipped — flagged as a deliberate, reviewed change, not a silent fix.

Step 2 — DECIDE. Iterate the ACTIVE subset only (§3). For each active agent, flip its per-tick act-coin (u < Traits.PAct, stateless, on the session RNG). If it acts, call decide(agent, snapshotView, readSurface) — the Tier-4 black box. decide() returns an optional order intent (already reduced to a concrete punch) or nothing. The agent's own position/cash/P&L are read fresh inside decide() via the read surface (§seam below). The framework cost per active agent is just the coin flip; decide() runs only for the sampled fraction.

Step 3 — COLLECT. Gather the produced orders into a batch. decide() mutates no shared state and never touches the engine directly — it only produces an intent — so this step is a pure gather.

Step 4 — EMIT. Order the batch by the seeded shuffle (session RNG) and hand it to the dispatcher channel, non-blocking. The shuffle is the liquidity-priority ordering (the broker preserves per-shard channel order, so shuffle order = arrival order at the book). The tick never waits on the network here.

The Tier-4 seam (what 3a provides to decide())

Tier 3 owns the framework; Tier 4 owns the decision. The seam 3a hands decide() is exactly three things:

the agent's frozen Traits + its runtime working-memory handle (so decide() can read/write its own standing opinion, wait-streak, position-context — Redis writes go direct on this goroutine, per the write-target rule);
the snapshot window-view + current quote (read-only, shared across all agents this tick);
a bound fresh-read surface = o.core.GetObservation(capital_handle) → {Balance, Positions, Orders, RecentTrades, TradesDegraded} (the shipped shape, core/observation.go).

How often decide() reads observation, and the latency that implies, is Tier 4's concern, not Tier 3's. 3a only exposes the accessor. The one honest hot-path cost is that a fresh GetObservation is a synchronous Core round-trip; in the sync build these run serially inside the tick. The framework's answer is the concurrency seam below — the latency question itself belongs to Tier 4 (or to a future load-bearing change that forces Tier 3 to react).

Order punch — fire-and-forget (the dispatcher)

The punch is fire-and-forget (locked). The dispatcher goroutine (§7) calls o.core.PunchOrder(request) → POST /v1/orders, which returns 202 {order_id} that L2 ignores. The punch request is the shipped core.OrderPunchRequest{AccountID, Instrument, Side, Type, TIF, Qty, LimitPrice}; Tier 4's executor fills it (side, type, tif, qty, price), and the client's pre-flight enforces the pairing (limit ⇒ tif=gtc + price>0; market ⇒ tif=ioc + price==0). Order/fill/reject state is broker-owned, read fresh — L2 caches no order ids; a reject (409 no_session, 422 insufficient_funds / bad_qty / no_market_price, 504) is observed after the fact on a later GetObservation, never at punch time.

CHANGE (L2 sends tif). The locked 3a said "broker computes TIF." The Tier-0 contract and the shipped client require L2 to send tif (gtc/ioc, never fok). Folded in: the punch payload carries tif; Tier 4's executor sets it from the order type.

Keeping the framework fast (the discipline)

The hot path stays lean by construction: SNAPSHOT reads the market once and shares it; the ACTIVE subset means no full-population scan; the act-coin gates decide(); EMIT is a non-blocking handoff; and presence is off the tick (§4). The 5-step separation is preserved precisely so DECIDE can go concurrent later — a bounded worker pool over the active agents — without touching SNAPSHOT / COLLECT / EMIT. That is the designated fix for the heavy-decide() regime, and it is deferred: the prototype runs DECIDE serially, one goroutine, no agent goroutines.

DECISION (sync build now, concurrency seam preserved). Prototype: zero agent goroutines, serial decide(). The stage-2-only switch to a worker pool is the sole change needed when decide() latency dominates — recorded, not built.

6. 3b — the PRESENCE phase (lifecycle)

The presence sweep advances every agent through its session life. It runs on T_presence (§4), over the working set, on the same goroutine — so it mutates the ACTIVE subset race-free (the action tick already ran before it this pass).

The lifecycle curves (built once, at startScheduler)

Before the loop, the goroutine realizes the session-level lifecycle curves once: the entry curve and the Gaussian-process realizations the step-out / done rates ride (LAYER_2_LIFECYCLE §4 and the λ formulas). The curve rate constants come from curve_eff — the scenario-shifted lifecycle rates carried on sessions:active — when a scenario is set, and from baseline rates when it is not. The curve math is unchanged; only the rate constants shift.

CHANGE (curve_eff is baseline-only today). The shipped generator.CurveEffective (orchestrator/generator/scenario.go) is an empty struct ("fields land as the lifecycle-curve mechanism is designed"), the scenario provider is the NoOpScenarioProvider (returns nil), and sessions:active has no writer/reader yet. Consequence: the 3b curve-builder is wired to baseline rates now; the curve_eff rate fields (entry/step-out/done/re-entry) are consumed when the scenario-engine phase lands. No Tier-3 blocker — baseline is the correct standing behavior (NoOpScenarioProvider → baseline everywhere).

The two clocks

Clock = Model A (checks ride the sweep; nothing fires mid-interval):

Clock 1 — entry (checked every sweep). Each DORMANT agent: compare now_norm = (now − SessionStart)/(SessionEnd − SessionStart) against its one-shot entry_time (stamped in 2c). When now_norm ≥ entry_time → ACTIVATE: set presence = ACTIVE, draw the first next_check_time = now + rand(20–40 min), add to the ACTIVE subset. Gated by the ceiling hook (below).
Clock 2 — recurring (per agent, when next_check_time ≤ now). Run the transition rolls using the LAYER_2_LIFECYCLE formulas verbatim (all trait factors f_i = 1, fatigue c_fat = 0 for the prototype), then re-arm next_check_time:
ACTIVE agent: roll done first; if not done, roll step-out. done → DONE; step-out → IDLE (stamp idle_since). (Done supersedes a temporary step-out near close.)
IDLE agent: roll re-entry first; if not returned, roll give-up. re-entry → ACTIVE; give-up → DONE.

Because the sweep runs after the action tick, every presence change takes effect on future ticks only — the active-set mutation can't race the decide loop.

The write split (the write-target rule)

The DECIDE/WRITE split, refined by the write-target rule:

ACTIVATE / step-out→IDLE / done→DONE touch only the in-memory ACTIVE subset + the Redis runtime snapshot → the goroutine writes them directly, inline (RuntimeStore.SetPresence / SetTimers, write-on-change — roughly once per agent per 30 min, cheap). Positions/P&L are never snapshotted (read fresh).
RETIRE (for-good) touches Postgres (AgentStore.Retire → retired_at) and Core (account close) → routed over the lifecycle→orchestrator channel to the orchestrator, which performs the durable writes. The goroutine decides; the orchestrator writes Postgres.

DECISION (the lifecycle channel + its message). Tier 3 defines the channel message type (carrying a RETIRE decision: the agent_id + reason) and the orchestrator-side drain that applies it. The channel buffer is the already-reserved OrchestratorConfig.LifecycleBuffer. The RETIRE execution — square-off → the once-per-session quit roll → AgentStore.Retire + Core account-close — couples to Tier-6 teardown. Tier 3 emits the decision and defines the channel; Tier 6 wires the close.

Decided here, parked here
Ceiling hook (X ≤ active ≤ N balancing). The ACTIVATE path passes through a ceiling hook that defaults to a no-op (ceiling = N, never blocks in the prototype). Real active-count balancing and blocked-entry behavior stay PARKED (LAYER_2_LIFECYCLE §10).
Snapshot cadence = write-on-change. presence / next_check_time / idle_since are written to Redis on each transition only; positions/P&L never.
Quit-for-good (#6). The roll is designed here; its execution (square-off then the quit roll then RETIRE) wires at Tier 6, with the X-floor deferred.
Mid-session join (#5) stays PARKED. P&L-driven DONE and the f_i trait factors remain Tier 4 internals (3b builds mechanical presence only, factors = 1).
7. The dispatcher goroutine

One dispatcher goroutine per exchange (symmetric with the 2c ingest goroutine), spawned at startScheduler. It owns a buffered punch channel: EMIT (step 4) hands it the ordered batch non-blocking; the dispatcher drains the channel and calls o.core.PunchOrder per order, ignoring the 202. Its sole purpose is to keep the tick from ever blocking on the network punch. It makes no other Core call; order outcomes are read fresh via GetObservation inside decide(), never correlated here. (Precise order-id correlation — the A2 fallback — is recorded as a switch to make only if Tier-4 cancels/exits ever need it; not built.)

8. Cross-tier access map

What Tier 3 touches, and in which direction:

Tier / package	Tier 3 uses	Direction	Status
Tier 2 handover (orchestrator)	population (traits/handle/A), request (clock/instruments/type), the lifecycle channel, sessions:active (curve_eff)	in	handover built; channel + sessions:active writer new
Tier 1 market-data (store)	read full OHLCV window + quote (SNAPSHOT)	read	reader is NEW (writers only exist)
Tier 1 runtime (store)	SetPresence / SetTimers (3b); Tier-4 working-memory writers (via decide)	write	presence/timer writers exist; working-memory writers + field constants NEW
Tier 1 durable (store)	AgentStore.Retire (via orchestrator on RETIRE)	write	exists
Tier 0 client (core)	GetObservation (in decide), PunchOrder (dispatcher)	out	built
Tier 4 (decide())	the decision black box	call	seam defined here; body = Tier 4
Tier 6	for-good execution, restart rebuild, WS reconnect, abort-cleanup	hand-off	deferred

New methods Tier 3 defines: startScheduler; the loop (runActionPhase + runPresencePhase); the lifecycle-curve builder; the market-data read accessors (ReadCandles, ReadQuote); the runtime working-memory writers + their field constants; the snapshot window-view accessor; the dispatcher loop; the LifecycleDecision message type + the orchestrator drain.

New dependencies the orchestrator must hold (composition-root change). The shipped Orchestrator holds only generator / accountOpen / capital / rngSource / config / logger. The goroutine needs direct handles to o.core, the RuntimeStore, the MarketDataStore, and the AgentStore (for RETIRE) — none wired today (main.go does not even construct the two Redis stores). Tier 3's wiring builds the two stores and injects the four handles.

9. Ordering & concurrency invariants (LOCKED)
One goroutine per exchange runs the whole tick serially — SNAPSHOT, DECIDE, COLLECT, EMIT, and the presence sweep all on it. No agent goroutines in the prototype.
ACTION before PRESENCE each time both are due — presence mutates the ACTIVE subset; running action first means every presence change affects future ticks only → race-free active-set, no locking.
Only two other goroutines exist per session: ingest (2c, WS→Redis) and dispatcher (Tier 3, channel→punch). Neither touches the agent working set.
Within-tick determinism holds (no order emits during a tick's decide phase, so own-state is stable across that phase). Global cross-tick settlement timing is NOT bit-reproducible (async dispatcher → a tick's fills land at nondeterministic wall-clock vs the next tick's fresh reads). Accepted for now; strict global repro is a joint L2+Core concern, deferred.
10. Failure policy

The session is long-lived and statistical, so the tick degrades, never aborts, on transient failure:

SNAPSHOT read miss / empty cache (e.g. an instrument not yet printed) → that instrument reads flat/absent this tick; no crash. (Fatal data failure is a 2c-join concern, already handled before go-live.)
GetObservation error inside decide (409 no_session, 5xx, timeout) → that agent skips/defers this tick; the retry wrapper on the hot-path read absorbs a transient blip. A persistent no_session means the agent's install hasn't landed — it reconciles on a later tick.
PunchOrder error → ignored by design (fire-and-forget); the order is "status unknown" and reconciled via the next GetObservation. Writes are never retried (504 ≠ nothing happened).
Core persistently unreachable → the tick keeps running against cached market data; what the session ultimately does about a sustained outage is Tier 6, not here.

No failure in a tick tears the session down; teardown is always a Tier-6 / WindDown concern.

11. What Tier 3 hands onward

To Tier 4: the decide() seam — the agent's traits + working-memory handle, the shared snapshot window-view + quote, and the bound GetObservation fresh-read surface. Tier 4 returns an order intent reduced to a core.OrderPunchRequest (or nothing). Everything inside decide() — perception (4a), the mixer/commit gate (4b), execution (4c), position/exit management (4d) — is Tier 4; Tier 3 never looks in.

To Tier 6 (named here, executed there): RETIRE execution (square-off → quit roll → Retire + Core account-close); the restart rebuild of the working set (the runtime reader + reconcile); WS reconnect policy; abort-cleanup; the X-floor for the quit path.

12. Decisions locked in this doc
D1 — Presence decoupled from the action tick. Two cadences on one goroutine: the action tick (T_action) and the presence sweep (T_presence ≫ T_action); sleep-to-next between passes; T_action is a floor, not a guarantee. (CHANGE from the locked "checks ride the tick"; lifecycle math unchanged.)
D2 — The action tick is four steps (snapshot → decide → collect → emit); presence is a separate sweep. 5-step separation preserved so DECIDE can go concurrent later (worker-pool seam, deferred).
D3 — ACTIVE subset is the iterate set. No full-population scan on the hot path; act-coin gates decide().
D4 — Current price = cached last_price; the cached quote shape is extended (add last_price/has_last/phase/depth) and the 2c ingest writes them. (CHANGE: forced Tier-1 store.Quote edit.)
D5 — L2 sends tif on the punch (gtc/ioc, never fok). (CHANGE from "broker computes TIF.")
D6 — Write-target split: Redis presence/timer/working-memory → goroutine writes direct; Postgres RETIRE → lifecycle channel → orchestrator. Positions/P&L never snapshotted.
D7 — Lifecycle math verbatim (LAYER_2_LIFECYCLE), factors = 1, fatigue = 0; only the scan cadence and the write targets are Tier-3 decisions.
D8 — Ceiling hook defaults to no-op (= N); X ≤ active ≤ N balancing and mid-session join stay PARKED.
D9 — Curve-builder consumes curve_eff from sessions:active, baseline today (scenario engine is a future phase; NoOpScenarioProvider → baseline).
D10 — Dispatcher goroutine per exchange carries the fire-and-forget punch; tick never blocks on the network.
D11 — Degrade-not-abort failure policy; teardown is always Tier 6.
D12 — Orchestrator gains four injected dependencies (o.core, runtime store, market-data store, agent store) + the two Redis stores built in the composition root.
13. Open / deferred / parked (collected)
Tier 4 internals — decide() body (perception/mixer/commit-gate/execution/exit); theta_act / theta_add still open; the channel→trait map still owed. 3a provides the seam only.
Scenario engine — produces EffParams + curve_eff; CurveEffective fields land then. Baseline until then.
Tier 6 — RETIRE execution, restart rebuild (runtime reader + reconcile), WS reconnect, abort-cleanup, the X-floor.
Calibration/ops — T_action, T_presence, the sleep granularity, the 20–40 min check interval bounds, and every lifecycle/scenario numeric — all configurable, none fixed in design.
Global cross-tick settlement determinism — accepted as non-reproducible for now.