# 4b-2 — Commit-Gate Traits Generation (Tier 4 / 4b-2, Phase 3 generator extension)
<!-- Stage CLAUDE.md, not a new package. READ ORDER:
       1) /CLAUDE.md                             — root constitution
       2) /orchestrator/generator/CLAUDE.md       — the package this stage extends
       3) /agent/CLAUDE.md                        — the package whose TraitVector this stage extends
       4) THIS file                               — specific to the 4b-2 draw
       5) the spec docs below                     — the authority
     Does not repeat the above. -->

## Spec (the authority)
- `docs/agents/LAYER_2_TIER4_4B2_TRAITS_GENERATION.md` (LOCKED) — the HOW for this stage, §1–7.
  This is the primary spec; implement exactly what it says.
- `docs/agents/LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md` §3 — the draw pattern this stage
  reuses verbatim (center = `lo+(hi−lo)·(1−driver)+ε` jitter; weighted coin for a boolean), with
  `aggressiveness` as the driver instead of `ρ`. Read this before writing a single line — do not
  reinvent the placement math.
- `docs/agents/LAYER_2_TIER4_4B_DECISION_IMPL.md` §7, §10 — the consumer: how `ThetaAct` /
  `NeedsConfirm` / `ConfirmsNeeded` are read at runtime in the 4b-2 commit gate. This stage only
  produces the traits; it does not implement the consumer.
- No new design in this stage — behavior/direction is LOCKED; this is purely "apply the already-
  locked perception-profile draw mechanism with a different driver."

## What this stage IS
Three new endogenous-only frozen traits — `theta_act`, `needs_confirm`, `confirms_needed` —
appended to the tail of the already-implemented endogenous draw sequence in
`orchestrator/generator`, plus the matching `agent.TraitVector` fields and `GenParams`
calibration ranges. It also DROPS the reserved/commented `ThetaAdd` field — θ_add is not a
trait at all, it is derived at runtime in 4d (`θ_act + margin`, a `decision.Params` global that
does not exist yet and is not this stage's concern — do not add `decision.Params` here).

## Owns / public surface (files touched)
- **`agent/traits.go`** — add to `TraitVector` (endogenous-only, `omitempty` pointers, same
  pattern as `Rho`/`Phi`/`MaxHold`):
  ```go
  ThetaAct       *float64 `json:"theta_act,omitempty"`
  NeedsConfirm   *bool    `json:"needs_confirm,omitempty"`
  ConfirmsNeeded *int     `json:"confirms_needed,omitempty"`
  ```
  Remove the existing reserved/commented `ThetaAdd` field and its explanatory comment entirely
  — it is superseded, not merely unblocked (§3 of the spec: dropped, not populated).
- **`orchestrator/generator/draw_commit_gate.go`** (new file) — the three draws (spec §2):
  `drawThetaAct(rng, aggressiveness, params)`, the `needs_confirm` coin, and
  `drawConfirmsNeeded(rng, aggressiveness, params)`. Reuse the existing placement helper used by
  perception numeric fields (same `(1−driver)` + `ε~N(0, σ_idio·width)` + clamp shape) rather than
  writing a parallel implementation — check `draw_perception.go` / the shared numeric-range
  helper first.
- **`orchestrator/generator/draw_traits.go`** — call the three new draws at the TAIL of the
  endogenous branch, strictly AFTER `max_hold` (spec §1: appending at the end is what keeps every
  already-implemented trait replaying identically under a fixed seed — inserting earlier breaks
  every existing golden/determinism test).
- **`orchestrator/generator/params.go`** — add to `GenParams`:
  ```go
  ThetaActRange       NumericRange // e.g. {Low: 0.3, High: 0.8}, within [0,1]
  ConfirmsNeededRange IntRange     // e.g. {Low: 1, High: 2}, Low >= 1
  ```
  Extend `Validate()`: `ThetaActRange` ordered and within `[0,1]`; `ConfirmsNeededRange.Low >= 1`
  and `Low <= High`. `needs_confirm` needs no range — it is driven directly by `aggressiveness`.
  `SigmaIdio` is reused as-is, no new field.
- **`cmd/agentsd/calibration.go`** — add concrete placeholder values for `ThetaActRange` /
  `ConfirmsNeededRange` to `defaultGenParams()`, same style as the existing perception ranges.

## Invariants (LOCKED)
- **Endogenous-only.** Herd agents never get these three fields (they follow tips, never decide)
  — same `omitempty` pointer pattern as `Disposition`/`Diet`/`Perception`. Do not draw them on
  the herd branch.
- **Draw order: strictly last.** Appended after `max_hold`, the final step of the endogenous
  branch. This is a reproducibility invariant, not a style preference — any existing trait
  shifting position in the RNG stream breaks every already-shipped determinism/golden test under
  a fixed seed.
- **Driver is `aggressiveness`**, already drawn earlier in the same endogenous block — do not
  redraw it, do not substitute `reactivity`/`ρ` (that drives perception, a different axis).
- **Directions are fixed, not calibration:** aggressive → lower `θ_act` (lower commit bar), more
  likely `needs_confirm=false` (immediate), fewer `confirms_needed`. Cautious → the reverse. Only
  the numeric ranges are calibration; the `(1−aggressiveness)` placement direction is LOCKED.
- **`confirms_needed` is drawn for EVERY endogenous agent**, even immediate actors — it is simply
  unread at runtime when `needs_confirm=false`. Do not skip the draw conditionally; that would
  make the RNG stream length depend on a prior draw's outcome, which breaks reproducibility the
  same way a reordered draw would.
- **Fresh `ε` per trait.** `θ_act` and `confirms_needed` each get their own independent jitter
  draw from the RNG (not a shared jitter value) — same-aggressiveness agents must still differ,
  exactly as in perception.
- **`θ_add` is NOT a trait.** Do not add it back to `TraitVector`, do not add a `decision.Params`
  margin constant in this stage — both are 4d's concern, out of scope here. This stage's only
  obligation toward θ_add is removing the stale reserved field so 4d has a clean slate.
- **Distribution families/shapes are LOCKED by the spec; only the range bounds
  (`ThetaActRange`, `ConfirmsNeededRange`) are calibration placeholders** — never hardcode a
  number inline in `draw_commit_gate.go`; always read it from `GenParams`.
- **Seeded RNG throughout** — the same `rng *rand.Rand` threaded through the rest of
  `drawTraitVector`; live = fresh, tuning = fixed seed; never derive a seed from an identifier
  (root CLAUDE.md standing rule).
- **Stale doc cross-references.** `agent/CLAUDE.md`'s Invariants line ("`theta_add` stays
  reserved/commented — not populated (blocked on `theta_act`)") and
  `orchestrator/generator/CLAUDE.md`'s Do-NOT line ("Do NOT populate `theta_add` — RESERVED...")
  are now both WRONG — `theta_act` existing doesn't unblock `theta_add`, it makes `theta_add`
  unnecessary as a trait at all. Update both lines as part of this change so they state the
  current, correct rule (three new traits exist; `theta_add` is gone, derived in 4d).

## Do NOT
- Do NOT implement the 4b-2 commit-gate CONSUMER (`decision/commit.go`, the
  COMMIT/WAIT/SKIP state machine, `eff_conv` shaving, `PendingWatch`) — that is 4b's own
  implementation stage (`LAYER_2_TIER4_4B_DECISION_IMPL.md`), not this one. This stage only
  produces the three traits the consumer will read.
- Do NOT touch `decision.Params`, `margin`, or anything 4d-shaped — out of scope, belongs to the
  θ_add consumer stage.
- Do NOT touch the perception-profile draw files beyond reusing their shared numeric-placement
  helper — `drawPerceptionProfile` and its sub-draws are LOCKED and already shipped.
- Do NOT change `TraitChannelSets`, `ChannelWeights`, or anything channel/disposition-shaped —
  unrelated to this stage.
- Do NOT make `confirms_needed`'s draw conditional on `needs_confirm`'s outcome (see Invariants).
- Do NOT invent new calibration dials beyond `ThetaActRange` / `ConfirmsNeededRange` — the spec
  names exactly these two as new; `SigmaIdio` is reused, not re-specified.

## Tests (ship with the code)
- **Determinism:** fixed seed → `drawTraitVector` run twice → `ThetaAct`/`NeedsConfirm`/
  `ConfirmsNeeded` byte-identical, AND every pre-existing trait field unchanged from before this
  stage's draws were appended (the RNG-stream-position regression test — this is the one most
  likely to silently break if the tail placement is wrong).
- **Direction:** table/property test sweeping `aggressiveness` across `[0,1]` — `ThetaAct`
  trends down, `P(NeedsConfirm=false)` trends up (via many draws at fixed aggressiveness,
  checking the empirical rate tracks `aggressiveness`), `ConfirmsNeeded` trends down, as
  aggressiveness rises.
- **Range respect:** `ThetaAct` always within `[0,1]` and within `ThetaActRange` after clamp;
  `ConfirmsNeeded` always an integer within `ConfirmsNeededRange` (inclusive).
- **Herd exclusion:** herd-kind agents carry `nil` for all three new pointer fields.
- **Always-drawn:** `ConfirmsNeeded` is non-nil even when `NeedsConfirm=false` (ignored-but-
  present, per spec §2).
- **Validate():** `ThetaActRange` unordered or outside `[0,1]` → error; `ConfirmsNeededRange.Low
  < 1` or `Low > High` → error.
- **TraitVector JSONB round-trip:** the three new fields (de)serialize correctly, absent on herd
  rows (not JSON `null`), present on endogenous rows.
- `go test ./agent/... ./orchestrator/generator/... ./cmd/agentsd/... -race` green; `go vet` +
  `golangci-lint run` clean.