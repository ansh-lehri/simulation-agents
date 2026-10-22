# decision — 4b Decision (Tier 4, second block of `decide()`)
<!-- Per-package CLAUDE.md, extends the `decision` package. READ ORDER:
       1) CLAUDE.md                            — root constitution
       2) session/CLAUDE.md                    — the goroutine that calls into this package (Tier 3)
       3) decision/perception/CLAUDE.md (4a section)       — the predecessor stage this one consumes
       4) THIS file                             — specific to 4b
       5) the spec docs below                   — the authority
     Does not repeat the above. -->

## Spec (the authority)
- `docs/agents/LAYER_2_TIER4_4B_DECISION_IMPL.md` (LOCKED) — the HOW-in-code, §1–13. This is
  the primary spec; implement exactly what it says, in its own order.
- `docs/agents/LAYER_2_TIER4_4B_DECISION.md` — the WHAT/WHY design (LOCKED except 4b-2, which
  the IMPL doc's §7 resolves and locks).
- `docs/agents/LAYER_2_TIER4_4A_PERCEPTION_IMPL.md` — the predecessor: the seam, `instrumentMemory`,
  the `Accessor`, the `PerceptionBundle` this stage consumes. Treat as already shipped exactly as
  that doc describes.
- `docs/agents/LAYER_2_TIER4_4B2_TRAITS_GENERATION.md` — `ThetaAct`/`NeedsConfirm`/
  `ConfirmsNeeded` generation. Treat as already shipped on `agent.TraitVector` exactly as that
  doc describes; this stage only READS those fields, never draws them.
- `docs/agents/LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md` — where `ChannelWeights` /
  `DecideSensitivity` / `Disposition` come from (already generated, read-only context).
- `docs/agents/LAYER_2_TIER4_FRAMING.md` — §2 the gated decision tree, §7–§8 noise-as-liveness /
  anti-deadlock. Governs every branch below.
- `docs/agents/LAYER_2_TIER4_IMPL_ALTERNATIVES.md` — "4b — Decision" section: rejected
  alternatives (mixer A/B vs chosen C, noise-as-perturbation, hard gate vs shave-only). Read
  before proposing a different shape for anything already settled there.

## What 4b IS
The second block inside `session.decide()`, for a FLAT instrument only (a holding instrument
runs 4d instead — root-dispatch fork, built in the separate tree-assembly phase, not this
stage). Turns 4a's `PerceptionBundle` + the agent's `Disposition` into an intent: at most one
`{dir, conviction}` to COMMIT, or WAIT (carry stance), or SKIP. **Intent only** — sizing/order-
type/execution is 4c; this stage never produces an order. This is the first stage allowed to be
disposition-coupled (4a is disposition-agnostic by construction; 4b is where observations become
opinions, per the framing doc §3.2).

## Owns / public surface
New `decision` package files (impl doc §1): `interpret.go`, `mix.go`, `gate_decide.go`,
`commit.go`.

- **`interpret(bundle, traits, roles) traitVerdicts{trend, meanRev *float64}`** (`interpret.go`,
  §4) — pure. Each verdict: signed `[-1,+1]`, sign=direction, magnitude=conviction, or `nil` =
  absent (lean channel nil/`ReadOK=false`). `trend`: lean=`dirSign(Chart.Direction)·Chart.Strength`,
  multipliers=cleanliness+volume. `mean_rev`: lean=`−Range.PositionInRange·Range.Stretch`,
  multiplier=volume. Noise produces no verdict here (mixer's coin, not interpretation's).
- **`TraitChannelRoles`** (fixed value, not generated) — `trend:{Lean:"chart",Mults:["volume"]}`,
  `mean_rev:{Lean:"range",Mults:["volume"]}`, noise absent. Replaces the
  `cmd/agentsd/calibration.go` `TraitChannelSets` placeholder (§3) — finalize that wiring here.
- **`mix(verdicts, weights, agentRNG) (opinion{dir,conviction}, bypass bool)`** (`mix.go`, §5,
  Option C) — noise coin FIRST (`u < w_noise` → random impulse, `bypass=true`, skips gate+4b-2
  entirely); else dominant+secondary combine (`argmax` weight → dominant; secondaries dampen/
  boost conviction via `1 + align·w_t·|V_t|·k`, **never flip direction**); 0 verdicts → SKIP; 1
  verdict → that one, no multiplier.
- **`gateDecide(pendingWatch, waitStreak, cur, lastDecidedReadings, sensitivities) {COAST,
  RE-DECIDE}`** (`gate_decide.go`, §6) — keys off perception-OUTPUT change (not market change —
  that's 4a's gate; the two desync on purpose). Waiter branch: `waitStreak > 
  WAIT_REASSESS_THRESHOLD` → force RE-DECIDE, else COAST + `waitStreak++` (the quiet-confirm
  fix — a waiter must NOT re-decide every tick). Flat-idle branch: any consumed directional field
  exceeding `DecideSensitivity[field]` → RE-DECIDE, else COAST.
- **`commit(opinion, traits, perceptionCtx, memory) {COMMIT{dir,effConv}, WAIT, SKIP}`**
  (`commit.go`, §7, "4b-2", LOCKED — was OPEN in the design doc, resolved here) — Step 0 shave
  (`time_mult` U-shape, `spread_mult` gated on `Diet.OrderBook`, `cooldown_mult` gated on
  `lastCommitTick`, each a FLOORED `[0,1]` multiplier, never driving `eff_conv` to exactly 0 by
  itself); Step 1 floor (`eff_conv < θ_act` → SKIP); Step 2 immediate-vs-waiter split
  (`!needs_confirm` → COMMIT; waiter → `pendingWatch` state machine against `confirms_needed`,
  direction flip restarts the count).
- **`ExecIntent{action, dir, conviction}`**, **`PendingWatch{Dir, ConfirmsSeen, SinceTick}`** —
  new types this stage introduces (§8, §12).

## Where 4b sits in the seam (read-only context, not this package's code to write)
- The control-flow chain stitching these four together per flat instrument (§9: noise coin →
  `gateDecide` → `mix` → `commit`, with the COAST/RE-DECIDE and bypass short-circuits) lives in
  the tree-assembly phase that calls into `decision`, NOT inside any one of these four files as
  a fifth orchestrating function — unless the impl doc's own file list implies otherwise, follow
  §1's file list exactly; do not invent a fifth file to hold the chain.
- `workingAgent.instrumentMemory` (from 4a) gains 4b's working-memory fields (§8):
  `standingOpinion float64`, `standingStance string` (`"FLAT_IDLE"|"WAIT"`),
  `lastDecidedReadings map[string]float64`, `pendingWatch *decision.PendingWatch`,
  `waitStreak int`, `lastCommitTick int64` (sentinel = never committed). Own by reference — these
  are read/written by the seam calling `decision`, not fields this package defines on
  `workingAgent` itself (that struct lives in `session`).
- `store.PerceptionRefs`-sibling reshape (§8, mirrors 4a's per-instrument reshape, already
  shipped pattern): `SetStandingOpinion`/`SetWaitStreak` gain an instrument arg; NEW setters
  `SetPendingWatch(inst, *PendingWatch)`, `SetLastCommitTick(inst, int64)`,
  `SetLastDecidedReadings(inst, map)`; new `store.PendingWatch{Dir, ConfirmsSeen, SinceTick}`.
  Write-on-change only, on a RE-DECIDE outcome — never on COAST.
- `cmd/agentsd/calibration.go` / composition root gains `decision.Params` (§11): mixer `k` +
  `NoiseConviction`; interpretation multiplier coefficients; `WAIT_REASSESS_THRESHOLD`; the
  four 4b-2 draw-down groups (`open_damp,close_damp,a,b,time_floor` / `k_spread,spread_ref,
  spread_floor` / `cool_damp,cool_tau,cool_floor`); θ_add's `margin` (consumed by 4d, defined
  here since this is where `decision.Params` is first introduced — but 4d's usage is out of
  scope for this stage).

## Invariants (LOCKED)
- **Pure package, same as 4a.** No I/O, no Redis, no Core calls, no `context.Context` anywhere
  in these four files. `agentRNG` is passed in, never package-local or re-seeded.
- **Absent ≠ neutral, carried forward from 4a.** `interpret` must return `nil` (not a
  zero-magnitude verdict) when the lean channel is nil/unreadable — the mixer's "≥2 verdicts
  present" branching depends on distinguishing absent from present-but-zero.
- **Directional vs condition channels never conflate.** chart/range produce signed leans;
  liquidity/volume/time produce unsigned multipliers (conviction scalers) or (for liquidity/
  time) global shaves applied once in 4b-2 — never per-trait, never signed.
- **Secondaries dampen/boost, never flip direction.** In both the mixer (secondary traits) and
  4b-2 (condition shaves), a multiplier can shrink conviction toward (but not below its floor at)
  zero; it can never change `dir`'s sign.
- **Noise is a pre-mixer bypass coin, not a blended verdict.** `w_noise` sets IMPULSE FREQUENCY,
  not a jittered signal — rejected alternative (impl-alternatives #1–2): do not blend a third
  "noise verdict" or layer jitter on the signal opinion. A coin hit bypasses gate AND mixer AND
  4b-2 entirely and commits directly.
- **4b-gate keys off perception-output change, not market change.** This is 4a's gate's sibling,
  deliberately desynchronized from it — do not reuse or reference 4a's `RereadSensitivity`
  thresholds here; use `DecideSensitivity` only.
- **Quiet-confirm fix is mandatory.** A waiter in WAIT never force-re-decides except past
  `WAIT_REASSESS_THRESHOLD` — getting this backwards (re-deciding every WAIT tick) reads a
  confirming consolidation as "nothing changed" and silently kills every confirmation path.
- **`waitStreak` and `confirmsSeen` are distinct counters, never merged.** `waitStreak` = anti-
  freeze (gate-level, resets to 0 on any RE-DECIDE). `confirmsSeen` = commit-progress (commit.go-
  level, lives inside `pendingWatch`, resets on direction flip). Do not let one stand in for the
  other.
- **4b-2 conditions shave, never hard-gate (iter 1).** No condition combination may force SKIP by
  itself below the `θ_act` floor check — shave-then-floor-check is the only gate (impl-
  alternatives #4: a hard gate for blown-out spread was explicitly rejected as redundant).
- **Shave multipliers are floored, never zero.** `time_mult`/`spread_mult`/`cooldown_mult` each
  clamp to their own floor constant — a genuinely strong view must survive a rough environment,
  never get force-zeroed by one bad condition alone.
- **`commit` reads ONLY `{dir, conviction}` from the mixer — never mixer internals.** Conviction
  already bakes in trait agreement (Option C); do not re-derive agreement inside `commit.go`
  (impl-alternatives #7).
- **COMMIT carries `eff_conv` (shaved), never raw conviction** — downstream 4c sizing depends on
  a thin-book/cooldown commit sizing smaller.
- **`lastCommitTick` updates on COMMIT only** (feeds the next cooldown calc); `pendingWatch`
  clears on COMMIT and on SKIP, is created/updated on WAIT, and restarts (not increments) when a
  waiter's direction flips.
- **Every RE-DECIDE outcome, regardless of branch, sets `lastDecidedReadings = cur` and
  `waitStreak = 0`** — this happens once, after the switch on `commit`'s result, not duplicated
  inside each case.
- **`θ_act`/`needs_confirm`/`confirms_needed` are read-only inputs here** — generated upstream
  (4b-2 traits generation), never computed, defaulted, or re-derived in this stage. If one is
  missing on a `TraitVector` for an endogenous agent, that's an upstream bug to flag, not a value
  to synthesize.
- **`ChannelWeights`/`imbalance`/`depth` stay exactly as inert/reserved as the design doc says**
  — `ChannelWeights` renormalizes to 1 at one lean (kept for a future multi-lean case, not
  removed); `imbalance` is perceived (4a) but consumed by no trait here (reserved seam, impl-
  alternatives #10); `depth` stays unused (rolling baseline dropped, 4a). Do not "complete" any
  of these as part of this stage.

## Do NOT
- Do NOT implement `session.decide`'s orchestration, the root flat/holding dispatch, or the
  candidate→one-punch resolution across instruments — those are the tree-assembly phase, a
  separate stage, not 4b.
- Do NOT implement 4c (execution/sizing/order-type) or 4d (position/exit management) — 4b's
  output is `ExecIntent` only; nothing downstream.
- Do NOT touch 4a's files (`accessor.go`, `bundle.go`, `gate.go`, `perceive.go`, `signals_*.go`,
  `bias_noise.go`, `talib_adapt.go`) or the 4b-2 trait generator (`draw_commit_gate.go`) — both
  are LOCKED, already shipped, read-only context for this stage.
- Do NOT build the deferred stochastic-dominant-near-ties mixer enrichment (impl-alternatives
  #3) — ship Option C as specified, nothing more.
- Do NOT add a hard can't-act gate on spread/liquidity in 4b-2 — explicitly rejected (impl-
  alternatives #4).
- Do NOT let `commit.go` read mixer internals beyond `{dir, conviction}` (impl-alternatives #7).
- Do NOT wire an order-flow/scalper trait to consume `imbalance` — reserved for later (§3, §13
  deferred list).
- Do NOT invent new calibration constants beyond those §11 names — if a shaping constant looks
  missing, it's either an existing generated trait field or belongs in `decision.Params`; stop
  and ask rather than hardcoding a magic number.

## Tests (ship with the code)
- **`interpret`:** per trait, table test against hand-computed `trend`/`mean_rev` verdicts from
  fixed `PerceptionBundle` fixtures — nil-lean → nil verdict; cleanliness/volume multiplier
  shaping; sign derivation (`dirSign`, the mean-rev sign flip).
- **`mix`:** noise-coin hit → bypass=true + random-impulse opinion, gate/mixer/4b-2 skipped
  entirely (assert the caller never calls `gateDecide`/`commit` on this path, if that's testable
  at this layer, or at least that `bypass=true` is returned); 0 verdicts → SKIP; 1 verdict → that
  one, conviction=magnitude, no multiplier; 2 verdicts → dominant+secondary combine, secondary
  never flips sign (property test: run many `(V_trend, V_meanRev, weights)` combinations, assert
  `sign(result.dir) == sign(V_dominant)` always); tie-break uses `agentRNG` deterministically
  under a fixed seed.
- **`gateDecide`:** flat-idle — each consumed field's staleness independently trips RE-DECIDE;
  all under threshold → COAST. Waiter — `waitStreak <= THRESHOLD` → COAST + increment;
  `waitStreak > THRESHOLD` → forced RE-DECIDE. Assert `waitStreak` resets to 0 only on RE-DECIDE,
  nowhere else.
- **`commit`:** each shave multiplier's floor respected (property test: multiplier never below
  its floor regardless of input magnitude); `eff_conv < θ_act` → SKIP with `pendingWatch`
  cleared; `!needs_confirm` → COMMIT with `eff_conv` (not raw conviction); waiter state machine —
  first WAIT creates `pendingWatch` with `confirmsSeen=1`; same-direction WAIT increments;
  `confirmsSeen >= confirms_needed` → COMMIT + clear; direction flip → restart at
  `confirmsSeen=1`, new `sinceTick`. `lastCommitTick` updates only on COMMIT.
- **Determinism:** fixed-seed `agentRNG` → the full 4b chain (noise coin → gate → mix → commit)
  run twice on identical inputs → byte-identical `ExecIntent`/`WAIT`/`SKIP` outcome and identical
  working-memory mutations (root CLAUDE.md standing rule).
- **Anti-deadlock property (framing §7.2):** across a population with varied `w_noise`, assert
  noise-bypass agents keep producing candidates even when `{trend,mean_rev}` verdicts are both
  absent — a thin-diet agent must never be structurally unable to act.
- **Store reshape:** `SetPendingWatch`/`SetLastCommitTick`/`SetLastDecidedReadings` write-on-
  change only; never called on a COAST outcome (assert via a spy/fake store in the calling-seam
  test, or at minimum unit-test the setters' key shape per instrument).
- `go test ./decision/... -race` green; `go vet` + `golangci-lint` clean.
