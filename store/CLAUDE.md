# store — Durable + Runtime Persistence (Tier 1 / Phase 2)
<!-- Per-package CLAUDE.md. READ ORDER:
       1) CLAUDE.md                                         — root constitution
       2) THIS file                                          — specific to store
       3) docs/agents/L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN.md + L2_TIER1_IMPLEMENTATION_DESIGN.md
     This file does not repeat the root. -->

## Spec (the authority)
- `docs/agents/L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN.md` (stores, schema, capital, lease model).
- `docs/agents/L2_TIER1_IMPLEMENTATION_DESIGN.md` Points 2, 4, 5, 6, 8 (interfaces + impls).

## What store IS (one line)
L2's persistence layer: the Postgres `agents` table + the Redis runtime / market-data /
lease / WAL stores, each behind a narrow interface. It holds the building blocks; the
bring-up flow (Phases 3/5) sequences them.

## Owns / public surface
- **`AgentStore`** (Postgres): `InsertAgent` (idempotent by PK), `LiveMembers(country, pop)`
  (the reuse-index query, `retired_at IS NULL`), `LoadMany(ids)`, `Retire(id, at)`. + the
  `agents` DDL + the `idx_agents_reuse` partial index (migrations).
- **`RuntimeStore`** (Redis): `WriteInitialSnapshot` (writes ONLY `{presence=DORMANT,
  trading_capital}`), `SetPresence`, `SetTimers`, `StampEntryTime`, `SetTipper`. The
  `RuntimeSnapshot` struct (presence/timers/4b+4d working memory/tip-state) — shape defined
  here; later fields written by Tier 3/4 consumers.
- **`MarketDataStore`** (Redis): `WriteCandles`, `AppendCandle`, `WriteQuote` + the key helpers.
- **`LeaseAllocator`** (Redis): `SelectAvailable` (ATOMIC pop+mark, Lua) + `Enlist` — LIVE this
  phase; `Release` + `Reconcile` — STUB (→ Tier 6).
- **`PendingMintWAL`** (Redis): `Stage` + `Clear` — LIVE; `Drain` — STUB (→ Tier 6).

## Invariants (ownership spine — LOCKED)
- **L2 stores ONLY durable identity (Postgres) + per-session runtime + market-data cache
  (Redis).** **NEVER** positions / cash / P&L / trades — no such column, no such key. There
  is **no trade-log / P&L table.** Positions are never snapshotted.
- **One `agents` table, `population`-discriminated.** `traits` is JSONB (read whole; never
  filter agents by a trait value). `trading_capital` is a nullable BIGINT — **NULL ≡
  on-demand**. `retired_at` is the only durable lifecycle field.
- **`InsertAgent` MUST be idempotent by PK** — the WAL recovery path and 2b retries depend on
  re-runs being no-ops.
- **No frozen traits in Redis**, no positions/cash/P&L in any Redis op.
- **Market-data keyed by `exchange_id`** → canonical/on-demand isolation for free. L2 does
  **no rollup** — each timeframe pulled cleanly from Core, never folded locally. Which
  timeframes get populated is a per-bring-up config dial (any non-empty subset of
  `{1m,5m,30m,60m}`); the store writes whichever ones the caller pulls, and serving a
  timeframe that was NOT pulled is the broker's job.
- **Lease:** `available` is a SET (native "take N"); `leased` a HASH (reverse-lookup). Select
  is **atomic** (Lua pop+mark) so concurrent on-demand sessions can't double-lease.
- **WAL:** staged before account-open, cleared after the durable row is written.
- No secret in logs; log store op + correlation ids only.

## Do NOT
- Do NOT store positions/cash/P&L, or add a trade-log table.
- Do NOT read env — take `*pgxpool.Pool` / `*redis.Client` handles injected by the composition
  root (built via `connectivity`).
- Do NOT implement `Release`/`Reconcile`/`Drain` execution — stub them, they're Tier 6.
- Do NOT read JSONB field-by-field or add per-trait columns.
- Do NOT invent schema/keys — take them verbatim from the Tier-1 docs; if one looks wrong,
  stop and ask.

## Tests (ship with the code)
- **Postgres: integration against a dedicated TEST DB** (`l2_test`, never dev data), with
  **transaction-rollback per test**. Assert: `InsertAgent` idempotency, `LiveMembers` excludes
  retired + filters by country+population, `LoadMany`, `Retire` sets `retired_at`.
- **Redis:** assert every key scheme; `SelectAvailable` atomicity (concurrent select → no
  double-lease); `WriteInitialSnapshot` writes only the two fields; WAL stage/clear.
- `-race` on. No RNG here, so no determinism-replay needed except where `drawTradingCapital`
  is exercised (that's `agent`'s test).