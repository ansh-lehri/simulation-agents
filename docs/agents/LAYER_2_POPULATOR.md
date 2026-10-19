Layer 2 — Populator Design Notes (Handoff, Pre-Lock)

Continues from LAYER_2_AGENTS.md. That file captured the agent-behaviour groundwork (fidelities, the four research roles as corners, herding/operator-tip with friction, lifecycle, deferred layers). This file captures everything settled after it: the finalized axis set, the perceive/decide/execute clarification, the coupling decision, and the population-generator ("populator") responsibilities + design plan.

Status convention unchanged: nothing is a final locked approach unless marked LOCKED. "Decided (prototype)" = the prototype uses this; still open to revision later. Candidate = one of possibly several options.

No-LLM constraint unchanged: heuristic agents only for the prototype.

1. The three-stage chain (the clarification that fixed a lot of confusion)

An agent's decision decomposes into three separate stages, each an independent axis. Earlier these were blurred (especially "information diet" folded into "disposition," and "skill" defined three different ways); this is the stable, corrected version:

Perceive → Decide → Execute Information diet (what it reads) → Disposition (what it decides) → Skill (how well it executes).

Each is separable because you can hold two fixed and vary the third and get a describable, distinct agent:

Information diet = what the agent reads / its inputs. (chart/technical, order-book, volume, mean-reversion inputs, herding/tips, time-of-session; news/fundamental deferred.) Strictly the inputs / perception — NOT "how it decides." The eyes.
Test: two trend-followers, same disposition; X reads only price, Y also reads volume → on a fake breakout (price up on no volume) X gets suckered, Y dodges. Same decision-rule, different inputs → different behaviour.
Disposition = what strategy it applies to what it reads. (follow the trend, fade/mean-revert, noise/impulse, follow the herd.) The rule / the decision.
Test: two agents see the same rising price; A "joins the move" (trend), B "it's overbought" (mean-revert) → opposite actions from identical inputs. Same inputs, different rule.
Skill = execution quality — how cleanly vs sloppily it carries out the decision it already made. NOT what it reads, NOT what it decides, NOT a win-rate/outcome dial. Only the fidelity of execution.
Test: two identical trend-followers; high-skill enters early at a good price and exits before reversal; low-skill enters late at a worse price, chases fake breakouts, exits slow. Same strategy, different execution quality.
Definition (LOCKED wording): skill = how much unintended noise/error sits between an agent's decision and its actual order. Profitability is emergent (does the well/poorly-executed disposition suit conditions), never dialed directly.
Separability proof: the "high-conviction but bad-at-timing" agent — low disposition-noise (commits hard, not random) AND low skill (executes that conviction at terrible entries). Cannot be described by disposition alone → skill is a genuinely separate axis.
Disposition-noise vs skill-noise (the one confusing overlap, resolved): disposition-noise = randomness as a chosen strategy (trades on whim on purpose). Skill-noise = error executing a non-random strategy (trying to follow trends, doing it badly). Separable but adjacent; keep them from collapsing during tuning.
2. The six axes for the prototype — DECIDED

The axis set is decided. (What is NOT yet decided: the range and distribution shape for each — that's population generation, §5/Step 2.)

Information diet — what it reads (perception).
Disposition — what it decides (strategy). Sampled coupled with diet — see §3.
Skill — execution quality (fidelity of carrying out the decision).
Aggressiveness — market vs limit order propensity (takes vs provides liquidity; the taker/maker balance). Structurally required — without it either everyone rests (no trades) or everyone takes (no book). Also what the operator-tip friction leans on ("some take, some provide").
Capital / size — bankroll from a realistic distribution. Structurally required, not flavour: wired to Layer 5 Gate 4 — agents reserve funds there, so a small agent literally cannot place a whale order; uniform capital → uniform order size → synthetic-looking book. (Prototype lean: gentler tail than a full power-law; range is a tuning question.)
Behavioral reactivity to own P&L — a per-agent sensitivity dial (fixed at creation) governing how strongly the agent's behaviour shifts as its own running P&L moves: steady/disciplined (≈0 reactivity) ↔ panic/capitulation (dumps when down) ↔ revenge/tilt (gets more aggressive when down). The P&L is the live input; the sensitivity is the fixed trait. This axis absorbed the essential slice of "discipline" (panic-selling is what turns a dip into a crash and fires the circuit breaker / CIR); it is the coupling the operator-tip's realistic staggered unwind depends on.

Reconsideration history (why six, not the earlier seven): the original LAYER_2_AGENTS.md listed 7 loose axes. On revisit: time-horizon/frequency deferred (some frequency variation emerges free from disposition-triggers; its absence doesn't make the market fake); skill re-included but redefined to execution-quality (the earlier "win-rate" definition was rejected as an invisible cheat-dial; the execution-quality definition earns its place because decision-noise is what makes flow feel human vs mechanical); discipline merged into P&L-reactivity (its market-relevant slice is panic, which is P&L-driven). Net: 6 axes.

Frequency note: time-horizon/frequency remains a deferred, independent candidate axis. Earlier it was wrongly said to be "pending the lifecycle" — that link was disproven: frequency (how often it acts while active) and lifecycle (when it's active) are independent clocks that merely both read time; sharing an input is not a dependency. Frequency returns on its own merits or not, independent of lifecycle.

3. Diet + disposition coupling — DECIDED: approach (a)

Information diet and disposition are logically separate (§1) but correlated in practice — a trend-follower reads charts because it follows charts; a noise/impulse trader reads little. Sampling them independently would generate incoherent agents (e.g. "follows the chart trend" but "reads only news, never sees a chart").

Two options were weighed:

(a) Keep two axes, sample coupled — draw disposition, then draw a diet consistent with it, but with variation inside those options (a trend-follower gets chart-heavy diet, and may or may not also watch volume). Keeps within-disposition variety (the X/Y fake-breakout distinction), no incoherent agents. Costs a coupling rule.
(b) Collapse into one "trader-kind" — bundle diet+disposition into a sealed unit. Simpler, but every agent of a given disposition becomes a clone (all trend-followers watch the same things, react to the fake breakout identically). Loses the within-disposition variety that the whole axis approach exists to produce.

DECIDED: approach (a) — two separate axes, sampled coupled. The harm of (b) is clone agents (no variety within a disposition); (a) keeps the "same strategy, different people" texture and rules out incoherent combos. Cost: the populator needs a coupling rule (disposition → consistent-diet-with-variation).

4. Population generator ("populator") — RESPONSIBILITIES

A pure generator + scheduler + lifecycle-runner. Nothing more.

Generate the population — sample a generic population across the six axes (diet+disposition coupled). Generic, not per-instrument — because agents self-select instruments via the two-stage selection layer (LAYER_2_AGENTS.md §6); per-instrument generation would contradict that locked selection mechanism. (At 1–2 prototype instruments, selection is degenerate, so generic vs per-instrument is near-identical in effect — but generic is the consistent principle at any instrument count.)
Generate the tipper and followers — instantiate both as agents. The generator only creates them; the tipper decides tips independently (direction/conviction/target/maybe stop), followers react independently. The generator drives no decisions (not the tipper's, not anyone's).
Run the lifecycle state machine — staggered entry, done-for-day exit (profit target / loss limit / time budget), random re-entry.
Schedule/tick active agents — drive active agents to produce orders into the engine's Admission Gate.
Enforce finite attention/capital per agent — the one LOCKED principle from LAYER_2_AGENTS.md §6.3.

Boundaries — explicitly NOT the populator's job:

Money / balances — Layer 5 is sole writer (Gate 4). Populator reads an agent's capital to know its size; never writes balances.
Matching / price — the engine owns these. Populator only submits orders.
Instrument personality — the instrument owns it (its volatility, circuit band, tick). How an agent reads/responds to that personality is the agent's concern. The populator has nothing to do with instrument personality at any instrument count. (This was briefly, wrongly, attached to the populator as a "per-instrument composition" concern and a later "open gap"; both were errors and are dropped entirely.)
Tip decisions — the tipper owns when/what to tip. Populator only generates the tipper.
5. What "axes decided" does and does not cover
Covered: which six dials exist; diet+disposition coupled.
NOT covered (next work): the range and distribution shape for each axis — what fraction trend vs mean-revert vs noise vs herd, how capital spreads, how many agents are panic-prone, etc. Per "fix the basis, tune the number later": fix each distribution's basis (what it's anchored to, what breaks if too high/low), leave exact numbers as prototype-tuned dials. This is the population-generation step (Step 2 below).
6. Populator design plan — the STEPS (dependency-ordered)

Step 1 — Agent representation. What an agent is concretely: the six axis-values + runtime state (position, running P&L, lifecycle state, capital handle). Includes how an axis-value is stored (number / distribution draw / category). Substrate everything else reads → first.

Step 2 — Generation step. How the generator samples one agent across the six axes, including the diet+disposition coupling rule (approach a) and how capital ties to the Layer 5 seam. Then: producing a whole population (count + per-axis distribution basis, not tuned numbers).

Step 3 — Tipper + followers. How the tipper is generated and independently decides/emits a tip (direction/conviction/target/stop). How followers are generated and wired to receive it. The friction stack (attention delay incl. never-notice, trust/act-probability, conviction/size, execution delay + market/limit, unsynchronized exit) lives here, reusing axes from Step 1. The parked "who emits the tip / on what schedule" question closes in this step (prior non-binding lean: manual-operator for the prototype so scenarios are controllable).

Step 4 — Lifecycle state machine. Entry / active / done / re-entry states and triggers (time, P&L target, loss limit, random clock). Operates on the Step-1 agent.

Step 5 — Scheduler. How active agents get ticked to produce orders and how those orders reach the engine's Admission Gate. The runtime loop; needs 1 + 2 + 4.

Step 6 — Finite attention/capital enforcement. Where each agent's attention/capital budget lives and how it's checked. Small at 1–2 instruments; mechanism specified here. May merge into Step 5.

Dependency logic: 1 = substrate; 2 builds populations of 1; 3 = specialized case of 2; 4 & 6 add state/rules to 1; 5 = runtime driving 1+2+4 into the engine (near-last); 6 may fold into 5.

7. Carried-forward open/deferred items (unchanged from LAYER_2_AGENTS.md, still live)
Deferred endogenous signals: order-flow/tape reading, volatility-as-direct-signal, own-position signals, round-number/level psychology.
Deferred exogenous: news/sentiment (PARK-2, needs LLM/news layer); fundamental is static/slow-drift without it.
Deferred herding sources: emergent-guru, crowd-flow, cluster/Cont-Bouchaud, threshold-cascade (operator-tip chosen for prototype; no emergent mechanism added on top).
Deferred axis: time-horizon/frequency (independent; not lifecycle-dependent).
Deferred layers: LLM institutional hierarchy; adaptation/learning (genetic-algorithm strategy switching). Slot onto the same order-submission seam.
Instrument selection: two-stage attention layer = candidate path (not locked); prototype builds the degenerate/fixed case. Finite attention/capital per agent = LOCKED (one principle of several, not the last).
8. Status summary (this document's additions)
Item	Status
Three-stage chain: perceive (diet) → decide (disposition) → execute (skill)	Clarified / stable
Skill = execution quality (unintended decision-noise); NOT win-rate	Decided (definition)
Six-axis set: diet, disposition, skill, aggressiveness, capital/size, P&L-reactivity	Decided (prototype)
Diet + disposition sampled coupled (approach a)	Decided (prototype)
Capital/size retained (Layer 5 Gate 4 wiring)	Decided (prototype)
Time-horizon/frequency	Deferred (independent of lifecycle)
Populator responsibilities (generate / tipper+followers / lifecycle / schedule / finite-budget)	Decided (scope)
Populator boundaries (money=L5, match=engine, personality=instrument, tip-decisions=tipper)	Decided
Generic population + agent self-selection (NOT per-instrument generation)	Decided (prototype)
Per-axis distribution ranges/shapes	Open — next work (Step 2)
Design plan Steps 1–6	Agreed sequence

End of populator design notes (handoff, pre-lock). Next thread starts at Step 1 — agent representation.