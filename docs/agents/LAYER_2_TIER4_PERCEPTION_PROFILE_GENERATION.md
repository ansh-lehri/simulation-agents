Layer 2 — Tier 4: perception_profile Generation (Design Notes, LOCKED)

What this doc is. The complete mechanism for how the generator draws each per-agent perception_profile field at mint, plus the two 4d frozen traits whose draws were designed in the same pass (exit_profile, max_hold). perception_profile itself (its fields, what they mean, the runtime bias/skill-noise application order) is defined in LAYER_2_TIER4_4A_PERCEPTION.md; this doc is only about how the values are produced at generation.

Scope guard. Three items are deliberately OPEN and NOT designed here (see §12): depth normalization, the channel→trait map content, and θ_add. Everything else below is settled.

Grounding. 4a schema/fields: LAYER_2_TIER4_4A_PERCEPTION.md. Trait persistence: rides the Tier-1 traits JSONB (drawn at mint, persisted for canonical, redrawn fresh for on-demand), same spine as perception_profile and diet. Reproducibility: every draw uses the seeded RNG (live = fresh, tuning = fixed seed).

1. Coupling philosophy — why draws are not independent (LOCKED)

The governing requirement: a single agent's draws must be mutually coherent — a "jumpy" agent must be jumpy across lookback AND sensitivity AND cutoff-width, not jumpy in one and calm in another. Independent per-field random draws produce incoherent Frankenstein agents (e.g. a 200-bar lookback but a 0.1% re-read threshold). So the design is: draw a few per-agent latent temperaments, then derive the fields from them (correlated), plus a small independent per-field jitter for texture.

Resolution chosen = Option C: reuse existing axes (skill, disposition) where they genuinely apply; add minimal new latents only for the temperament facets no existing axis honestly covers. aggressiveness is execution-urgency and was deliberately NOT reused for perception-patience (semantically wrong — a patient perceiver can be an aggressive executor).

2. The latents / drivers (LOCKED)

Three drivers feed the draws:

Reactivity ρ ∈ [0,1] (NEW latent). 0 = deliberate/calm, 1 = jumpy. Drawn once at mint: ρ ~ Beta(α_ρ, β_ρ), frozen. α_ρ, β_ρ = calibration (population jumpy/calm mix). Optional (default OFF): nudge α up for noise-disposition agents since impulsive ≈ jumpy.
Optimism φ ∈ [−1,+1] (NEW latent). Signed: − = permabear/skeptic, 0 = neutral, + = permabull/optimist. Drawn once at mint: φ ~ 2·Beta(α_φ, β_φ) − 1, frozen. Symmetric Beta (α_φ = β_φ) → most agents near 0, few extreme. α_φ, β_φ = calibration. Optional (default OFF): disposition nudge.
skill ∈ [0,1] — existing axis, already drawn. Used here for method-selection filtering and exit-mode selection. (skill also drives runtime perception-noise per 4a — that is NOT stored in the profile; global skill drives it.)

New-latent note: ρ and φ are genuine additions to the generator / trait vector → a Tier-1 traits schema amendment (same category as perception_profile itself).

Latent → field map (full, so nothing is unsourced):

driver	fields it governs
ρ	method selection (proximity), all windows/periods, all cutoff widths, reread_sensitivity, decide_sensitivity, max_hold, the ma_type coin
φ	bias (directional fields only)
skill	method-selection skill-gate, exit_profile.mode, (runtime noise — existing, not stored here)
ε (per-field jitter)	idiosyncratic texture on every numeric draw
Dirichlet	channel_weights
3. The general numeric draw rule (LOCKED)

Every numeric param/cutoff is produced the same way. Each has a design range [lo, hi] (its allowed box). ρ picks the point in the box; ε jitters it so same-ρ agents are not clones:

center = lo + (hi − lo) · (1 − ρ)                         # ρ places the point (see direction rule)
value  = center + ε ,   ε ~ Normal(0, σ_idio · (hi − lo)) # fresh independent draw per param, seeded
value  = clamp(value, lo, hi)                             # keep inside the box
round to int where the param is integer
Direction rule — everything maps (1 − ρ): jumpy (high ρ) → short windows / tight cutoffs (reacts on little, declares signals readily); deliberate → long windows, wide dead-zones. One consistent rule, which is what keeps an agent coherent.
ρ is shared across all the agent's fields (one coherent personality); ε is an independent fresh draw per param (texture on top). ε is NOT a temperament signal and NOT (1−ρ) — it is zero-mean Gaussian noise, so most draws stay near the ρ-center.
σ_idio = one global calibration fraction (small, e.g. ~0.05–0.1 of the range) — the "how much individual quirk around temperament" knob.
The [lo,hi] boxes are the only per-field calibration. Where a box brackets a known TA default it is grounded (see §5), not a free guess.
4. Method selection — chart & range only (LOCKED)

Only the two method-forked channels (chart, range) pick a method; fixed-calc channels (liquidity/volume/time) have no method. Disposition does NOT discriminate among methods (all chart methods detect trend); the honest driver is reactivity ρ (methods differ by lag: MA laggy, structure fast, RSI early), with skill as a secondary filter (sophisticated methods demand skill).

Fixed design (not per-agent):

Each method has an anchor a_m ∈ [0,1] on the reactivity/lag axis. Ordering is designed & locked (a fact about the method); spacing is calibration:
chart: MA < RSI < structure (placeholders 0.2 / 0.55 / 0.85)
range: channel < Bollinger < RSI (placeholders 0.25 / 0.5 / 0.75)
Each method has a skill-demand d_m ∈ [0,1] (ordering designed: structure high, MA/channel ≈ 0; magnitude calibration).

Mechanism (produce method for the channel):

1. proximity   s_m = exp( −(ρ − a_m)² / (2τ²) )     # nearer the agent's jumpiness → higher
2. skill gate  g_m = skill^(η · d_m)                 # skill-heavy methods suppressed for low skill
3. weight      w_m = s_m · g_m
4. normalize   P(m) = w_m / Σ_k w_k
5. method ~ sample(P)        # seeded RNG; store in perception_profile[channel].method
Sample, NOT argmax — argmax collapses all same-ρ agents onto one method (method-level clones), destroying the "MA-users vs structure-users disagree at the same top" realism. Sampling keeps the population spread while still correlating method with temperament.
d_MA = 0 → g = 1 (unaffected); d_structure = 1, η = 1 → g = skill (low-skill agent almost never lands on structure).
τ = calibration (how strongly ρ dictates method); η = calibration (how hard skill filters). Both tuned, not designed.
Edge: skill = 0 + d_m = 1 → g_m = 0 → that method impossible for a zero-skill agent (acceptable; floor as (skill+ε) only if "rare not impossible" is wanted).
5. Per-method param & cutoff blocks (LOCKED)

Only the agent's chosen method gets its params drawn. reading_params compute the method's continuous number; reading_cutoffs slice that number into the categorical field (direction up/flat/down, overbought/oversold). Both are method-specific schemas. All numeric draws use the §3 rule with (1−ρ). Ranges that bracket a known TA default (RSI 14, Bollinger 20/2σ, MA periods) are grounded; the softer ones (swing, cutoff widths) are placeholders to calibrate.

CHART — MA

ma_type:     EMA with prob ρ else SMA        # categorical, ρ-coin (jumpy → faster EMA)
fast_period: [5, 20]     ← (1−ρ)
slow_period: [20, 100]   ← (1−ρ) ; enforce slow > fast (swap/clamp)
flat_band:   [0.1%, 1.0%] ← (1−ρ)            # dead-zone on (price−MA)/MA

CHART — structure

swing_window:   [3, 15] bars  ← (1−ρ)
min_swing_pct:  [0.2%, 2.0%]  ← (1−ρ)
confirm_swings: [1, 3] (int)  ← (1−ρ)         # HH/HL needed to call a direction

CHART — RSI

rsi_period: [7, 21]            ← (1−ρ)   # brackets classic 14
mid_band:   [±3, ±15] around 50 ← (1−ρ)   # flat-zone width

RANGE — Bollinger

bb_period:     [10, 40]    ← (1−ρ)   # brackets classic 20
bb_sigma_mult: [1.0, 2.5]  ← (1−ρ)   # brackets classic 2σ; jumpy → tighter bands
ob_os_band:    %B extreme, dist-from-0.5 ∈ [0.2, 0.5] ← (1−ρ)

RANGE — channel (min-max / Donchian)

channel_window: [10, 50]   ← (1−ρ)
extreme_band:   position thresh, dist-from-0.5 ∈ [0.3, 0.5] ← (1−ρ)

RANGE — RSI

rsi_period:   [7, 21]                ← (1−ρ)
ob_os_levels: dist-from-50 ∈ [10, 30] ← (1−ρ)   # → 60/40 … 80/20

Special handling: ma_type is the one categorical param (ρ-coin, not the range rule); fast < slow enforced; integer params rounded after ε.

6. bias — the optimism lean (LOCKED)

Scope: directional fields ONLY — chart.direction, chart.strength, range.position_in_range, range.stretch. Condition fields (volume/liquidity/time) get no bias (their diversity is params). Levels/pure facts never biased (4a lock).

Mechanism:

bias_f = φ · β_f            # per field f
φ = the per-agent optimism latent (§2), one value shared across the agent's directional fields (one coherent personality).
β_f = tilt-strength, a per-field population-wide constant (same for all agents), set by us, sized small relative to that field's own range so bias tilts, never fabricates. Placeholders: β_direction ≈ 0.2, β_position ≈ 0.2 (field range width 2), β_strength ≈ 0.1, β_stretch ≈ 0.1 (field range width 1). (Optional per-field ε for texture — default off.)
Applied at runtime PRE-cutoff (4a order): biased = raw + bias_f → cutoff → skill-noise → clamp.

Effect: near a cutoff boundary, bias flips the category (optimist reads up where neutral reads flat) → different action; far from the boundary it only shifts strength/conviction → different sizing. Stable same-direction lean every read (distinct from zero-mean per-read skill-noise). Produces persistent optimists/pessimists → standing two-sided pressure in the population.

7. Sensitivities (LOCKED)

Both are jumpiness dials → straight (1−ρ) draws (§3 rule), sharing the same ρ (so a jumpy agent re-perceives AND re-decides readily). Ranges are calibration placeholders (the volatility-anchoring idea was considered and rejected — stay with plain calibration ranges).

reread_sensitivity (4a gate — "how big a MARKET move before I re-perceive"): per ON-channel, in that channel's own units.

chart     0.1%–1.0% price move      ← (1−ρ)
range     0.05–0.3 position shift    ← (1−ρ)
volume    10%–50% volume change      ← (1−ρ)
liquidity 5%–30% spread/depth change ← (1−ρ)
time      none (phase change is a discrete boundary event — nothing to threshold)

decide_sensitivity (4b gate — "how big a READING change before I re-decide"): per consumed directional field (direction, strength, position_in_range, stretch), in reading units (normalized).

range 0.05–0.3 reading change  ← (1−ρ)

Runtime use: compare current reading vs the reading at last decision; if change > threshold → re-decide, else coast.

8. Fixed-calc channels (LOCKED, drawn only if that channel's diet flag is ON)

No method fork; same §3 draw rule.

Liquidity: depth_levels [1,5] ← (1−ρ) (jumpy = shallow/top-of-book; calm = deeper). (NOTE: depth normalization_baseline is OPEN — see §12. With normalization dropped, depth is read without a rolling "normal" reference.)

Volume:

avg_window:     [10, 50]    ← (1−ρ)         # volume history available from candles
spike_multiple: [1.5, 4.0]  ← (1−ρ)         # ×normal that counts as a spike (a cutoff)
level_bands:    low/normal/high boundaries ← (1−ρ) widths
baseline_type:  mean | median  ← free 50/50 coin   # categorical, no latent (optional: weight median by (1−ρ))

Time: no per-agent params (4a locked time as "no method, no params"). phase_bounds (open/mid/close) are fixed global boundaries, same for all agents. (Optional future per-agent jitter — default none.)

9. channel_weights (LOCKED mechanism; per-trait sets OPEN)

Per trait, weights over that trait's consumed channels, drawn from a Dirichlet (the locked simplex family):

channel_weights[trait] ~ Dirichlet(c · [1, 1, …])   # one weight per channel the trait consumes
c = concentration (calibration): high → near-equal weights across agents; low → lopsided (one chart-heavy, one 50/50). This is the "80/20 vs 50/50" diversity.
Free draw — no latent coupling (how much I trust volume vs chart within a strategy is idiosyncratic, not reactivity/optimism; coupling was deliberately NOT invented).
Dependency (OPEN, §12): which channels each trait consumes = the channel→trait map (config, not settled). The draw is map-agnostic — given the trait's channel set, Dirichlet over it — so the mechanism is locked but the per-trait vectors can't be finalized until the map is.
10. 4d frozen traits drawn in this pass (LOCKED)

All ride the traits JSONB, drawn at mint.

exit_profile.mode ∈ {distance, level, hybrid} — skill-coupled, same kernel shape as method selection (structural S/R stops are the more sophisticated skill):

anchors (skill axis): distance 0.2 | hybrid 0.55 | level 0.85     # ordering fixed, spacing calibration
score_style = exp( −(skill − a_style)² / (2τ²) )
P(style)    = score_style / Σ
mode ~ sample(P)         # seeded, frozen

τ = calibration (how hard skill dictates). Low-skill → mostly distance; high-skill → mostly level/hybrid.

exit_profile numeric fields:

stop_pct:      free draw in a calibration range (e.g. 0.5%–3%)   # risk appetite; NO clean latent (not coupled)
target_pct:    = stop_pct × RR ,  RR ~ [1, 3]                    # reward:risk ratio; guarantees target > stop
stop_buffer:   small free draw (e.g. 0.1%–1%)                    # level-mode offset beyond S/R, % of level price
target_buffer: small free draw (e.g. 0.1%–1%)

max_hold (ticks) — (1−ρ) draw in a tick range (calibration): jumpy → shorter hold (exits/bored faster), calm → longer. (Honest approximation: holding-patience isn't identical to perception-jumpiness, but reusing ρ keeps the agent coherent and avoids a new latent.)

θ_add — NOT designed (OPEN, §12): blocked on θ_act (4b-2), which is unresolved. Intended relationship θ_add ≥ θ_act.

11. Persistence & assembly (LOCKED)
The generator, at mint, given the agent's diet (ON channels) + disposition + skill + freshly drawn ρ, φ: draws a block only for each ON channel (off channels have no entry — absent, per 4a). Method-forked channels run §4 then §5; fixed-calc channels run §8; then bias (§6, directional fields), sensitivities (§7), channel_weights (§9), and the 4d traits (§10).
All frozen → traits JSONB; persisted for canonical, redrawn fresh for on-demand. ρ, φ are the Tier-1 traits schema amendment.
Every draw uses the seeded RNG.
12. OPEN points (deliberately NOT designed here)
Depth normalization / normalization_baseline — dropped/left open. Depth history isn't stored (quote is overwrite-only); options were (a) shared in-memory rolling window, (b) change the store, (c) static per-instrument constant. Not chosen. Consequence accepted for now: depth read without a rolling "normal" reference (absolute, not relative) → weaker intraday/cross-instrument liquidity sensitivity and a more brittle thin-liquidity scenario.
channel→trait map content — which channels each trait (trend/mean_rev/noise) consumes, as lean vs multiplier. Config, still owed. channel_weights and which channels are even on depend on it.
θ_add — blocked on θ_act (4b-2 commit gate, still OPEN).
13. Calibration inventory (tuned later, none designed here)

α_ρ, β_ρ; α_φ, β_φ; σ_idio; method anchor spacings a_m + skill-demands d_m + τ + η; all per-method param/cutoff [lo,hi] boxes; β_f tilt-strengths; sensitivity ranges; fixed-calc ranges; Dirichlet c; exit-mode anchors + τ; stop_pct/RR/buffer ranges; max_hold tick range. Ordering/direction rules are designed & fixed ((1−ρ) direction, method lag-order, slow>fast, EMA=jumpy, skill→sophisticated-mode); only magnitudes are calibrated.

End of perception_profile generation design — LOCKED except the three §12 open points.