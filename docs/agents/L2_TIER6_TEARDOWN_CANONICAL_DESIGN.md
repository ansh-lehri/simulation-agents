Layer 2 — Tier 6: Teardown (Canonical) — Design

What this doc is. The complete canonical session-end flow for L2: what happens inside the L2 agent process when a canonical market's trading day ends and Core sends WindDown. It is the canonical half of Tier 6 teardown; the on-demand half is the sibling doc L2_TIER6_TEARDOWN_ONDEMAND_DESIGN. Resilience/restart (the unclean path) is in the two recovery docs — this doc is only the clean, deterministic end-of-day shutdown.

Read first. L2_OVERVIEW_AND_TOPOLOGY (process topology, ownership spine, the two populations), L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN (the stores, the agents table, the capital model), L2_TIER2_BRINGUP_DESIGN (what bring-up created and handed to the goroutine), L2_TIER3_RUNTIME_LOOP_DESIGN (the goroutine, dispatcher, ingest, lifecycle channel).

Grounding (source docs, with what each term means).

market_simulator_decisions.md — the master simulator decision log (the STL/SES/CIR coded decisions). STL-3: open positions auto-square at the closing-auction price. STL-1: intraday-only, every session starts flat.
LAYER_5_4_REALIZATION.md — the Layer 5 (economy) realization/P&L doc; §5 "Item 41 — session-end auto-square settlement": the broker force-closes every open position at close as a settlement-side mark.
BROKER_ACCOUNT_SESSION_LIFECYCLE.md — the broker account/session lifecycle doc; the SweepMarketClose broadcast where each shard auto-squares its canonical positions before saving.
LAYER7_CANONICAL_TEARDOWN_IMPL.md — Layer 7's canonical teardown implementation; the Shape-B scheduled-close sequence (FinalizeMarket → WindDown → Stop), and B-3 the broker-owned pre-close auto-square.
LAYER_2_LIFECYCLE.md — the lifecycle-math doc; §9 transition #6 (quit-for-good), the once- per-session end-of-session quit roll.
L2_TIER0_CORE_API_CONTRACT.md — the locked L2↔Core endpoint list (GET /v1/account/cash, the durable cash read; and the fact that there is no account-close endpoint).

Scope.

In: the WindDown(Canonical) trigger and where it sits relative to the broker's close; the seven teardown concerns (square-off, quit-for-good + retire, P&L→A fold, goroutine/WS shutdown, broker session-end, lease release, Redis cleanup) as they apply to canonical; who owns each; the exact ordering; the full pipeline; edges.
Out: on-demand teardown (sibling doc); all restart/crash recovery (recovery docs); the lifecycle math of #6 (that is LAYER_2_LIFECYCLE §9, reproduced only as needed); Tier 4 decision internals.
1. The frame — WindDown arrives after the broker has already closed

Canonical teardown is driven by Layer 7's per-market close timer. The locked L7 sequence (LAYER7_CANONICAL_TEARDOWN_IMPL §7, Shape B; decisions CT.6/CT.7) is:

L7 shapeB_close(market):
    flip market → TearingDown                 // joins now rejected
    broker.FinalizeMarket(exchID)             // BARRIER: persist + unload ALL canonical accounts
    agents.WindDown(tag, Canonical)           // ← L2 is called HERE, fire-and-forget
    exch.Stop()                               // exchange stops; market-data self-cleans
    reap market, free byUser

Two broker facts that happen before L2 is ever called, and that shape everything below:

Pre-close auto-square is the broker's, not L2's (LAYER7_CANONICAL_TEARDOWN_IMPL B-3 / BROKER_ACCOUNT_SESSION_LIFECYCLE SweepMarketClose / LAYER_5_4 §5 / STL-3). Shortly before close the broker self-triggers squaring of every canonical position for the market (attached and detached accounts alike), as a settlement-side mark at the closing-auction price. L7 never triggers, sequences, or waits on it, and neither does L2. LAYER_2_LIFECYCLE §9 states the same precondition from L2's side: "at session close agents are flat (squared off), so no flatten step is needed before REMOVE(for_good)."
FinalizeMarket runs before WindDown and is a barrier L7 waits on. It persists every canonical account to Postgres (trading_cash reconciled = Available + Settled) and unloads it from the shard. So by the time L2 receives WindDown:
every agent's position is flat;
every agent's account is persisted (durable trading_cash is final);
every agent's account is unloaded (a live GET /v1/account/observation would 409).

Consequence for L2: L2 does no flattening and no broker session-end call. Its teardown is (a) compute and persist the two canonical end-of-day effects — retirement and capital evolution — and (b) tear down its own goroutine, WS, and Redis runtime. WindDown being fire-and-forget means L7 does not wait for L2; L2's teardown runs asynchronously and has a full day until the next bring-up, so nothing races it.

2. Ownership — orchestrator vs goroutine

Two actors, exactly as at bring-up (the Tier-2 split, carried):

The orchestrator receives WindDown, holds the goroutine's stop-handle, drains the lifecycle→orchestrator channel (lifecycleCh), and performs all durable writes (Postgres) and all shared-key writes (sessions:active). Postgres is orchestrator-only (the write-target rule, L2_TIER3_RUNTIME_LOOP_DESIGN §6).
The per-exchange goroutine (runAgentPopulation) owns the in-memory working set, o.core, the dispatcher, and the WS/ingest. It computes the end-of-day effects (it alone holds the population, the count P, and the session RNG) and self-stops, emitting its results to the orchestrator over lifecycleCh.

So: goroutine decides/computes, orchestrator writes. The channel closing is the barrier that tells the orchestrator the goroutine has finished and exited.

3. The seven teardown concerns, for canonical

Each is tagged A1–A7 (the shared teardown ledger). For canonical, four are no-ops and three are real work.

A1 — Square-off — NO-OP for L2

The broker auto-squares pre-close (§1). L2 must not flatten anything — doing so would inject orders that fight the settlement-side mark, and by WindDown the accounts are unloaded anyway. Nothing to do. (The only square-off-adjacent obligation L2 has is to stop trading promptly — that is A4's tick-top cancel, below, not a flatten.)

A2 — Quit-for-good roll + RETIRE — canonical only, real work

What it is. Transition #6 of the presence state machine (LAYER_2_LIFECYCLE §9): the once-per- session, end-of-session roll that permanently removes a small, population-level trickle of agents from the canonical population — the death half of canonical churn (mint at bring-up is the birth half). ~0.3 agents/session (E_quit = 0.3, ≈ one every 3–4 sessions).

The roll (goroutine, at close). Rolled over the entire population — every agent that exists in the session, regardless of presence state (ACTIVE / IDLE / DONE / DORMANT). This is required for the math: the per-agent probability is E_quit/P, which yields E_quit expected quitters only if all P agents roll. Rolling a subset would under-shoot.

for each agent i in population:                     // ALL presence states
    p = clamp( (E_quit / P) * f_quit_i , 0, 1 )     // f_quit_i = 1 for the prototype
    u = sessionRNG.Float64()                        // seeded session RNG
    if u < p:
        mark i retiree
        emit RETIRE{ agent_id: i } on lifecycleCh
f_quit_i = 1 for the prototype (the per-agent quit factor that later reads losing streaks is deferred — LAYER_2_LIFECYCLE §9, Tier-4 direction).
Floor X is deferred and safe to omit. LAYER_2_LIFECYCLE §9's floor guard (if population_count > X) is not applied. It is safe because the next canonical bring-up mints the shortfall N − |existing| back to N (Tier 2 Stage A) — so quit-for-good is pure identity turnover and the population self-heals to N every session. The mint-to-N at bring-up is the implicit floor; the explicit X-floor would only cap the turnover rate, a later refinement.

RETIRE — what it does (orchestrator, Postgres only). For each retiree the orchestrator sets the durable mark:

AgentStore.SetRetired(agent_id)      // agents.retired_at = now()
retired_at is the only durable lifecycle field (L2_TIER1 §2): NULL = live member, set = RETIRED, never revived. The canonical reuse query filters WHERE retired_at IS NULL, so a retired agent is never re-queried at the next bring-up → it is gone permanently.
The row is kept, not deleted (a tombstone). LAYER_2_LIFECYCLE §9's "retain log" is reinterpreted: L2 no longer holds a trade log (ownership spine — P&L/trades are broker-owned), so the retired agents row is the retained record.
No Core account-close call. L2_TIER0_CORE_API_CONTRACT has no account-close endpoint. The retired agent's economy account was already persisted + unloaded by the broker's FinalizeMarket and is simply never re-loaded (L2 never revives the agent → never session- loads it again). It sits dormant-but-durable. An explicit Core account-close is a deferred cleanliness item + Core-side dependency, not built for iteration 1.

Tipper continuity. If the one static is_tipper canonical agent quits, no special handling here: the next bring-up's Stage A promotes a new tipper if no revived agent carries the flag (L2_TIER2_BRINGUP_DESIGN §3).

A3 — P&L → A fold — canonical only, real work

What it is. Canonical trading_capital (A — the agent's session money, the self-sizing budget it trades against) persists and evolves with realized P&L across sessions (L2_TIER1 §4). The fold is the mechanism that evolves it at session end.

Writes the existing column — no schema change. The fold updates the already-specified agents.trading_capital column. Nothing is added to the schema.

Data source — the durable cash read. Because FinalizeMarket unloaded the accounts before WindDown, the live observation read is unavailable (409). The fold reads the durable GET /v1/account/cash?account_id=<handle> (L2_TIER0_CORE_API_CONTRACT §2.2 — the durable, not-live cash read), which returns the final, persisted trading_cash. The L7 ordering (FinalizeMarket barrier → WindDown) guarantees this figure is final by the time L2 reads it.

The formula — a delta, not a copy. At session start the broker funds every account's trading_cash to a flat default (broker_default); during the session it moves with realized P&L; at close it is broker_default + net_session_P&L. So:

cash  := o.core.GetAccountCash(handle).trading_cash        // final, durable
A_new := max(0, A_old + (cash - broker_default))           // evolve A by this session's P&L
emit CAPITAL{ agent_id, A_new } on lifecycleCh
It is a delta applied to A, not A = cash, because A (the self-sizing budget) and the broker's trading_cash are different bases: the broker funds to broker_default; A is L2's own budget that the agent sizes against (L2_TIER1 §4). The session's P&L = cash − broker_default; that delta is what evolves A. (Stable property: A and trading_cash move by the same delta each session.)
broker_default = the broker's flat session-start funding — a known config constant (default_funded_cash, L2_TIER1/L2_TIER2). Assumption made explicit (do not leave to chance): the broker funds every canonical account's trading_cash to this same default at each session start. If that ever stops holding, L2 must instead capture each account's actual session- start trading_cash at bring-up; for iteration 1 the config default is authoritative.
Clamp A_new ≥ 0. An agent can lose up to ~A in a session; the floor keeps A from going negative. An agent folded to A ≈ 0 simply goes inactive next session (its A_available ≈ 0 → qty 0) — exactly the "blown-up trader," which the later P&L-driven quit factor eventually retires.

Who, and the order. The goroutine reads the cash and computes A_new (it has o.core and A_old in the working set); the orchestrator applies the Postgres write (AgentStore.SetTradingCapital). Fold survivors only — run A2's roll first, then fold the non-retired set (no point folding a retiree's A).

A4 — Goroutine + WS shutdown — (identical for both populations; documented here)

Mechanical goroutine lifecycle. The goroutine architecture is the same for canonical and on-demand (runAgentPopulation + one dispatcher + one WS ingest, per exchange_id); only the compute steps before it differ.

Stop trading promptly — tick-top cancel. The orchestrator signals cancel; the goroutine checks the cancel flag at the top of the tick, before SNAPSHOT/DECIDE. So the moment cancel is set, the next tick produces zero orders. This bounds order leakage to at most the one tick already mid-flight (sub-second at T_action). The broker is the hard backstop: any straggler punch hits 409 no_session (accounts already unloaded by FinalizeMarket) and cannot execute. No mid-tick draining, no coordination with L7's exact close instant — unnecessary given the 409 backstop.

Shutdown sequence (goroutine):

tick-top sees cancel → break loop
A2 quit roll        (§A2)   → emit RETIREs on lifecycleCh
A3 fold (survivors) (§A3)   → emit CAPITALs on lifecycleCh
A4 dispatcher.stop()        → drop any queued punches (fire-and-forget; they'd 409 anyway)
   o.core.closeWS(exch)     → ends the ingest goroutine (blocked on WS read; close errors it out)
   join ingest              → no late Redis writes can race A7's cleanup
close(lifecycleCh)          → signals "done" to the orchestrator
return
Dropping queued punches is correct: L2 has no correctness dependency on a punch landing (fire-and- forget, L2_TIER3 §5), and post-close they reject anyway.
Closing the WS is the teardown action named in L2_TIER0_CORE_API_CONTRACT §3 ("closed at teardown"); the ingest goroutine exits when its WS read errors, and the main goroutine joins it before returning so no stray ingest write lands after A7 wipes the market-data cache.
A5 — Broker session teardown — NO-OP for L2

L7 runs FinalizeMarket (persist + unload all accounts) before WindDown. L2 makes no broker session-end call for canonical. Nothing to do.

A6 — Lease release — N/A for canonical

Canonical agents are durable members revived by the reuse query, not pool-leased. The lease model (lease:{country}:available / :leased) is on-demand only (L2_TIER1 §6). Nothing to do.

A7 — Redis runtime cleanup — (identical for both populations; documented here)

The orchestrator drops all of this session's runtime state, keyed on exchange_id. Everything is {exchange_id}-scoped, and that namespace is unique per canonical market, so there is no collision with any other session.

UNLINK  runtime:{exchange_id}:agent:*        // per-agent runtime (presence, timers, working memory, A, tip-state)
UNLINK  runtime:{exchange_id}:tipslot        // shared tip slot
UNLINK  marketdata:{exchange_id}:*           // candle + quote cache
HDEL    sessions:active  <exchange_id>       // LAST — the teardown commit marker
HDEL sessions:active is done LAST, after the durable A2/A3 writes have been applied. It is the commit point: sessions:active is the respawn registry (L2_TIER1 §5.2), so once this entry is gone a restarted orchestrator will not try to respawn this (now dead) session. Doing it last means "this session is finished" becomes true only after everything is persisted.
Canonical market-data loss is harmless: dropped candles are re-pullable next session from the broker's persisted candles.
Postgres is not touched by A7 — retired_at and trading_capital are marks/updates (A2/A3), never deletes.
4. The full canonical WindDown pipeline
L7 (pre-WindDown):  pre-close auto-square  →  FinalizeMarket(exch)      // all accounts flat, persisted, UNLOADED
L7 → agents.WindDown(tag, Canonical)                                   // fire-and-forget

ORCHESTRATOR.WindDown(tag, Canonical):
    exch := registry[tag]
    if exch absent: return                                            // idempotent (double-WindDown / already reaped)
    signal cancel(exch)                                               // sets the goroutine's tick-top cancel flag
    // drain concurrently until the goroutine closes the channel (that close = "goroutine exited" barrier):
    for msg in drain(lifecycleCh(exch)) until closed:
        RETIRE{id}        → AgentStore.SetRetired(id)                 // A2  (agents.retired_at = now)
        CAPITAL{id, A_new}→ AgentStore.SetTradingCapital(id, A_new)   // A3  (agents.trading_capital = A_new)
    // A7 — after all durable writes:
    UNLINK runtime:{exch}:agent:*  ;  UNLINK runtime:{exch}:tipslot  ;  UNLINK marketdata:{exch}:*
    HDEL sessions:active <exch>                                       // LAST — commit point
    delete registry[exch]
    return

GOROUTINE runAgentPopulation(exch)  [on cancel]:
    tick-top sees cancel → break loop                                // ≤1 tick of order leakage; stragglers 409
    // A2 — quit roll over FULL population:
    for each agent i in population:
        if sessionRNG.Float64() < clamp(E_quit/P * 1.0, 0, 1):
            mark i retiree ; emit RETIRE{i}
    // A3 — fold survivors only:
    for each non-retired agent i:
        cash  := o.core.GetAccountCash(i.handle).trading_cash         // durable read — final post-FinalizeMarket
        emit CAPITAL{ i, max(0, i.A + (cash - broker_default)) }
    // A4 — shutdown:
    dispatcher.stop()                                                 // drop queued punches
    o.core.closeWS(exch) ; join ingest                               // end + join the WS ingest goroutine
    close(lifecycleCh)
    return

    // A1 square-off: none (broker).  A5 broker session-end: none (L7 FinalizeMarket).  A6 lease: N/A.

Why this order is correct:

Cancel first → the goroutine stops producing orders before it does anything else.
A2 before A3 → fold only survivors.
A2/A3 emit over the channel; the orchestrator applies them as they arrive; the sets are disjoint so arrival order is immaterial.
Channel close = goroutine fully stopped (loop broken, dispatcher stopped, WS closed, ingest joined) → only then does the orchestrator run A7, so no runtime write can race the cleanup.
HDEL sessions:active strictly last → the session is marked dead only after every durable effect is committed.
5. Edges & failure handling
Double WindDown / already-reaped. registry[tag] absent → return immediately. Idempotent.
A3 cash read fails for an agent (transient Core blip). The read is a normal o.core call; on error, retry briefly; if it still fails, skip that agent's fold (leave its A unchanged) and log — a one-session missed fold is a negligible drift in a value that evolves over many sessions; never block teardown on it. Do not retry forever (teardown must make forward progress).
Agent with no trading activity (stayed DORMANT / never traded) → cash == broker_default → delta 0 → fold is a harmless no-op write (optionally skip-if-zero; not required).
Closed while L2 was down (L2 missed this market's WindDown because it was restarting). This is not handled here — it is the deferred-teardown case in L2_TIER6_RECOVERY_CANONICAL_DESIGN: on restart, L2 finds a sessions:active entry whose end has passed and runs this same A2+A3+A7 sequence late (the broker already finalized, so the durable cash is final and the fold still works).
lifecycleCh message type. The channel (reserved at bring-up as LifecycleBuffer, L2_TIER3 §6) carries two teardown variants: RETIRE{agent_id} and CAPITAL{agent_id, A_new}. During the live session it carries only RETIRE (and only at close, since #6 is close-only); the CAPITAL variant is the one addition this tier makes to the message type.
6. Decision log (Canonical Teardown)
#	Decision	Status
T6C.1	A1 square-off is a no-op for L2 — broker auto-squares pre-close (B-3/STL-3), attachment-independent; L2 never flattens	LOCKED
T6C.2	WindDown(Canonical) arrives after FinalizeMarket; accounts are flat, persisted, unloaded by then; WindDown is fire-and-forget	LOCKED (from LAYER7_CANONICAL_TEARDOWN_IMPL CT.6/CT.7)
T6C.3	A2 #6 quit roll runs over the entire population (all presence states); f_quit=1; floor X omitted (mint-to-N self-heals)	LOCKED
T6C.4	RETIRE = L2-side tombstone only (agents.retired_at), row kept; no Core account-close (no endpoint); explicit close deferred	LOCKED
T6C.5	A3 fold reads the durable GET /v1/account/cash (accounts unloaded → live read unavailable)	LOCKED
T6C.6	A3 fold is a delta on A: trading_capital = max(0, A_old + (trading_cash − broker_default)); existing column, no schema change	LOCKED
T6C.7	A3 folds survivors only; A2 (roll) runs before A3 (fold)	LOCKED
T6C.8	A4 stops orders via tick-top cancel (≤1 tick leaks); broker 409 backstops stragglers; dispatcher drops queued; WS closed + ingest joined	LOCKED
T6C.9	A5 broker session-end + A6 lease release are not L2's for canonical (L7 FinalizeMarket; canonical not leased)	LOCKED
T6C.10	A7 drops runtime:* + marketdata:*, then HDEL sessions:active LAST (commit point); market-data re-pullable next session	LOCKED
T6C.11	Goroutine computes (A2/A3), orchestrator writes (Postgres + sessions:active); channel close = exit barrier	LOCKED
T6C.12	broker_default is assumed to be the broker's flat per-session funding; if it ever diverges, capture session-start cash at bring-up instead	ASSUMPTION — flagged
7. Open / deferred / flagged
Explicit Core account-close for retirees — deferred cleanliness + Core-side dependency (no endpoint today); dormant-but-durable accounts are acceptable for iteration 1.
Floor X for #6 — deferred (LAYER_2_LIFECYCLE §10); mint-to-N is the implicit floor.
f_quit and P&L-driven DONE — Tier-4 internals; f_quit=1 here.
Bulk durable cash read — the fold does N per-agent GET /v1/account/cash reads; a bulk variant is a future Core optimization (same category as bulk account-open).
broker_default divergence — see T6C.12.
Deferred-teardown (closed while L2 down) — handled in the canonical recovery doc, reusing A2/A3/A7.

End — Tier 6 canonical teardown. Sibling: L2_TIER6_TEARDOWN_ONDEMAND_DESIGN. Recovery: L2_TIER6_RECOVERY_CANONICAL_DESIGN.