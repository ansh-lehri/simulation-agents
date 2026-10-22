# decision — 4c Execution (Tier 4, the shared executor)
<!-- Per-package CLAUDE.md, extends the `decision` package. READ ORDER:
       1) CLAUDE.md                            — root constitution
       2) session/CLAUDE.md                    — the goroutine that calls into this package (Tier 3)
       3) core/CLAUDE.md                       — the punch contract this stage builds toward
       4) decision/perception/CLAUDE.md, decision/commit_traits/CLAUDE.md, decision/decision_compute/CLAUDE.md (4a + 4b sections) — the intent producers this stage consumes
       5) THIS file                             — specific to 4c
       6) the spec docs below                   — the authority
     Does not repeat the above. -->

## Spec (the authority)
- `docs/agents/LAYER_2_TIER4_4C_EXECUTION_IMPL.md` (LOCKED) — the HOW-in-code, §1–12. This is
  the primary spec; implement exactly what it says. No open design — 4c is pure implementation
  of an already-fully-LOCKED design.
- `docs/agents/LAYER_2_TIER4_4C_EXECUTION.md` — the WHAT/WHY (LOCKED, no open forks).
- `docs/agents/LAYER_2_TIER4_4A_PERCEPTION_IMPL.md` — the accessor (`CurrentPrice`, `Spread`,
  the bid/ask/quote reads), `workingAgent`, the seam. Read-only context, already shipped.
- `docs/agents/LAYER_2_TIER4_4B_DECISION_IMPL.md` — the `ExecIntent` producer for `open`
  (4b) and `decision.Params`'s first introduction. Read-only context, already shipped.
- `docs/agents/LAYER_2_TIER4_4D_POSITION_EXIT.md` — the OTHER `ExecIntent` producer, for `add`
  and `close` (4d). Read-only context for how 4c's `close`/`add` paths get fed — 4d itself is
  NOT part of this stage.
- `docs/agents/L2_TIER0_CORE_API_CONTRACT.md` + `core/order_punch.go` / `core/observation.go` —
  the exact wire contract this stage builds toward and reads from. Field names/types below are
  reproduced verbatim from the shipped code; do not rename or reshape them.
- `docs/agents/LAYER_2_TIER4_IMPL_ALTERNATIVES.md` — "4c — Execution" section: rejected
  alternatives (tick-size grid, open/add derivation, the IGNORE guard, θ_market placement,
  pre-flight reliance). Read before proposing a different shape for anything already settled.

## What 4c IS
The shared execute stage: `Execute(intent, ambient, acc, traits, rng, cfg) (core.OrderPunchRequest,
bool)` in `decision/execute.go`. Turns a committed `ExecIntent` (from 4b's `open`, or 4d's `add`/
`close`) into a `core.OrderPunchRequest`, filling the three unknown punch fields `{Type, Qty,
LimitPrice}` (plus `Side`/`TIF`) — `AccountID`/`Instrument` are ambient. It is the ONE executor
both callers feed; it does not know or care which stage sent the intent. **Disposition does not
enter 4c** — same conviction + same capital execute identically regardless of trend/mean-rev/
noise (framing doc: execution is disposition-agnostic). Axes consumed: capital → qty ceiling,
aggressiveness → type + price, skill → execution-noise on (qty, price).

## Owns / public surface
- **`decision.ExecIntent{Dir, Conviction, Action}`** (type, if not already introduced by 4b —
  check `decision/mix.go`/`commit.go` first; this stage does not redefine it, only consumes it).
  `Dir` ∈ {long, short}; `Conviction` ∈ [0,1]; `Action` ∈ {open, add, close}.
- **`Execute(intent decision.ExecIntent, ambient AmbientInputs, acc *Accessor, traits
  agent.TraitVector, rng *rand.Rand, cfg decision.Params) (core.OrderPunchRequest, bool)`**
  (`decision/execute.go`) — the single pure entry point. `bool=false` ⇒ no order; caller sends
  nothing to the dispatcher.
- Internal to `execute.go` (impl doc §3–8), not separately exported unless the impl doc's own
  file list says otherwise: the position-case dispatch (open/add vs close vs IGNORE), the
  formulation-Y qty calc, the market-vs-limit type calc, the spread-fraction limit-price calc,
  the skill-noise application, and the close-path sizing.
- **`decision.Params`** gains (composition root, `cmd/agentsd/calibration.go`): `MaxFrac`,
  `Gamma`, `ThetaMarket`, `KPlace`, `SigmaExec` — all global calibration placeholders, numbers
  TBD, shapes LOCKED.

## Where 4c sits in the seam (read-only context, not this package's code to write)
- 4c is invoked FROM the tree-assembly phase, once that phase has resolved the agent's single
  candidate intent across instruments (the one-punch resolution) — 4c itself never arbitrates
  between multiple candidate intents, never touches the network, and never touches
  `punchCh`/`runDispatcher`. `session.decide` supplies the observation, accessor, traits, and
  RNG, and sends 4c's result to the EXISTING dispatcher.
- `core.AccountObservation.Positions []core.Position` (already shipped, `core/observation.go`)
  is the ambient position read 4c consumes: `Qty` (signed, int64), `ReservedQty`,
  `WeightedAvgEntry`. Absent entry for an instrument ⇒ flat.
- `core.OrderPunchRequest{AccountID, Instrument, Side, Type, TIF, Qty, LimitPrice}` (already
  shipped, `core/order_punch.go`) is the exact output shape — field names/types above are
  copied verbatim from the real struct, do not deviate.

## Invariants (LOCKED)
- **Pure function, same discipline as 4a/4b.** No I/O, no Core calls, no `context.Context`
  parameter, no Redis. `Execute` reads only what's passed in (`ambient`, `acc`, `traits`, `rng`,
  `cfg`) and returns a value — it never calls `GetObservation`/`PunchOrder` itself.
- **Position-case dispatch collapses to three paths (§3):** `Action==close` → CLOSE;
  `holding && sign(pos.Qty) != sign(Dir)` → IGNORE (`return _, false` — reduce/flip is 4d's job,
  never 4c's); else (flat, or holding same-dir) → OPEN/ADD, one shared formula. Do not write
  separate OPEN and ADD code paths — formulation Y nets out the existing position, so they are
  the same math by construction (impl-alternatives #2).
- **`A_available` is ONE shared pool across ALL instruments** — `A − Σ(WeightedAvgEntry×|Qty|)`
  summed over every position in `observation.Positions`, not just the instrument being traded.
  Positions compete for the same capital.
- **No separate ceiling clamp on OPEN/ADD qty.** `max_frac, Conviction ≤ 1` already guarantees
  `base_notional ≤ A_available` by construction — do not add a redundant clamp here (one is
  reintroduced ONLY post-noise, see below).
- **`effective_aggr = aggressiveness ^ (1 − γ·Conviction)`, asymmetric/up-only.** Conviction can
  only warp aggressiveness TOWARD market, never toward patient. Power form, not addition — the
  warp must stay in `[0,1]` by construction and bite hardest near the cutoff.
- **`effective_aggr` carries forward into `limit_price`** — one consistent personality per
  trade; do not recompute a separate aggressiveness for the price calc.
- **`round_to_tick` rounds TOWARD less-aggressive** — BUY rounds DOWN, SELL rounds UP — so a
  tick-snap never accidentally crosses the book. Grid step is 1 (nearest whole tick); a per-
  instrument coarser tick size is explicitly deferred (impl-alternatives #1 — `store.Quote`
  carries none yet).
- **Skill-noise perturbs magnitudes only — qty and limit_price — never `Type`/`Dir`/`Side`.**
  Same categorical-vs-magnitude guardrail as 4a's bias/noise rule. Two INDEPENDENT fresh seeded
  draws (`rng`) — one for qty, one for price — never shared or derived from each other.
- **`qty_final` is re-clamped post-noise** to `[1, floor(A_available/current_price)]` for
  open/add, or `[1, closeable_qty]` for close — this is the ONE place a capital ceiling clamp
  belongs (post-noise safety net, not a pre-noise redundancy).
- **`σ_exec` is a single GLOBAL constant, never per-agent** — per-agent variation already lives
  in `traits.Skill`; a per-agent σ would double-encode skill (impl-alternatives entry mirrors
  4a's σ_perc reasoning).
- **`θ_market` is a global dial, never a per-agent trait** — per-agent variation in market-vs-
  limit comes entirely through `aggressiveness` (impl-alternatives #4).
- **CLOSE differs from OPEN/ADD only in sizing (§8) — type/price/skill machinery is reused
  verbatim, not duplicated.** `qty = |pos.Qty| − pos.ReservedQty` (binary full exit); capital
  math is SKIPPED (unwinding, not spending); `Conviction` is IGNORED; `effective_aggr =
  aggressiveness` RAW — no conviction warp (exit urgency is base temperament, not this trade's
  conviction). Do not write a second type/price/noise implementation for close — call into the
  same internal helpers OPEN/ADD uses.
- **CLOSE's post-noise ceiling is `closeable_qty`, not `A_available`** — do not reuse the
  open/add capital-based clamp bound on the close path.
- **Valid-by-construction output.** Every `core.OrderPunchRequest` 4c builds must already
  satisfy `core.validateOrderPunchRequest`'s pairing (`limit⇒gtc & price>0`;
  `market⇒ioc & price==0`; `Qty>0`; `Side∈{buy,sell}`) — pre-flight is a guard that should NEVER
  fire on a 4c-built order, not a filter 4c relies on to fix a malformed request (impl-
  alternatives #5). If a code path could produce an invalid combination, fix the path, don't
  lean on pre-flight to catch it.
- **No-order conditions (`return _, false`):** IGNORE (opposite-dir-while-holding guard);
  `qty_final == 0` after floor/clamp; `A_available ≤ 0`; a limit order with no two-sided book
  (`!HasBid || !HasAsk`) — market orders are unaffected by the book-sidedness check.
- **Fire-and-forget stays fire-and-forget.** 4c itself never retries, never correlates a punch
  to its later outcome — that discipline lives in the caller/dispatcher, but 4c's OUTPUT must
  never assume a retry will fix a bad request (reinforces the valid-by-construction rule above).

## Do NOT
- Do NOT implement 4b (`interpret.go`/`mix.go`/`gate_decide.go`/`commit.go`) or 4d (position/
  exit management, the stop/reversal/target/time-stop checks, the add-gate) — both are separate
  stages; 4c only consumes their `ExecIntent` output.
- Do NOT implement the tree-assembly phase's one-punch resolution across instruments, or the
  dispatcher/`PunchOrder` call — 4c hands back a value, it never sends anything over the
  network or a channel itself.
- Do NOT add a hard capital ceiling clamp before the skill-noise step on open/add — formulation
  Y's `max_frac·Conviction≤1` already bounds it; the only clamp is the post-noise safety net.
- Do NOT let CLOSE read or warp by `Conviction` — it's explicitly ignored on that path.
- Do NOT make `σ_exec` or `θ_market` per-agent fields on `TraitVector` — both are global
  `decision.Params` constants (see Invariants).
- Do NOT build partial trim/scale-out on close — binary full exit only, iteration 1 (impl doc
  §12 deferred list).
- Do NOT thread a per-instrument tick size through `round_to_tick` — deferred until one arrives
  upstream in `store.Quote`/the snapshot (impl-alternatives #1).
- Do NOT treat the IGNORE guard as dead code to skip — it's a safety guard for a future flow
  where 4b could fire while holding; keep it even though the current root dispatch makes it a
  cold path (impl doc §3, §12).
- Do NOT invent new calibration constants beyond `MaxFrac, Gamma, ThetaMarket, KPlace,
  SigmaExec` — if a shaping constant looks missing, it's a bug in the spec reading, not license
  to add a new global.

## Tests (ship with the code)
- **Position-case dispatch:** flat + any dir → OPEN math runs; holding same-dir → ADD math runs
  (same formula, nonzero start); holding opposite-dir → IGNORE (`false`, no order); `Action==
  close` routes to CLOSE regardless of dir-vs-position sign (close doesn't check sign agreement
  the same way — it reads `Action` directly per §3).
- **qty (open/add), formulation Y:** table test against hand-computed `A_available`/
  `base_notional`/`qty` fixtures, including a multi-instrument fixture proving the shared pool
  (an existing position in instrument B reduces available capital for a trade in instrument A).
  Edge: `A_available ≤ 0` → `false`; `qty==0` after floor → `false`.
- **type (market vs limit):** property test sweeping `aggressiveness`×`Conviction` — `
  effective_aggr` always in `[0,1]`; monotonically non-decreasing in `Conviction` at fixed
  aggressiveness (the up-only warp); matches `aggressiveness` exactly at `Conviction=0`.
- **limit_price:** BUY rounds down / SELL rounds up at the tick boundary (a fixture placed
  exactly mid-tick); edge — no two-sided book → `false`, market path unaffected by the same
  fixture.
- **Skill-noise:** fixed-seed `rng` → deterministic qty/price perturbation, byte-identical on
  repeat; `Type`/`Dir`/`Side` NEVER change under noise (property test over many noise draws);
  post-noise clamp respected at both bounds (`[1, ceiling]`).
- **Close path:** `qty = |pos.Qty| − pos.ReservedQty`; `Conviction` ignored (vary it, assert no
  effect on output); `effective_aggr == aggressiveness` raw (no warp, vary `Conviction` again to
  confirm); post-noise ceiling is `closeable_qty`, not a capital-derived bound.
- **Valid-by-construction:** every output of `Execute` across a wide property-test sweep passes
  `core.validateOrderPunchRequest` (import and call it directly in the test, or replicate its
  exact pairing assertions) — this is the single most important test in this stage.
- **Determinism:** fixed-seed `rng` → `Execute` run twice on identical inputs → byte-identical
  `(core.OrderPunchRequest, bool)` (root CLAUDE.md standing rule).
- `go test ./decision/... -race` green; `go vet` + `golangci-lint` clean.
