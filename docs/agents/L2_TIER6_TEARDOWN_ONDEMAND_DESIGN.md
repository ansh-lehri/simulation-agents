Layer 2 — Tier 6: Teardown (On-Demand) — Design

What this doc is. The complete on-demand session-end flow for L2: what happens inside the L2 agent process when a private, single-user on-demand (practice) instance winds down and Core sends WindDown(OnDemand). It is the on-demand half of Tier 6 teardown; the canonical half is the sibling doc L2_TIER6_TEARDOWN_CANONICAL_DESIGN. Restart/crash recovery is in the two recovery docs — this doc is only the clean teardown.

Read first. L2_OVERVIEW_AND_TOPOLOGY, L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN (the lease model especially, §6), L2_TIER2_BRINGUP_DESIGN, L2_TIER3_RUNTIME_LOOP_DESIGN, and the canonical teardown sibling (shares A4 and A7 verbatim).

Grounding (source docs, with what each term means).

LAYER7_ONDEMAND_TEARDOWN_IMPL.md — Layer 7's on-demand teardown (Shape C); the triggers (explicit exit, duration timeout, vs accidental nav), and the shapeC_teardown sequence (EndSession(OnDemand=discard) → WindDown → Stop).
BROKER_ACCOUNT_SESSION_LIFECYCLE.md — the broker lifecycle doc; TeardownSession / session_discard (Redis DEL only, no Postgres) — the on-demand discard.
L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN.md — §6 the on-demand lease model (lease:{country}:available SET, lease:{country}:leased HASH agent_id → exchange_id), and the rule that release follows discard.
L2_TIER0_CORE_API_CONTRACT.md — the L2↔Core endpoints.

Scope.

In: the WindDown(OnDemand) trigger and its three source events; the seven teardown concerns as they apply to on-demand (five no-ops/absent, two real: goroutine/WS shutdown and lease release, plus Redis cleanup); who owns each; ordering; the full pipeline; edges.
Out: canonical teardown (sibling doc); all restart/crash recovery (recovery docs); on-demand reconnect-after-nav (that is L7's POST /session/current, not an L2 teardown — LAYER7_ONDEMAND_ TEARDOWN_IMPL §6).
1. The frame — three triggers, one teardown, broker discard already done

On-demand teardown (Shape C) is driven by L7 and fires on three events (LAYER7_ONDEMAND_TEARDOWN_IMPL §1–2, OT.1):

Explicit exit/leave — the user ends their private instance → teardown now.
Duration timeout — L7's per-session reap timer fires at session_ends_at → same teardown.
Accidental nav / socket drop — NOT a teardown; the instance stays live and the user can reconnect. (So there is no L2 action for this case at all.)

For the two that are teardowns, L7's locked sequence (LAYER7_ONDEMAND_TEARDOWN_IMPL §3, OT.5):

L7 shapeC_teardown(market):
    flip market → TearingDown
    broker.EndSession(OnDemand = discard)     // ← discard the ONE ephemeral account (Redis DEL, no Postgres)
    agents.WindDown(tag, OnDemand)            // ← L2 is called HERE, fire-and-forget
    exch.Stop()                               // exchange stops; market-data self-cleans (nothing persisted)
    reap, free byUser

Two broker facts that happen before L2 is ever called:

No square-off, no persist. On-demand positions are discarded, not squared or saved — PersistOnClose = false (the ephemeral account was sealed that way at the on-demand seal); EndSession(OnDemand) drops the loaded account entirely and never writes Postgres (BROKER_ACCOUNT_SESSION_LIFECYCLE session_discard / LAYER7_ONDEMAND_TEARDOWN_IMPL §4, OT.6).
EndSession(discard) runs before WindDown (a bounded N=1 barrier L7 waits on, then proceeds). So the broker session is already gone when L2 is called.

Consequence for L2: there is nothing to flatten, nothing to persist, nothing to fold, nobody to retire. On-demand's entire durable footprint for the session is zero (its agents.trading_capital is NULL; its traits are the accepted dead weight in the pool). L2's teardown is purely: stop its goroutine/WS, return the leased agents to the free pool, and wipe its Redis runtime. WindDown is fire-and-forget — L2 runs this asynchronously, no barrier back to L7.

2. Ownership — orchestrator vs goroutine

Same split as canonical:

The orchestrator receives WindDown, holds the goroutine stop-handle, and owns all shared- key writes — critically the lease pool (lease:{country}:*), which is cross-session shared state (multiple on-demand sessions for a country share it), so it must have a single writer to avoid a dying-goroutine-vs-orchestrator race on shared keys.
The per-exchange goroutine owns o.core, the dispatcher, and the WS/ingest; it just stops (there is no compute — no A2/A3 — for on-demand).
3. The seven teardown concerns, for on-demand
A1 — Square-off — NO-OP for L2

On-demand positions are discarded wholesale with the ephemeral account (PersistOnClose=false). Nothing to flatten. (As with canonical, the only trading-stop obligation is A4's tick-top cancel.)

A2 — Quit-for-good roll + RETIRE — NONE for on-demand

On-demand agents are leased throwaways — their traits are redrawn fresh each session, they have no cross-session identity, and there is no churn model to apply the #6 death-roll to. "Quit for good" is meaningless for a recycled practice-session agent. On-demand agents are released back to the pool (A6), never retired. No roll, no retired_at.

A3 — P&L → A fold — NONE for on-demand

On-demand A is Redis-only, drawn fresh each session, never persisted (agents.trading_capital is NULL for on-demand rows, L2_TIER1 §4). There is nothing to evolve and nowhere to write it. No fold.

A4 — Goroutine + WS shutdown — (identical to canonical)

Mechanically identical to the canonical case (see L2_TIER6_TEARDOWN_CANONICAL_DESIGN §A4). The goroutine architecture is the same; only the compute steps before it differ (and on-demand has none).

tick-top sees cancel → break loop        // ≤1 tick of order leakage; stragglers rejected (session gone)
dispatcher.stop()                        // drop queued punches
o.core.closeWS(exch) ; join ingest       // end + join the WS ingest goroutine
close(lifecycleCh)                       // drained EMPTY by the orchestrator (no A2/A3 messages)
return
The lifecycleCh carries no teardown messages for on-demand (no RETIRE, no CAPITAL); the orchestrator's drain just blocks until the channel closes — i.e. it serves purely as the "goroutine exited" barrier.
Straggler punches from the ≤1 leaked tick are rejected at Core (the on-demand session is being discarded / already gone); L2 ignores them (fire-and-forget).
A5 — Broker session teardown — NO-OP for L2

L7 runs EndSession(OnDemand=discard) before WindDown. L2 makes no broker session-end call. This is also what guarantees A6's ordering (next section).

A6 — Lease release — on-demand only, the real work

What it is. Return this session's agents to the free pool so a later on-demand session can lease them, and clear their busy marks. The lease model (L2_TIER1 §6): lease:{country}:available is a Redis SET of free agent_ids; lease:{country}:leased is a Redis HASH agent_id → exchange_id (who holds each busy one).

Mechanism (orchestrator, Redis pipeline). For every agent this session holds — both the existing-leased agents (leased at 2a select) and the new-minted agents (enlisted into leased at 2b P4):

for each agent_id in session.roster:
    HDEL  lease:{country}:leased      agent_id        // clear the busy mark
    SADD  lease:{country}:available   agent_id        // return to the free pool
Existing and new release identically. This is also how the pool grows: a minted agent goes minted → enlisted (leased, 2b P4) → released to available here → reusable thereafter.
No Postgres. On-demand durable rows stay in the pool (membership is durable; trading_capital stays NULL; the stored traits stay as the accepted dead weight, L2_TIER1 §2). A6 only flips the two Redis lease keys.
Owner = orchestrator (it did the 2a select + 2b enlist; and lease:{country}:* is cross- session shared state needing a single writer). It releases from its in-memory session roster.

Ordering — guaranteed for free. The locked rule (L2_TIER1 §6) is "broker discard first, then release" — otherwise the next lease's session-load hits ErrSessionAlreadyLive. A5 settles this automatically: L7 runs EndSession(discard) before firing WindDown, and A6 runs inside WindDown handling → discard-before-release holds with no coordination from L2.

A7 — Redis runtime cleanup — (identical to canonical)

Same as L2_TIER6_TEARDOWN_CANONICAL_DESIGN §A7, keyed on this instance's exchange_id (unique per on-demand instance, so no collision with canonical or other on-demand instances):

UNLINK  runtime:{exchange_id}:agent:*
UNLINK  runtime:{exchange_id}:tipslot
UNLINK  marketdata:{exchange_id}:*
HDEL    sessions:active  <exchange_id>       // LAST — commit marker
HDEL sessions:active LAST — the commit point; once gone, a restart won't try to touch this (dead) instance.
On-demand market-data loss is accepted: the on-demand live tape is thrown away and unrecoverable (L2_TIER1 §5.2) — it is a throwaway session.
A6 (lease keys) and A7 (runtime/marketdata keys) are disjoint key sets; order between them does not matter, but both must precede the HDEL sessions:active.
4. The full on-demand WindDown pipeline
L7 (pre-WindDown):  broker.EndSession(OnDemand = discard)              // ephemeral account dropped; no Postgres
L7 → agents.WindDown(tag, OnDemand)                                    // fire-and-forget

ORCHESTRATOR.WindDown(tag, OnDemand):
    exch := registry[tag]
    if exch absent: return                                            // idempotent
    country, roster := sessionInfo(exch)                              // the agents this instance holds
    signal cancel(exch)                                               // goroutine tick-top cancel
    drain(lifecycleCh(exch)) until closed                             // EMPTY — just the "goroutine exited" barrier
    // A6 — lease release (discard already done by L7 → discard-before-release holds):
    pipeline:
        for each agent_id in roster:
            HDEL lease:{country}:leased    agent_id
            SADD lease:{country}:available agent_id
    // A7 — Redis cleanup:
    UNLINK runtime:{exch}:agent:*  ;  UNLINK runtime:{exch}:tipslot  ;  UNLINK marketdata:{exch}:*
    HDEL sessions:active <exch>                                       // LAST — commit point
    delete registry[exch]
    return

GOROUTINE runAgentPopulation(exch)  [on cancel]:
    tick-top sees cancel → break loop                                // ≤1 tick leaks; discarded (session gone)
    dispatcher.stop()                                                // drop queued punches
    o.core.closeWS(exch) ; join ingest
    close(lifecycleCh)
    return

    // A1 square-off: none.  A2 retire / A3 fold: none (throwaway, trading_capital NULL).  A5 broker session-end: none (L7 EndSession-discard).

Why this order is correct:

Cancel first → goroutine stops producing orders.
The goroutine has no compute, so it just stops; the orchestrator's drain is a pure barrier.
A6 release is safe because L7 already discarded (A5) → no ErrSessionAlreadyLive on the next lease.
A6/A7 before HDEL sessions:active → the instance is marked dead only after the pool and runtime are cleaned.
5. Edges & failure handling
Accidental nav / socket drop → no teardown at all (the instance stays live; reconnect via L7's POST /session/current). L2 does nothing. Only explicit-exit and duration-timeout reach WindDown.
Exit-vs-timeout race → resolved on L7's side by the Live → TearingDown flip (whoever wins runs it; the other no-ops). L2 receives at most one WindDown; a duplicate finds registry[tag] absent → returns. Idempotent.
EndSession(discard) failed on L7 but WindDown fired anyway (L7's forward-progress policy logs + proceeds, OT.5). Then A6 returns a leased agent to available whose broker binding may not have actually been uninstalled → a later lease of that agent could hit ErrSessionAlreadyLive on session-load. Not handled here — it is caught by the Tier-6 lease reconcile in L2_TIER6_RECOVERY_ONDEMAND_DESIGN. This straggler-lease case is exactly why leased is a HASH keyed to exchange_id.
Crash mid-release (L2 dies partway through A6) → some agents left in leased pointing at a dead exchange_id. Also caught by the lease reconcile on restart (recovery doc). The happy-path A6 release uses the in-memory roster; reconcile uses leased-by-exchange_id.
6. Decision log (On-Demand Teardown)
#	Decision	Status
T6O.1	A1 square-off, A2 retire, A3 fold are absent for on-demand (positions discarded; throwaway identity; trading_capital NULL)	LOCKED
T6O.2	WindDown(OnDemand) arrives after EndSession(OnDemand=discard); fire-and-forget; three triggers (exit / timeout / nav≠teardown)	LOCKED (from LAYER7_ONDEMAND_TEARDOWN_IMPL OT.1/OT.5)
T6O.3	A4 goroutine/WS shutdown identical to canonical; lifecycleCh drained EMPTY	LOCKED
T6O.4	A5 broker session-end is not L2's (L7 EndSession-discard), and that guarantees A6's discard-before-release ordering	LOCKED
T6O.5	A6 lease release = HDEL leased + SADD available per agent, orchestrator-owned, Redis-only, no Postgres; existing + new release identically	LOCKED
T6O.6	A7 drops runtime:* + marketdata:*, then HDEL sessions:active LAST; on-demand live tape lost — accepted	LOCKED
T6O.7	Straggler/orphan leases (discard-failed, or crash-mid-release) are not handled here → lease reconcile in the on-demand recovery doc	LOCKED (carried)
7. Open / deferred / flagged
Lease reconcile (straggler + crash-partial + Redis-flush rebuild) — in L2_TIER6_RECOVERY_ONDEMAND_DESIGN.
WindDown signature — placeholder in-process interface today (L2_TIER0_CORE_API_CONTRACT §4, LAYER7_*_TEARDOWN_IMPL note "WindDown signature will change with L2"); HTTP-swap is transport-only.

End — Tier 6 on-demand teardown. Sibling: L2_TIER6_TEARDOWN_CANONICAL_DESIGN. Recovery: L2_TIER6_RECOVERY_ONDEMAND_DESIGN.