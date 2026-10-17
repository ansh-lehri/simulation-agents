# transport — Inbound Seam & HTTPS Server (Phase 0)
<!-- Per-package CLAUDE.md. READ ORDER, all apply together:
       1) CLAUDE.md                               — root project constitution
       2) THIS file                                — what is specific to transport
       3) docs/agents/L2_TIER0_CORE_API_CONTRACT.md §4 — the authority for the seam
     This file does not repeat the root. -->

## Spec (the authority — read in orientation, implement exactly, invent nothing)
- `docs/agents/L2_TIER0_CORE_API_CONTRACT.md` §4 — the inbound BringUp/WindDown seam.
- `docs/agents/L2_TIER2_IMPLEMENTATION_DESIGN.md` Point 1 — the `BringUpRequest` fields +
  the async `HandleBringUp` shape (TYPE + seam only this phase; body is Phase 3/5).

## What transport IS (one line)
This process's **only inbound surface**: the HTTPS server + the `AgentOrchestrator` seam Core calls
to populate (`BringUp`) and wind down (`WindDown`) a market. Phase 0 stands up the server
and an **in-memory placeholder** behind the seam — no agent logic.

## Owns / public surface
- **`AgentOrchestrator` interface** — `BringUp(ctx, BringUpRequest) (BringUpAck, error)` +
  `WindDown(tag string, mode string) error`. This is the seam; the real impl is the
  `orchestrator` package (later phases).
- **`Server`** — the HTTPS server: construction (addr, TLS config, the injected
  `AgentOrchestrator`), route wiring, `Start`/`Shutdown` (graceful).
- **The placeholder orchestrator** (this phase only) — logs the call at the boundary and
  returns a stub ack, so the seam is exercisable end-to-end.
- A **health** route (liveness) returning 200 once stores are connected.

## Invariants
- **Seam shape is swap-ready (Tier-0 §4, LOCKED).** The interface is designed so swapping
  the in-process placeholder for a real HTTP endpoint is **transport-only, zero logic
  change**. Do not bake HTTP-vs-in-process assumptions into `BringUpRequest`/`BringUpAck`.
- **`BringUp` is async (Tier-2 Point 1).** It validates, and in the real impl returns once
  account-open completes while 2b/2c run as a tail. The placeholder returns the ack
  immediately; the *signature* must already be the async one, not a blocking one.
- transport **holds no agent state** — it owns the server + routing + the seam. It calls
  the orchestrator; it never reads Postgres/Redis or Core directly.
- No secret in any log line (root rule). Boundary-log every BringUp/WindDown with
  `ExchangeID`, `Country`, `SessionType`.

## Do NOT (in this package)
- Do NOT implement any BringUp/WindDown body — generation, persistence, Core calls,
  spawning goroutines. That is `orchestrator` (Phases 3/5). The placeholder only logs +
  returns a stub.
- Do NOT add outbound Core calls here — that is the `core` package (Phase 1).
- Do NOT read env vars here — config comes from `connectivity/config.Load()`, injected via
  `cmd/agentsd`.
- Do NOT invent trigger fields — `BringUpRequest` is exactly Tier-2 Point 1; if a field
  looks missing, stop and ask.

## Tests (this package — ship with the code)
- Server boots, health route returns 200, graceful shutdown on signal.
- A `BringUp` round-trip reaches the placeholder with the request intact; a `WindDown`
  round-trip routes through.
- The seam is an interface: the server test injects a spy `AgentOrchestrator` and asserts
  it was called with the decoded `BringUpRequest`. `-race` on.