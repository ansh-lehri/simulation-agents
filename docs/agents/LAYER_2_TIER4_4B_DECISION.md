Layer 2 — Tier 4 / 4b: Decision (Disposition) Stage (Design Notes, LOCKED except 4b-2)

What this doc is. The design of 4b — the decide stage: it turns perception's channel objects (from 4a) + the agent's disposition into an intent (direction + conviction, or wait/skip). This doc covers the tick model, what 4b consumes, the interpretation step, the mixer (4b-1), and the re-decide gate (4b-gate). The commit gate (4b-2) is deliberately EXCLUDED — it is still OPEN and documented/decided elsewhere.

Cross-refs. Perception fields/objects: LAYER_2_TIER4_4A_PERCEPTION.md. Tier framing & the six axes: LAYER_2_TIER4_FRAMING.md. How the per-agent perception_profile values (incl. channel_weights, decide_sensitivity) are generated: LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md.

1. What 4b is (LOCKED)
4b is the decide stage: perception objects + disposition → intent. This is where observations become opinions (the 4a boundary) — 4b is the first stage allowed to be disposition-coupled.
Output = INTENT only: {direction, conviction} to act, or WAIT (carry stance), or SKIP. It does not produce an order (sizing/order-type/execution = 4c) and does not manage an existing position (hold/add/trim/exit = 4d).
Tick model (LOCKED): one tick = one full top-to-bottom pass (perceive → decide → execute). An agent does not do 4a this tick and 4b next tick; when it acts, it runs the whole tree in that tick.
Branch boundary: 4b is the flat-agent entry branch. A holding agent runs 4d instead (the hold-check fork at the top of the tree). Exactly one of 4b / 4d runs per tick.
2. What 4b consumes (LOCKED)
Directional channel objects (chart, range) → feed the direction decision.
Condition channel objects (liquidity, volume) → gate/modulate whether & how much (not which-way).
Context (time) → modulates.
absent vs neutral — 4b must treat "no reading" differently from "neutral reading" (4a read_ok).
Disposition {trend, mean_rev, noise} — the mixing weights.
Carried stance (working memory) — am I already watching / waiting?
3. Sub-structure (LOCKED)

4b-gate (do I re-decide this tick?) → 4b-1 (the mixer: form a view) → 4b-2 (commit gate: act/wait/skip). This doc covers 4b-gate and 4b-1. 4b-2 is OPEN and excluded. The only contract 4b-2 must satisfy: reduce its output to {dir, conviction∈[0,1]} on COMMIT (the input 4c expects).

4. Interpretation step — channel objects → per-trait verdicts (LOCKED)

Each channel, per trait, returns either a directional lean or a conviction multiplier — never both the same number:

Directional channels → leans ∈ [−1,+1] (chart → trend lens; range → mean-rev lens; imbalance → whoever, currently unconsumed).
Condition channels → conviction multipliers ∈ [0,~1.5] (volume, spread/depth, time) — no direction, they scale.

Per-trait verdict:

trait_verdict_direction  = weighted_sum( directional leans the trait reads, by per-agent channel_weights )
trait_verdict_conviction = |that| × product( conviction multipliers the trait reads )
→ trait verdict = signed value in [−1,+1]; magnitude = conviction  (or 0/absent)
Primary/secondary: the primary channel provides the directional lean (chart for trend, range for mean-rev); secondaries are multiplier-providers. trait → 0 iff its primary direction channel is absent.
Structural split (survives adding traits): the signed part of a reading lives in a trait verdict; an unsigned trait-specific conditional (e.g. chart cleanliness) lives in that trait's verdict as a multiplier; an unsigned global conditional (spread/depth, time) lives at the 4b-2 gate once (not per-trait). "Global AND signed" cannot occur (a sign is always read through a trait).
channel_weights = per-agent intra-trait weights (generator-drawn, free Dirichlet; see perception-profile doc). Default equal.
Map content OPEN: which channels each trait consumes (incl. imbalance/volume placement) is config, still owed.
5. 4b-1 — The Mixer (LOCKED = option C)

Combines the per-trait verdicts into one opinion {direction, conviction}.

Noise first (pre-mixer bypass coin):

roll noise-coin( w_noise ):
   hit  → opinion = random impulse (bypass the whole mixer + gate)   # anti-deadlock liveness
   miss → opinion = C over {trend, mean_rev}, weights renormalized

Option C — dominant + secondary:

dominant   = argmax(w_trend, w_mr)            # ties broken by seeded coin
direction  = sign(V_dominant)
conviction = |V_dominant|
for each secondary trait t:
    align = +1 if sign(V_t)==direction else −1
    conviction *= (1 + align · w_t · |V_t| · k)
conviction = clamp(conviction, 0, 1)          # secondaries dampen/boost, NEVER flip direction
Runs only when ≥2 traits have verdicts; if only the dominant has one, conviction = |V_dom|, no multipliers.
k ≈ 0.5 is the single calibration knob (secondary sensitivity).
Why C: rejected pure-A (weighted sum → balanced agents cancel to paralysis in clear markets) and pure-B (weighted-pick → loses the lopsided agent's nuance). C is right in both regimes: lopsided → committed with slight temper; balanced → directional but low-conviction ("nervous").
Deferred: stochastic-dominant-near-ties (B-like flip-flop for near-50/50 agents) — enrichment, not in prototype.
6. 4b-gate — do I re-decide this tick? (LOCKED)

Keys off perception-output change (NOT market data — that is 4a's re-perceive gate). Sibling pattern to 4a's gate; this desynchronizes the stages.

Flat-idle / holding agents — staleness test:

for each directional field the agent's traits consume (direction, strength, position_in_range, stretch):
    Δ = | current_reading − last_decided_reading |
    if Δ > decide_sensitivity[field]:  mark DECIDE-STALE
if ANY field DECIDE-STALE → RE-DECIDE (run mixer)   # Decision A: any trips → re-run all
else                      → COAST (reuse standing opinion/stance)
decide_sensitivity[field] = per-agent, per-consumed-field, in perception_profile (reading-units; distinct from 4a's reread_sensitivity which is market-units).

Agents in WAIT — the quiet-confirm fix: A waiter does not force-re-decide every tick (a small consolidation move that IS the confirmation would otherwise be coasted past, freezing it). Instead:

maintain wait_streak = consecutive ticks spent in WAIT
if wait_streak > WAIT_REASSESS_THRESHOLD:   → force RE-DECIDE
else:                                       → COAST (stay in WAIT)

WAIT_REASSESS_THRESHOLD = a global constant (not per-agent).

Carried state (Redis working memory): last_decided_readings (consumed fields only), standing_opinion, standing_stance, wait_streak.

OPEN sub-piece: exact wait_streak increment/reset bookkeeping across a forced re-decide's outcome (continued-WAIT vs COMMIT vs SKIP) — behavior stated, tick-level counter bookkeeping not yet pinned.

7. What this doc excludes
4b-2 (commit gate) — STILL OPEN. Applies global-conditional multipliers (liquidity spread/depth, time) to mixer conviction once, thresholds vs a per-agent θ_act, immediate-vs-confirmation-waiter split → COMMIT/WAIT/SKIP, noise bypasses. Must emit {dir, conviction}. Not designed here.

End of 4b (Decision) — LOCKED except 4b-2.