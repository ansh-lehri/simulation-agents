# scenario — Parameter-Manipulation Engine (Phase 4)
<!-- Per-package CLAUDE.md. READ ORDER:
       1) CLAUDE.md                              — root constitution
       2) orchestrator/CLAUDE.md                 — the caller (generation path)
       3) THIS file                               — specific to scenario
       4) the spec docs below                     — the authority
     This file does not repeat the above. -->

## Spec (the authority — read BOTH in orientation; design first, then impl)
- `docs/agents/L2_SCENARIOS_DESIGN.md` — concept, the link-space math (§3), the knob catalogue
  (§4), the bimodal switch (§5), the nine surfaces (§6), the fitting loop (§7). READ FIRST.
- `docs/agents/L2_SCENARIOS_IMPLEMENTATION_DESIGN.md` — the point-by-point build (Points 0–7).

## What scenario IS (one line)
The machinery that turns a scenario NAME into shifted generation params (`EffParams`) +
shifted lifecycle-curve params (`CurveEff`), so an on-demand market's requested character
**emerges from a differently-shaped crowd** — never from an `if scenario == X` branch.

## Owns / public surface
- **`ScenarioEngine`** built once at startup (loaded registry + precomputed baseline link-space
  + η + the knob catalogue).
- **`Effective(country, name) → (*EffParams, *CurveEff)`** — the one entry point the generation
  path calls. `name == "" → (nil, nil)` = baseline identity.
- **`ScenarioRegistry`** loader (name → `{direction d, intensity s, switches, target}`), a
  startup-loaded **config artifact** (not a DB table).
- **`BaselineLinkSpace`** (`u0`, `η`) + the **`KnobCatalogue`** (name → ruler) — built once from
  baseline `GenParams`.
- The four **link rulers** (logit / log / arctanh / log-ratio) + inverses, the **shove→invert→
  switches** merge engine, and the `EffParams`/`CurveEff` output shapes.

## Invariants (LOCKED)
- **Emergence, not inducement.** No `if scenario == X { force condition }` anywhere. The ONLY
  scenario-aware code that reaches the generator is the φ-bimodal flag (one branch) — every
  other trait just receives shifted numbers.
- **Param-level, not trait-level** — shift the dials, the generator redraws fresh; never mutate
  an already-drawn agent. This is WHY on-demand redraws traits (coherence re-derives by
  construction).
- **Domain-safe, NO clamps** — shift in link space (`u_eff = u0 + s·d·η`), invert back; the
  inverse lands in-range by construction. (Refines FRAMING §5.4's "clamp".)
- **Shift measured in crowd-widths** — `shift = s · d · η`, `η` empirical (sample baseline →
  push through ruler → std-dev), computed once at startup + cached.
- **Direction `d` = market-fact signs** (identity, principled); **intensity `s` = a single
  fit-to-target scalar** per scenario. Prototype: `d` magnitudes equal (Level-0).
- **All scenario calculation happens once, in the 2a generation path** (surface 4). `CurveEff`
  is computed here but consumed later at 3b — so it rides the 2b handover in `sessions:active`.
- **Empty scenario → identity** — `Effective("") = (nil, nil)`; the non-scenario on-demand path
  stays byte-identical and never touches this package.
- **On-demand only.** Canonical never calls `Effective`. Unknown name → the bring-up was already
  rejected at intake (don't silently fall back — that's the exact lie scenarios must avoid).
- **Mechanism locked; VALUES are calibration** — every `s`, target, and `c`/`w`/camp-spread is a
  slot, never fixed here.

## Do NOT
- Do NOT add per-scenario behavior branches, or touch derived fields (diet, perception_profile,
  exit_profile, max_hold) — they re-derive downstream from the shifted roots.
- Do NOT clamp — if you reach for a clamp, the ruler is wrong.
- Do NOT own or copy the baseline GenParams — read it, transform on the fly.
- Do NOT build event/shock scenarios (flash crash, scripted breakout) — DEFERRED (future
  macro-event generator). Only composition-driven presets are in scope.
- Do NOT build the measurement/`s`-fitting tool here — it's an offline calibration-time tool
  (surface 9), not runtime, not in the agent path.

## Tests (ship with the code)
- Ruler round-trips: `fromLink(toLink(m)) == m`; inverse always in-domain for extreme shifts
  (logit→(0,1), log→(0,∞), arctanh→(−1,1)) — assert NO clamp needed.
- `Effective("")` returns `(nil, nil)` and the generator path is byte-identical to baseline.
- A shove moves only the knobs named in `direction` (untouched `d==0` knobs pass through at
  baseline); disposition shoves log-ratios then softmax-renormalizes to a simplex summing to 1.
- `phi_bimodal` ON → `EffParams.PhiBimodal` set with `{c,w,spread}`; it overrides any phi.center
  arrow.
- Registry load validates knob names against the catalogue (typo → fail fast at startup).
- Determinism: same baseline + seed → same `η`, same `EffParams`. `-race` on.