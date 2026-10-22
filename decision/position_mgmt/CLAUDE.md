# decision — 4d Position & Exit Management (Tier 4, the holding branch)
<!-- Per-package CLAUDE.md, extends the `decision` package. READ ORDER:
       1) CLAUDE.md                             — root constitution
       2) session/CLAUDE.md                     — the goroutine that calls into this package (Tier 3)
       3) core/CLAUDE.md                        — the observation contract this stage reads
       4) decision/perception/CLAUDE.md, decision/commit_traits/CLAUDE.md, decision/decision_compute/CLAUDE.md, decision/executor/CLAUDE.md (4a + 4b + 4c sections) — the seam/mixer/executor this stage reuses
       5) THIS file                              — specific to 4d
       6) the spec docs below                    — the authority
     Does not repeat the above. -->

## Spec (the authority)
- `docs/agents/LAYER_2_TIER4_4D_POSITION_EXIT_IMPL.md` (LOCKED) — the HOW-in-code, §1–11. This
  is the primary spec; implement exactly what it says, in its own order.
- `docs/agents/LAYER_2_TIER4_4D_POSITION_EXIT.md` — the WHAT/WHY (LOCKED except θ_add, which was
  blocked on θ_act and is now RESOLVED: `θ_add = θ_act + margin`, impl doc §6).
- `docs/agents/LAYER_2_TIER4_4A_PERCEPTION_IMPL.md` — the accessor, `instrumentMemory`,
  `tickContext`, the `PerceptionBundle` this stage re-reads for reversal/levels. Read-only,
  already shipped.
- `docs/agents/LAYER_2_TIER4_4B_DECISION_IMPL.md` — the mixer (`interpret`/`mix`,
  `TraitChannelRoles`) this stage REUSES verbatim for the add-check, `ExecIntent`, `ThetaAct`,
  the per-instrument store-reshape pattern. Read-only, already shipped.
- `docs/agents/LAYER_2_TIER4_4C_EXECUTION_IMPL.md` — the close/add executor this stage hands
  its `ExecIntent` to. Read-only, already shipped.
- `docs/agents/LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md` — where `exit_profile` /
  `max_hold` are drawn. Read-only, already shipped (`orchestrator/generator/draw_exit_and_hold.go`).
- `docs/agents/L2_TIER0_CORE_API_CONTRACT.md` + `core/observation.go` — the own-state read
  contract this stage consumes (`core.AccountObservation`, `core.Position`).
- `docs/agents/LAYER_2_TIER4_IMPL_ALTERNATIVES.md` — the "4a/4b/4c" rejected-alternatives tables
  (round_to_tick grid, open/add derivation, θ_market placement, mixer Option C, etc.) govern
  surfaces this stage reuses; this stage's OWN alternative (add-check skips the noise-coin
  bypass — impl doc §6) should be appended to this log as part of the change, not left only in
  the impl doc's prose.

## What 4d IS
The manage-what-I-hold branch: `Manage(in ManageInput) (ExecIntent, bool)` in a new
`decision/manage.go`. For an instrument the agent already holds, decide hold / add / exit THIS
tick and hand any resulting intent to 4c. It consumes own-position + computed P&L (which 4b
lacks) and produces actions 4b can't (hold/add/exit). **Intent only — 4d never forms a
`core.OrderPunchRequest`**; that's 4c. Exactly one of 4b/4d runs per tick per instrument (the
root-dispatch fork in the tree-assembly phase, not this stage, decides which).

## Owns / public surface
- **`ManageInput{pos core.Position, bundle *decision.PerceptionBundle, posCtx
  *store.PositionContext, traits agent.TraitVector, price int64, tick tickContext, roles
  decision.TraitChannelRoles, rng *rand.Rand, cfg decision.Params}`** — the input struct
  (`decision/manage.go`).
- **`Manage(in ManageInput) (decision.ExecIntent, bool)`** — the single pure entry point.
  `bool=false` ⇒ HOLD, emit nothing.
- **`levelsFor(entryChannel string, bundle *PerceptionBundle) (support, resistance int64, ok
  bool)`** (impl doc §5.2) — small helper, chart→`KeyLevels` nearest-below/above,
  range→`RangeBounds`, noise→`ok=false`.
- **`channelOf(dominantTrait string) string`** (impl doc §9) — `trend→"chart"`,
  `mean_rev→"range"`, noise-bypass→`"noise"`. Used both by 4d's reversal check AND by the
  stamping this stage forces onto 4b's COMMIT path (see below).
- **`decision.Params` gains `Margin`** (θ_add offset; composition root,
  `cmd/agentsd/calibration.go`). Every other dial 4d uses (`k`, `NoiseConviction`, interpret
  coefficients) is already present from 4b.

## Where 4d sits in the seam (read-only context, not this package's code to write)
- **One shared observation read.** `decide()`'s root-dispatch calls `input.observe()`
  (`func() (core.AccountObservation, error)`, bound to `core.Client.GetObservation`) exactly
  ONCE per tick, then routes each instrument: flat→4b, holding→4d. 4d receives its
  `core.Position` from that single read — **it does not call `observe()` itself.** The same
  read serves root-dispatch, 4b's capital sizing, and 4d.
- **4d REUSES 4b's mixer verbatim for the add-check** — `interpret(bundle, traits, roles)` →
  `mix(verdicts, traits, rng, cfg)` → `{mix_dir, mix_conviction}`. Do not write a second
  interpretation/mixing implementation; call the exact same functions 4b calls, with THIS
  tick's bundle.
- **4d hands its result to 4c, never builds a punch itself.** `ExecIntent{Action: close, Dir:
  opposite(entry_dir)}` or `ExecIntent{Action: add, Dir: entry_dir, Conviction: mix_conviction}`
  flows to the tree-assembly phase's one-punch resolution, then `decision.Execute` (4c).
- **`positionContext` is stamped by 4b (on COMMIT/open) and re-stamped by 4d (on ADD)** — this
  stage FORCES an addition onto 4b's commit path: 4b's COMMIT must also write
  `positionContext = {EntryDir: dir, EntryChannel: channelOf(dominantTrait), EntryTick:
  current_tick}`. If 4b's `commit.go` doesn't already do this, adding it is IN SCOPE for this
  stage (impl doc §9: "the one addition 4d forces onto 4b's COMMIT"). Do not implement this as
  a silent side-effect inside `manage.go`; it belongs at 4b's commit site.
- **`store.PositionContext{EntryDir, EntryChannel, EntryTick}` already exists**
  (`store/runtime_store.go`) — no new type. The reshape (§9) is: `SetPositionContext` gains an
  instrument arg, writes one blob per instrument under the agent's runtime hash (field
  `position_ctx:{instrument}`), replacing the flat `FieldPositionContext`;
  `RuntimeHandle.SetPositionContext` gains the same arg. Write-on-change only (open/add), never
  cleared at close-emit (impl doc §9: a stale stamp for a since-closed instrument is harmless —
  the flat path is 4b, which overwrites it on its own next COMMIT).
- **`workingAgent.instrumentMemory` gains `positionContext *store.PositionContext`** alongside
  the existing 4a (`refs`, `lastBundle`) and 4b (`standingOpinion`, `pendingWatch`, `waitStreak`,
  `lastCommitTick`, …) fields — own by reference, defined/mutated by the calling seam in
  `session`, not by this package.

## Invariants (LOCKED)
- **Pure function, same discipline as 4a/4b/4c.** No I/O, no Core calls, no
  `context.Context`, no Redis, inside `decision/manage.go`. `Manage` reads only its
  `ManageInput` and returns a value.
- **No own read.** 4d must NEVER call `observe()`/`GetObservation` itself — it receives
  `pos core.Position` already resolved by root-dispatch from the single per-tick read. Treat a
  missing/zero position as a defensive guard (`pos.Qty==0` → not 4d's tick), not a trigger to
  re-fetch.
- **`unrealized` is signed-qty arithmetic, no branch:** `(current_price −
  WeightedAvgEntry) × pos.Qty` — the sign of `pos.Qty` (+long/−short) makes the P&L sign
  automatic. Do not write a long/short conditional for this.
- **4d marks itself.** `pos.MarkAvailable`/`LTP`/`GrossUnrealized` are IGNORED — the broker
  ships them as zero (mark cache misses until a later phase) — 4d computes `unrealized` itself
  against its own perceived `current_price`, the one consistent choice.
- **Exit priority is fixed and first-trigger-wins: stop → signal-reversal → target → time-stop.**
  The first trigger that fires builds the close intent; the rest are NOT evaluated after that
  (do not compute all four and pick one by some other rule).
- **Exit-mode price derivation (§5.1):** `distance` uses `WeightedAvgEntry × (1∓pct)` on both
  sides (target>stop guaranteed by construction at generation — `TargetPct = StopPct ×
  rewardRisk`, already enforced upstream, 4d doesn't re-derive that guarantee). `level` requires
  `levelsFor(...)` to succeed; on failure → **ABRUPT MARKET EXIT this tick** (the Y-rule — a
  deliberate chart-blind cost, not a bug to "fix" by falling back silently). `hybrid` tries level
  first, falls back to distance only if `levelsFor` fails — never the reverse order.
- **`levelsFor` is channel-scoped (§5.2):** `chart` needs `Chart.ReadOK` AND at least one
  `KeyLevels` entry below price (support) and one above (resistance); `range` needs
  `Range.ReadOK` and uses `RangeBounds` directly as `[support, resistance]`; `noise` always
  `ok=false` (noise entry read no structure — there is nothing to look up). Any required side
  missing, channel nil, or `ReadOK=false` → `ok=false`.
- **Reversal reuses THIS tick's bundle — no re-perceive.** `entry_channel=="chart"` needs
  `Chart.Direction` ACTIVELY opposite `entry_dir` (`DirFlat` is NOT a reversal);
  `entry_channel=="range"` fires when price is OUTSIDE `RangeBounds` (the range broke — this has
  no direction field, so it can fire even in profit, and is non-redundant with stop/target by
  design); `entry_channel=="noise"` never reverses (noise entry read no signal);
  `ReadOK==false` or the channel is nil → no reversal that tick. This is exactly why 4a's gate
  perceives ALL instruments (not just flat ones) — reversal depends on it.
- **Time-stop compares against `*traits.MaxHold`** — dereference the pointer; it's a frozen
  per-agent trait, never recomputed here.
- **`position_context == nil` is a harmless degrade, not a crash** (recovery edge — e.g. after
  an L2 restart, a position exists but no stamp yet exists). Treat `entry_channel` as unknown →
  skip reversal that tick; `entry_tick` defaults to `current_tick` → time-stop disarmed until
  re-stamped. Never panic on a nil `positionContext` while holding.
- **Add-check gate is a strict AND of three conditions (§6):** `mix_dir == entry_dir` (same
  direction only — 4d never reverses via add), `unrealized > 0` (add to winners only), AND
  `mix_conviction ≥ θ_add` where `θ_add = *traits.ThetaAct + cfg.Margin` (runtime-derived, NEVER
  a stored/generated trait — `agent/traits.go`'s reserved `ThetaAdd` field stays dropped, the
  generator's "do NOT populate theta_add" rule still holds exactly as before). Any one condition
  failing → HOLD, return `_, false`.
- **The add-check SKIPS the noise-coin bypass** — 4d calls the mixer's SIGNAL verdict path only,
  never the pre-mixer noise coin. An add is a signal-driven scaling decision; the noise coin is
  an entry-liveness impulse (4b/framing §7), not a reason to pyramid an already-live position.
  This is this stage's own resolved alternative — log it in
  `LAYER_2_TIER4_IMPL_ALTERNATIVES.md` under a new "4d — Position & Exit" section as part of
  this change.
- **`Margin ≥ 0`** is the calibration constraint that guarantees `θ_add ≥ θ_act` always — the
  add bar can never be laxer than the entry bar. Validate this in `decision.Params`
  construction/validation if one exists, or at minimum document it as a constraint on the
  placeholder value.
- **Re-stamping on ADD restarts the clock and re-bases the levels.** On ADD, 4d re-stamps
  `positionContext` with `EntryChannel` re-resolved from the ADD mixer's dominant trait and
  `EntryTick = current_tick` — the time-stop clock restarts from the last add, and
  stop/target recompute off the broker's new (post-add) `WeightedAvgEntry` automatically on the
  NEXT tick's read (not something 4d computes itself at add-time).
- **Reactivity hooks are dead seams, not code.** 4e (P&L-reactivity) was dropped, so the designed
  hooks (`stop_distance × hook`, `θ_add × hook`, `max_hold × hook`) are permanently 1 — do NOT
  write a `× 1` multiply anywhere to "keep the seam visible." Absence of the multiply IS the
  correct implementation; a comment noting the dropped hook is enough if anything.
- **All arithmetic is int64 ticks/coins; `unrealized` is coins.** Percentage math
  (`v − v×pct`) rounds once at the boundary (`math.Round`), consistent with 4a's level
  rounding — never carry floats through multiple steps before rounding.

## Do NOT
- Do NOT implement the root-dispatch fork (flat→4b vs holding→4d) or the tree-assembly phase's
  one-punch resolution — those live in a separate stage; 4d is the function they call for a
  holding instrument.
- Do NOT implement 4c (`decision.Execute`) — 4d only constructs an `ExecIntent` and hands it on.
- Do NOT implement or duplicate 4b's `interpret`/`mix` — call them directly; two implementations
  of the mixer WILL drift.
- Do NOT call `observe()`/`GetObservation` from inside `decision` — the one read happens in
  `session`'s root-dispatch and is passed down as `pos core.Position`.
- Do NOT add θ_add as a new field on `agent.TraitVector` — it is runtime-derived
  (`*traits.ThetaAct + cfg.Margin`), never stored, never generated.
- Do NOT let the add-check roll the noise-coin bypass — signal-only path, explicitly excluded.
- Do NOT clear `positionContext` when emitting a close intent — the order may not have filled
  yet; clearing would lose entry info for the still-open position next tick.
- Do NOT build partial trim/scale-out on exit (binary full exit only, per 4c's existing
  constraint) or per-instrument tick size for level math (step-1 rounding, per 4c) — both
  explicitly deferred (impl doc §11).
- Do NOT build the recovery-rebuild that would repopulate `positionContext` after an L2 restart
  — Tier 6; §4's nil-degrade is the only handling this stage does.
- Do NOT write a literal `× 1` for the dropped reactivity hooks — see Invariants.
- Do NOT invent new calibration constants beyond `Margin` — everything else 4d uses already
  exists in `decision.Params` from 4b.

## Tests (ship with the code)
- **`unrealized`/`closeable_qty`:** table test over signed long/short positions — sign falls out
  automatically, no branch in the test subject's code path (assert via both long and short
  fixtures, not just one).
- **Exit priority (first-trigger-wins):** a fixture where stop AND target would BOTH fire this
  tick → assert stop wins (priority order respected, target never evaluated/returned).
  Construct one fixture per adjacent pair in the priority chain (stop-vs-reversal, reversal-vs-
  target, target-vs-time) to pin the exact order.
- **Exit-mode price derivation:** `distance` table test (target>stop by construction, both
  sides for long/short); `level` success + `levelsFor` failure → ABRUPT MARKET EXIT (assert the
  resulting intent is `close` even with price nowhere near a "normal" stop/target); `hybrid`
  falls back to distance ONLY when `levelsFor` fails, uses level otherwise.
- **`levelsFor`:** chart with both sides present → `ok=true`; chart missing one side → `ok=
  false`; range `ReadOK=false` → `ok=false`; noise → always `ok=false`; nil channel → `ok=
  false`.
- **Reversal:** chart `DirFlat` while holding long → NOT a reversal; chart actively opposite →
  reversal; range price outside bounds while in profit → reversal fires anyway (the non-
  redundancy property); noise entry → never reverses; `ReadOK=false` → never reverses this tick.
- **Time-stop:** `current_tick − entry_tick > *MaxHold` → fires; `==` does not fire (boundary
  test, confirm strict `>` per §5.3).
- **`positionContext == nil`:** holding with nil posCtx → no panic, reversal skipped, time-stop
  computed from `entry_tick = current_tick` (effectively disarmed this tick).
- **Add-check:** all three conditions true → ADD with `mix_conviction`; any one false
  (opposite mix_dir, losing position, or conviction below θ_add) → HOLD; assert the noise-coin
  path is never consulted (e.g. via a mixer stub that would panic/flag if the bypass branch is
  invoked, or by asserting output is identical regardless of a noise-coin RNG draw that would
  otherwise have hit).
- **`θ_add` derivation:** property test — for any `ThetaAct ∈[0,1]` and `Margin ≥ 0`, `θ_add =
  ThetaAct + Margin ≥ ThetaAct` always.
- **Re-stamp on ADD:** after an ADD, the (test-level) `positionContext` reflects the ADD
  mixer's dominant channel and `EntryTick = current_tick`, not the original open's values.
- **Determinism:** fixed-seed `rng` → `Manage` run twice on identical inputs (including the
  reused mixer call) → byte-identical `(ExecIntent, bool)` and identical `positionContext`
  mutation (root CLAUDE.md standing rule).
- **4b commit-path stamping (cross-stage, verify not just assume):** write/extend a test in the
  4b commit-path test suite confirming COMMIT also stamps `positionContext` with
  `EntryChannel = channelOf(dominantTrait)` — this is 4d's forced addition onto 4b, so its test
  belongs wherever 4b's commit tests live, not duplicated here.
- `go test ./decision/... -race` green; `go vet` + `golangci-lint` clean.
