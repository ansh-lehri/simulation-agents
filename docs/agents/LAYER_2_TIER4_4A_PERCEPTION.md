Layer 2 — Tier 4 / 4a: Perception (Design Notes, LOCKED, Pre-Impl)

What this doc is. The complete design of 4a — Perception, the first sub-step of Tier 4 (agent internals). Perception is the agent's eyes: it turns the in-memory market snapshot into observations the decision stage (4b) consumes. This doc covers the perception gate, the read accessor, the representation decision, the five channel schemas, the params-vs-thresholds split, the methods, the bias/skill-noise mechanism, the perception_profile object, edge cases, and every consideration weighed. The cross-cutting Tier-4 framing (principles, scenarios, events, the clone-problem resolution, sub-step plan) is in LAYER_2_TIER4_FRAMING.md.

Status convention: LOCKED = settled; OPEN (→stage) = deferred to a later sub-step; calibration = a value tuned later, not a design decision.

Grounding. Diet (5 flags) + the perceive→decide→execute chain: LAYER_2_POPULATOR.md §1–3, LAYER_2_AGENT_SCHEMA.md §1, LAYER_2_AGENTS.md §5 (endogenous signal list, no-LLM). The snapshot source + read surface: LAYER_2_SCHEDULER.md / 3a (full OHLCV per instrument + quote/depth snapshotted once per tick into goroutine memory). perception_profile persistence rides the Tier-1 traits JSONB.

No-LLM constraint unchanged. Endogenous signals only; news/fundamental deferred (PARK-2).

1. What perception is (and isn't)

Perception turns raw market data into readings — "the trend is up," "the book is thin on the ask," "volume just spiked," "price is near the top of its range." It does not decide anything; it doesn't know what "buy" means. 4b consumes the readings; perception only produces them.

The governing boundary — observations, not opinions (LOCKED):

Perception answers "what is true about the market right now, as far as I bother to look?"
"Price crossed above its MA" = observation (perception). "Therefore I should buy" = opinion (decision, 4b).
A trend-follower and a mean-reverter looking at the same chart perceive the same thing (price above MA) and decide opposite. So perception is disposition-agnostic in what it computes — it computes the reading; disposition decides what the reading means.
The test for any field: "could a trend-follower and a mean-reverter both agree this field's value is correct, while still deciding opposite things?" If yes → observation (belongs in perception). If the field only makes sense relative to a strategy/goal → opinion → cut it.

Where perception lives in the tree: it's the perceive stage, run when the agent is awake (p_act fired in 3a) AND the perception gate says "re-read" (§3). Its output feeds the decide stage (4b).

2. Which axes touch perception (LOCKED)

Owns it:

Information diet (the 5 flags) — the perception axis. The flags are the on/off switches of the eyes: chart/technical, order-book reading, volume-awareness, mean-reversion/price-in-range, time-of-session. An agent computes only the readings its on-flags cover. A chart-only agent is blind to the order book (doesn't read depth → can walk into a thin-book trap a volume-aware agent dodges).

Shapes it (but doesn't own it):

p_act / cadence (Tier-3, not one of the six) — governs how often the eyes open at all. Inherited upstream; 4a doesn't re-implement it.
Skill — degrades perception (misreading / noise), in addition to degrading execution (4c). This resolved the earlier "does skill touch perception" question → YES. Skill now has two channels.

Absent from perception (by design):

Disposition — absent (observations-not-opinions). Indirect influence only: diet was coupled to disposition at generation (a trend agent was born chart-heavy), so disposition shapes perception upstream, through the diet it was born with. At runtime, perception reads the diet, not the disposition. Diet↔disposition coupling is a generation fact, not a runtime perception step.
Aggressiveness, capital — purely execution (4c).
P&L-reactivity — absent from base perception; a possible 4e modulation hook (does being down bad tunnel your vision?) deferred to 4e. Base perception is reactivity-free.
3. The perception gate — "do I re-read this tick?" (LOCKED)

Even when awake (p_act fired), an agent does not always re-run full perception. Reading ≠ being awake. Some people glance constantly; some check rarely and coast on their standing view.

The two skips (must stay distinct — general Tier-4 pattern):

Coast-skip — nothing changed enough; reuse carried readings, don't re-compute.
Ran-and-got-nothing — ran perception, output is just unremarkable readings (this is NOT a gate skip; that's the node producing a result 4b finds uninteresting).

The gate handles only #1.

3.1 Re-read frequency is NOT a new trait (LOCKED)

"How often do I bother to re-read" falls out of p_act (how often I'm awake) + staleness (has the market moved). No separate re-read-frequency dial. (Confirmed: don't add a trait.)

3.2 The gate mechanism — per-channel change vs per-agent threshold (LOCKED)

"Market changed enough" is not one global check — perception has 5 channels, each watching a different aspect, each with its own notion of "changed." The gate is per-channel:

For an awake agent, a cheap glance: for each on diet channel c, compute c's change-metric from the snapshot vs its last-read reference:

Channel	Change metric (cheap, off the snapshot)
chart/technical	price move since last read (% move, or a new bar closed)
order-book	spread or depth change (did the book shift materially)
volume	volume change (current vs recent-average)
mean-reversion/range	price move relative to range (did position-in-range shift)
time-of-session	session phase boundary crossed (discrete, rare)

Each on-channel has a per-agent threshold (its re-read sensitivity), stored in the perception_profile. A jumpy agent re-reads on a 0.1% move; a calm one needs 1%.

for each ON diet channel c:
    change_c    = metric(snapshot, last_read_ref_c)
    threshold_c = perception_profile[c].reread_sensitivity   # per-agent
    if change_c > threshold_c:  channel c is "stale" → wants re-read

if ANY on-channel wants re-read → re-perceive EVERYTHING (Decision A)
else                           → coast (reuse last readings)
3.3 Gate decisions (LOCKED)
Decision A — ANY channel tripping → re-read everything. (Chosen over B = re-read only tripped channels.) Simpler; one gate, whole node runs or doesn't. Cost (occasionally re-reading an unchanged channel) is cheap. B (per-channel partial re-read) is a clean later refinement.
The gate is a cheap glance, not free-of-looking. It reads current price/spread/volume to compare against last-read — in real life the "do I bother to properly analyze" decision is preceded by a glance ("eh, hasn't moved"). The gate is the glance; the node is the real read.
Natural desync: chart and mean-reversion both key off price but differently (chart = raw move, range = move relative to the band), so one price tick may trip chart-recheck but not range-recheck. Time-of-session changes only at phase boundaries (rare discrete). This per-channel-own-staleness keying is what desynchronizes agents and prevents lockstep.
3.4 Carried state the gate requires (first working memory)

To compute "changed since last read," the agent carries last-read reference values (last-read price, spread, volume, range-position, phase). This is the first slice of per-agent working memory, living in the Redis runtime snapshot. (Working memory accretes across sub-steps; this is where it starts.)

4. The read accessor (LOCKED)

The thin, read-only interface between a signal and the tick snapshot. Signals don't poke the raw snapshot; they ask the accessor ("give me the last 20 closes," "give me the current spread"). It's the vocabulary of what perception may look at.

Why it exists:

It's the contract that keeps signals honest (signals see only what it exposes — e.g. no forming bar, no forward-looking, no per-agent Redis hit, enforced in one place).
"What data exists" is defined once (grows when higher TFs / more depth arrive, with no signal rewrites).
Shared, stateless, read-only — safe for the future concurrent-decide switch (3a). Computes nothing agent-specific.

Source nuance (LOCKED): the accessor reads the in-memory snapshot (which 3a copied out of L2 Redis once at the top of the tick), not Redis per-agent — otherwise thousands of Redis reads per tick. Same underlying data (OHLCV/quote lives in Redis), different access path.

What it exposes (the vocabulary):

Price/bar history: last N bars; a field series over N bars (closes/highs/lows/volumes); the current/most-recent bar; trivial conveniences (high/low over last N, current price = last close or quote mid).
Current market state: latest quote (bid/ask/mid), spread (ask−bid), depth (size at/near bid/ask), imbalance (bid-size vs ask-size).
Volume: current bar's volume; volume over last N bars.
Session/time: normalized session time t (0=open, 1=close) and/or phase (open/mid/close).

Key properties:

Low-level — serves "last N closes," not "the trend." Turning closes into a trend reading is the signal's job (where per-agent method/params/bias enter). The accessor has no per-agent anything — identical for every agent. Diversity happens one layer up.
Lookback N is supplied by the signal, not fixed by the accessor — N comes from the agent's perception_profile (its chart lookback param). Accessor = "how much do you want"; signal+profile = "I want 20 because that's my style."
"Not enough bars" is the accessor's to report honestly — asking for 50 when 12 exist returns the 12 (or signals "short"); the signal decides what to do with a short window. The accessor never fakes missing bars. (Feeds §8 edge cases.)

The one real accessor decision (LOCKED = B): does the accessor do any computation?

(A) Pure data-server — returns raw slices only; every calc (even "average last N volumes") is the signal's job.
(B, CHOSEN) Data-server + strategy-free primitives — also offers cheap, agent-agnostic, interpretation-free arithmetic that many signals share (average-volume-over-N, range high/low over N, simple moving average). Hard guardrail: if it encodes any interpretation, it's a signal, not an accessor primitive. Test: if two different dispositions would compute it identically, it can live in the accessor; if it embeds any reading/thresholding/bias, it's a signal.
5. Representation — what a signal outputs (LOCKED: structured objects)

The hinge between perceive (4a) and decide (4b). Three candidates were weighed:

Option 1 — continuous score ([-1,+1]): sign = direction, magnitude = strength.
Option 2 — discrete flag/category: {up, down, flat} etc.
Option 3 — level/structured: "support at 100.5, resistance at 103."
5.1 Why not scores-as-primary (the downsides that killed it)
Lossy and permanent — +0.6 hides "clean steady climb" vs "one violent spike"; both compute to +0.6; 4b can never recover the texture.
Forces a fake sign on condition signals — "spread is wide" / "volume is thin" have no inherent sign. Forcing them to [-1,+1] means inventing a convention ("wide = −0.5 because bad?") — but bad for whom? A liquidity-provider likes a wide spread. The sign is only meaningful relative to a disposition → that smuggles a decision into perception (violates observations-not-opinions). Some signals are naturally directional (trend, range-position); others are naturally conditions (liquidity, volume, volatility) with no sign.
Hidden normalization burden — each signal needs raw→[-1,+1] mapping; mismatched normalizations silently over-weight whichever "runs hotter."
False precision — +0.6 vs +0.58 looks meaningful but isn't; invites treating noise as signal.
5.2 The decision — structured per-channel objects (user's idea, adopted)

Each channel outputs a fixed-schema structured object — a standard set of observation-only fields describing several angles of what that channel sees. Not a single number, not a flag.

Kills the fake-sign problem — liquidity's object is just {spread, depth, imbalance}, honest descriptive fields, no invented sign.
Stops throwing away texture — if "cleanliness" is a field, both direction and texture survive to 4b.
Honest for a TA platform — a structured "here's what the trend looks like from several angles" is truer to how a human reads, and reusable (could feed explanations later).
Levels stop being special — they're just fields (support/resistance, range bounds).

Standard schema, per-agent values (LOCKED): the schema (fields) of a channel object is fixed and shared — every agent's trend reading has the same fields. What values fill it varies per agent (my 20-bar vs your 50-bar lookback → different direction/strength in the same structure). The structured-object schema is the shared container; within-cluster diversity lives in the values (via perception_profile).

5.3 The two guardrails that keep structured objects from becoming cognition

The richer the object, the more it risks (1) fields that are secretly opinions, and (2) 4b becoming a rule-reasoning engine over 50 fields. So:

Constraint 1 — fields are observations only (the agree-on-value / decide-opposite test, §1). No field encodes "good/bad/tradeable."
Constraint 2 — lean, not maximal. ~3–5 fields per channel; each earns its place by changing what some disposition would do. If a field changes no decision, it's decoration → cut. (Keeps 4b tractable — mixing/gating over ~15 fields, not 50.)
5.4 absent ≠ neutral (LOCKED)

A channel that's off (diet flag not set) or can't read (not enough data) outputs absent/null, which is distinct from an object with neutral-valued fields. "I read trend and it's neutral" ≠ "I have no trend reading." 4b must tell them apart. (Implemented via read_ok — §8.)

5.5 The shape this produced (fell out, not imposed)

Honest per-channel schemas naturally split into:

Directional channels (chart, range) → scored directional fields → feed the mixer's direction.
Condition channels (liquidity, volume) → descriptive condition fields → feed as gates/modulators (whether/how-much, not which-way).
Context channel (time) → pure clock → modulates.
Levels (range_bounds, chart key_levels) → carried alongside for execution (4c)/exit (4d).

The "directional-scores + condition-modulators" split emerged from asking what each channel observes — it wasn't imposed. Good sign the design is faithful.

6. The five channel schemas (LOCKED — ~15 fields)

Every channel object also implicitly carries read_ok (did I have enough data) — the absent≠neutral mechanism (§8). If read_ok=false, fields are null and 4b treats the channel as unread. Not repeated per channel below.

6.1 Chart / technical (directional workhorse)
Field	Meaning	Why it changes a decision
direction	up / down / flat	primary directional input to the mixer
strength	how pronounced the move (0–1)	conviction scaling — strong trend pulls harder
cleanliness	smooth/steady vs choppy (0–1)	clean trend invites following; choppy invites fading/skipping (texture scores lost)
maturity	young vs extended (bars since move began)	extended move is exhaustion-prone → mean-reverters lean against it, late trend-followers hesitate
key_levels	nearby support/resistance prices (level)	carried for 4c (place limit at support) / 4d (stop below support); not mixed, not thresholded

maturity kept (user confirmed — it's where "tops roll over" behavior comes from). key_levels added after user catch: chart was missing its levels supplement (parallel to range_bounds); S/R is levels ("what prices matter"), distinct from the directional fields ("what price is doing"). Cut (as opinions): "tradeable," "momentum-adjusted strength."

6.2 Mean-reversion / range (counterweight to trend)
Field	Meaning	Why it changes a decision
position_in_range	where price sits, −1 bottom … +1 top	primary input for fade/reversion
stretch	how far beyond "normal" (0–1)	extreme stretch → strong reversion pull
range_bounds	actual high/low prices of the range (level)	carried for 4c/4d

time_at_extreme (symmetric to chart's maturity — how long price has been pinned at the extreme) considered and deferred (user). Note: position_in_range near the top is the same market fact a chart agent reads as "strong trend" — same data, two channels, two framings; disposition picks which matters. That's the trend-vs-mean-revert tension working as designed.

6.3 Liquidity / order-book (condition — no inherent direction)
Field	Meaning	Why it changes a decision
spread	tight vs wide (normalized)	wide → costlier to cross → shrink/skip or rest a limit
depth	thick vs thin (normalized)	thin → my order moves price → size down; how thin-liquidity scenarios land
imbalance	bid-size vs ask-size (−1…+1)	short-term pressure read — order-book agents lean with/against it

imbalance kept (user). Flag retained: it's the field most at risk of being "secretly a direction vote" — it's a fact about the book, not a price-direction reading, so it stays an observation, but watch it in 4b.

6.4 Volume (condition / confirmation)
Field	Meaning	Why it changes a decision
level	current vs recent-normal (low/normal/high)	thin → moves untrustworthy → skip/shrink; high → something's happening
spike	sudden burst now (bool / 0–1)	flags potential event/breakout → momentum agents perk up, careful agents wait
confirming	is volume rising with the price move	the key volume insight — "price up on high volume" (real) vs "up on no volume" (fake) → dodges fake breakouts

confirming honest flag: it correlates volume with price, so it peeks at price (not purely volume-in-isolation). Acceptable — still an observation ("volume and price moving together: yes/no"), disposition-agnostic.

6.5 Time-of-session (context / clock — barely a reading)
Field	Meaning	Why it changes a decision
phase	open / mid / close	avoid the open, square up near close
session_progress	normalized t (0→1)	continuous version — some agents ramp behavior smoothly toward close

Does almost no computation (reads the clock) — correct; time-of-session is nearly free. Mostly modulates other decisions (near close → more likely to flatten) rather than driving direction.

Totals: 4+3+3+3+2 = ~15 fields, all observation-only, each justified, lean. "May add fields later if a channel needs one" (user) — e.g. time_at_extreme, depth-shape — recorded as future, not now.

Volatility note: no dedicated volatility channel. Folded into chart's cleanliness and range's stretch. Flagged as the one structural gap; user accepted covering it via those fields for now.

7. Params vs thresholds, and methods (LOCKED)
7.1 Two kinds of knob, two homes (the core clarification)
Reading params → PERCEPTION (in perception_profile). Shape how you compute the observation: lookback, band width, "normal volume" baseline, swing-size, reading cutoffs (e.g. RSI>60 → overbought). They produce the field values. Disposition-agnostic (a lookback isn't bullish/bearish). Proof: a param that produces a disposition-agnostic field must itself be agnostic.
Action thresholds → DECISION (4b). "How much of a reading before I act" (strength > 0.5 → buy, stretch > 0.7 → fade). Disposition-coupled ("how strong a trend before I chase" only means something to a trend-chaser). They consume the fields.

The RSI-60 worked example (settled): "RSI > 60 → overbought = true" is a perception reading cutoff — a statement about the data, true/false regardless of strategy; a trend-follower and mean-reverter agree RSI=62 is "above 60," they disagree on what to do. "overbought → therefore sell" is the action threshold (4b). The tell: the 60 produces an observation; it doesn't commit you to a trade.

Same word, two jobs — kept distinct: perception has reading cutoffs (how a categorical field like direction/volume.level is drawn — perturbed before the cutoff by bias/noise); 4b has action thresholds. "Reading cutoff" is reserved for perception; "threshold" for 4b's action cutoffs.

7.2 Cross-check — fields are shaped to be threshold-able by 4b (passed)
Magnitude fields 4b thresholds (strength, stretch, depth, imbalance, level, spike, confirming) → normalized continuous (0–1 / −1..+1) → > 0.5 is meaningful. ✅
Category fields (direction, phase, volume.level) → clean enumerables → 4b matches on them. ✅
Level fields (range_bounds, key_levels) → prices → not thresholded; used by execution/exit. ✅
7.3 Methods — pick-one, families where they exist (LOCKED)

A field is produced by a method chosen from a small family; perception_profile picks which + holds its params. Pick-one per field (A) for the prototype; multi-method blend (B) deferred as a clean seam.

Chart (method-rich): direction/strength via MA-based / structure-based (higher-highs/higher-lows swings) / RSI-based. They disagree characteristically at turns (MA laggy, structure fast, RSI early) → messy staggered tops = the realism payoff. cleanliness, maturity = simpler shared calcs.
Range (method-rich): position_in_range/stretch via Bollinger-style / min-max channel / RSI-style; range_bounds = window high/low. (σ-multiple of a Bollinger band is itself a per-agent reading param — 1σ agent calls "stretched" far more than 2σ.)
Liquidity: direct arithmetic (spread = ask−bid normalized; depth = summed top-level size vs normal; imbalance = (bid−ask)/(bid+ask)). Not method-forked, but still per-agent params (depth levels, normalization baseline).
Volume: level = current/rolling-avg; spike = sudden deviation vs recent; confirming = price-volume agreement. Per-agent params (avg window, mean-vs-median baseline, spike multiple).
Time: pure clock. No method, no params.

Correction captured (user catch): the line is NOT "method-diverse channels vs single-calc channels." Almost every field has methodological/param variation — chart/range have dramatic variation (different indicators), volume/liquidity have milder variation (same calc, different window/baseline/σ). So every channel carries per-agent reading params; chart/range additionally carry a method-family switch. This gives within-cluster diversity even in the "simple" channels (two volume-watchers with different baselines genuinely disagree about "high volume").

7.4 "Normal math" logic, stated (not hand-waved)

Ratios, differences, and comparisons-to-own-baseline: spread = ask−bid scaled; depth = summed size vs recent-normal; imbalance = (bid−ask)/(bid+ask) ∈ [−1,1]; volume.level = current/rolling-avg; volume.spike = sudden deviation vs recent; volume.confirming = did volume rise as price moved.

8. Bias & skill-noise mechanism (LOCKED)

Each magnitude/score field goes through three steps before output:

raw_value = method(data, reading_params)     # the computation
biased    = raw_value + bias                  # stable per-agent lean (fixed offset, drawn once)
final     = biased + noise(skill)             # fresh per-read random jitter, magnitude ∝ (1 − skill)
→ clamp final back into the field's valid range (−1..1 or 0..1)
Bias — a fixed per-agent offset (frozen). Same direction every read → a stable personality (optimist reads trends stronger; skeptic discounts breakouts). Applies to magnitude/score fields; not to levels (don't bias a support price) or pure facts (phase).
Skill-noise — fresh random each read, magnitude set by (1 − skill). High skill → ~0 noise (clean); low skill → wide noise (sees trends in chop, misreads). Seeded RNG. Global skill drives all channels' noise (per-channel skill-noise deferred — user).
Order: bias (deterministic lean) then noise (random error), then clamp (perturbation can't push out of bounds).
Categorical fields (direction, volume.level): bias/noise applied to the underlying continuous quantity before the reading cutoff (perturb the MA-distance, then apply up/flat cutoff) — so a low-skill agent flips category near the boundary = the realistic misread. Not applied to the label after the fact.
Levels & pure facts (key_levels, range_bounds, phase, session_progress): no bias, no noise — a price level and the clock aren't mis-perceived as a magnitude is.

This is what makes two same-method, same-param agents still differ: one leans (bias), one is sloppier (skill-noise). Combined with per-agent params (§7.3) and method choice (§7.3), it's the full within-cluster diversity stack ((a) params + (b) bias + (c) skill-noise + (d) method).

9. The perception_profile object (LOCKED — Tier-1 traits schema amendment)

A new frozen-trait object, one entry per on diet channel, drawn by the generator at mint using diet as input (only draw params for on-flags). A distinct field in the trait vector — not part of diet (diet stays 5 pure flags). Rides the existing traits JSONB → persists for canonical, redraws for on-demand. traits_version is the hook for the future learning tier to mutate it.

perception_profile = {
  <channel>: {
     method:             <choice from the channel's family>   # only for method-forked channels (chart, range)
     reading_params:     { ... }                              # channel-specific (see table)
     reading_cutoffs:    { ... }                              # categorical cutoffs (e.g. up/flat boundary, RSI levels)
     reread_sensitivity: <float>                              # the gate threshold for this channel (§3.2)
     bias:               <float>                              # stable magnitude offset (§8)
  },
  ...  # ONLY channels whose diet flag is ON; off channels have NO entry (absent, not null block)
}
# global skill (an existing axis) drives skill-noise for all channels (§8); not stored per-channel here

Per channel:

Channel	method (family)	key reading_params / cutoffs
chart	MA / structure / RSI	lookback; up/flat cutoff; (RSI: period & levels; structure: swing size)
range	Bollinger / channel / RSI	window; sigma_mult (1σ/2σ); overbought/oversold cutoffs
liquidity	— (fixed calc)	depth_levels; normalization baseline
volume	— (fixed calc)	avg_window; baseline_type (mean/median); spike_multiple
time	— (clock)	(none; or phase boundaries)

Why the generator must draw it (not a separate component): drawing frozen per-agent traits IS the generator's job; a second minter would break the single-writer / reproducibility spine (Tier-1 lock). What's protected is diet's purity, not the generator's non-involvement — the generator grows by one draw, diet is untouched, they're distinct fields.

10. Edge cases (LOCKED — mostly stating what's decided)
read_ok = false when not enough data (early session, thin on-demand history) → fields null → channel treated as unread (absent ≠ neutral). The accessor surfaces the shortness honestly (§4); the signal decides "compute anyway" or "can't read."
Off-diet channel → no perception_profile entry → not computed → outputs absent (not a neutral value). absent ≠ 0.
Noise-agent thin diet → diet is thin (coupled from noise disposition at generation — "reads little") → computes few/no channels → mostly absent. Nothing special in 4a; its impulsiveness is 4b's job (and the anti-deadlock mechanism — see framing doc §7.2/§8.4).
Levels/facts never biased or noised (§8).
11. Perception's obligations to the rest of the system (LOCKED)
To 4b: hands over the bundle of channel objects (some absent). 4b mixes directional-channel fields, gates on condition-channel fields, reads context. absent must be distinguishable from neutral.
To 4c/4d: carries levels (key_levels, range_bounds) for order placement / exit setting.
To scenarios & shocks (the acceptance test): the signal set reads real conditions — liquidity (spread/depth), volume (level/spike/confirming), volatility (via cleanliness/stretch), range-position — not just direction. So any population preset or injected shock is perceptible by whichever fraction's diet covers it, and is felt, not known (no agent reads a scenario label). If this coverage holds, scenarios/shocks produce felt behavior for free.
12. What 4a leaves to later sub-steps
Action thresholds (how much reading before acting) — 4b.
The mixer (how disposition-weights combine these objects into direction + conviction) — 4b (the biggest fork in the tier).
How absent vs neutral is consumed — 4b.
Levels usage (place limit at key_levels, stop at range_bounds) — 4c/4d.
Own-position-as-perception-input (is "I'm holding and it's underwater" a perception or a position-management input) — flagged, lands in 4d.
P&L-reactivity bending perception (tunnel vision when down) — deferred hook, 4e.
Per-channel skill-noise, multi-method blend (B), per-channel partial re-read (gate Decision B), time_at_extreme, extra fields — named future refinements.
All parameter VALUES (lookbacks, σ-multiples, cutoffs, sensitivities, bias magnitudes, noise scale) — calibration.
13. 4a status summary
Item	Status
Perception = observations, not opinions; disposition-agnostic	LOCKED
Axis composition: diet owns; p_act gates; skill degrades (2 channels); disposition/aggr/capital absent; reactivity deferred-hook	LOCKED
Gate: per-channel change vs per-agent threshold; ANY trips → re-read all (A); coast otherwise	LOCKED
Re-read frequency = NOT a new trait (falls out of p_act + staleness)	LOCKED
Gate carried state = last-read reference values (first working memory, Redis)	LOCKED
Read accessor = data-server + strategy-free primitives (B); in-memory snapshot, not per-agent Redis; N from profile	LOCKED
Representation = structured per-channel objects; standard schema, per-agent values; observation-only + lean guardrails	LOCKED
absent ≠ neutral (read_ok / off-channel / short-data)	LOCKED
5 channel schemas (~15 fields: chart 5, range 3, liquidity 3, volume 3, time 2) incl. key_levels	LOCKED
Params→perception / action-thresholds→4b; reading-cutoffs (RSI>60) are perception	LOCKED
Methods = pick-one (A); families for chart/range; per-agent params everywhere	LOCKED
Bias (fixed lean) + skill-noise (random ∝1−skill), pre-cutoff for categoricals, not on levels/facts; global skill	LOCKED
perception_profile object (frozen trait, generator-drawn, rides traits JSONB) — Tier-1 amendment	LOCKED
time_at_extreme; per-channel skill; multi-method (B); partial re-read (gate B); dedicated volatility channel	Deferred / future
All parameter values	Calibration

End of 4a (Perception) design notes — LOCKED, pre-impl. Cross-cutting Tier-4 framing is in LAYER_2_TIER4_FRAMING.md. Neither doc is written to the decision log yet; decision-log registration (incl. the perception_profile traits amendment) is the next action before 4b.