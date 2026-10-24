# Tier 6 — Teardown (clean session-end / WindDown)
<!-- Spans FOUR packages: store (new/filled methods), session (shutdown tail + CoreClient
     addition), orchestrator (registry + WindDown body + spawn change), cmd/agentsd (the one
     adapter method + composition wiring). READ ORDER:
       1) CLAUDE.md                               — root constitution
       2) store/CLAUDE.md                         — the stores this stage extends
       3) session/CLAUDE.md                       — the scheduler shutdown tail this stage adds
       4) orchestrator/CLAUDE.md                  — the registry + WindDown this stage builds
       5) THIS file                                — specific to teardown
       6) the spec docs below                      — the authority
     Does not repeat the above. -->

## Spec (the authority)
- `docs/agents/L2_TIER6_TEARDOWN_IMPL.md` (the doc at hand) — the HOW-in-code, §0–4. This is the
  primary spec; implement exactly what it says, in its own order. It is explicitly a SHARED
  backbone (§0) plus two populations (§1 canonical, §2 on-demand) — the backbone is built once,
  both populations reuse it.
- `docs/agents/L2_TIER6_TEARDOWN_CANONICAL_DESIGN.md` + `L2_TIER6_TEARDOWN_ONDEMAND_DESIGN.md` —
  the WHAT/WHY, the A1–A7 concern ledger the impl doc's §1/§2 walk through concern-by-concern.
  Read for the "why" behind each A-number; the impl doc's §1/§2 are the authoritative HOW.
- `L2_TIER6_RECOVERY_CANONICAL_DESIGN.md` + `L2_TIER6_RECOVERY_ONDEMAND_DESIGN.md` (if present in
  your docs tree) — the companion UNCLEAN/restart path. **Do not implement recovery in this
  stage** — but `computeCanonicalTeardown` (§1.A3) is explicitly built to be reused verbatim by
  recovery's `DEFERRED_TEARDOWN` later, so its signature must stay generic over its inputs (take
  a plain `[]workingAgentView` + `rng`, not a live scheduler reference) even though only
  teardown calls it today.
- `core/account_cash.go` (`GetAccountCash`) — the durable read this stage's fold needs. Already
  shipped on `*core.Client`; this stage only extends `session.CoreClient`'s narrower interface
  to include it.

## What this stage IS
The clean session-end path: `Orchestrator.WindDown(ctx, exchangeID, teardownMode)` — today a
log-only stub at `orchestrator/orchestrator.go:310` — becomes the real teardown: cancel the
session goroutine, drain its lifecycle decisions, write the durable consequences, release any
leased agents (on-demand only), wipe Redis, and deregister the session (the commit point). The
per-session goroutine (`session.LiveScheduler`) computes the end-of-day EFFECTS (the roll +
fold) in its own shutdown tail and emits them; the orchestrator DRAINS and WRITES them —
the decide/write split already governing every other write path in this codebase, unchanged
here, just extended to cover teardown.

## Owns / public surface (by package)

**`store`** (fill/extend existing interfaces — no new store type):
- `AgentStore` gains `UpdateTradingCapital(ctx, agentID, value int64) error` — `UPDATE agents
  SET trading_capital=$2 WHERE agent_id=$1`. Today's `InsertAgent` is `ON CONFLICT DO NOTHING`
  so cannot update capital; this is the A3 write.
- `ActiveSessionStore` gains `Deregister(ctx, exchangeID) error` — `HDEL sessions:active
  <exch>`. This is the A7 commit point — the LAST write in `WindDown`.
- `RuntimeStore` gains `DropSession(ctx, exchangeID) error` — `SCAN MATCH
  runtime:{exch}:agent:*` + `UNLINK`, `UNLINK runtime:{exch}:tipslot`, `SCAN MATCH
  marketdata:{exch}:*` + `UNLINK`. New because `RuntimeStore` had no delete and Redis `UNLINK`
  cannot glob — a SCAN loop is required, not a single command.
- `LeaseAllocator.Release(ctx, country, agentIDs)` — fill the current `ErrNotImplementedThisPhase`
  stub (`store/lease_allocator.go`) with the real `HDEL lease:{country}:leased <id>` + `SADD
  lease:{country}:available <id>` pipeline, per agent ID.

**`session`:**
- `CoreClient` (`session/interfaces.go`) gains `GetAccountCash(ctx, accountID) (core.AccountCash,
  error)` — today the interface only has `GetObservation`; the fold needs this durable read.
- `LifecycleDecision` (`session/lifecycle_decision.go`) gains `NewCapital int64` (set only when
  `Reason == ReasonCapital`) and the new const `ReasonCapital = "capital_fold"` (`ReasonRetire`
  already exists, unchanged).
- `LiveScheduler.StartScheduler` (`session/scheduler.go`) gains a shutdown tail: AFTER
  `runLoop` returns, for a CANONICAL session, compute the close-effects via the new
  package-level helper `computeCanonicalTeardown(rng, agents []workingAgentView, P int, eQuit
  float64, brokerDefault int64, getCash func(handle string) (int64, error)) []LifecycleDecision`
  and send each decision on `lifecycleChannel`. For ON-DEMAND, emit nothing. Then return — the
  orchestrator's spawn wrapper closes the channel.

**`orchestrator`:**
- `sessionHandle` struct + `Orchestrator.sessions map[string]*sessionHandle` +
  `sessionsMu sync.Mutex` — the session registry (§0.1). Helpers `registerHandle(exch,
  *sessionHandle)` and `takeHandle(exch) *sessionHandle` (lookup+delete, so `WindDown` is
  idempotent — a second call finds nothing and returns `nil` cleanly).
- The `continueBringUp` spawn change (§0.2): switch from the current bare `go
  orchestrator.sessionRunner.Run(context.Background(), ...)` to building a cancellable
  `sessionCtx` via `context.WithCancel(context.Background())`, registering a `*sessionHandle`
  BEFORE spawning, and wrapping the goroutine so IT owns `defer close(lifecycleChannel)` — the
  exit barrier `WindDown` drains against.
- `Orchestrator.WindDown` body (§0.5) — replaces the log-only stub: `takeHandle` → idempotent
  nil-check → `h.cancel()` → drain `h.lifecycleCh` applying `ReasonRetire`/`ReasonCapital` →
  (on-demand only) `leaseAllocator.Release` → `runtimeStore.DropSession` →
  `activeSessionStore.Deregister` (LAST, the commit point).

**`cmd/agentsd`:**
- `sessionCoreClientAdapter` (`cmd/agentsd/session_wiring.go`) gains the one forwarding method
  `GetAccountCash`, mirroring its existing `GetObservation`/`PunchOrder` pass-throughs exactly.
- Confirm (during orientation, do not guess) which existing config field the doc's
  `broker_default` (flat session-start funding) maps to — the strongest candidate in the current
  codebase is `agent.CapitalParams.DefaultFundedCash` (wired in `cmd/agentsd/calibration.go:204`
  as `100_000`) — verify this is the SAME number the broker actually seeds new accounts with
  before wiring it into `computeCanonicalTeardown`'s `brokerDefault` parameter; if it's a
  different config entirely, say so rather than silently reusing the wrong constant.

## Invariants (LOCKED)
- **Decide/write split, unchanged and extended, never bypassed.** `session.LiveScheduler`
  COMPUTES A2 (roll) and A3 (fold) and EMITS; `Orchestrator` DRAINS and WRITES Postgres. The
  scheduler package must never call `AgentStore`/`ActiveSessionStore`/`RuntimeStore` directly —
  `session.Runner` is forbidden from writing Postgres (root CLAUDE.md ownership spine).
- **`WindDown` is idempotent.** A double call (or a call after the session already reaped
  itself) must `takeHandle` → `nil` → `return nil` cleanly, no panic, no error. `takeHandle`'s
  lookup+delete semantics are what makes a second call safe.
- **Drain until the channel CLOSES, not until some count is reached.** `for d := range
  h.lifecycleCh` — the `close(lifecycleChannel)` the spawn wrapper's `defer` performs IS the
  barrier proving the scheduler goroutine has fully exited. Do not drain with a timeout, a
  `select` with a `default`, or any mechanism that could return before the goroutine is
  provably done — `A4`'s whole purpose is bounding order leakage to ≤1 in-flight tick via THIS
  exact barrier.
- **A7 ordering is fixed: `RuntimeStore.DropSession` THEN `ActiveSessionStore.Deregister`
  LAST.** Deregister is explicitly the commit point — "last strike marks the day officially
  closed" (impl doc, plain-English summary). Reversing this order, or doing them concurrently,
  breaks the "if Deregister succeeded, teardown fully completed" guarantee a restart-recovery
  path would otherwise rely on.
- **Canonical's drain applies BOTH `ReasonRetire` and `ReasonCapital`; on-demand's drain is
  EMPTY (a pure barrier).** On-demand's `LiveScheduler` shutdown tail emits nothing onto the
  channel at all — do not special-case the orchestrator's drain switch to silently ignore
  on-demand decisions; there should be none to ignore.
- **A2 (retire) runs over the FULL population (all presence states), A3 (fold) runs over
  SURVIVORS ONLY, and A2 happens BEFORE A3** within `computeCanonicalTeardown` — a retired
  agent must not also receive a capital-fold decision in the same pass.
- **The fold reads `core.GetAccountCash` (the DURABLE read), never `GetObservation` (the live
  read)** — by the time `WindDown(Canonical)` arrives, the broker has already finalized and
  unloaded every account, so the live observation read would 409. This is why `session.
  CoreClient` needs the new method rather than reusing the existing one.
- **`A_new = max(0, A_old + (TradingCash − broker_default))`**, clamped at zero (an agent can
  never carry negative capital into the next session) — never clamp the subtraction before
  adding, only the final result.
- **A1 (square-off) and A5 (broker session-end) are explicit NO-OPs in BOTH populations** — do
  not add any flattening order or broker call here; the broker has already squared/discarded
  by the time `WindDown` is invoked. Writing code for these is adding scope the spec explicitly
  says has none.
- **A6 (lease release) is on-demand-ONLY** — canonical is not pool-leased; gate the
  `leaseAllocator.Release` call on `h.sessionType == SessionTypeOnDemand`, never call it for a
  canonical handle.
- **Discard-before-release ordering is guaranteed by Tier 7, not by this code** — Tier 7 calls
  `broker.EndSession(discard)` before calling `WindDown`, so by the time this stage's `A6` runs,
  the ephemeral account is already gone. Do not add a defensive check/wait for broker discard
  here; it's an external ordering guarantee, not something this package verifies.
- **🚩 The stale-cash-on-fold bug is CARRIED, not fixed, in this stage (T6I.3, OPEN but
  accepted).** `LiveScheduler.runLoop` exits on its own `SessionEnd` time check, which can fire
  before the conductor's `broker.FinalizeMarket` has actually finalized `trading_cash` and
  before `WindDown`'s cancel even arrives — so the fold can read a one-session-stale cash
  figure. This is ACCEPTED for this stage (drift self-corrects over many sessions; agents are
  internal-only). **Do not attempt to fix it as part of this change** — e.g. do not gate the
  shutdown tail on `ctx.Done()` instead of the `SessionEnd` loop-exit as a "while I'm in here"
  improvement. That specific fix (parking after `SessionEnd` until `ctx` cancellation, only then
  running roll+fold) is explicitly deferred to later. Leave a loud comment at the fold site
  referencing this doc's 🚩 so it isn't silently lost, but ship the bug as specified.
- **`computeCanonicalTeardown` is pure over its inputs** (`rng`, a plain agent-view slice, `P`,
  `eQuit`, `brokerDefault`, a `getCash` function) — no scheduler-internal types leak into its
  signature, specifically so recovery's `DEFERRED_TEARDOWN` can call it later with a
  rebuilt list instead of a live scheduler's working set. Keep the function in a place both
  call sites can reach without an import cycle (package-level in `session`, per the doc).
- **`f_quit = 1` (prototype) and floor `X` is omitted** — the next bring-up re-minting back to
  `N` is treated as the implicit floor. Do not add an explicit floor-enforcement mechanism here.
- **No schema migration.** `agents.retired_at` and `agents.trading_capital` already exist; both
  new writes are `UPDATE`s on existing columns. If implementing either write seems to need a new
  column, stop — that means a misread of the spec, not a missing migration.
- **The `LifecycleBuffer` config field and the composition-root switch from `NewNoOpScheduler`
  to `NewLiveScheduler` in `cmd/agentsd/main.go` are explicitly flagged by the spec as NOT part
  of this tier** ("Not Tier 6 but flagged") — do not perform that main.go swap as part of this
  change; it's a separate, later composition-root decision. `LifecycleBuffer` itself is already
  wired and consumed as-is.

## Do NOT
- Do NOT implement `L2_TIER6_RECOVERY_IMPL` (the unclean/restart path) — this stage is the clean
  WindDown path only. `computeCanonicalTeardown`'s generic signature is the ONLY concession made
  toward recovery reuse; do not build the recovery call site itself.
- Do NOT fix the 🚩 stale-cash-on-fold bug — carry it forward exactly as specified (see
  Invariants). Do not gate the shutdown tail on `ctx.Done()` as an unrequested improvement.
- Do NOT swap `cmd/agentsd/main.go`'s `NewNoOpScheduler` for `NewLiveScheduler` — explicitly a
  separate, later composition-root change per the spec.
- Do NOT add any square-off/flatten order logic (A1) or any broker session-end call (A5) in
  either population — both are no-ops by design, not omissions to fill in.
- Do NOT call `leaseAllocator.Release` for a canonical session, or retire/fold an on-demand
  agent — the two populations' drains are structurally different (full vs empty), not the same
  code with a flag.
- Do NOT reorder A7 (`DropSession` before/after/concurrent-with `Deregister` in any order other
  than DropSession-then-Deregister-last).
- Do NOT add a timeout, polling loop, or `select`-with-default around the `lifecycleCh` drain —
  it must drain to channel-close, full stop.
- Do NOT invent a new store type for any of the four new/filled methods — each lands on an
  EXISTING interface (`AgentStore`, `ActiveSessionStore`, `RuntimeStore`, `LeaseAllocator`).
- Do NOT guess which config constant `broker_default` refers to — verify against
  `agent.CapitalParams.DefaultFundedCash` (or correct it) during orientation before wiring it in.

## Tests (ship with the code)
- **`WindDown` idempotency:** call it twice for the same `exchangeID` — second call is a clean
  no-op (`takeHandle` returns nil, `nil` error, no panic, no double-drain, no double-Deregister).
- **Canonical drain:** a fixture `lifecycleCh` emitting a mix of `ReasonRetire` and
  `ReasonCapital` decisions — assert `AgentStore.Retire` and `AgentStore.UpdateTradingCapital`
  are each called with the right arguments, and ONLY after the channel closes (not mid-drain).
- **On-demand drain:** a fixture `lifecycleCh` that closes immediately with zero decisions —
  assert the drain loop exits cleanly with zero store calls, then `leaseAllocator.Release` IS
  called (on-demand-only gate) and `AgentStore.Retire`/`UpdateTradingCapital` are NOT.
- **A7 ordering:** a spy/fake on `RuntimeStore`/`ActiveSessionStore` asserting `DropSession` is
  called strictly before `Deregister`, every time, for both populations.
- **`computeCanonicalTeardown`:** table test — A2 rolls over the FULL population (including
  non-live presence states); A3 folds ONLY agents not retired by A2 in the same pass; `A_new =
  max(0, A_old + (TradingCash − brokerDefault))` including the clamp-at-zero edge (a large
  negative delta); determinism under a fixed seed (same inputs, same `rng` seed → identical
  decision list, run twice).
- **`GetAccountCash` on `sessionCoreClientAdapter`:** a direct forwarding test, same shape as
  the existing `GetObservation`/`PunchOrder` adapter tests.
- **`LeaseAllocator.Release`:** per-agent-ID, asserts both the `HDEL leased` and `SADD
  available` happen for every ID in the input slice; a partial-failure case (if the underlying
  client supports simulating one) behaves sensibly (document the chosen behavior — e.g.
  continue-and-report vs fail-fast — since the spec doesn't pin one explicitly; ask if genuinely
  ambiguous rather than guessing silently).
- **`RuntimeStore.DropSession`:** asserts the SCAN+UNLINK loop actually walks multiple SCAN
  pages (not just a single-page happy path) and covers all three key patterns
  (`runtime:{exch}:agent:*`, the single `tipslot` key, `marketdata:{exch}:*`).
- **`ActiveSessionStore.Deregister` / `AgentStore.UpdateTradingCapital`:** straightforward
  integration tests against the existing store test harness (transaction-rollback for Postgres,
  real Redis commands for the Redis-backed stores) per each package's own `CLAUDE.md` testing
  discipline.
- **Spawn-change regression:** after the `continueBringUp` change, assert a registered
  `sessionHandle`'s `cancel()` actually stops the (test-doubled) scheduler and the channel
  closes — this is the regression guard for `WindDown`'s drain barrier actually working
  end-to-end, not just in isolation.
- `go test ./store/... ./session/... ./orchestrator/... ./cmd/agentsd/... -race` green;
  `go vet` + `golangci-lint` clean.
