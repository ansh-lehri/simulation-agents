# session — Per-Exchange Session Runner (2c go-live + Tier-3 runtime loop)
<!-- Per-package CLAUDE.md. READ ORDER:
       1) /CLAUDE.md                            — root constitution
       2) /orchestrator/CLAUDE.md               — the actor that spawns this goroutine (2a/2b)
       3) THIS file                             — specific to session
       4) the spec docs below                   — the authority
     Does not repeat the above. -->

## Spec (the authority)
- `docs/agents/L2_TIER2_BRINGUP_DESIGN.md` §5–7 (2c pre-open run, handover, failure policy).
- `docs/agents/L2_TIER2_IMPLEMENTATION_DESIGN.md` Point 6–7 (runAgentPopulation, failure).
- `docs/agents/L2_TIER3_RUNTIME_LOOP_DESIGN.md` (D1–D12) + `L2_TIER3_IMPLEMENTATION_DESIGN.md` (Points 1–10).
- `docs/agents/LAYER_2_LIFECYCLE.md` §4–9 (entry curve + transition rolls — reproduced VERBATIM).

## What session IS
The per-`exchange_id` goroutine that OWNS a live session. Two lives:
- **2c** — pre-open: stamp entry_time → three concurrent ops (session-load, history pull, WS
  subscribe + the ingest goroutine) → join → go-live at SessionStart → call startScheduler.
- **Tier 3** — post-go-live: the two-phase tick — ACTION (3a) drives the market, PRESENCE (3b)
  advances lifecycle. One goroutine runs the whole tick serially; it calls the Tier-4 `decide()`
  black box and never looks inside.

## Owns / public surface
- `Run(request, population, curveEff, lifecycleCh)` — the goroutine body: 2c, then startScheduler
  (synchronous tail).
- `ingest` — WS→Redis goroutine (candle→AppendCandle, snapshot→WriteQuote overwrite); outlives
  the join.
- `startScheduler` — build the working set, realize lifecycle curves once, spawn the dispatcher,
  run the loop.
- the in-memory **working set** + derived **ACTIVE subset**; the **two-cadence loop**
  (`TAction` market / `TPresence ≫ TAction` presence, sleep-to-next).
- **3a** runActionPhase: SNAPSHOT (once, bounded K) → DECIDE (ACTIVE subset, act-coin → decide())
  → COLLECT → EMIT (seeded shuffle → dispatcher, non-blocking).
- the **dispatcher** goroutine (drains punch channel → `o.core.PunchOrder`, 202 ignored).
- **3b** runPresencePhase: curve builder, two clocks, verbatim rolls, write split.
- the `LifecycleDecision{AgentID, Reason}` channel (drain is orchestrator-side).

## Invariants (LOCKED)
**2c:** entry_time is stamped HERE (not 2b); agents stay DORMANT through all of 2c; the three ops
are concurrent and nothing goes live until all JOIN; history/WS failure is FATAL, partial
session-load PROCEEDS; on-demand history source = the canonical market's candles.
**Tier 3:**
- One goroutine runs the tick serially — **no agent goroutines** (prototype). Only two other
  goroutines per session: ingest and dispatcher; neither touches the working set.
- **ACTION before PRESENCE** when both due → presence changes affect future ticks only →
  race-free active set, no locks.
- **`TAction` is a FLOOR** — sleep computed after work; heavy ticks clamp to 0 and chain. The
  heavy-`decide()` fix is the deferred DECIDE worker-pool; the 4-step split exists for it.
- Iterate the **ACTIVE subset only**; act-coin (`u < PAct`, session RNG) gates `decide()`.
- Read market **once per tick**, shared (bounded K). Current price = `quote.LastPrice` gated by
  `HasLast`; absent/not-present → that instrument reads flat this tick.
- Punch is **fire-and-forget**; L2 SENDS `tif` (gtc limit / ioc market, never fok); outcomes read
  fresh via `GetObservation` inside `decide()`, never correlated at punch.
- **Ownership spine:** positions/cash/P&L NEVER in the working set or snapshotted — read fresh.
- **Write split (D6):** ACTIVATE / IDLE / DONE write Redis runtime DIRECT, inline (write-on-change);
  RETIRE routes over `lifecycleCh` → orchestrator writes Postgres. This goroutine never writes PG.
- **Lifecycle math verbatim** (LAYER_2_LIFECYCLE), `f_i=1`, `c_fat=0`; Model A (checks ride the
  sweep; 20–40 min re-arm; `·Δ` on step-out/done, none on re-entry/give-up).
- Curves built ONCE; session owns baseline rates; `curveEff != nil` overrides StepOut/Done/Reentry
  only (EntryRate is consumed by 2c). Binding: EntryRate→A, StepOutRate→s, DoneRate→D, ReentryRate→p_max.
- **Degrade, never abort**; teardown is always Tier 6. Seeded session RNG (live = fresh).

## Do NOT
- Do NOT implement `decide()` — Tier-4 black box; provide the 3-part seam only (traits +
  RuntimeHandle, shared snapshotView, bound GetObservation). This phase decide() is a stub.
- Do NOT write Postgres (RETIRE goes via the channel); do NOT retry writes; do NOT cache order ids
  or correlate punches; do NOT snapshot positions/P&L; do NOT read Redis for agent state on the
  live path (working set is built from the handover).
- Do NOT build: the DECIDE worker-pool; the scenario engine that PRODUCES curveEff (only consume
  it); the real X≤active≤N ceiling (hook = no-op = N), mid-session join, P&L-driven DONE, f_i
  factors; RETIRE execution (square-off → quit roll → Core close), restart rebuild, WS reconnect,
  abort-cleanup, the X-floor, the real sessionOver/teardown — all Tier 6.

## Tests (ship with the code)
- 2c: entry_time stamped for all (deterministic, fixed seed); three ops concurrent + join;
  history/WS failure → failBringUp; partial session-load proceeds; DORMANT at go-live.
- Loop: ACTION-before-PRESENCE; sleep-to-next (cheap paces, heavy chains); TAction a floor.
- 3a: SNAPSHOT once + bounded K; only ACTIVE iterated; act-coin gates decide() (stub → deterministic
  batch under fixed seed); EMIT shuffle seeded; non-blocking (full channel drops, logged).
- 3b: verbatim rolls (done-before-step-out, re-entry-before-give-up); ACTIVATE on entry_time; write
  split (Redis direct vs RETIRE→channel); ceiling hook no-op.
- Degrade: snapshot miss / GetObservation error / punch error — no crash. `-race` on.