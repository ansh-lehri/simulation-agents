# orchestrator — Bring-up Spine (Tier 2 / Phase 3 = 2a) 
<!-- Per-package CLAUDE.md. READ ORDER:
       1) CLAUDE.md                            — root constitution
       2) THIS file                             — specific to orchestrator
       3) docs/agents/L2_TIER2_BRINGUP_DESIGN.md + L2_TIER2_IMPLEMENTATION_DESIGN.md
     This file does not repeat the root. -->

## Spec (the authority)
- `docs/agents/L2_TIER2_BRINGUP_DESIGN.md` §1–3 (two actors, the whole shape, the 2a spine).
- `docs/agents/L2_TIER2_IMPLEMENTATION_DESIGN.md` Points 1–4 (intake, spine, account-open, capital).

## What orchestrator IS (one line)
The actor that runs **2a**: receive `BringUp`, assemble `Population{existing, new}`, open
broker accounts for the new ones, resolve each agent's session capital — then (Phase 5) hand
off to the goroutine. Phase 3 builds 2a; `continueBringUp` (2b/2c) is a STUB until Phase 5.

## Owns / public surface
- **`HandleBringUp(req)`** — the async intake: `validateTrigger` → run the 2a spine (sync) →
  **return once account-open completes** (`accounts_ready`) → `go continueBringUp` (STUB this
  phase). It is the real impl behind transport's `AgentOrchestrator` seam (Phase 0 placeholder
  is replaced).
- **`bringUp(req, rng)`** — the spine over one `Population`: Stage A (generation, via
  `generator`) → Stage B (`AccountOpenClient`) → Stage C (`CapitalResolver`).
- **`AccountOpenClient`** — the Stage-B bulk call (new agents only) over `o.core`.
- **`CapitalResolver`** — Stage C three-case dispatch using `drawTradingCapital`.
- The `BringUpRequest`/`BringUpAck`, `Population`, `SessionType` types.

## Invariants
- **Stage order A→B→C is forced by data dependency** — A makes identities; B needs them to
  open accounts and yields `funded_cash`; C needs `funded_cash` to bound A. Do not reorder.
- **Generic population** — generation is NOT per-instrument; the trigger's `instruments` list
  is carried, never consumed by generation.
- **Reproducibility** — the spine draws from a seeded RNG (`o.rng.Fresh()` for live). No seed
  derived from `exchange_id` or any identifier.
- **Cross-layer boundary rule** — Stage B/C touch Core only via `o.core`; spec request-out /
  response-in only. `funded_cash` comes from `GetAccountCash` (`trading_cash`), NOT the
  account-open response (Tier-0 correction).
- **Capital three cases (Tier-2 Point 4):** NEW → draw A ≤ funded_cash; on-demand existing →
  draw fresh A ≤ default_funded_cash; canonical existing → keep persisted A.
- **Async return point = post-account-open** (supersedes the old sync lock). The async tail's
  failures surface via L7 poll-status, not this return.

## Do NOT
- Do NOT persist, snapshot, spawn, or touch Redis runtime here — that is 2b/2c (Phase 5). Stage
  A only STAGES the WAL (inside generator's mint); the clear/persist is Phase 5.
- Do NOT implement `continueBringUp` / `runAgentPopulation` bodies — stub them (Phase 5).
- Do NOT draw traits here — delegate to `generator`. Orchestrator sequences; generator draws.
- Do NOT read env; take injected `o.core`, `o.store`, `o.generator`, `o.rng`, config.

## Tests (ship with the code)
- `validateTrigger`: dispatch fields (session_type/country/exchange_id) checked; others carried.
- Spine golden: a fake `generator` + spy `o.core` → assert Stage order, account-open called for
  `new` only, Stage-C three-case dispatch picks the right ceiling per case.
- Determinism: fixed seed → identical `Population` (ids, traits, A). `-race` on.
