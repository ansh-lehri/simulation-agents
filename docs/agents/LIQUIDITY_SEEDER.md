# L2 Liquidity Seeder — Mechanism (discussion capture)

**Status:** mechanism agreed in discussion; not yet a locked design. This is the input for the impl design.

**What it is:** a dedicated group of dumb "operator" agents that bootstrap a live, two-sided, *moving* book at market open. It is **not** a separate engine/process/operator account. It is folded into L2's existing per-exchange goroutine at bringup, reusing machinery that already exists there (OHLCV in L2 Redis from 2c; the punch path + dispatcher from 3a).

**Supersedes:** the locked L7 "market-operator participant + `SeedLiquidity()` trigger" design. L7 just fires `BRING_UP` as normal; seeding becomes L2-internal. Covers **both canonical and on-demand** — folding it into the per-exchange goroutine removes the old "one operator can't serve N on-demand instances" blocker, so on-demand liquidity (previously parked) is unparked.

---

## Core shape

- A **separate control context** for a small group of seeder agents, created **before** the scheduler starts the real agent loop. Few **buyers**, few **sellers**.
- Seeders are **dumb**: no trait struct, no Tier-4 decision tree. Just "compute levels → punch limit orders". They only need a minimal broker participant identity to punch.
- They place **limit orders only**, at computed price levels.
- Hardcode-friendly (all throwaway knobs): buyer/seller counts, order sizes, the level set, the ~5-min active duration, the random re-entry cadence.

## Price levels

- **R1/R2/R3 + S1/S2/S3** (floor-trader pivots) computed from OHLCV, **plus** some orders at random levels within those bounds.
- **History exists** (on-demand, or canonical with a prior bar) → compute pivots off prior-session OHLC.
- **Genesis** (fresh canonical market, no prior bar) → anchor levels off **L7's reference price ± hardcoded offsets** instead of pivots.

## Timing

- The seeder control context is **created pre-open**, alongside agent population (PhaseClosed — population isn't orders, so no gate rejects it).
- Orders **fire at/after internal-open** only — limit orders bounce during PhaseClosed and rest only once continuous trading opens. So "first 5 min" = first 5 min of *continuous trading*, not the pre-open window.
- **Continuous for ~first 5 min**, then the seeders drop into **random (lifecycle-driven) activity** rather than continuous — this also covers the one-shot-seed "no replenishment" gap.

## Ask-side bootstrap (sellers need inventory)

- A SELL reserves *units*, not cash, and positions can't go negative / no shorting. So sellers must hold inventory before resting asks.
- **Sellers:** call the broker `POST /v1/account/seed-inventory` endpoint first (mints opening inventory via L5 `CreditPosition`, no cash side) → **then** rest asks. Seed must land **before** the first sell. (See `BROKER_SEED_INVENTORY_IMPLEMENTATION_DESIGN.md`.)
- **Buyers:** no seeding — cash-backed bids work as-is.

## Getting the market to actually move (matching, not just a static book)

A two-sided resting book alone produces no trades. To force crosses:

1. **Rest the sell side first** — seed sellers → rest their asks as **makers** at the computed levels.
2. **Then buyers send crossing buy limits as takers** that lift those asks.
3. Buyers need no seeding (simplest as takers), and lifting the asks gives buyers **real inventory** they can later sell — the book becomes self-sustaining.

**Fill price:** every fill executes at the **resting/maker** order's price, never the taker's (L1 lock: *taker-then-maker, fill-at-maker-price*; confirmed in `convert.go`). A buy limit at 101 crossing a resting ask at 100 fills at **100** — the taker can get price improvement, never worse. "Exactly at that price" holds for the maker side.

---

## Ordered mechanism (per exchange goroutine, at bringup)

1. **Pre-open:** orchestrator creates the seeder control context alongside the normal agent population (both DORMANT, PhaseClosed).
2. Compute price levels: pivots from OHLCV if a prior bar exists, else reference ± hardcoded offsets.
3. **At/after internal-open:**
   a. For each **seller** seeder: call `seed-inventory` (per its instrument(s)) → on success, rest **asks** (makers) at S/R + random levels.
   b. For each **buyer** seeder: send **crossing buy limits** (takers) to lift the resting asks → trades print at maker prices; buyers acquire inventory.
4. **~5 min continuous:** keep both sides working the levels to establish a moving book.
5. **After ~5 min:** seeders switch to **random lifecycle-driven** activity (sparse top-ups), not continuous.
6. **Then:** the real agent scheduler loop goes live over the now-liquid book.

## Open / carried

- **Seed call granularity:** single-shot vs batch per seller (open — see seed-inventory doc §10).
- **Seeder persistence:** cleanest if seeder accounts are `PersistOnClose=false` (throwaway) so seeded-basis trades don't hit `participant_trades` — an L2 population decision.
- All hardcoded knobs (counts, sizes, levels, durations, cadence) are deliberate placeholders, not final design.