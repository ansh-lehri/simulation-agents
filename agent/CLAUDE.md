# agent — Identity & Trait Records (Tier 1 / Phase 2)
<!-- Per-package CLAUDE.md. READ ORDER:
       1) /CLAUDE.md                                         — root constitution
       2) THIS file                                          — specific to agent
       3) docs/agents/L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN.md + L2_TIER1_IMPLEMENTATION_DESIGN.md
     This file does not repeat the root. -->

## Spec (the authority)
- `docs/agents/L2_TIER1_IMPLEMENTATION_DESIGN.md` Points 1, 3, 7 (records + the capital helper).
- `docs/agents/L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN.md` §3 (trait vector), §4 (capital model).

## What agent IS (one line)
The **pure data types** of an L2 agent — `Agent`, `TraitVector`, and the capital helper — a
leaf package with no I/O and no deps on store/core/orchestrator. Everyone imports it.

## Owns / public surface
- **`Agent`** (Tier-1 impl Point 1): identity + durable-mutable (`CapitalHandle`,
  `TradingCapital`, `RetiredAt`, …) + `Traits` + the transient `FundedCash` (2a-only).
- **`TraitVector`** (Point 3): universal fields always present; endogenous-only and herd-only
  blocks as `omitempty` pointers so herd rows stay lean and OFF channels are absent.
- **`drawTradingCapital`** (Point 7): pure, seeded. `A = clamp(capital_size × jitter, floor,
  funded_cash)`; `funded_cash <= 0 → default_funded_cash`.

## Invariants
- **Money is `int64`** (`TradingCapital`, `CapitalSize` scale). Never float.
- **`capital_size` (frozen trait, the SCALE) and `trading_capital` (the session money A) are
  two different numbers** — scale lives in `Traits`, A is the mutable column. Do not conflate.
- **Frozen = write-once at mint.** The hot path never mutates `Traits`; immutability is
  enforced by code, not a DB constraint.
- **Tier-4 trait fields (`rho`, `phi`, `perception_profile`, `exit_profile`, `max_hold`) are
  DEFINED here as shape** (Tier 1 persists them) but their VALUES are drawn in generation
  (Phase 3). `theta_add` stays reserved/commented — not populated (blocked on `theta_act`).

## Do NOT
- No I/O, no store calls, no RNG beyond the injected seeded source in `drawTradingCapital`.
- Do NOT add a trade-log / P&L field — broker owns that, L2 never stores it.
- Do NOT invent trait fields or fill `channel_weights` contents (awaits the channel→trait map).

## Tests (ship with the code)
- Table-driven units for the record (de)serialization (JSONB round-trip, herd vs endogenous
  sparsity — absent blocks absent, not null).
- `rapid` property tests for `drawTradingCapital`: result always in `[floor, funded_cash]`,
  fallback to `default_funded_cash` when funded ≤ 0, deterministic under a fixed seed. `-race`.