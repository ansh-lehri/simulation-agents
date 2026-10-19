Layer 2 — Lifecycle Design Notes (Handoff, Pre-Lock)

Continues from LAYER_2_ORCHESTRATOR.md. That file settled the orchestrator's five responsibilities, the execution model (records + scheduler, not threads), trait vectorization, persistence/identity, and the generator's four actions (CREATE, ACTIVATE, REMOVE(kind), REVIVE-load). This file captures the lifecycle piece — the presence decider: the state machine and all its transitions, designed in this thread.

Status convention unchanged: nothing is a final locked approach unless marked LOCKED. "Decided (prototype)" = the prototype uses this; still open to revision later.

No-LLM constraint unchanged. All transitions here are mechanical presence only — traits + shaped randomness. No P&L-reading, no market-reading (those are internals, deferred).

1. What lifecycle is

Responsibility: presence — given a population that exists, decide who is participating now vs waiting vs done, and the rhythm of churn. Lifecycle DECIDES; the generator WRITES (locked seam from LAYER_2_ORCHESTRATOR.md). Lifecycle never mutates agent state directly — it calls generator actions (ACTIVATE, REMOVE(kind)) and signals CREATE.

Long-term direction (recorded, not prototype): the transition decisions should eventually be made by the agent itself (from its characteristics/strategy), with lifecycle reading those emitted intents and doing the mechanical processing (generator calls, floor/ceiling). Prototype: lifecycle decides. Design lifecycle as a processor of transition requests so "lifecycle decided it" and "agent requested it" are interchangeable inputs later — no rewrite needed. (Principle only; not built now.)

2. The presence state machine

States:

DORMANT — exists, has not entered this session yet (fresh from CREATE/REVIVE, awaiting entry).
ACTIVE — participating; in the set the scheduler ticks.
IDLE — stepped out, waiting; keeps its position; may re-enter this session.
DONE — finished for the session; keeps position; no re-entry this session; revived next session.
RETIRED — quit for good; existence torn down (terminal).

Transitions (all designed in this thread):

#	Transition	Trigger	Generator action
1	DORMANT → ACTIVE	entry time reached (entry curve)	ACTIVATE
2	ACTIVE → IDLE	step-out roll	REMOVE(step_out)
3	IDLE → ACTIVE	re-entry roll	ACTIVATE
4a	ACTIVE → DONE	done roll (close-heavy)	REMOVE(done_for_session)
4b	IDLE → DONE	idle-too-long give-up roll	REMOVE(done_for_session)
5	(mid-session join)	PARKED — not built now	(CREATE)
6	ACTIVE/IDLE → RETIRED	end-of-session quit roll	REMOVE(for_good)

DONE is terminal for the session — only → RETIRED (via #6) or next-session revive. No DONE → ACTIVE.

3. The clock model — LOCKED

Model A: lifecycle checks ride the scheduler tick. One clock, no separate lifecycle timer (avoids the concurrency/races the single-loop model exists to prevent; preserves seeded reproducibility).

The tick = one step of the scheduler loop = the simulator's heartbeat / unit of simulated time. (Exact real-duration of a tick and which agents act per tick = scheduler design, deferred.)
Per-agent checks: each agent carries its own next-check time, rolled independently as now + random(20,40 min) → staggered, uncoordinated presence changes. The tick loop fires an agent's lifecycle check when its scheduled check time is crossed.
Interval-driven only (prototype): all presence transitions evaluate on the agent's 20–40 min check. Nothing fires mid-interval. Target-price/stop-loss hooks exist (an agent may carry them) but no agent uses them in the prototype → no event-driven immediate exits. (When enabled later, event-driven triggers are a second path — deferred.)
4. Transition #1 — DORMANT → ACTIVE (first entry)

Mechanism: entry time assigned per agent at bring-up (not at creation — entry timing is per-session runtime state, not a trait; applies identically to revived and freshly-created agents). Lifecycle flips DORMANT → ACTIVE when the agent's entry time is reached (gated by the active ceiling, §9).

Entry-time distribution = inhomogeneous Poisson process on normalized session time t ∈ [0,1] (0 = open, 1 = close), rate function:

$$\lambda(t) = \lambda_{\text{base}}(t)\cdot e^{\,g(t)},\qquad \lambda_{\text{base}}(t) = A\cdot e^{-t/\tau}$$
A = height (cancels under fixed-N normalization — not tuned; = 1).
τ = base decay: intensity highest at open, decaying through session. Main shape dial.
g(t) = Gaussian process (log-Gaussian Cox process), RBF kernel:
$$k(t,t') = \sigma^2 \exp\!\left(-\frac{(t-t')^2}{2\ell^2}\right)$$
σ² = bump height (roughness amplitude), ℓ = bump width/count.
e^{g(t)} keeps λ(t) positive; g(t) wanders around 0, drawn fresh each session (seeded) → unscripted local peaks that differ per session.

Why GP: produces organic, per-session random local peaks (not hand-placed), overall peaked at open via the base. This was chosen over cruder random bumps for fidelity.

Sampling — thinning: propose candidate times at λ_max; keep each with probability λ(t)/λ_max. Fixed-N reconciliation: the process only decides when, not how many — draw exactly N entry times from the normalized shape of λ(t) (treat λ(t)/∫λ as a density, sample N by thinning). N stays fixed (existence ceiling untouched).

Output: one entry_time ∈ [0,1] per agent (mapped to real session time), stored as disposable per-session runtime state (not persisted).

Defaults: τ = 0.35 (≈6% of open intensity by close); A = 1 (not tuned); ℓ = 0.15 (~4–6 local peaks); σ² = 0.25 (σ≈0.5, bumps ~0.6×–1.65× trend). Reasoned starting points, tuned by watching.

Note (recorded): the close-side pickup source (held-back late entries vs re-entry churn) was flagged for revisit after #3; not resolved — left as-is.

5. Transition #2 — ACTIVE → IDLE (step-out)

Scope: mechanical presence only. Driven by a shaped departure curve + trait factor. P&L-driven leaving is routed to DONE, not step-out.

Two-part structure: step-out probability = (population-level curve at now) × (per-agent trait factor). Curve sets timing; traits pick who.

Step-out curve (temporary wandering — low at open, ramps to a plateau through the session; not close-spiking):

$$\lambda_{\text{out}}(t) = s\left(1 - e^{-t/\tau_{\text{out}}}\right)\cdot e^{\,g_{\text{out}}(t)}$$
s = plateau height (overall step-out rate ceiling).
τ_out = ramp speed to plateau.
g_out(t) = its own GP (RBF, σ²_out, ℓ_out), independent of entry's.
Built once per session at bring-up (seeded); checks read it.

Trait factor f_i — per-agent multiplier, flat (=1) initially (all coefficients 0 → anyone steps out uniformly at the curve rate). Eventual drivers (direction only, tuned later):

disposition — noise-weight ↑ → f_i ↑ (flighty); trend/mean-revert ↑ → f_i ↓ (committed).
agent_kind — herd steps out more between tips (a dial).
frequency — deferred axis; disposition-noise proxies it now, becomes primary driver when frequency returns.
(skill, aggressiveness, capital, P&L-reactivity do not feed step-out.)

Per-check probability (Δ = check interval, converts rate → per-check prob):

$$p_i(t) = \text{clamp}\big(\lambda_{\text{out}}(t)\cdot f_i \cdot \Delta,\ 0,\ 1\big)$$

Procedure (ACTIVE agent, on its check):

t   = current normalized session time
p_i = clamp(λ_out(t) · f_i · Δ, 0, 1)
u   = Uniform(0,1)  [seeded]
if u < p_i:  REMOVE(step_out)     # ACTIVE → IDLE, keeps position
else:        stay ACTIVE
next check = now + random(20,40 min)

Output nature: live per-check yes/no roll (NOT a pre-assigned time — deliberate contrast with entry, because an ACTIVE agent's state is live). (Option A, chosen over pre-scheduling.)

Defaults: s low (most active agents stay active per check); τ_out = 0.2; σ²_out = 0.25; ℓ_out = 0.15; f_i = 1 (flat).

6. Transition #3 — IDLE → ACTIVE (re-entry)

Driver: time-since-idle (not session-time). Break length has its own clock, independent of where in the session it happens.

Input: d = idle_duration (how long the agent has been idle).

Return-probability curve — Option 2 (peak at a typical break-length):

$$r(d) = p_{\max}\cdot \exp\!\left(-\frac{(d - \mu_{\text{break}})^2}{2\,\sigma_{\text{break}}^2}\right)$$
μ_break = typical break length (peak location).
σ_break = spread of break lengths.
p_max = peak per-check return probability at d = μ_break.

Low when just idled, highest near μ_break, low again if very long. Typical break length emerges from re-rolling this peaked curve each check (no central picker).

Trait factor: p_i = clamp(r(d) · f_i, 0, 1), f_i = 1 flat initially (same trait direction as step-out later).

Procedure (IDLE agent, on its check):

d   = current idle_duration
p_i = clamp(r(d) · f_i, 0, 1)
u   = Uniform(0,1)  [seeded]
if u < p_i:  ACTIVATE             # IDLE → ACTIVE, resumes with kept position
else:        stay IDLE, next check in random(20,40 min)

Output nature: live per-check yes/no roll.

Defaults: μ_break = 20 min; σ_break = 8 min; p_max = 0.7; f_i = 1.

Recorded note (interaction): check cadence (20–40 min) is the same order as μ_break (20 min), so actual breaks run somewhat longer than μ_break due to check timing. Two options flagged (accept the widening, or finer re-entry cadence) — not decided, left as-is.

7. Transition #4 — ACTIVE → DONE and IDLE → DONE

Scope: mechanical only. Drivers: (1) time-of-session close-heavy curve, (4) idle-too-long soft conversion, (2) fatigue optional. P&L-driven DONE deferred to internals — confirmed to land on this transition when built.

4a — ACTIVE → DONE (time-of-session, close-heavy)

Curve — mirror of entry: near 0 at open, rising to peak at close:

$$\lambda_{\text{done}}(t) = D\cdot e^{-(1-t)/\tau_{\text{done}}}\cdot e^{\,g_{\text{done}}(t)}$$
τ_done = how sharply DONE ramps toward close; D = height; g_done(t) = its own GP.

Optional fatigue (cumulative active time a_i): fatigue_i = 1 + c_fat · a_i, with c_fat = 0 initially (off/flat), tuned up later.

Per-check: p_done_i(t) = clamp(λ_done(t) · fatigue_i · f_i · Δ, 0, 1), f_i = 1 flat.

ACTIVE agent, on check:
  p = clamp(λ_done(t) · fatigue_i · f_i · Δ, 0, 1)
  u = Uniform(0,1)
  if u < p:  REMOVE(done_for_session)   # ACTIVE → DONE, keeps position
  else:      stay ACTIVE
4b — IDLE → DONE (idle-too-long, soft)

Complement of re-entry: 0 until idle passes a normal break, then rises toward giving up:

$$q(d) = q_{\max}\cdot\Big(1 - e^{-(d - \mu_{\text{break}})/\tau_{\text{giveup}}}\Big)\quad \text{for } d > \mu_{\text{break}},\ \text{else } 0$$
q_max = max per-check give-up prob; τ_giveup = how fast give-up rises past μ_break.
IDLE agent, on check (after the re-entry roll #3, if it did not return):
  q = clamp(q(idle_duration) · f_i, 0, 1)
  u = Uniform(0,1)
  if u < q:  REMOVE(done_for_session)   # IDLE → DONE, keeps position
  else:      stay IDLE

So an idle agent each check rolls re-entry first, then (if not returned) rolls give-up. Early: re-entry dominates, give-up ≈ 0. Long idle: re-entry fades, give-up rises → long-tail idlers convert to DONE. Closes the re-entry long-tail overlap.

Output nature: live per-check yes/no rolls (both 4a and 4b).

Defaults: τ_done = 0.25; σ²_done = 0.25; ℓ_done = 0.15; D low; c_fat = 0 (off); q_max ≈ 0.8; τ_giveup ≈ 30 min; f_i = 1.

8. Transition #5 — mid-session join — PARKED

Same action as bring-up CREATE (generator makes a new DORMANT agent), but triggered by lifecycle mid-session rather than by the orchestrator at boot. Determined not needed for the prototype: staggered entry (the entry curve) already produces within-session arrival (a late entry_time = "showed up at 11am"); genuine mid-session population growth beyond N is the only thing it adds, and existence is capped at N. Cross-session new-blood can enter at bring-up instead. PARKED — revisit only if a need surfaces. (Generator retains the mint capability; lifecycle simply does not drive it in the prototype.)

9. Transition #6 — ACTIVE/IDLE → RETIRED (quit for good) — LOCKED

Very slow, population-level, end-of-session. Quitting the market for good is multi-day-rare (~once per 3–4 sessions across the whole population). Evaluated once per session, at end-of-session — not a per-check roll. At session close agents are flat (squared off), so no flatten step is needed before REMOVE(for_good).

Per-agent end-of-session quit probability:

$$p^{\text{quit}}_i = \text{clamp}\!\left(\frac{E_{\text{quit}}}{P}\cdot f^{\text{quit}}_i,\ 0,\ 1\right)$$
E_quit = target expected quitters per session (population-wide).
P = current population size → per-agent base = E_quit/P.
f^quit_i = quit factor, flat (=1) initially; later reads the cross-session trade log (persistent losers / losing streaks quit more), normalized so population-average f^quit ≈ 1 (keeps E_quit as the true expected rate).

Key property: expected total quitters = P × (E_quit/P) = E_quit, independent of P.

Procedure (once, at session close, all agents flat):

for each agent i in population:
    if population_count > X:                       # floor guard
        p = clamp((E_quit / P) · f_quit_i, 0, 1)
        u = Uniform(0,1)  [seeded]
        if u < p:  REMOVE(for_good)                # close L5, mark RETIRED, retain log
    else:
        skip                                        # at floor, no quit

Defaults: E_quit = 0.3 (≈one quit every 3–4 sessions); f^quit_i = 1 (flat); floor X = deferred.

10. What remains in lifecycle (open)
X ≤ active ≤ N balancing (floor/ceiling maintenance) — NOT designed. Deferred cross-state coordination. All inputs now exist: entry inflow (#1), step-out/done outflow (#2/#4), the k·N active ceiling gate at ACTIVATE, the floor X, and pool changes from quit (#6) / parked join (#5). The coordination logic (what happens at the ceiling — defer activations; near the floor — suppress step-out/done or force re-entry) is unspecified. This is the remaining substantial lifecycle stage.
The lifecycle check-loop driver (how agents' checks are walked/fired relative to the scheduler tick) — sketched (Model A, §3); the formal runtime procedure overlaps the scheduler piece (deferred).

Note on population bounds levels: #5/#6 change population (existence, pool size); #1–4 change active count within the pool. Both feed X ≤ active ≤ N, at different levels.

11. Status summary (this document)
Item	Status
Presence state machine (DORMANT/ACTIVE/IDLE/DONE/RETIRED)	Decided (prototype)
Lifecycle DECIDES, generator WRITES	LOCKED (carried)
Clock model A (checks ride tick; per-agent 20–40 min; interval-driven only)	LOCKED
Long-term: agent-emitted intents, lifecycle as processor	Recorded (principle, deferred)
#1 entry — inhomogeneous Poisson, base A·e^{-t/τ} × GP, thinning, fixed-N	Locked
#2 step-out — ramp-plateau curve × GP × trait factor; live roll; mechanical only	Locked
#3 re-entry — time-since-idle, Gaussian peak at μ_break; live roll	Locked
#4a done — close-heavy curve × GP (× optional fatigue); live roll	Locked
#4b idle→done — soft give-up past μ_break; live roll	Locked
#5 mid-session join	PARKED
#6 quit-for-good — end-of-session, E_quit/P × factor, floor-guarded	Locked
Trait factors f_i everywhere	Flat (=1) initially; directions recorded; tuned later
P&L-driven DONE	Deferred to internals (lands on #4)
X ≤ active ≤ N balancing	Open — remaining lifecycle stage
Lifecycle check-loop driver	Overlaps scheduler (deferred)

End of lifecycle design notes (handoff, pre-lock). Remaining: the X ≤ active ≤ N balancing (cross-state), then the scheduler piece, then Step 3 (tipper + followers)