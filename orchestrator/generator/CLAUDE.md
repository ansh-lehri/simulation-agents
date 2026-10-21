# orchestrator/generator — Trait-Draw Engine (Tier 2 / Phase 3)
<!-- Per-package CLAUDE.md. READ ORDER:
       1) /CLAUDE.md                            — root constitution
       2) /orchestrator/CLAUDE.md               — the caller
       3) THIS file                             — specific to generator
       4) the spec docs below                   — the authority for the draw
     This file does not repeat the above. -->

## Spec (the authority — the draw is defined across four docs; read all in orientation)
- `docs/agents/L2_TIER2_IMPLEMENTATION_DESIGN.md` Point 2 — generateCanonical / generateOnDemand /
  mint / the `drawTraits(rng, eff, ctx)` signature + the scenario `*EffParams` seam.
- `docs/agents/LAYER_2_AGENT_SCHEMA.md` — the six axes, kind-first draw order, the named
  distributions (Beta / Dirichlet / log-normal / coin).
- `docs/agents/LAYER_2_POPULATOR.md` — diet+disposition coupling (approach a).
- `docs/agents/LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md` — the ρ/φ latents, method selection,
  per-method param/cutoff boxes, bias, sensitivities, channel_weights, exit_profile, max_hold.
  (Framing/why: `LAYER_2_TIER4_FRAMING.md`.)

## What generator IS (one line)
A pure, seeded population generator: given country + N + optional scenario `EffParams`, it
produces `Population{existing, new}` — reusing/leasing durable rows and minting the shortfall,
each minted agent a fully-drawn frozen trait vector.

## Owns / public surface
- `generateCanonical(country, N, rng)` — revive-first (`AgentStore.LiveMembers`) then mint the
  shortfall; `ensureOneTipper`.
- `generateOnDemand(country, ex, N, rng, scenario)` — lease-select (`LeaseAllocator.Select`) +
  load, **redraw** their traits fresh, then mint the shortfall. Calls the scenario provider
  (Phase 3: a no-op returning nil `EffParams` = baseline; Phase 4 swaps in the real engine).
- `mint(rng, ctx, eff)` — draw id + `drawTraits`, STAGE the `pending_mint` WAL **before**
  account-open.
- `drawTraits(rng, eff, ctx)` — THE engine (see Invariants).
- `GenParams` — the full generation calibration config (distribution params + all the
  perception/exit boxes). Values are calibration placeholders; families/ordering are fixed.

## Invariants (LOCKED — a wrong draw order breaks reproducibility, a wrong coupling breaks realism)
- **Kind-first draw order** — draw `agent_kind` first (gates the next draws AND keeps the RNG
  sequence stable), then endogenous: disposition → diet-coupled-from-disposition → skill,
  aggressiveness, capital_size, pnl_reactivity, p_act → (endogenous) ρ, φ, perception_profile,
  exit_profile, max_hold. Herd: act_prob instead of disposition/diet/perception_profile. Exact
  order per the schema doc — do not rearrange.
- **Diet coupled from disposition (approach a)** — never sampled independently (no incoherent
  "trend-follower who reads only news").
- **perception_profile is endogenous-only, drawn per ON diet-channel only** (OFF channels
  absent, not null). Numeric fields follow the §3 `(1−ρ)` rule + per-field ε jitter + clamp;
  method is **sampled, not argmax** (same-ρ agents must not collapse to one method); `bias` =
  `φ · β_f` on **directional fields only**.
- **exit_profile.mode skill-coupled (sampled); max_hold is a (1−ρ) draw.**
- **One tipper** — canonical sets a static `is_tipper` on exactly one endogenous agent
  (`ensureOneTipper` promotes if no revived one carries it). On-demand's tipper is a RUNTIME
  assignment (Phase 5), NOT drawn here.
- **Scenario-blind except one branch** — every trait just receives shifted numbers from `eff`;
  the ONLY scenario-aware code is the φ-bimodal branch (`eff.PhiBimodal`), and `eff == nil` →
  baseline. (The engine producing `eff` is Phase 4.)
- **Generic, seeded, frozen** — not per-instrument; seeded RNG (live=fresh); traits write-once.
- **Distribution FAMILIES locked; VALUES are calibration** (in `GenParams`), never hardcoded.

## Do NOT
- Do NOT add `theta_add` as a trait — it is runtime-derived in 4d as `theta_act + margin` (a `decision.Params` global), never stored. `ThetaAct`, `NeedsConfirm`, and `ConfirmsNeeded` are the three commit-gate traits; they are drawn at the tail of the endogenous block, after `max_hold`.
- Do NOT finalize `channel_weights` contents without the channel→trait map (config still owed,
  per PERCEPTION_PROFILE_GENERATION §12 / Tier-1 §7) — build the Dirichlet mechanism over a
  trait's channel set; the per-trait channel sets are a placeholder to fill at calibration.
- Do NOT open accounts, persist, or write Redis — generator only assembles + stages the WAL.
- Do NOT draw per-instrument or derive any seed from an identifier.

## Tests (ship with the code — golden-FIRST for the draw)
- Determinism: fixed seed → identical population (every trait field) across two runs.
- Kind-first order: assert herd agents carry act_prob + no disposition/diet/perception_profile;
  endogenous carry the full endogenous block.
- Coupling: diet flags consistent with disposition; perception_profile blocks present only for
  ON channels; method distribution spreads (not collapsed) at fixed ρ across many draws.
- bias applies to directional fields only; (1−ρ) direction holds (jumpy → shorter windows).
- `eff == nil` path = baseline; one tipper exactly; WAL staged for every minted agent.
- `-race` on. Pure package — no store/Core calls (inject fakes for LiveMembers/Select/WAL).