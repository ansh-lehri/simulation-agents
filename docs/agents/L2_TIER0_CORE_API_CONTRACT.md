Layer 2 — Tier 0: Core API Contract & Client (Design + Implementation) — LOCKED

What this doc is. The L2↔Core boundary: the exact Core HTTP/WS endpoints L2 calls, their real request/response shapes, the single client (o.core) that speaks them, and the one inbound seam (Core→L2) that is a placeholder today. Small by design — after the topology is settled, Tier 0 is just "pin the wire + build the client." Read L2_OVERVIEW_AND_TOPOLOGY first.

Grounding. Every endpoint shape below is read directly from the Core API contract (docs/api-contract/ — README.md, cross-cutting.md, conductor.md, broker-order-transfer.md, broker-reads.md, marketdata.md, verified against the repo up to commit 1c436cc). Where our earlier Tier‑1/2 assumptions disagreed with the shipped contract, the contract wins and the correction is noted. Nothing here designs Core's internals (cross-layer boundary rule) — only L2's request-out / response-in and L2's own client.

Frame (settled, not reopened). L2 is a separate process. Agents use the same Core HTTP APIs humans use — there is no separate agent surface. So Tier 0 = L2 as an HTTP/WS client to Core's existing endpoints, plus L2's own tiny inbound surface for the bring-up trigger.

1. Global conventions (apply to every call below)

From cross-cutting.md:

No auth anywhere. Identity is a trusted plain string (account_id / user.user_id / ?session=). L2 sends it verbatim; builds no auth.
JSON in/out; Content-Type: application/json.
Money/prices/qty = bare int64 (coins/ticks). Never float. (Only floats on the wire are change_pct on the WS snapshot and pnl_pct on trade rows — both display percentages.)
Error envelope: {"error":"<free text>"}. error is not a machine code — never branch on its text. Order place/cancel add {"reason":"<snake_case_tag>"} on business rejects — reason and the HTTP status are the only safe things to branch on.
Timestamps differ per endpoint (no shared parser): candles & WS = naive-local 2006-01-02T15:04:05 (in the market's tz, named on the envelope); session timers = RFC3339Nano; /v1/markets = RFC3339. L2 uses the right one per call.
No idempotency keys (except order-cancel). A 504 never means "nothing happened" — reconcile with a read before retrying a write.
Versioning is inconsistent: most routes are /v1/…; the /session/* routes are unversioned. Use the literal paths below.
2. Outbound — the endpoints L2 calls (L2 → Core)
2.1 POST /v1/account/open — open an agent's account
Request: { "user": { "user_id": "<agent_id>", "email": "", "owner_type": "agent" } }
Response 200: { "account_id": "<opaque>" } — only account_id, no funded_cash.
L2 use: new agents only (2a Stage B). account_id → capital_handle. Idempotent (get-or-create by user_id), so re-sends are safe.
Corrections vs our Tier‑1/2 assumption: (a) the response does not carry funded_cash — read it from §2.2 instead; (b) [superseded] this endpoint used to hardcode owner type OwnerHuman for every caller, including agents — Core now accepts owner_type on the wire ("agent"/"human"), defaulting an omitted field to "human" so the existing human signup/login caller is unaffected; L2 always sends "agent" explicitly.
Bulk: one call per agent today; a bulk variant is a known future Core addition (L2's payload = a list of {user_id}).
2.2 GET /v1/account/cash?account_id=<handle> — the funded-cash source
Response 200: { "trading_cash": <int64>, "bank_cash": <int64> } (durable; 404 if the account doesn't exist).
L2 use: read trading_cash to get the broker's starting cash per agent → this is funded_cash, the ceiling for the per-agent trading_capital A (A = clamp(capital_size × jitter, floor, trading_cash)). This replaces the locked "funded_cash from the account-open response" — it comes from here.
Note: durable/pre-session read; not live. Fine for bring-up (A is drawn before orders).
2.3 Session install + session-id — the join endpoints

An agent's account must be installed on its shard before it can order (/v1/orders returns 409 no_session otherwise), and the live feed needs a session id (§2.7).

Canonical: POST /session/canonical — the same door humans use.

Request: { "user": { "user_id": "<agent_id>", "owner_type": "agent" }, "market": "<tag>" }
Response 200: { "session_id": "<uuid>", "participant_id": "<uuid>", "status": "active" }
Correction: owner_type is a new field on this request — an omitted value defaults to "human" (the existing frontend caller is unaffected), L2 always sends "agent" explicitly. [superseded] this endpoint used to hardcode owner type OwnerHuman for every caller, including L2's agents.

On-demand — [superseded, corrected]: POST /session/ondemand?market=<tag>&duration=<minutes 5–120> is NOT an endpoint L2 calls. That endpoint spins up a brand-new on-demand session/exchange instance; it is the human-facing trigger, and Core calls L2's own BringUp as part of handling it (Core→L2, not L2→Core). An L2 agent calling it per-agent would incorrectly mint one new on-demand session per agent instead of joining the one the human already created.

The endpoint L2 actually calls for on-demand is a separate, per-agent join door onto the market Core already created:

POST /session/ondemand/join

Request: { "user": { "user_id": "<agent_id>" }, "exchange_id": "<marketID>" } — exchange_id is the same value L2 received on its own BringUp request (AgentBringUpRequest.ExchangeID), carried straight through.
Response 200: { "session_id": "<uuid>", "participant_id": "<uuid>", "status": "active" }
No owner_type field — this endpoint's only caller is L2, so Core resolves OwnerAgent unconditionally.
Errors: 409 if no Live on-demand market exists under exchange_id, or if the agent already holds a live session elsewhere (same per-agent XOR gate as canonical join — shared registry, one slot per agent across both market modes).

L2 use (both doors): installs the account on the shard and yields a session_id (used for the WS feed) + participant_id. L2 stores session_id only as needed for the feed; participant_id is broker/L7-side.
Note: participant_id is injected broker-side at order time — L2 identifies an agent on orders by account_id (= capital_handle), not participant_id.
2.4 POST /v1/orders — place order (fire-and-forget)
Request: { "account_id", "instrument", "side": "buy"|"sell", "type": "limit"|"market", "tif": "gtc"|"ioc", "qty": <int64>, "limit_price": <int64> }
tif is REQUIRED (contract makes it mandatory). L2 sets gtc for limit (rests on book) and ioc for market (take-and-done). Do not use fok — it's accepted but broken (behaves as ioc). (This replaces the locked "broker computes TIF" — L2 sends it.)
limit_price > 0 for limit; 0/omitted for market.
lowercase, case-sensitive enums.
Response 202: { "order_id": "<account_id>:<seq>" }. L2 ignores it (fire-and-forget, A1). Order/fill/reject state is broker-owned, read fresh.
Rejects L2 may observe later (not at punch): 409 {"reason":"no_session"} (account not installed), 422 {"reason":"<tag>"} (e.g. insufficient_funds, bad_qty, no_market_price for an untraded instrument), 504 (shard slow).
L2 use: Tier‑3 dispatcher. Payload = {account_id, instrument, side, type, tif, qty, limit_price}.
2.5 GET /v1/account/observation?account_id=<handle> — the fresh own-state read
Response 200: { "balance": {available, reserved, settled, bank}, "positions": [...], "orders": [...], "recent_trades": [...], "trades_degraded": bool }
L2 use: the single read 4d's position/exit logic uses (and own-state for P&L-reactivity). 409 if no live session. Position marks (mark_available/ltp) are populated once the instrument has traded on a wired instance.
2.6 GET /v1/candles?market=&instrument=&timeframe=&from=&to=&cursor=&limit= — history
Params: market (IN/US), instrument, timeframe ∈ 1m|5m|30m|60m, from/to naive-local, optional cursor (naive-local), limit (default 100, cap 500).
Response 200: { market, instrument, timeframe, timezone, from, to, "candles":[{start,end,open,high,low,close,volume}], "next_cursor" } (candles never null; next_cursor null on last page).
L2 use: 2c history pull → Redis. On-demand: market = the canonical market (broker never persists on-demand candles). Broker serves multiple TFs (not 1m-only) — confirmed.
2.7 GET /v1/ws?session=<session_id> — live feed (WebSocket)
Open: GET /v1/ws?session=<session_id> (session id from §2.3; conductor resolves session→market→fanout). JSON text frames, anonymous otherwise.
On connect: {"type":"connected","market","timezone"}.
L2 sends: {"type":"subscribe","subs":[{"stream":"candle","instrument":"X","timeframe":"1m"},{"stream":"snapshot","instrument":"X"}]} — the subscribe batch on the WS can mix stream kinds and multiple candle timeframes freely. One WS per exchange_id carries every subscription L2 needs: one snapshot sub per instrument plus one candle sub per (instrument × configured timeframe).
L2 receives: ack (per-sub ok/error) · snapshot (phase, last_price, change_pct, quote, depth — whole-picture re-emit) · candle (finalized bars only, no forming bar).
L2 use: 2c WS subscribe → Redis (candles append per timeframe key, quote overwrite). One WS per exchange (public data; use one session id for the exchange's feed). Which candle timeframes ride the subscribe batch is a per-bring-up dial — the same list the history pull uses, any non-empty subset of {1m,5m,30m,60m}. Live movement of the forming bar rides snapshot.last_price, not candle.
Known races (retry): subscribe nacks unknown instrument until that instrument has printed once on the tape; slow-client eviction has no backfill (re-fetch via §2.6 on reconnect).
3. The o.core client (L2's single client to Core)

What it is. One client object owning all communication with Core. L2 treats Core as one endpoint — not split into broker/L7 clients (routing is Core's business). Built once at L2 startup, shared across goroutines.

Two transports (forced by the two interaction kinds):

HTTP — a shared connection pool, process-wide, for all request/response calls (§2.1–2.6). Short, stateless, pooled/reused.
WebSocket — one long-lived connection per exchange, opened by that session's goroutine in 2c (§2.7), closed at teardown. Not pooled (session-scoped, stateful).

Method set:

HTTP (request/response):
  OpenAccount(user_id)                 → account_id                         [2.1]
  GetAccountCash(account_id)           → {trading_cash, bank_cash}          [2.2]
  JoinCanonical(user_id, market)       → {session_id, participant_id}       [2.3]
  JoinOnDemand(user_id, exchange_id)   → {session_id, participant_id}       [2.3]
  PunchOrder(order)                    → (ignored; fire-and-forget)         [2.4]
  GetObservation(account_id)           → {balance, positions, orders, …}    [2.5]
  GetCandles(market, instrument, tf, from, to, cursor, limit) → candles     [2.6]
WS (streaming):
  Subscribe(session_id, instruments)   → (connection handle + connected frame)  [2.7]

Policy (per-call-class, from the contract):

Timeouts: pre-open HTTP calls (open/cash/join/candles) tolerate slack; order punch is fire-and-forget (no wait on result); observation reads are hot-path (short). Each call-class sizes its own timeout; no single global.
Retry: idempotent calls (account-open) are safe to retry; writes (orders, transfer) are not (504 ≠ nothing happened) — reconcile via a read instead.
Serialization: JSON (contract-wide). No auth headers.
WS reconnect: on drop, re-subscribe and re-pull history via GET /v1/candles (no server backfill). Depth of reconnect policy = Tier‑6.
Core unreachable: calls return errors cleanly; what the session does on persistent failure = Tier‑6.
4. Inbound — the one seam Core calls on L2 (BringUp / WindDown)

What it is. Core (conductor) triggers L2 to populate a market (BringUp) and to wind it down (WindDown). These are the only endpoints L2 itself exposes.

Current reality (contract). In the shipped code these are an in-process Go interface (AgentOrchestrator.BringUp(ctx, …), agents.WindDown(tag, mode)) that the conductor calls directly — because there is no composition root / HTTP server yet (cross-cutting.md §7).

L2's design (locked):

L2 exposes BringUp and WindDown behind an interface, implemented today as an in-memory placeholder that the conductor calls in-process (matching current code).
The interface is shaped to swap to an HTTP endpoint with zero logic change once Core stands up a server and calls L2 over the network (the separate-process target). The swap is transport-only; the BringUp inputs/behavior are already designed in Tier 1/2.
BringUp inputs/semantics = Tier 1/2 (L2_TIER2_BRINGUP_DESIGN); WindDown = Tier 6 (teardown). Tier 0 only fixes that the seam exists and is placeholder-now / HTTP-later.
[Core-side dependency, not L2's to build]: the conductor-side change to call L2 over the network, and the composition root joining the muxes onto one listener, are Core's. L2 designs its own side only.
5. Settled corrections to earlier locks (so Tier 1/2 stays consistent)
funded_cash ← GET /v1/account/cash (trading_cash), not the account-open response (which returns only account_id). Capital draw unchanged otherwise.
tif is sent by L2 on every order: gtc (limit) / ioc (market); never fok. (Was "broker computes TIF".)
account-open returns only account_id; owner type is OwnerHuman server-side (agent owner-type = a future Core concern, not a prototype blocker).
Session install + feed session-id come from the /session/* join endpoints (same as humans); orders need the account installed first (409 no_session).
6. What Tier 0 guarantees / what it defers
Guarantees: the exact outbound contract L2 codes against; one o.core client (pooled HTTP + per-exchange WS); L2's inbound BringUp/WindDown seam (placeholder-now, HTTP-later); the three corrections folded back.
Defers: the Core-side network wiring of BringUp/WindDown + composition root (Core's); WS reconnect/Core-unreachable session policy (Tier 6); any bulk account-open/session-load endpoints (future Core additions; L2's payloads are settled).

End of Tier 0. This closes the L2 foundation: Tier 0 (this) + Tier 1 (identity/persistence) + Tier 2 (bring-up) are design- and implementation-complete for the prototype.