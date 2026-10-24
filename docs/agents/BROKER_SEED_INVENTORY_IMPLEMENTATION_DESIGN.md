# Broker — SeedInventory Implementation Design

**Scope:** a new bringup-time broker endpoint that mints opening inventory onto a (live) seller-seeder account so it can rest asks at market genesis. One in-memory `CreditPosition` on the owner goroutine. No cash side, no counterparty — the same mint every buy-fill already does, done explicitly.

**Status:** design drafted (source-grounded in the real `economy/account` package + the locked broker funnel docs). Not yet locked.

**Why it exists (the ask-bootstrap problem):** In the `economy` package a SELL reserves *units*, not cash (`reserveSell` → `MovePosition(PosAvailable→PosReserved)`; fill → `RemovePosition`). Positions cannot go negative and there is no short-selling. Positions are also **session-scoped and never persisted to PG**, so *every* account opens flat every session. With zero inventory anywhere, only bids can rest → no cross → no trade → nobody ever acquires inventory → the book is frozen one-sided. The locked L7 "unlimited-balance operator" never solved this: unlimited *cash* seeds bids but cannot post a single ask (asks need inventory, not cash). SeedInventory bootstraps ask-side inventory the cheap way — no signed positions, no economy redesign.

**Key safety fact:** there is **no global unit conservation** in the economy. Every ordinary buy-fill already *mints* units into the buyer via `CreditPosition` (`convertBuy` step 2). Units are minted/burned per-account on every fill, never summed or balanced across accounts. SeedInventory is just that same mint, done explicitly at bringup — fully inside the existing model, not an exception to it. No reconciler undoes it (only `trading_cash` is reconciled; units are not).

**Where it sits:** new small phase after Phase 5. Reuses the Tier-0 funnel wholesale (`route`, `Command`, buffered-1 reply, `ErrBrokerTimeout`), the Phase-2 `s.sessions` map (read-only), and the L5 `account.CreditPosition` hand. New machinery is thin: one command, one handler body, one endpoint.

---

## 1. The command + result (`funnel/commands.go`, `funnel/results.go`)

```go
type SeedInventory struct {
    AccountID  AccountID
    Instrument contract.InstrumentID
    Qty        int64                    // > 0 (handler-validated pre-funnel)
    EntryPrice int64                    // > 0; becomes the FIFO cost basis
    ReplyTo    chan SeedInventoryResult // buffered(1) — funnel convention
}
func (c SeedInventory) Account() AccountID { return c.AccountID }

type SeedInventoryResult struct {
    Present   bool   // false = no live session on this shard → caller error (NO durable fallback)
    Available int64  // seeded PosAvailable for the instrument, on success
    Err       error  // reserved; CreditPosition only fails on <=0 args, already 400'd pre-funnel
}
```

Note vs `Transfer`: **no `From/To` buckets** (not a move, a mint), and `Present:false` is a hard error, not a fall-through to a durable path.

## 2. The facade (`funnel/broker.go`)

Thin shard round-trip, identical shape to `Broker.Transfer`:

```go
func (b *Broker) SeedInventory(ctx context.Context, aid AccountID, instr contract.InstrumentID, qty, price int64) (SeedInventoryResult, error) {
    reply := make(chan SeedInventoryResult, 1)
    b.route(SeedInventory{AccountID: aid, Instrument: instr, Qty: qty, EntryPrice: price, ReplyTo: reply})
    select {
    case r := <-reply: return r, nil
    case <-ctx.Done(): return SeedInventoryResult{}, ErrBrokerTimeout
    }
}
```

## 3. Dispatch case (`funnel/` serial loop)

```go
case SeedInventory:   c.ReplyTo <- s.handleSeedInventory(c)
```

## 4. The handler body (`funnel/shard_handlers.go`) — runs on the owner, single-writer

```go
func (s *shard) handleSeedInventory(c SeedInventory) SeedInventoryResult {
    sess, ok := s.sessions[c.AccountID]
    if !ok {
        return SeedInventoryResult{Present: false}          // not live here → caller 409
    }
    acct := sess.account

    // lazy-init the Positions map entry — exactly as convertBuy does
    // (account exposes no ensure-position helper; CreditPosition is a *Position method)
    pos := acct.Positions[c.Instrument]
    if pos == nil {
        if acct.Positions == nil {
            acct.Positions = map[contract.InstrumentID]*account.Position{}
        }
        pos = &account.Position{}
        acct.Positions[c.Instrument] = pos
    }

    // THE mint. units → PosAvailable + a new FIFO lot at EntryPrice. No cash touched.
    if err := pos.CreditPosition(c.Qty, c.EntryPrice, time.Now()); err != nil {
        // only ErrNonPositiveUnits / ErrBadPrice — both pre-validated to 400,
        // so reaching here is a programming error. Return, don't panic (caller 500).
        return SeedInventoryResult{Present: true, Err: err}
    }

    s.persist.MarkSnapshotDirty(sess.SessionID(), c.AccountID)  // so the Redis mirror / crash-recovery sees the minted lot
    return SeedInventoryResult{Present: true, Available: pos.Available}
}
```

That's the whole mutation. `CreditPosition` self-preserves the §0.6 position↔lot coupling (adds `Qty` to both `Available` and the lot's `RemainingQty`); no cash-conservation assert is in play because no cash moves.

## 5. The HTTP endpoint (`bringup` mux, Tier-4 house style)

```
POST /v1/account/seed-inventory
body: { account_id, instrument, qty, entry_price }
```

```
handler (request goroutine):
  1. ctx := r.Context()
  2. parse body
  3. validate: account_id present, instrument non-empty, qty > 0, entry_price > 0  → else 400
  4. ctx, cancel = context.WithTimeout(ctx, seedTimeout)   // handler owns timeout; same knob-shape as readTimeout/transferTimeout
  5. res, err := broker.SeedInventory(ctx, aid, instr, qty, entry_price)
       err == ErrBrokerTimeout                → 504
       res.Present == false                   → 409  (no live session for this account)
       res.Err != nil                         → 500  (should be unreachable post-validation)
       ok                                     → 200 { instrument, available }
```

No auth, `account_id` in body (Tier-4 convention). `seedTimeout` from broker config.

## 6. Caller contract (L2, cross-process — the trigger)

Per the **cross-layer boundary rule** (L2 specifies only its own request + expected response, never how Core fulfills it): during bringup **2c**, after the concurrent session-load join completes and **before go-live**, the L2 orchestrator calls this endpoint once per dedicated seller-seeder account over the network (same `o.core` HTTP client it uses for session-load). Buyers need no call — cash-backed bids work as-is.

**Timing is load-bearing:** `session-load` must precede SeedInventory (the in-memory `Account` only exists once loaded), and SeedInventory must precede the seeder's first sell (`reserveSell` reads `PosAvailable`).

## 7. Decisions / notes

- **SI-D1 — Session-only, no durable branch.** Unlike `Transfer`, there's no off-session path: positions live only in the in-memory session `Account` and are never persisted to PG. `Present:false → 409`, full stop.
- **SI-D2 — Return, don't panic, on `CreditPosition` err.** Transfer/place panic on post-read hand failures as single-writer invariant breaks; here the only failure modes (`<=0` args) are pre-validated to 400, so a non-nil `Err` means a caller bypassed the endpoint — return 500, don't take down the shard.
- **SI-D3 — `EntryPrice` = market reference price** (the same reference L7 computes for the InstrumentSpec), not an arbitrary constant — it's the FIFO cost basis, so it governs realized-P&L sign/magnitude on both voluntary close and auto-square.
- **SI-D4 — Persistence follows the account's existing `PersistOnClose`** (set at session-load), not SeedInventory. Cleanest if seeder accounts are a non-persisted sub-population (`PersistOnClose=false`) → seeded-basis trades never hit `participant_trades`. **That's an L2 population decision, out of scope here** — flagged so it's not lost.
- **SI-D5 — Instrument-membership guard: deferred/optional.** Seeding an instrument the session's market doesn't trade produces un-sellable (harmless) inventory. Can reuse Phase 5's `knownInstrument` seam to 400 it, but it's a carried nicety, not required for correctness.

## 8. Sell-path verification (why nothing hampers seeded inventory)

Traced end-to-end against source — seeded units flow cleanly through every path:

- **`reserveSell`**: reads `ReadAvailablePosition` → seeded units in `Available` → passes guard → `MovePosition(Available→Reserved)`. No cash assumption on the sell side.
- **`convertSell`** (fill): `RemovePosition` consumes the seeded FIFO lot from Reserved → `CreditCash(Available, proceeds)` → sell costs debited *after* proceeds land. No panic.
- **`ComputeRealizedPnl`**: pure `(makerPrice − lot.EntryPrice)×qty` using the seeded `EntryPrice`; zero assumption that cash ever flowed in to acquire the lot.
- **`SettleSessionEnd`** (auto-square): if a seeder still holds seeded units at close, it explicitly moves any `Available` portion → `Reserved` first (handles unreserved held units), then `RemovePosition` + proceeds at closing price. Clean.

## 9. Files touched

- `funnel/commands.go` — `+ SeedInventory` command (+ `Account()`).
- `funnel/results.go` — `+ SeedInventoryResult`.
- `funnel/broker.go` — `+ Broker.SeedInventory` facade.
- `funnel/shard_handlers.go` — `+ handleSeedInventory` + dispatch case.
- `bringup/` — register `POST /v1/account/seed-inventory`; supply `seedTimeout` config.
- **Unchanged / reused:** `economy/account/*` (`CreditPosition` as-is), the funnel, `s.sessions`, `MarkSnapshotDirty`.

## 10. Open micro-decision (not yet taken)

Single-shot (one `{instrument, qty, price}` per call, as written here, matching Transfer's one-mutation-per-call simplicity) vs **batch** (`[{instrument, qty, price}]` for a multi-instrument seller in one round-trip). Batch is trivial to add if seeders hold inventory across several instruments.