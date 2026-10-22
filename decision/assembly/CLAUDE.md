# Tree Assembly — the real `session.decide()` (Tier 4, the capstone wiring)
<!-- Spans two packages: `session` (the I/O orchestrator) and `decision` (new pure surface +
     a required refactor of 4b/4d). READ ORDER:
       1) CLAUDE.md                              — root constitution
       2) session/CLAUDE.md                      — the goroutine/seam this stage completes
       3) decision/perception/CLAUDE.md, decision/commit_traits/CLAUDE.md, decision/decision_compute/CLAUDE.md, decision/executor/CLAUDE.md, decision/position_mgmt/CLAUDE.md (4a + 4b + 4c + 4d sections) — every node this stage binds together
       4) THIS file                               — specific to tree assembly
       5) the spec docs below                     — the authority
     Does not repeat the above. -->

## Spec (the authority)
- `docs/agents/LAYER_2_TIER4_TREE_ASSEMBLY_IMPL.md` (LOCKED) — the HOW-in-code, §1–10. This is
  the primary spec; implement exactly what it says, in its own order.
- `docs/agents/LAYER_2_TIER4_4A_PERCEPTION_IMPL.md` — perceive/coast, the accessor,
  `instrumentMemory`, `tickContext`. This stage depends on 4a's OWN addition of
  `snapshotView.instruments []string` (stable order) — that field is 4a's codebase-change,
  not this stage's; confirm it exists before relying on it, do not add it here redundantly.
- `docs/agents/LAYER_2_TIER4_4B_DECISION_IMPL.md` — the flat-path producer, now RENAMED/
  RESHAPED by this stage: `decision.Decide` must return a `nodeOutcome` and become side-effect-
  free (§6) — **this supersedes that doc's §7/§9 description of 4b's commit stamping
  `lastCommitTick`/`positionContext` inline.** That inline write is removed; 4b now reports a
  `commitmentDelta` instead.
- `docs/agents/LAYER_2_TIER4_4D_POSITION_EXIT_IMPL.md` — the holding-path producer, `decision.
  Manage`, under the SAME side-effect-free requirement — **this also supersedes that doc's §9
  description of 4d re-stamping `positionContext` directly on ADD.** 4d now reports the
  re-stamp as a `commitmentDelta`; the assembly applies it only post-resolution, post-4c-success.
- `docs/agents/LAYER_2_TIER4_4C_EXECUTION_IMPL.md` — `decision.Execute`, invoked exactly once
  per tick, on the resolved winner only. Read-only, unmodified by this stage.
- `docs/agents/LAYER_2_TIER4_FRAMING.md` §2 — the gated decision tree this assembly completes.
- `docs/agents/LAYER_2_TIER4_IMPL_ALTERNATIVES.md` — the 4a/4b/4c/4d rejected-alternatives
  tables; this stage's own locked rule (close-beats-open/add, then conviction, then RNG coin)
  should be appended as a new "Tree Assembly" section as part of this change.
- `docs/agents/L2_TIER3_IMPLEMENTATION_DESIGN.md` + `session/scheduler.go` /
  `session/scheduler_types.go` — the exact seam this stage fills: `decideFunc func(decideInput)
  (core.OrderPunchRequest, bool)`, called once per acting agent, the act-coin and cross-agent
  shuffle (both untouched, upstream/downstream of `decide()`).
- `docs/agents/L2_TIER0_CORE_API_CONTRACT.md` + `core/observation.go` — the one shared
  `GetObservation` read this stage's `decide()` body performs.

## What this stage IS
The wiring, not a new node. Node docs 4a–4d each design one box; this stage designs how the
boxes run in order inside the real `session.decide()`: the root hold-check fork (holding→4d,
flat→4b, mutually exclusive, per instrument), the per-instrument traversal in STABLE order, and
the one-punch resolution that collapses however many instruments want to act into the single
`(core.OrderPunchRequest, bool)` the Tier-3 seam allows. **This is the point where Tier 4
becomes real** — the stub `return core.OrderPunchRequest{}, false` in
`session/scheduler_types.go` is replaced by the full tree.

## Owns / public surface
- **`session.decide(input decideInput) (core.OrderPunchRequest, bool)`** — replaces the stub.
  The I/O orchestrator: the ONE `observe()` call, the accessor build, the per-instrument
  traversal, applying deltas, mirroring to Redis, invoking `decision.Execute`. All I/O stays
  here; all computation it calls into is pure.
- **New pure `decision` types (§4):**
  ```go
  type nodeOutcome struct {
      assessment assessmentDelta       // ALWAYS applied, every instrument, win or lose
      candidate  *decision.ExecIntent  // nil ⇒ coast/skip/hold/wait
      commitment *commitmentDelta      // non-nil ONLY for open/add; applied ONLY if this
  }                                     //   candidate wins resolution AND 4c emits a real punch
  ```
  `assessmentDelta` carries the 4a/4b working-memory updates (standingOpinion, standingStance,
  pendingWatch, waitStreak, lastDecidedReadings, refreshed perception refs/lastBundle).
  `commitmentDelta` carries `lastCommitTick` + the `positionContext` stamp — the record that an
  order was ACTUALLY placed.
- **`decision.ResolveOnePunch(candidates []instrumentCandidate, rng *rand.Rand)
  instrumentCandidate`** (§5) — pure, deterministic given stable candidate order + the per-agent
  seeded `agentRNG`. Rule, LOCKED: any `close` beats any `open`/`add` (risk-off first); within
  the winning class, higher `ExecIntent.Conviction` wins; exact tie → `agentRNG` coin.
- **`positionFor(obs core.AccountObservation, instrument string) (core.Position, bool)`** (§8) —
  pure helper; `Qty != 0` ⇒ holding, else flat. Relies on `GetObservation`'s existing non-nil
  normalization of `Positions`.
- **`applyAssessment`/`applyCommitment`** (mutate `instrumentMemory`, pure-ish local mutators)
  and **`mirrorAssessment`/`mirrorCommitment`** (the Redis write-on-change calls through
  `memoryHandle`) — appliers are decision-package-adjacent helpers; mirrors live in `session`
  per the I/O-stays-in-session discipline.
- **The required refactor of `decision.Decide` (4b) and `decision.Manage` (4d):** both must
  return `nodeOutcome` and become side-effect-free — they READ carried state, they never WRITE
  it. The inline stamps their own impl docs described are removed from those files as part of
  THIS stage's change (cross-package refactor, not a reopening of 4b/4d's locked design — the
  computation is unchanged, only how its result gets reported).

## Where this stage sits in the seam
- **The hard constraint (§1) that shapes everything:** the Tier-3 seam returns exactly one
  order, or none, per agent per tick. The entire per-agent tree — 4a over all instruments, the
  fork, 4b/4d per instrument, down to one punch — runs inside ONE `decide()` call and MUST
  collapse to ≤1 punch. This is forced by the seam, not a design choice available to revisit
  here.
- **The act-coin and cross-agent seeded shuffle are Tier-3's**, upstream/downstream of
  `decide()` — unchanged, not touched by this stage.
- **`snapshotView.instruments []string`** is 4a's addition (owned by the 4a doc's own
  codebase-changes list, per §9) — this stage consumes it as a stable-order dependency, it does
  not introduce or own that field.
- **No new generated trait, no new Core call** (§3, §9) — this stage introduces zero new
  surfaces toward Core or the generator; everything it adds is pure wiring + the two new delta
  types.

## Invariants (LOCKED)
- **Traverse `input.market.instruments` as a stable SLICE, never a map range.** Perception
  consumes `agentRNG`; iterating a randomized map would make per-agent draw order (and thus
  decisions) differ run-to-run, violating the reproducibility rule every other stage upholds.
  This is the single most important determinism invariant in this stage.
- **One `observe()` call per `decide()`, shared by the whole tree.** It feeds the root fork, 4c's
  capital calc, and 4d's position read. An `observe()` error ⇒ the agent skips this tick
  entirely (`return {}, false`) — the hot-path rule "any error causes the agent to skip its
  action," not a retry-inline or a partial-tree execution.
- **4a (perception) runs FIRST and UNCONDITIONALLY for every present instrument — flat or
  holding.** A held instrument still needs a fresh bundle this tick for 4d's reversal check and
  level-mode exits (both read THIS tick's bundle, no re-perceive). Do not skip perception for
  holding instruments as an optimization — it breaks 4d's reversal/level logic.
  `!acc.Present(instrument)` is the only skip condition at this stage.
- **The root fork is exactly one branch per instrument, never both.** `holding → Manage (4d)`,
  `flat → Decide (4b)` — `positionFor` decides which, using the SAME shared `obs` from stage 0.
- **Assessment applies unconditionally, every instrument, win or lose (§4, §7 stage 1c).**
  Whether or not an instrument's candidate ultimately wins resolution or 4c succeeds, its
  `assessmentDelta` (what the agent THOUGHT) is applied and mirrored immediately, in the
  traversal loop — this is NOT deferred to after resolution.
- **Commitment applies ONLY post-resolution AND post-4c-success (§4, §7 stage 4).** This is the
  trap the whole assembly exists to avoid: if instrument A produced an open but instrument B's
  candidate won the single order slot, writing A's commitment would make the agent believe it
  holds A when no order was placed for A. `commitment` is non-nil only on the WINNING candidate,
  and even then is applied only after `decision.Execute` returns `ok=true`.
- **`nodeOutcome.candidate == nil` means "doesn't want to act this instrument"** — coast, skip,
  hold, or wait all collapse to this; only a non-nil candidate gets parked for resolution.
- **One-punch resolution order is fixed: class first, then conviction, then RNG** — never
  conviction-first (a high-conviction open must never beat a low-conviction close). Degenerate
  today (≤1 candidate in the single-instrument demo) does not excuse getting the rule wrong —
  it must be correct now so the multi-instrument path is correct later without revisiting this
  file.
- **4c (`decision.Execute`) runs exactly ONCE per tick, on the resolved winner only.** Never run
  it speculatively on multiple candidates to "see which succeeds" — a declined winner (`ok=
  false`) means the tick emits nothing; iteration 1 does NOT fall back to the next-best
  candidate (§10, explicitly deferred).
- **Three leaves return `{}, false` (§7):** observe error (stage 0); zero candidates after
  traversal (stage 2); 4c declines (stage 3). No other early return exists in this body.
- **4b/4d become side-effect-free as part of THIS stage's change, not before.** Do not treat the
  refactor as optional cleanup — the assessment/commitment split cannot work if `Decide`/
  `Manage` mutate `instrumentMemory` inline. If you find an inline stamp in either file while
  implementing, removing it and reporting the delta instead IS this stage's work, correctly
  scoped.
- **CLOSE never carries a commitment.** `commitment == nil` for a close outcome — `positionContext`
  is NOT cleared on a close emit (the order may not have filled yet; clearing would lose entry
  info for the still-open position next tick if the close doesn't fill). It is simply
  overwritten on the instrument's next open, per 4b/4d's existing (unchanged) behavior.
- **Integer/ambient plumbing only** — `accountID`, `instrument`, `acc` passed as `ambient` into
  `decision.Execute` exactly as 4c's signature expects; this stage does not reshape 4c's input
  contract, only supplies it.

## Do NOT
- Do NOT re-litigate or redesign 4a/4b/4c/4d's own computation — this stage touches 4b/4d ONLY
  to change HOW their result is reported (return a delta instead of mutating), never WHAT they
  compute.
- Do NOT add a 4c-decline fallback to the next-best candidate — explicitly deferred (§10);
  iteration 1 emits nothing on decline and relies on next-tick retry.
- Do NOT add the real instrument-attention mechanic — degenerate `instruments[0]`/≤1 candidate
  stays as-is; this stage makes the degenerate case correct, not richer.
- Do NOT build the DECIDE worker-pool — Tier-3's deferred item; this per-agent `decide()` body
  is the unit that pool would eventually parallelize, not something this stage parallelizes.
- Do NOT build restart-rebuild of `instrumentMemory`/`positionContext` — Tier 6.
- Do NOT add `snapshotView.instruments` here if it's missing — that's 4a's codebase-change; if
  it's absent, stop and flag it rather than silently adding it under this stage's banner.
- Do NOT apply commitment inside the traversal loop, and do NOT apply assessment only for the
  winner — both directions of this split are load-bearing; get either backwards and the agent's
  working memory lies about what it holds.
- Do NOT run `decision.Execute` more than once per tick, and do NOT run it before resolution
  completes.

## Tests (ship with the code)
- **Determinism (the headline test):** fixed-seed `agentRNG` + a fixed `input.market.instruments`
  order → `decide()` run twice on identical inputs → byte-identical `(core.OrderPunchRequest,
  bool)` AND identical `instrumentMemory` mutations across every instrument. Then: shuffle the
  SAME instrument set into a different map-iteration order while keeping the slice order fixed →
  still byte-identical (proves the traversal is slice-driven, not map-driven).
- **Assessment-always-applies:** a multi-instrument fixture where instrument A coasts/skips and
  instrument B produces a winning candidate — assert A's `assessmentDelta` (e.g. updated
  `lastDecidedReadings`) is applied and mirrored even though A never became a candidate.
- **Commitment-only-on-win-and-success:** a multi-instrument fixture with two open candidates
  (A lower conviction, B higher) — assert ONLY B's `commitmentDelta` is applied; A's working
  memory shows no `lastCommitTick`/`positionContext` change despite A having produced a
  `nodeOutcome.candidate != nil`. Then: force `decision.Execute` to decline (`ok=false`) for the
  winner — assert NEITHER candidate's commitment applies, and the tick returns `{}, false`.
- **Root fork exclusivity:** a held instrument never calls `Decide`; a flat instrument never
  calls `Manage` — fixture-driven, assert via a spy/counter on each.
- **4a-runs-for-holding too:** a holding instrument's bundle is refreshed (or coasted per 4a's
  own gate) even though it routes to 4d, not 4b — assert `mem.lastBundle` reflects this tick's
  perception regardless of the fork outcome.
- **ResolveOnePunch:** property test — any close candidate beats any open/add candidate
  regardless of relative conviction (construct a low-conviction close vs a high-conviction open,
  assert close wins); within one class, higher conviction wins; exact-tie conviction resolves
  via the seeded RNG deterministically (same seed → same winner, across repeated runs).
- **positionFor:** `Qty != 0` → holding=true with the matching `core.Position`; `Qty == 0` or
  instrument absent from `obs.Positions` → holding=false, zero-value `core.Position`.
- **Three-leaves coverage:** `observe()` error → `{}, false` immediately, no traversal side
  effects at all; zero candidates after a full traversal (all coast/skip/hold/wait) → `{}, 
  false`; a single candidate that 4c declines → `{}, false`, no commitment applied.
- **Side-effect-free 4b/4d regression:** a direct unit test on `decision.Decide` / `decision.
  Manage` in isolation asserting NEITHER function mutates its input `instrumentMemory`/working-
  state arguments — only returns a `nodeOutcome`. This is the regression guard for the
  cross-package refactor this stage forces.
- `go test ./session/... ./decision/... -race` green; `go vet` + `golangci-lint` clean.
