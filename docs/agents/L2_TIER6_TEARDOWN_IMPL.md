Layer 2 — Tier 6: Teardown — Implementation Design

What this is. The implementation design for the clean session-end (WindDown) path in the L2 agent process (simulation-agents), grounded in the real code. It has a shared backbone and then two sections — Canonical and On-Demand. Companion doc: L2_TIER6_RECOVERY_IMPL (the unclean / restart path). Source design: L2_TIER6_TEARDOWN_CANONICAL_DESIGN, L2_TIER6_TEARDOWN_ONDEMAND_DESIGN (the A1–A7 concern ledger used throughout).

Who calls it (Tier 7 / conductor — confirmed from the conductor codebase). AgentOrchestrator.WindDown(market MarketTag, mode Mode) — fire-and-forget; the conductor never waits (conductor/ports.go).

Canonical: conductor/canonical_close.go runs, off-lock, broker.FinalizeMarket(exchangeID) → agents.WindDown(tag, Canonical) → exchange.Stop().
On-demand: conductor/session_leave.go runs broker.EndSession(OnDemand=discard) → agents.WindDown(tag, OnDemand) → exchange.Stop() (fired by /session/exit or the reaper at closeAt).

Over HTTP this reaches L2 at /wind-down → transport.handleWindDown → Orchestrator.WindDown(ctx, exchangeID, teardownMode). teardownMode carries canonical vs on-demand straight from the conductor's Mode, so it is authoritative. The identifier L2 receives as exchange_id is the conductor's MarketTag, which is the same string L2 used at bring-up (BringUpRequest.ExchangeID), so it keys everything consistently.

Ownership (the decide/write split already in the code, unchanged). The per-session goroutine (session.LiveScheduler) computes the end-of-day effects and emits them over the lifecycleChannel; the Orchestrator drains the channel and writes Postgres and shared Redis keys. Postgres is orchestrator-only (session.Runner is forbidden from writing it). This is why the scheduler can roll/fold but cannot persist — it hands results back for the orchestrator to save.

🚩 CARRIED BUG — stale-cash-on-fold (make highly visible; fix later)

The scheduler runs the P&L fold (A3) in its own shutdown tail, but LiveScheduler.runLoop exits on its natural SessionEnd check (time.Now().After(request.SessionEnd)) which can fire a moment before the conductor's broker.FinalizeMarket has finalized trading_cash, and before the WindDown cancel even arrives. So the fold can read a one-session-stale durable cash figure for an agent and evolve its trading_capital off it.

Accepted for now: the drift is one session on a value that evolves over many, it self-corrects next session, and agents are internal-only — no external surface is wrong. Proper fix later: gate the close-effects on the WindDown cancel signal (which is guaranteed post-FinalizeMarket) rather than on the SessionEnd loop exit — i.e. after SessionEnd the loop stops trading but parks until ctx is cancelled, and only then runs the roll+fold. Tracked here so it is not lost.

0. Shared backbone (both populations)

The teardown/recovery pseudocode assumes registry[tag], signal cancel, and drain(lifecycleCh). None exist today: continueBringUp spawns sessionRunner.Run(context.Background(), …) fully detached and drops both the channel and any stop handle; nothing reads the channel's receive end; and LifecycleDecision has no field for folded capital. The backbone adds them.

0.1 Session registry on Orchestrator.

go
type sessionHandle struct {
    sessionType string                            // SessionTypeCanonical | SessionTypeOnDemand
    country     string                            // A6 lease key (on-demand)
    population  Population                         // roster + count P → A2/A3 (canonical), A6 (on-demand)
    cancel      context.CancelFunc                 // A4 stop
    lifecycleCh <-chan session.LifecycleDecision   // receive end, for the drain
}
// Orchestrator gains:  sessions map[string]*sessionHandle  +  sessionsMu sync.Mutex
// helpers: registerHandle(exch, *sessionHandle), takeHandle(exch) *sessionHandle (lookup+delete)

0.2 Spawn change in continueBringUp (P5) — retain the handle; own the channel close:

go
sessionCtx, cancel := context.WithCancel(context.Background())
lifecycleChannel := make(chan session.LifecycleDecision, orchestrator.config.LifecycleBuffer)
orchestrator.registerHandle(request.ExchangeID, &sessionHandle{
    sessionType: request.SessionType, country: request.Country,
    population: population, cancel: cancel, lifecycleCh: lifecycleChannel,
})
go func() {
    defer close(lifecycleChannel)   // close == goroutine fully exited  (the barrier)
    orchestrator.sessionRunner.Run(sessionCtx, request, population, curveEffective, lifecycleChannel)
}()

Run still takes the send-only chan<-; the registry keeps the receive end. The defer close lives in this wrapper so session.Runner stays unaware.

0.3 LifecycleDecision gains the CAPITAL variant (the one message-type change this tier makes):

go
type LifecycleDecision struct {
    AgentID    agent.AgentID
    Reason     string  // ReasonRetire | ReasonCapital
    NewCapital int64   // A_new; set only when Reason == ReasonCapital
}
const ReasonRetire  = "retire_for_good" // already exists
const ReasonCapital = "capital_fold"    // NEW

0.4 Scheduler shutdown tail. In LiveScheduler.StartScheduler, after runLoop returns, run the canonical close-effects (§1.A2/A3 — the shared helper computeCanonicalTeardown) and send each LifecycleDecision on lifecycleChannel; for on-demand it emits nothing. Then return → the wrapper closes the channel. (This is where the 🚩 bug lives.)

0.5 Orchestrator.WindDown skeleton (replaces the current log-only stub):

go
func (o *Orchestrator) WindDown(ctx context.Context, exchangeID, teardownMode string) error {
    h := o.takeHandle(exchangeID)
    if h == nil { return nil }                 // idempotent: double WindDown / already reaped
    h.cancel()                                 // A4: scheduler exits → emits → channel closes
    for d := range h.lifecycleCh {             // drain until closed == "goroutine exited" barrier
        switch d.Reason {
        case session.ReasonRetire:  o.agentStore.Retire(ctx, d.AgentID, o.now())
        case session.ReasonCapital: o.agentStore.UpdateTradingCapital(ctx, d.AgentID, d.NewCapital)
        }
    }
    if h.sessionType == SessionTypeOnDemand {  // A6 (on-demand only)
        ids := agentIDsOf(h.population.All())
        o.leaseAllocator.Release(ctx, h.country, ids)
    }
    o.runtimeStore.DropSession(ctx, exchangeID)        // A7 runtime + market-data wipe
    o.activeSessionStore.Deregister(ctx, exchangeID)   // A7 LAST == commit point
    return nil
}

Canonical's drain applies RETIRE + CAPITAL; on-demand's drain is empty (pure barrier).

1. Canonical teardown

Frame: by the time WindDown(Canonical) arrives, the broker's FinalizeMarket has already squared, persisted, and unloaded every account (so the live observation read 409s; the durable GET /v1/account/cash is final). L2 does no flatten and no broker session-end call.

A1 square-off — NO-OP. Broker auto-squared pre-close. L2 never injects flattening orders.
A2 quit-for-good roll + RETIRE — real work (compute in scheduler, write in orchestrator). Rolled over the entire population (all presence states) so the per-agent E_quit/P yields E_quit expected quitters. f_quit = 1 (prototype); floor X omitted (next bring-up mints back to N, which is the implicit floor). Emits RETIRE{id}; orchestrator writes AgentStore.Retire(id, now) (sets agents.retired_at; tombstone, row kept; no Core account-close — there is no such endpoint).
A3 P&L → A fold — real work (survivors only; A2 before A3). Reads the durable core.GetAccountCash(handle).TradingCash; A_new = max(0, A_old + (TradingCash − broker_default)); emits CAPITAL{id, A_new}; orchestrator writes AgentStore.UpdateTradingCapital(id, A_new). A_old is the agent's TradingCapital carried in the working set / population; broker_default is the flat session-start funding config (assumption flagged in the design: if it ever diverges, capture session-start cash at bring-up instead). The shared helper (so recovery's DEFERRED_TEARDOWN reuses the exact logic — see L2_TIER6_RECOVERY_IMPL):
go
  // computeCanonicalTeardown runs the #6 roll over the full population and the
  // fold over survivors. Pure over its inputs; the caller routes the decisions.
  func computeCanonicalTeardown(
      rng *rand.Rand, agents []workingAgentView, P int,
      eQuit float64, brokerDefault int64, getCash func(handle string) (int64, error),
  ) []session.LifecycleDecision

The scheduler calls it with its working set and emits the decisions on lifecycleChannel; recovery calls it with a rebuilt list and applies them to Postgres directly.

A4 goroutine + WS shutdown. WindDown cancels sessionCtx; LiveScheduler.runLoop already exits on ctx.Done() (tick-top and in the sleep select), bounding order leakage to ≤1 in-flight tick (stragglers 409 at the unloaded broker). The dispatcher goroutine exits on ctx.Done() or the punch channel closing; runIngest exits on ctx.Done() and defer-closes the WS. The wrapper's defer close(lifecycleChannel) is the exit barrier the orchestrator waits on.
A5 broker session-end — NO-OP (L7 FinalizeMarket).
A6 lease release — N/A (canonical is not pool-leased).
A7 Redis cleanup — RuntimeStore.DropSession wipes runtime:{exch}:agent:* + :tipslot + marketdata:{exch}:*; then ActiveSessionStore.Deregister does HDEL sessions:active <exch> last (the commit point). Postgres is not touched by A7.

Plain English. When a canonical day closes, the session rolls dice once over every agent to retire a tiny trickle, then reads each remaining agent's final cash and computes its carried-over capital for tomorrow. It shouts both back; the orchestrator saves them to the database, wipes the session's scratch data from Redis, and finally strikes the market off the live-sessions list — that last strike is what marks the day officially closed.

2. On-demand teardown

Frame: WindDown(OnDemand) arrives after broker.EndSession(discard) has already dropped the one ephemeral account (Redis DEL, no Postgres). On-demand's durable footprint for the session is zero (trading_capital is NULL; traits are accepted dead weight). So there is nothing to flatten, persist, fold, or retire.

A1 square-off — NO-OP (positions discarded with the ephemeral account).
A2 retire — ABSENT. Leased throwaways with no cross-session identity; never retired, only released. lifecycleChannel carries no RETIRE/CAPITAL.
A3 fold — ABSENT (trading_capital is NULL; nothing to evolve).
A4 goroutine + WS shutdown — identical to canonical (same cancel → runLoop/dispatcher/ingest exit → channel close barrier). The drain just sees the close.
A5 broker session-end — NO-OP (L7 EndSession(discard)), and that is also what guarantees A6's discard-before-release ordering for free.
A6 lease release — real work. For every agent this instance holds (existing-leased + new-minted, from h.population.All()): HDEL lease:{country}:leased id + SADD lease:{country}:available id — the orchestrator-owned LeaseAllocator.Release (filling the current stub). Discard-before-release holds because Tier 7 ran EndSession(discard) before calling WindDown.
A7 Redis cleanup — same as canonical (DropSession then Deregister last); the on-demand live tape is thrown away (accepted).

Plain English. A practice session's agents are borrowed from a shared pool. On teardown there is nothing to save — the account was already discarded — so the session just stops, its borrowed agents are handed back to the free pool, and its scratch data is wiped.

3. New in the codebase (detailed — what this tier introduces)

New types / fields

session.LifecycleDecision.NewCapital int64 and session.ReasonCapital const — the CAPITAL variant (§0.3).
orchestrator.sessionHandle struct + Orchestrator.sessions map[string]*sessionHandle + sessionsMu sync.Mutex — the session registry (§0.1).

New store methods

store.AgentStore.UpdateTradingCapital(ctx, agentID, value int64) → UPDATE agents SET trading_capital=$2 WHERE agent_id=$1. (Today's InsertAgent is ON CONFLICT DO NOTHING, so it cannot update capital — this is the A3 write.)
store.ActiveSessionStore.Deregister(ctx, exchangeID) → HDEL sessions:active <exch> (the A7 commit point).
store.RuntimeStore.DropSession(ctx, exchangeID) → SCAN MATCH runtime:{exch}:agent:* + UNLINK, UNLINK runtime:{exch}:tipslot, SCAN MATCH marketdata:{exch}:* + UNLINK. (New, because RuntimeStore had no delete and Redis UNLINK cannot glob — a SCAN loop is required.)
store.LeaseAllocator.Release(ctx, country, agentIDs) — fill the current ErrNotImplementedThisPhase stub with the HDEL leased + SADD available pipeline (A6).

New session-layer surface

session.CoreClient gains GetAccountCash(ctx, accountID) (core.AccountCash, error) — today the interface only has GetObservation; the fold needs the durable read. The real *core.Client already implements it; the composition-root adapter sessionCoreClientAdapter (cmd/agentsd/session_wiring.go) adds the one forwarding method.
session.LiveScheduler gains a shutdown tail in StartScheduler (compute canonical effects + emit on lifecycleChannel) and the package-level computeCanonicalTeardown helper (§1.A3).

New orchestrator behavior

Orchestrator.WindDown body (replaces the log-only stub) + registerHandle/takeHandle + continueBringUp spawn change (§0.2, §0.5).

Config already present, now consumed: broker_default (flat session funding) for the fold; LifecycleBuffer for the channel (already a config field).

No schema migration. agents.retired_at and agents.trading_capital already exist; the fold is an UPDATE and the retire is an UPDATE on existing columns.

Not Tier 6 but flagged: cmd/agentsd/main.go currently wires NewNoOpScheduler; teardown's compute lives in LiveScheduler, so production must be switched to NewLiveScheduler (a composition-root change, separate from this tier).

4. Decision / flag log
#	Decision	Status
T6I.1	Session (LiveScheduler) computes A2/A3 and emits on lifecycleChannel; orchestrator drains + writes Postgres (decide/write split, unchanged)	LOCKED
T6I.2	Close-effects run in the scheduler's shutdown tail, triggered by the current runLoop exit	LOCKED (carries the 🚩 bug)
T6I.3	🚩 stale-cash-on-fold — fold may read pre-FinalizeMarket cash on the SessionEnd exit; accepted; later fix = gate on the WindDown cancel	OPEN — highly visible
T6I.4	A2/A3 logic lives in computeCanonicalTeardown, reused verbatim by recovery's DEFERRED_TEARDOWN	LOCKED
T6I.5	A4 relies on existing ctx.Done() exits in runLoop/dispatcher/runIngest; defer close(ch) is the barrier	LOCKED
T6I.6	A6 fills LeaseAllocator.Release; discard-before-release guaranteed by Tier 7 ordering	LOCKED
T6I.7	A7 = DropSession then Deregister LAST; no schema migration	LOCKED
T6I.8	GetAccountCash added to session.CoreClient + adapter	LOCKED

End — Tier 6 teardown implementation. Companion: L2_TIER6_RECOVERY_IMPL. Source design: L2_TIER6_TEARDOWN_CANONICAL_DESIGN, L2_TIER6_TEARDOWN_ONDEMAND_DESIGN.