# orchestrator — Bring-up Spine + Tail (Tier 2 / 2a + 2b)
<!-- Per-package CLAUDE.md. READ ORDER:
       1) CLAUDE.md                            — root constitution
       2) THIS file                             — specific to orchestrator
       3) docs/agents/L2_TIER2_BRINGUP_DESIGN.md + L2_TIER2_IMPLEMENTATION_DESIGN.md
     This file does not repeat the root. -->

## Spec (the authority)
- `docs/agents/L2_TIER2_BRINGUP_DESIGN.md` §1–4 (two actors, the whole shape, the 2a spine,
  the 2b writer tail) and §7 (failure policy).
- `docs/agents/L2_TIER2_IMPLEMENTATION_DESIGN.md` Points 1–5 (intake, spine, account-open,
  capital, continueBringUp body) and Point 7 (failure routing).

## What orchestrator IS (one line)
The actor that runs **2a + 2b**: receive `BringUp`, assemble `Population{existing, new}`,
open broker accounts for the new ones, resolve each agent's session capital, persist +
snapshot + register, then **spawn the per-exchange session runner** (the per-exchange
goroutine lives in the `session` package). The 2c pre-open ops + go-live are the
session runner's job.

## Owns / public surface
- **`HandleBringUp(req)`** — the async intake: `validateTrigger` → run the 2a spine (sync) →
  **return once account-open completes** (`accounts_ready`) → `go continueBringUp` (the tail).
  Real impl behind transport's `AgentOrchestrator` seam.
- **`bringUp(req, rng)`** — the 2a spine over one `Population`: Stage A (generation, via
  `generator`) → Stage B (`AccountOpenClient`) → Stage C (`CapitalResolver`).
- **`AccountOpenClient`** — the Stage-B bulk call (new agents only) over `o.core`.
- **`CapitalResolver`** — Stage C three-case dispatch using `drawTradingCapital`.
- **`continueBringUp(req, pop)`** — the 2b writer tail (`continue_bringup.go`):
  - P2 `persistNewAgents` — `agentStore.InsertAgent` + `pendingMintWAL.Clear` per new agent.
  - P3 `writeInitialRuntimeSnapshots` — `runtimeStore.WriteInitialSnapshot` for every agent
    (existing + new); writes only `{presence=DORMANT, trading_capital}` — entry fraction
    is deferred to the session runner.
  - P4 on-demand only: `enlistOnDemandAgents` (`leaseAllocator.Enlist` per new agent) +
    `setOnDemandRuntimeTipper` (one `runtimeStore.SetTipper` using a fresh seeded RNG).
  - P5 `registerActiveSession` (`activeSessionStore.Register` with `curve_eff` from
    `scenarioProvider.Effective` when on-demand+scenario) → `go sessionRunner.Run(...)`.
- **`FailBringUp(req, cause)` / `ReportReady(req)`** — the single failure / ready surfaces
  (satisfy `session.FailureReporter`). Logger-only this phase; the resilience layer fills
  in cleanup (deregister, release leased, drop partial snapshot/market-data, close WS)
  later.
- **`SessionRunner` interface** — the orchestrator's view of the per-exchange runner;
  satisfied structurally by `*session.Runner`. The compile-time witness in
  `session_runner.go` catches signature drift at build.
- The `BringUpRequest`/`BringUpAck`, `Population`, `SessionType` types.

## Invariants
- **Stage order A→B→C is forced by data dependency** (2a): A makes identities; B needs
  them to open accounts and yields `funded_cash`; C needs `funded_cash` to bound A.
- **Tail order P2→P3→P4→P5 is strict**: any step's failure routes through
  `failureReporter.FailBringUp` and skips every downstream step — the session runner is
  NOT spawned on failure, so a mid-tail abort does not leak a goroutine.
- **Persist then WAL.Clear** (P2): the WAL entry is cleared AFTER the durable row is
  written, so a crash in the window leaves a recoverable WAL entry rather than an orphan
  Postgres row.
- **Thin DORMANT snapshot** (P3): write only `{presence, trading_capital}` — entry fraction
  is deliberately NOT written here (the session runner stamps it inside its own pre-open
  phase so a restart of the agent process does not redraw and change the entry pattern).
- **Canonical skips P4** entirely; **on-demand runs both P4 steps** (enlist new agents +
  pick one runtime tipper under a fresh seeded RNG — deterministic replay under fixed seed).
- **Spawn with background ctx** (P5): the session runner is spawned on
  `context.Background()`, not the request ctx — the inbound handler may have long since
  returned, but the session is only just going live.
- **Generic population** — generation is NOT per-instrument; the trigger's `instruments`
  list is carried, never consumed by generation.
- **Reproducibility** — the spine and the tail's tipper pick both draw from a seeded RNG
  (`o.rngSource.Fresh()` for live). No seed derived from `exchange_id` or any identifier.
- **Cross-layer boundary rule** — Stage B/C touch Core only via `o.core`; spec request-out /
  response-in only. `funded_cash` comes from `GetAccountCash` (`trading_cash`), NOT the
  account-open response (Tier-0 correction).
- **Capital three cases:** NEW → draw A ≤ funded_cash; on-demand existing → draw fresh A
  ≤ default_funded_cash; canonical existing → keep persisted A.
- **Async return point = post-account-open**. The async tail's failures surface via L7
  poll-status, not the BringUp return.

## Do NOT
- Do NOT touch Postgres from inside the session runner — durable writes are
  orchestrator-only (the decide/write split). The runner may write Redis runtime directly.
- Do NOT draw traits here — delegate to `generator`. Orchestrator sequences; generator
  draws.
- Do NOT read env; take injected `o.core`, the four store surfaces, `scenarioProvider`,
  `sessionRunner`, `rngSource`, config.
- Do NOT implement teardown / abort-cleanup inside `FailBringUp` — this phase ships the
  surface only; the resilience layer wires the real cleanup into the same method body
  later.

## Tests (ship with the code)
- `validateTrigger`: dispatch fields (session_type/country/exchange_id) checked; others
  carried.
- Spine golden: a fake `generator` + spy `o.core` → assert Stage order, account-open
  called for `new` only, Stage-C three-case dispatch picks the right ceiling per case.
- Tail golden (continue_bringup_test.go): canonical happy path writes the right counts
  per step and skips P4; on-demand happy path adds P4 + a non-nil `curve_eff` on
  Register when a scenario is attached; each of P2/P3/P4-enlist/P4-tipper/P5 failure
  paths calls `FailBringUp` and skips every downstream step (session runner not
  spawned); the on-demand tipper pick is deterministic under a fixed seed.
- Determinism: fixed seed → identical `Population` (ids, traits, A) + identical tipper
  pick. `-race` on.
