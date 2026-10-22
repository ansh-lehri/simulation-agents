Layer 2 — Tier 4 / 4b-2 Commit-Gate Traits — Generation (LOCKED)

What this doc is. How the generator draws the three new per-agent commit-gate traits θ_act, needs_confirm, confirms_needed, and how θ_add is handled. No new design — the behavioural design is locked (aggressiveness drives all three; directions fixed), and the draw mechanism is the already-locked perception-profile draw pattern (LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md §3: center = lo+(hi−lo)·(1−driver) + ε jitter; weighted coin for a boolean) applied with aggressiveness as the driver instead of ρ. Consumed by LAYER_2_TIER4_4B_DECISION_IMPL.md §7 (4b-2) and …4D_POSITION_EXIT.md (θ_add).

Why it was owed. 4b-2 was never documented when generation was built, so these traits are absent from agent.TraitVector and orchestrator/generator. ThetaAdd sits reserved/commented in agent/traits.go precisely because it was blocked on θ_act.

1. Scope & placement (reproducibility)
Endogenous-only — like disposition/perception_profile. Herd agents follow tips and never decide, so these are not drawn for them (omitempty pointers, absent on herd rows).
Draw order: appended at the end of the endogenous block (after max_hold). Appending at the end means no existing draw shifts position in the RNG stream, so every already-implemented trait replays identically under a fixed seed. (Generator CLAUDE: kind-first, then the endogenous sequence; these extend its tail.)
Driver aggressiveness is already drawn earlier in the endogenous block, so it is available.
Seeded RNG throughout (live = fresh, tuning = fixed), per the CLAUDE reproducibility rule.
2. The draws (orchestrator/generator/draw_commit_gate.go)

All directions locked by the 4b-2 design: aggressive → lower bar, more likely immediate, fewer confirms; cautious → the reverse.

// θ_act — the commit bar. Aggressive → low bar. Same (1−driver) placement as perception params.
θ_act = clamp( lo + (hi−lo)·(1−aggressiveness) + ε , lo, hi )          // ε ~ N(0, σ_idio·(hi−lo)); [lo,hi]=ThetaActRange

// needs_confirm — immediate actor vs waiter. P(immediate) = aggressiveness.
immediate     = rng.Float64() < aggressiveness
needs_confirm = !immediate

// confirms_needed — if a waiter, how many confirmations. Aggressive → fewer. Int placement.
confirms_needed = clampInt( round( lo_c + (hi_c−lo_c)·(1−aggressiveness) + ε_c ) , lo_c, hi_c )   // [lo_c,hi_c]=ConfirmsNeededRange
θ_act: at aggressiveness=1 → (1−aggr)=0 → θ_act≈lo (lowest bar); at 0 → ≈hi. Reuses the perception draw's placement helper (centerFromReactivity-style, with aggressiveness as driver) and SigmaIdio for the ε jitter — same machinery, nothing new.
confirms_needed is drawn for every endogenous agent (even immediate ones); it's simply ignored at runtime when needs_confirm=false — matches how perception draws all on-channel params regardless of later use. Integer, rounded + clamped like drawIntPerceptionField.
ε/ε_c are fresh per-trait jitters so two same-aggressiveness agents still differ (the σ_idio texture), exactly as in perception.
3. θ_add — derived, not generated

θ_add = θ_act + margin (margin = a global calibration constant). Not stored as a trait — derived at runtime in 4d from θ_act + cfg.margin (decision.Params), so there's no redundant field to keep in sync. The reserved/commented ThetaAdd in agent/traits.go is therefore dropped (or left unused); θ_add lives in 4d's logic, not the trait vector.

4. TraitVector additions (agent/traits.go)

Endogenous-only omitempty pointers, same pattern as Rho/Phi/MaxHold:

ThetaAct       *float64 `json:"theta_act,omitempty"`        // commit bar, [0,1]
NeedsConfirm   *bool    `json:"needs_confirm,omitempty"`    // immediate(false) vs waiter(true)
ConfirmsNeeded *int     `json:"confirms_needed,omitempty"`  // confirmations if waiter
// ThetaAdd: removed/unused — θ_add is runtime-derived (θ_act + margin) in 4d

They ride the existing traits JSONB persist/revive path (Tier 1) with the rest of the vector — canonical reuses across sessions, on-demand redraws fresh. No new persistence wiring.

5. GenParams additions (cmd/agentsd/calibration.go placeholders)

Shapes/families locked; numbers are calibration:

ThetaActRange        NumericRange   // e.g. {Low: 0.3, High: 0.8}  (bar range on [0,1])
ConfirmsNeededRange  IntRange       // e.g. {Low: 1, High: 2}      (waiter patience; cap 2 per the 4b-2 lean)
// SigmaIdio: reused (already exists)

needs_confirm needs no range (driven directly by aggressiveness). margin for θ_add is not here — it's a decision.Params global (4d runtime), not a generation dial.

GenParams.Validate() gains: ThetaActRange ordered and within [0,1]; ConfirmsNeededRange.Low ≥ 1 and Low ≤ High.

6. Codebase changes (summary)
agent/traits.go: add ThetaAct, NeedsConfirm, ConfirmsNeeded; drop the reserved ThetaAdd.
orchestrator/generator/draw_commit_gate.go: the three draws (§2), called at the tail of the endogenous mint path.
orchestrator/generator/params.go + cmd/agentsd/calibration.go: ThetaActRange, ConfirmsNeededRange + Validate checks.
decision.Params: add margin (for 4d's θ_add = θ_act + margin).
7. Resolved micro-choices (not design forks)
θ_add → runtime-derived, not stored (§3).
confirms_needed → drawn always, ignored when immediate (§2).
margin → global constant, not per-agent (§3/§5).

End — 4b-2 commit-gate traits generation. Mechanism = the locked perception-profile draw pattern with aggressiveness as driver; no new design.