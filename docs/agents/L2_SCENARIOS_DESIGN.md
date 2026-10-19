Layer 2 — Scenario-Based On-Demand Sessions (Design)

What this doc is. The full design of the scenario layer: how an on-demand user can select a named market scenario ("thin liquidity", "calm", "busy", …) and have the market behave that way by emergence, not by scripting. It covers the founding principle, why the mechanism is parameter-level (not trait-level), the complete manipulation mathematics (how the numbers get shifted scientifically, no hardcoded ranges), the knob catalogue, the bimodal switch, and the nine implementation surfaces (where it plugs into Tiers 1–2). On-demand only. Read L2_OVERVIEW_AND_TOPOLOGY, Tier 1, and Tier 2 first.

Grounding. The concept is locked in LAYER_2_TIER4_FRAMING §5 (and §6 for the deferred event layer). The coherence constraints come from LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION. The baseline trait dials come from LAYER_2_AGENT_SCHEMA §1 + the perception-generation latents. The link-space manipulation math, the registry shape, and the nine surfaces were designed in the scenario thread and are transcribed here. Mechanism is locked; all numeric values (scenario intensities, targets, bimodal parameters) are calibration slots, never fixed in design.

Scope. In: the param-level mechanism end to end — concept, math, knob catalogue, bimodal switch, registry, the nine surfaces, the fitting loop, the measurement obligation. Out: event/shock scenarios (flash crash, scripted breakout) — deferred to a future macro-event generator (Monte-Carlo / Markov), explicitly not built now; the runtime perception/P&L-reactivity machinery that makes agents react to conditions (Tier 4 — referenced, not designed here).

Status flag — the scenario set is NOT fixed. The example scenarios below (thin / calm / busy / dispersion / …) are illustrative candidates, deliberately left unfixed so the implementation isn't biased toward a particular set. What is locked is the mechanism that would realize any such scenario. Choosing and calibrating the shipped set is a later step.

1. The concept (locked — FRAMING §5)

A scenario is a user-selectable preset describing the aggregate character of the market (thin, calm, busy, volatile, …). The founding principle is emergence, not inducement: there is no if scenario == X branch that forces market conditions. A scenario is a parameter preset that shifts the population-generation dials (and the lifecycle-curve dials); the generator then draws a differently-shaped crowd, and the requested market character emerges from who is in the room and how they trade.

Key locked properties:

Parameter preset, not behavior (§5.2): e.g. "thin liquidity" = a population drawn toward high-aggressiveness, low-capital, fast step-out; the thin book emerges.
Scenario-neutral (§5.5): no knob is privileged, and no agent ever knows its scenario — it only feels the conditions the crowd produces (its perception reads the real book/vol/flow). This is what keeps it honest and undetectable.
Touches generation params AND lifecycle-curve params (§5.6): "how thin" is both who is drawn and how many are awake at once.
The acceptance test (§5.7): perception must read real conditions (liquidity/volume/volatility) so that any preset produces agents that feel it — this is the obligation on Tier 4 that makes scenarios verifiable.
On-demand only (§5.8); named presets now; scripted events deferred.
The three-lever model (why param-level is the whole in-scope story)

A real market's character comes from three places, and a scenario could in principle act on any:

Who is in the room — the population composition. Param-level. This is the in-scope mechanism.
How each present agent reacts to the conditions and to their own P&L — perception + P&L-reactivity. Runtime, Tier 4 — already designed there; a scenario does NOT edit this, it lets it operate on the conditions the composition produces.
A triggering event mid-session (a shock the crowd reacts to). Event-level — DEFERRED (the future macro-event generator). "Inject the pebble, not the ripples" (FRAMING §6).

A scenario reaches its effect through lever 1 only (shape who's present), and levers 2–3 then operate naturally: the present crowd reacts (lever 2, emergent), and — for scenarios that need a trigger — a future event (lever 3) lights the match. This doc builds lever 1. Levers 2 and 3 are not edited by scenarios; lever 2 already exists (Tier 4), lever 3 is out of scope.

2. Why parameter-level, not trait-level (the foundational decision)

Two mechanisms were considered for "adapt the agents to the scenario":

Param-level ("edit the recipe, bake fresh"): the scenario shifts the generation dials, then the generator draws the population fresh from the shifted dials. No individual agent is touched; each drawn agent is coherent.
Trait-level ("mutate the already-baked agents"): the population is already drawn, and the scenario edits each agent's existing trait values (e.g. ×1.5 aggressiveness, clamp).

Param-level is chosen, decisively, for three reasons:

A scenario is a distributional statement. "Thin market" is a fact about the crowd's composition ("fewer deep liquidity-providers present"), not about any individual's personality. Parameters are exactly the distributional controls; editing individuals expresses a distribution-level intent at the wrong altitude. The real-world analogue of a crunch is "the composition of who's actively participating shifted" (patient deep-pocket providers pulled back, twitchy thin-capital crowd remains) — a census change, which is a parameter act.
Coherence (the decisive technical reason). An agent's perception_profile (~20 fields: lookbacks, cutoffs, sensitivities, method pick, max_hold) is derived coherently from a few latents (rho, phi, skill) via the (1−ρ) rule (LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION §1–3). Trait-level post-hoc mutation can't cleanly propagate through that derivation — you'd either re-derive the whole profile (which is a redraw) or bump fields independently and produce the "Frankenstein agent" (200-bar lookback + 0.1% trigger) the coherence design exists to prevent. Param-level redraws rho/phi/skill from shifted dials and re-derives everything together → coherence preserved by construction.
Full-population coverage is automatic. Under param-level every agent in the room is drawn from the scenario distribution, so N agents = N scenario-shaped participants. (Under trait-level, the leased majority would need per-agent transformation and partial coverage.)

Consequence (the one Tier-1 lock this reopens): param-level only works on on-demand if on-demand redraws traits fresh each session. On-demand therefore reuses the economy account (the pool's actual benefit — skip account-open) but redraws traits from the (possibly scenario-shifted) dials. This refines the original lease lock (which reused stored traits). Because on-demand runs PersistOnClose=false (no P&L history, no cross-session continuity, A redrawn each session anyway), reusing the same trait vectors bought no behavioral continuity — so nothing behavioral is lost. (Whether a persistent on-demand identity should ever exist is an open question, parked; the durable row still stores traits, so the door isn't foreclosed — Tier 1 §2.)

3. The manipulation mathematics (how the numbers get shifted, scientifically)

The scheme shifts a few root generation dials; everything derived (diet, perception_profile, exit_profile, max_hold) re-derives itself downstream through machinery that already exists — so only the roots are touched. The science is in how a dial is shifted so there are no hardcoded ranges and no magic multipliers. Four ideas.

3.0 Two levels
Level 1 — population parameters (GenParams): the distribution dials describing the whole crowd (e.g. aggressiveness ~ Beta(α,β)). The ~13 dials.
Level 2 — per-agent trait values: the actual drawn numbers for one agent.

The generator flows Level 1 → draw → Level 2. A param-level scenario edits Level 1, then draws.

3.1 Idea 1 — shift in a stretched (link) coordinate → domain-safe, no clamp

A dial like "mean aggressiveness" m must stay in (0,1). Multiplying m directly can leave the range (0.7 × 1.5 = 1.05) and force a hardcoded clamp. Instead, transform m to an unbounded coordinate (the "ruler"), shift there, transform back — the inverse is guaranteed in-range, so no clamp is ever needed:

u = link(m)            -- go to the stretched ruler (−∞..+∞)
u_eff = u + shift      -- move freely; can't fall off, there is no edge
m_eff = link⁻¹(u_eff)  -- come back; always lands in the valid domain

This refines FRAMING §5.4's "clamp into valid ranges" — the clamp is replaced by domain-safe link-space shifting. (Recorded refinement of §5.4.)

3.2 Idea 2 — measure the shift in the crowd's own spread (standardized units)

The shift amount must mean something, not be "+0.8 of what". Measure it in units of the baseline crowd's own spread in link-space, η ("one crowd-width"):

shift = s × η

s = how many crowd-widths the crowd is shoved. Unitless, self-describing (s=1 = one standard-spread shift), and it travels correctly if the baseline is ever retuned (always relative to the baseline's own spread). η is computed empirically (§6, surface 3): sample the baseline distribution, push each draw through the ruler, take the standard deviation.

3.3 Idea 3 — direction × intensity (the scenario = a preset + one volume knob)

A real scenario moves several dials. Split the move into:

d — the direction: a per-knob list of signed, relatively-weighted arrows (which knobs move, which way, in what proportion). This is the scenario's identity.
s — the intensity: a single scalar for the whole scenario (how hard to push along d).

Per-knob shift: shift_i = s · d_i · η_i. Analogy: d is a graphic-equalizer preset (which sliders up/down), s is the one volume knob driving the whole preset. Calibration collapses from "a dozen multipliers per scenario" to one tunable s per scenario.

3.4 Idea 4 — direction signs are market facts; intensity is fit to a measurable target
d's signs are FREE and principled — known market cause→effect (aggressiveness↑→spread↑; mean-rev↑→depth↑; faster step-out→fewer awake→thinner). Defensible, not invented.
d's magnitudes: equal (Level-0, prototype default, zero invented numbers) or measured (Level-1, rigorous: perturb one knob, measure the metric's response). Never hand-shaped (the whimsy trap). Prototype uses Level-0; the future Monte-Carlo generator upgrades to Level-1.
s is FIT, not derived. A scenario is defined by a measurable market target (e.g. "thin = median spread ≥ 3× baseline"); s is tuned until a measured session statistic hits the target band. s is irreducibly fit (no closed form exists — the map from dials to emergent outcome only exists once the whole population interacts), but it is not whimsy because it is checked against a pre-declared, measurable outcome (§7, the fitting loop).
3.5 The full move, per knob
u_eff[i] = u₀[i] + s · d_i · η_i        -- shove, in link space, s crowd-widths along the arrow
θ_eff[i] = link⁻¹(u_eff[i]) → params    -- invert the ruler, back to real distribution params

The only non-derived numbers in the whole scheme: each scenario's target (a plain-market-terms choice, e.g. "3× spread") and its s (fit to that target, logged with its measurement). Everything else is structure or known market facts. Zero hardcoded multipliers, zero clamps.

4. The knob catalogue (what a scenario can touch)

Everything a scenario may move is a root dial (an independent generator input) or a switch. Derived fields (diet, perception_profile, exit_profile, max_hold) are never touched — they re-derive from the shifted roots. There are exactly three link rulers + one wrapper + a switch category.

4.1 Trait-distribution dials
Knob(s)	Knob handle	Ruler
aggressiveness, skill, pnl_reactivity, rho (means)	mean (0,1)	logit
p_herd	probability (0,1)	logit
capital median, capital spread	positive	log
presence concentrations / Dirichlet concentration	positive	log
phi center	(−1,+1)	arctanh
disposition {trend, mean_rev, noise}	3 proportions, sum=1	log-ratio + renormalize (location); log (tightness)
4.2 Presence-curve dials (lifecycle; FRAMING §5.6, 3b curves)
Knob	Ruler
entry rate, step-out rate, done rate, re-entry rate	log (positive rates)
4.3 Switches (set absolutely, not shoved)
tipper on/off, min_noise_floor on/off — trivial boolean overwrites.
phi_bimodal on/off — not a shove; splits φ into two camps (§5). The one switch that reaches into the generator.
4.4 The four ruler-types (summary)
logit — (0,1) means.
log — positive values (sizes, spreads, rates, concentrations).
arctanh — (−1,+1) (φ center).
log-ratio + renormalize — the sum-to-1 disposition simplex (the one wrapper: shift the log-ratios, renormalize back to proportions).

Every continuous knob is shoved identically (s·d·η on its ruler, invert); the knob's only individuality is which ruler it stands on.

5. The bimodal switch (phi_bimodal)

Some scenarios (e.g. "dispersion" / two opposed camps) want the crowd split into two groups, not shifted. A single mean/spread can't describe two humps, so this is a switch, not a shove, and it is the one place a scenario reaches the generator.

What φ becomes when ON — a two-camp mixture, drawn per agent:

camp ~ Bernoulli(w)                 -- BULL (prob w) or BEAR (prob 1−w)
φ_i  ~ (camp == BULL) ? hump(+c)     -- bull camp centered at +c
                      : hump(−c)     -- bear camp centered at −c

Both camps are drawn on the same arctanh ruler as normal φ, so bimodal φ stays in (−1,+1) by construction (no clamps). Parameters:

c — camp separation (bigger = more polarized).
within-camp spread — tightness of each camp (reuses the φ-spread handle).
w — bull/bear split (0.5 = balanced fight).

c, camp-spread, and w are calibration slots (fit to the dispersion target, like any s). When phi_bimodal is ON, it overrides any phi.center direction-arrow — so in a bimodal scenario, φ is governed by the switch block, not the direction block (a registry-consistency rule).

Generator cost (honest): phi_bimodal is a flag passed through the merge engine to the generator, and the generator gains one flag-gated branch, for φ only — "if bimodal: draw the two-camp way; else: draw the one-hump way." Every other trait draws identically regardless of scenario. This is the single point where scenario-awareness leaks into the generator; it exists only because the "dispersion" style scenario is kept. (An all-shove scenario set would keep the generator 100% scenario-blind.)

6. The nine implementation surfaces

The blast radius of a scenario-capable on-demand bring-up. Locked decisions are stated; the L2_SCENARIOS_IMPLEMENTATION_DESIGN doc has the point-by-point build.

Surface 1 — BRING_UP gains scenario (trigger)

One optional, separate field on the on-demand BRING_UP payload: a name string (e.g. "thin"), or absent. Absent → Δ=0 → identical to normal on-demand. It is its own field (orthogonal to mode). Validated at intake against the registry; an unknown name → reject the bring-up early (never silently fall back to normal — that would be the exact lie scenarios must avoid). Canonical never sends it. BRING_UP sends only the name, not the blob (locked).

Surface 2 — the scenario registry (new, L2-owned)

Where a name becomes content. One global, market-agnostic, startup-loaded config artifact (not a runtime DB table — d/s/targets are design-time constants we recalibrate, not per-session data). Read-only at runtime. Per scenario it holds:

direction d — the signed per-knob arrows (identity).
intensity s — a single calibration slot (placeholder until fit).
switches — absolute settings (tipper, min_noise_floor, phi_bimodal).
target — the measurable market outcome the scenario is defined by and s is fit against (metric + relation + value-slot + unit, e.g. median_spread ≥ <slot> ×baseline).

The knob catalogue (names + rulers) is owned by surface 3 (a ruler is a property of the knob, not the scenario); the registry references catalogue names. Structure (illustrative scenarios; values are slots):

knobs: (owned by surface 3) aggressiveness.mean→logit, capital.median→log, disposition→logratio, phi.center→arctanh, presence.*→log, …
defaults: { intensity_default: <slot>, magnitude_mode: equal }   # Level-0 for prototype
scenarios:
  normal:     { direction: {}, intensity: 0, switches: {}, target: none }          # the control
  thin:       { direction: {aggressiveness.mean:+, disposition:{toward:noise,away_from:mean_rev},
                            capital.median:−, capital.spread:−, presence.stepout_rate:+, presence.done_rate:+},
                intensity: <slot>, switches: {}, target: {metric: median_spread, rel: ">=", value: <slot>, unit: ×baseline} }
  calm:       { direction: {disposition:{toward:mean_rev,away_from:noise}, capital.median:+, pnl_reactivity.mean:−,
                            skill.mean:+, aggressiveness.mean:−, presence.stepout_rate:−},
                intensity: <slot>, switches: {}, target: {metric: price_range_width, rel: "<=", value: <slot>, unit: ×baseline} }
  busy:       { direction: {presence.entry_rate:+, presence.done_rate:−, aggressiveness.mean:+},
                intensity: <slot>, switches: {}, target: {metric: trades_per_min, rel: ">=", value: <slot>, unit: ×baseline} }
  dispersion: { direction: {aggressiveness.mean:+, pnl_reactivity.mean:+},
                intensity: <slot>, switches: {phi_bimodal: on}, target: {metric: realized_volatility, rel: ">=", value: <slot>, unit: ×baseline} }

(Disposition arrows use {toward, away_from}, not +/−, because it is the log-ratio simplex knob.)

Surface 3 — baseline (θ₀, η) + the transform layer

The "normal crowd" every scenario shoves away from — the existing generation params + 3b presence-curve params (read, never copied/owned here). Adds a transform layer:

raw baseline params ──(to moments)──▶ θ₀ ──(apply knob ruler)──▶ u₀   (logit/log/arctanh/log-ratio)
η = per-knob baseline spread, measured IN LINK SPACE, EMPIRICALLY (sample→transform→std-dev), computed ONCE at startup (cached)

Baseline representation stays raw + transform on the fly (locked) — no change to the existing non-scenario path, which never touches surface 3. Surface 3 owns the knob catalogue (names + rulers) and shares a bidirectional transform library with surface 4 (3 runs raw→link; 4 runs link→raw). Outputs (u₀, η) to surface 4.

Surface 4 — the merge / shift engine (the heart)

Pure L2-internal computation, runs once in the 2a on-demand generation path (all scenario calculation happens in one place — locked). Pipeline:

A  resolve scenario name → blob (d, s, switches)          [registry]
B  SHOVE (continuous knobs):  u_eff[i] = u₀[i] + s·d_i·η_i ; untouched knobs: u_eff[i] = u₀[i]
C  INVERT:  moments_eff = ruler⁻¹(u_eff[i]) → θ_eff[i]     [shared transform library, run backwards]
D  SWITCHES (absolute): set tipper / min_noise / phi_bimodal flag
E  SPLIT outputs:  θ_eff → generator (surface 5) ; curve_eff → 3b curve-builder (surface 7)

Disposition runs B/C vector-valued (shove log-ratios, softmax-renormalize back). phi_bimodal is passed through D to the generator (not computed away). Empty scenario → B is identity → θ_eff = θ₀ → byte-identical to normal on-demand (surface 4 is skipped when scenario is absent).

Surface 5 — the generation plug-point (into 2a Stage A)

mintAgent receives θ_eff instead of baseline params — same draw formulae (AGENT_SCHEMA / perception-generation), only the source of the params changed; the generator stays param-driven and scenario-blind except the one phi_bimodal branch (§5). Three-set coverage: both leased agents (reuse account, redraw traits — the Tier-1 refinement) and minted agents draw from the same θ_eff → 100% of the population is scenario-shaped. Coherence (perception_profile) re-derives from each agent's own drawn rho/phi/skill — it never sees θ_eff. Canonical never enters this (no scenario).

Surface 6 — register in the decision log

Bookkeeping, but load-bearing: the scenario locks must land in design-decisions.md, including the two supersessions (leased agents redraw traits; §5.4 clamp → link-space) recorded as supersessions, not silent edits. The decision log is near its size cap → this needs a consolidation pass first, so surface 6 is "consolidate + write", an explicit action, not an append. (Not yet executed.)

Surface 7 — the presence-curve plug-point (into 3b)

Surface 4 emits curve_eff (shifted presence-curve rates). It is computed in 2a but consumed later by the goroutine at 3b (curves built once at session start). So curve_eff rides the 2b handover in sessions:active (locked) — so a restart rebuilds scenario-shaped curves, not baseline ones. The 3b curve-builder's logic is unchanged (same λ formulae); it just receives shifted rate constants. This is the only scenario output that reaches past 2a into the running session.

Surface 8 — schema touches
rho, phi latents in the traits JSONB — no migration (JSONB). (Already pending from the Tier-4 work; scenarios make phi load-bearing.)
On-demand durable row — kept as-is (locked): still stores traits even though leased agents redraw on reuse (documented dead weight; keeps the persistent-on-demand-identity door open). Net schema change: effectively none.
Surface 9 — the measurement obligation (so s can be fit)

An offline, calibration-time, read-only tool (not runtime, not in the agent path): compute the four target metrics (median_spread, price_range_width, trades_per_min, realized_volatility) from a finished session's existing cached market data (marketdata:…:candles:1m + :quote). The raw material already exists in Redis; this is computation over it. It needs: a pinned definition per metric (window/estimator), decided once; and a baseline reference per metric (measure on a normal session, for the "×baseline" targets). The automated sweep over it = the future Monte-Carlo generator's job.

7. The fitting loop (how s is found)
for a scenario (direction d fixed, target declared):
    set s (start from intensity_default)
    run an on-demand session at that s        -- offline calibration run
    measure the target metric (surface 9) from the session's cached market data
    in target band?  → done; record this s with its measured value
    else             → nudge s, repeat        -- 1-D search along d

Prototype: hand-fit over a few runs — but, because one session is one random draw, a defensible fit averages a few seeds (not a single run). Future: the Monte-Carlo generator industrializes this (sweeps s over many seeds, and upgrades d's magnitudes from equal → measured). Same targets, same metrics — nothing in the prototype loop is thrown away.

8. Open / flagged items
Scenario set not fixed — the examples above are candidates; the shipped set is chosen + calibrated later (deliberately unfixed to avoid biasing implementation).
phi_bimodal is the one non-uniform lever — it adds a single flag-gated φ branch in the generator. Kept (dispersion retained). Dropping dispersion for an all-shove set would remove even that branch.
Event/shock scenarios OUT — deferred to the future macro-event generator (Monte-Carlo / Markov). Only composition-driven (param-level) scenarios are in scope.
d magnitudes = equal (Level-0) for the prototype; measured (Level-1) later.
All s, targets, c/w/camp-spread = calibration slots — none fixed in design.
Metric definitions (surface 9) owed — pin window/estimator per metric before fitting.
Channel→trait map still owed (shared with Tier 1 §7) — unrelated to scenarios directly, but it bounds what perception_profile fully contains.
Decision-log consolidation (surface 6) owed before registering these locks.

End of scenario design. Next: the three implementation docs (L2_TIER1_…, L2_TIER2_…, L2_SCENARIOS_IMPLEMENTATION_DESIGN).