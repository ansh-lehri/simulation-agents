Layer 2 — Tier 4 Implementation — Alternatives Not Chosen (Revisit Log)

What this doc is. The roads-not-taken for the Tier-4 implementation design — each decision with the chosen path, the alternative(s) set aside, and why. A place to revisit if a chosen path hurts at calibration or scale. Grows per sub-step. Pairs with the *_IMPL.md docs.

4a — Perception
#	Decision	Chosen	Alternative(s) not chosen	Why deferred / revisit
1	Where decide logic lives	Pure decision package; session.decide orchestrates I/O	all in session; a shared sub-fn both call	Pure pkg = golden-testable. Revisit if the adapter boundary gets awkward.
2	Instrument scope	DS multi-instrument; action on [0]	hardcode single instrument; full per-agent attention now	Count-agnostic + degenerate action. Revisit when the attention layer is built.
3	Timeframe	Configured list, dynamic, agent picks (one configured: 30m); no profile field	generated TF trait; per-channel TF; 1m-only hardcode; multi-TF-live + staleness	One TF now → moot. Revisit when >1 TF live (staleness reopens).
4	Perception memory	Per-instrument (instrumentMemory) + per-instrument Redis blob	single refs per agent (shipped Tier-1 shape)	Required once perception is per-instrument. No revisit.
5	Indicator math	Pure-Go markcheno/go-talib	hand-rolled; C TA-Lib via cgo	cgo breaks clean build; hand-roll re-derives. Revisit only if go-talib fails.
6	Strength/stretch scaling	Param-derived (natural bounds; MA via own flat_band)	invented global scales	No magic globals. One residue = MA saturation multiple m; revisit → tie to volatility.
7	Skill-noise width σ_perc	Single global constant	per-agent σ	Per-agent double-encodes skill; mirrors 4c's σ_exec.
8	Gate granularity	Decision A (any channel trips → recompute all)	Decision B (partial per-channel re-read)	A simpler; recompute cost cheap. Revisit if perception dominates a heavy tick.
9	Swing detection	Fractal pivots + min_swing_pct filter	zig-zag (retracement threshold)	Fractal stateless/cheap. Revisit if staggered-top realism needs zig-zag.
10	confirming (volume)	Standalone volume condition, latest bar, direction-agnostic	coupled to chart direction; multi-bar window	Keeps channels independent + param-free. Revisit if a 1-bar look is too thin.
11	cleanliness	Path efficiency	variance ratio / fit R²	Cheapest honest measure. Revisit if it conflates chop shapes.
12	maturity	Run length / window	regime detector	Cheap proxy. Revisit if "old trend" needs precision.
13	Range-RSI range_bounds	Max/Min window fallback (RSI has no band)	omit for RSI range agents	4c/4d need a level. Revisit if the fallback misleads exits.
14	Depth normalization	Dropped — depth absolute	in-memory rolling window; store change; static per-instrument const	Carried from design §12. Revisit when thin-liquidity scenarios need relative depth.
4b — Decision
#	Decision	Chosen	Alternative(s) not chosen	Why deferred / revisit
1	Noise in the mixer	Pre-mixer bypass coin weighted by w_noise	blend a 3rd "noise verdict"; fixed birth-assigned noise type	Noise reads nothing → no verdict to blend; coin expresses weight as impulse-frequency + anti-deadlock. Revisit if aggregate impulse behaviour reads wrong.
2	Noise as bypass vs perturbation	Exclusive bypass (impulse OR signal)	jitter layered on the signal opinion	Perturbation has nothing to perturb for a signalless high-w_noise agent — breaks the agents that need noise.
3	Mixer combine rule	Option C (dominant dir; secondary dampens/boosts conviction, never flips)	A weighted-sum (balanced cancel → paralysis); B weighted-pick (loses lopsided nuance)	From 4B_DECISION.md §5. Revisit stochastic-dominant-near-ties if balanced agents feel rigid.
4	4b-2 conditions	Shave only (no hard gate) in iter 1	hard can't-act gate for blown-out spread	Shave→under-θ_act already SKIPs; hard gate redundant. Thin edge with depth dropped — revisit if an unsafe-market gate is needed.
5	Time draw-down shape	Full U (cautious open & close)	late-only ramp 1 − close_damp·p²	Open explicitly wanted cautious. Revisit if the open dip adds nothing.
6	Spread draw-down ref	spread/spread_ref (static per-instrument "normal spread")	raw %-of-price, no ref	Ref is the one echo of the dropped depth baseline; no-ref form is the fallback.
7	4b-2 reads mixer internals?	No — only {dir, conviction}	re-read trait agreement	Conviction already bakes in agreement (Option C).
8	Aggressiveness placement	Drives θ_act/patience at generation	runtime conviction multiplier	Keeps execution-urgency vs decide-threshold distinct. Revisit if under-expressed.
9	ChannelWeights at one lean	Kept (inert, renormalizes to 1)	remove from generation	Keeps the mechanism for a future multi-lean / order-flow trait.
10	imbalance (order-book direction)	Perceived but consumed by no trait (reserved seam)	hand to trend as a minor lean	No principled order-flow trait yet. Revisit when one is added.
4c — Execution
#	Decision	Chosen	Alternative(s) not chosen	Why deferred / revisit
1	round_to_tick grid	Round to nearest whole tick (step 1) — prices are int64 ticks	per-instrument coarser tick size	store.Quote/snapshot carries no tick size. Revisit if a per-instrument tick size arrives upstream (snap to its multiple).
2	open-vs-add resolution	Derive from position (one Y formula; only close is explicit)	trust Action for all three; separate open/add math	Formulation Y collapses open/add; deriving is robust to the sender. No revisit expected.
3	opposite-dir-while-holding	IGNORE (drop; bool=false) — guard only	treat as reduce/flip in 4c	Reduce/flip is 4d's branch; root dispatch means it shouldn't reach 4c. Kept as a safety guard.
4	θ_market placement	Global calibration dial	per-agent generated trait	Per-agent variation already comes from aggressiveness; a global cutoff suffices.
5	Pre-flight reliance	Build valid-by-construction; core pre-flight is a guard	let core.validateOrderPunchRequest shape/reject	A rejected punch is silently dropped (fire-and-forget) — correctness must live in 4c, not the guard.

End — Tier-4 implementation alternatives (4a, 4b, 4c). Extend per sub-step.