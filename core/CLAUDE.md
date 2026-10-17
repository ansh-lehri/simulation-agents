# core — The Core API Client (o.core) (Tier 0 / Phase 1)
<!-- Per-package CLAUDE.md. READ ORDER, all apply together:
       1) /CLAUDE.md                               — root project constitution
       2) THIS file                                — what is specific to core
       3) docs/agents/L2_TIER0_CORE_API_CONTRACT.md    — the authority for every endpoint
     This file does not repeat the root. -->

## Spec (the authority — read in orientation, implement exactly, invent nothing)
- `docs/agents/L2_TIER0_CORE_API_CONTRACT.md` — the whole doc (endpoints, client, policy, the
  three settled corrections).

## What core IS (one line)
L2's **single outbound client to Core** — pooled HTTP for request/response + one long-lived
WebSocket per exchange. L2 treats Core as one endpoint; routing to L7/broker is Core's
business, never modelled here. This is the ONLY package that makes network calls to Core.

## Owns / public surface
- **`o.core` client** — built once at startup, shared across goroutines. Methods (Tier-0 §3):
  - HTTP: `OpenAccount(user_id) → account_id` · `GetAccountCash(account_id) → {trading_cash,
    bank_cash}` · `JoinCanonical(user_id, market) → {session_id, participant_id}` ·
    `StartOnDemand(user_id, market, dur) → {session_id, warmup/session_ends}` ·
    `PunchOrder(order)` (fire-and-forget, response ignored) · `GetObservation(account_id) →
    {balance, positions, orders, recent_trades}` · `GetCandles(market, instrument, tf, from,
    to, cursor, limit) → candles`.
  - WS: `Subscribe(session_id, instruments) → (conn handle + connected frame)`.
- The request/response **DTOs** for each call, and the error-envelope decoder.

## Invariants (Tier-0 §1 conventions — LOCKED)
- **Cross-layer boundary rule:** specify ONLY L2's request + the response L2 expects. Never
  model how Core fulfills a call.
- **No auth** anywhere — identity is a trusted plain string; send verbatim, add no headers.
- **Money/price/qty = `int64`.** The only floats on the wire are display percentages
  (`change_pct`, `pnl_pct`).
- **Branch only on `reason` (snake_case) + HTTP status** — never on the `error` free-text.
- **`tif` is sent by L2:** `gtc` for limit, `ioc` for market. **Never `fok`.**
- **`funded_cash` comes from `GetAccountCash` (`trading_cash`)** — NOT from the account-open
  response (which returns only `account_id`).
- **Timestamps differ per endpoint** — candles/WS = naive-local; session timers =
  RFC3339Nano; `/v1/markets` = RFC3339. Use the right one per call; no shared parser.
- **Versioning is inconsistent** — most routes `/v1/…`, `/session/*` unversioned. Use the
  literal paths from the doc.
- **No secret in any log line.** Boundary-log method + status + correlation ids only.

## Policy (per-call-class, Tier-0 §3)
- **Timeouts sized per class:** pre-open calls (open/cash/join/candles) tolerate slack;
  `PunchOrder` is fire-and-forget (no wait on result); `GetObservation` is hot-path (short).
  No single global timeout.
- **Retry only idempotent calls** (account-open is get-or-create). **Never retry a write**
  (`504` ≠ nothing happened) — the caller reconciles via a read.
- **WS:** one connection per exchange; on drop, re-subscribe + re-pull history via
  `GetCandles` (no server backfill). Depth of reconnect policy = Tier 6.
- **Core unreachable → return a clean error.** What the *session* does about it = Tier 6, not
  here.

## Do NOT (in this package)
- Do NOT cache, store, or interpret responses — core returns values; the stores (Phase 2)
  and orchestrator (Phases 3/5) own what happens next. In particular, do NOT run the WS
  ingest/caching loop here — core hands back the connection; the orchestrator (2c) drains it.
- Do NOT read env vars — take a `ClientConfig` (base URL, per-class timeouts) injected by the
  composition root.
- Do NOT retry writes; do NOT branch on `error` text; do NOT send `fok`.
- Do NOT invent endpoints or fields — if the doc's shape looks wrong vs Core, stop and ask.

## Tests (this package — ship with the code)
- Drive every HTTP method against an `httptest.Server` returning the contract shapes; assert
  the **request** (path incl. versioning, payload, `tif` value per order type) and the
  **response decode**.
- Error paths: `{error}` envelope + `{reason}` on 409/422; assert the caller gets a typed
  error carrying `reason` + status, never the free text.
- Per-call-class timeout behaviour; account-open retry-safe, order punch not retried.
- WS: a fake WS server — assert the subscribe frame shape and the `connected`/`ack`/`candle`/
  `snapshot` frame decode. `-race` on.