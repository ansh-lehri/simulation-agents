# session — Per-Exchange Session Runner (Tier 2 2c + Tier 3 home)
<!-- Per-package CLAUDE.md. READ ORDER:
       1) CLAUDE.md                            — root constitution
       2) orchestrator/CLAUDE.md               — the actor that spawns this one (2a/2b)
       3) THIS file                             — specific to session
       4) the spec docs below                   — the authority
     This file does not repeat the above. -->

## Spec (the authority)
- `docs/agents/L2_TIER2_BRINGUP_DESIGN.md` §5–6 (2c pre-open run, the handover contract) + §7
  (failure policy).
- `docs/agents/L2_TIER2_IMPLEMENTATION_DESIGN.md` Point 6 (`runAgentPopulation`) + Point 7 (failure).
- `docs/agents/LAYER_2_LIFECYCLE.md` §4 (the inhomogeneous-Poisson entry curve → `entry_time`).
- Grows next into Tier 3 (`L2_TIER3_RUNTIME_LOOP_DESIGN.md` / `..._IMPLEMENTATION_DESIGN.md`) —
  the tick loop lands HERE, NOT in orchestrator. Out of scope for Phase 5.

## What session IS (one line)
The **second actor**: the per-`exchange_id` goroutine that OWNS a live session from the 2b
spawn onward. Phase 5 builds its pre-open run (2c) + go-live; Tier 3 adds the tick loop. One
goroutine per exchange.

## Owns / public surface
- **`Run(req, pop, ch)`** (the `runAgentPopulation` body) — the goroutine entry the orchestrator
  spawns at the end of 2b: (1) stamp each agent's `entry_time` from the entry curve over
  `[start,end]` · (2) run the THREE pre-open ops CONCURRENTLY — `loadSessions` (bulk install),
  `pullHistory` (`GetCandles` per configured timeframe (subset of `{1m,5m,30m,60m}`) → `WriteCandles` keyed per `(instrument, tf)`), `subscribeLive` (one WS per exchange, one batched subscribe carrying one snapshot sub per instrument plus one candle sub per `(instrument × configured TF)` → cache snapshot + spawn `ingest`) · (3) JOIN all three · (4) `waitUntil(start)` →
  `startScheduler` (STUB → Tier 3) → `reportReady`.
- **`ingest`** — one WS-consumer goroutine per exchange: `candle`→`AppendCandle` (keyed by the frame's own timeframe), `snapshot`→`WriteQuote` overwrite; outlives the join; `SessionClosed` hook → Tier 6.
- **`drawEntryTime`** — the entry-curve helper (seeded). *(This package is also the natural home
  for the Tier-3 3b presence curves that consume `curve_eff`.)*
- Consumes the **handover**: the in-memory `Population` (frozen traits = specs), the
  lifecycle→orchestrator channel `ch`, and the `sessions:active` record (incl. scenario
  `curve_eff`).

## Invariants (LOCKED — Tier-2 §5–7)
- **Owns the session from spawn; needs NO Postgres read to run** — identity/traits arrive in
  `Population`, runtime lives in Redis, positions/P&L read fresh from the broker.
- **Write-target split:** this goroutine writes **Redis runtime directly**; it NEVER writes
  Postgres (that is the orchestrator's — the DECIDE/WRITE rule). It also never writes
  positions/cash/P&L anywhere.
- **`entry_time` is stamped HERE (2c), not in 2b** — so a restart doesn't redraw it and change
  the entry pattern. Seeded (live = fresh). Agents stay **DORMANT** through all of 2c — none
  flip ACTIVE here; they activate at their own `entry_time` inside the Tier-3 loop.
- **The three pre-open ops are independent → concurrent; NOTHING goes live until all JOIN.**
- **Failure policy:** history or WS failure is **FATAL** (→ `failBringUp`); **partial
  session-load PROCEEDS** with the installed agents (the population is statistical).
- **On-demand history source = the CANONICAL market's candles** (broker never persists on-demand
  candles); live comes from the on-demand instance; both cached under the on-demand `exchange_id`.
- **Stores nothing from session-load but per-agent success/fail** — agent identity to the broker
  at order time is `capital_handle`, not participant_id.
- One WS per exchange; the subscribe batch on it covers one snapshot sub per instrument plus one candle sub per `(instrument × configured TF)`. All Core calls via `o.core`, spec L2's side only (cross-layer rule).

## Do NOT
- Do NOT write Postgres — hand durable writes back through the orchestrator.
- Do NOT implement the Tier-3 tick loop — `startScheduler` is a STUB this phase.
- Do NOT flip any agent ACTIVE in 2c.
- Do NOT implement lease Release/Reconcile, WAL Drain, `failBringUp` abort-cleanup,
  respawn-on-restart, or the canonical P&L→A fold — all Tier 6 (invoke the failure exit here;
  the cleanup is implemented there).

## Tests (ship with the code)
- `Run`: entry_time stamped for all (deterministic under fixed seed); the three ops run
  concurrently and the join blocks until all succeed; history/WS failure → failBringUp; partial
  session-load → proceeds; agents still DORMANT at go-live; `startScheduler` stub reached only
  after `waitUntil(start)`.
- `ingest`: candle appends, snapshot overwrites quote; outlives the join.
- Spy `o.core` + fake stores; no Postgres writes asserted. `-race` on.