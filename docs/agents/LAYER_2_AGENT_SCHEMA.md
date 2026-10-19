Layer 2 — Consolidated Agent Schema (Reference)

Purpose: a single, current, field-by-field picture of what an agent is, pulled together from the six Layer 2 docs (LAYER_2_AGENTS.md, LAYER_2_POPULATOR.md, LAYER_2_ORCHESTRATOR.md, LAYER_2_LIFECYCLE.md, LAYER_2_SCHEDULER.md, LAYER_2_TIPPER_FOLLOWERS.md). The infra is complete for the prototype and each doc added a piece of the agent; this file assembles them so the internals work starts against one clean definition. Nothing here is new — it restates what those docs locked, with the source noted. Where a field's meaning/derivation is still internals, it says so.

The one organizing distinction (LOCKED, LAYER_2_ORCHESTRATOR.md):

Frozen trait vector — drawn once at CREATE, immutable, persisted.
Durable record — trait vector + identity + trade log + capital handle; persisted across sessions (canonical) or thrown away (on-demand).
Disposable per-session runtime state — presence, timers, position context, tip-state; not persisted, reset each session.
1. Frozen trait vector (drawn once at CREATE, immutable, persisted)

Source: LAYER_2_ORCHESTRATOR.md §4 (the locked vectorization table) + LAYER_2_TIPPER_FOLLOWERS.md (the act_prob addition).

Field	Type	Draw recipe	Applies to
agent_id	stable unique id	assigned at CREATE	all
agent_kind {endogenous, herd}	enum	weighted coin(p_herd) — herd rare	all
disposition {trend, mean_rev, noise}	3-simplex (sums to 1)	Dirichlet(a_trend, a_mr, a_noise)	endogenous only
diet (5 bool flags)	flags	coupled to disposition (mandatory forced true; optional = coin each)	endogenous only
skill	[0,1]	Beta(α,β)	all
aggressiveness	[0,1]	Beta(α,β)	all
capital_size	>0	Log-normal(μ,σ)	all
pnl_reactivity	[0,1]	Beta(α,β)	all
p_act	prob per tick	draw from a slow↔fast shape (once at CREATE)	all
act_prob (credulity)	[0,1]	dedicated dial — NOT derived from other axes	herd only
is_tipper	bool flag	generator sets on exactly one agent	all (one true)

Kind is a discriminant (LOCKED): herd agents carry no disposition, no diet — they read only the tip. disposition/diet exist for endogenous agents only. act_prob exists for herd agents only. The tipper is an ordinary endogenous agent with is_tipper = true.

Population-level (not per-agent): N (population size — static configurable). ~13 generation parameters define the whole population character: p_herd, (a_trend, a_mr, a_noise), (α,β)×3 for the Betas, (μ,σ) for capital, diet optional-flag probabilities. Distribution families are locked; parameter values are fit in calibration (seed → generate → run → measure stylized facts → adjust → repeat).

Draw ordering at CREATE (LOCKED, reproducibility-driven): kind first (gates the next two + keeps the RNG sequence stable) → if endogenous: disposition then diet-coupled-from-disposition → skill, aggressiveness, capital_size, pnl_reactivity → assemble frozen vector + assign id. p_act and (for herd) act_prob drawn in this same CREATE pass.

2. Durable record (persisted; the unit of cross-session identity)

Source: LAYER_2_ORCHESTRATOR.md §5. Persisted for the canonical population (generated once ever, then evolved via churn); on-demand populations draw fresh and are never persisted/revived.

agent_id — stable, unique.
frozen trait vector (§1) — drawn once, immutable.
performance record = per-agent trade log — per trade: trade_id, P/L amount, P/L %. This is the only thing P&L-reactivity reads. The exact derived view reactivity consumes (window / cumulative / streak) is internals — pinned later.
provenance — created-at, boot-vs-mint.
capital handle — a reference into Layer 5 (the money lives in L5; the agent holds the handle, never the balance).

Not persisted: positions (never), and all per-session runtime state (§3). Restart = REVIVE (load the durable record, no RNG, no new id/account) — never a seeded re-CREATE. REVIVE skips RETIRED records (for-good never returns).

3. Disposable per-session runtime state (NOT persisted; reset each session)

Assembled from lifecycle, scheduler, and tipper/follower docs. This is the part §1b of the plan left "to fall out at its consumers" — the consumers now exist, so it's enumerated here.

3.1 Presence (source: LAYER_2_LIFECYCLE.md)
presence state ∈ {DORMANT, ACTIVE, IDLE, DONE, RETIRED}.
DORMANT = exists, not yet entered. ACTIVE = participating; in the set the scheduler ticks. IDLE = stepped out, keeps position, may re-enter. DONE = finished for the session, keeps position, no re-entry this session, revived next session. RETIRED = quit for good, existence torn down (terminal).
entry_time ∈ [0,1] — assigned per agent at bring-up (per-session runtime, not a trait; applies identically to revived and fresh agents). Drives DORMANT → ACTIVE.
next_check_time — the agent's own staggered lifecycle-check time (rolled now + random(20–40 min)). Drives when presence transitions are evaluated (the scheduler's presence phase).
idle_duration / time-since-idle — clock for the re-entry (#3) roll (break length has its own clock, independent of session time).
3.2 Position context (source: LAYER_2_AGENTS.md §runtime; consumed by internals)
current position(s) — held across IDLE/DONE within a session; never persisted; flattened only by auto-square-off at close.
own-position signals (held-too-long, too-concentrated) — named; their consumption is internals (deferred).
3.3 Trading cadence (source: LAYER_2_SCHEDULER.md)
No stored field beyond p_act (a trait) — the act decision is a per-tick coin-flip (u < p_act), no persisted cadence state. (If exact regular intervals were ever wanted, a next_act_tick would live here — not used in the prototype; the coin-flip is stateless.)
3.4 Tip-following state (source: LAYER_2_TIPPER_FOLLOWERS.md; herd agents only)
last_acted_tip_id — which tip this agent has already acted on (acts once per tip; distinguishes a superseding new tip from the same one — must be an id, not a boolean).
currently-tracking {tracking_tip_id, target, notice_at} — the one tip it is mid-"noticing"; notice_at = now + notice_delay (per-agent draw from a wide, long-tailed distribution). One tracked at a time.
exit_offset / my_exit_level — per entered tip: my_exit_level = target + exit_offset (offset drawn near-symmetric around the target, tunable slight-below skew). Drives the mechanical upside exit.
submit_at — per act: now + small random execution delay (few seconds), before the order reaches Gate 4.
4. Shared orchestrator-level state (not per-agent)

Source: LAYER_2_TIPPER_FOLLOWERS.md.

active-tip slot — holds the current/latest tip (one at a time; a new emission supersedes). All active herd agents pull from it on their own ticks. Seam: one slot now (one tipper) → "the slots of subscribed tippers" later, no rework.

The tip object in the slot: {tip_id, direction (buy for prototype), conviction 0–1 (carried, unused), target (price), stop_loss (optional, ignored in prototype)}. Structure locked; value population = tipper internals (deferred).

5. How the pieces connect at runtime (one active endogenous agent, one tick)
scheduler tick (1s):
  ACTION phase (seeded-shuffle order, live book):
    for each ACTIVE agent:
      flip p_act → if acts: decision procedure [INTERNALS] → order
        → L5 Gate 4 (afford? reserve) → Admission Gate → engine → book updates
  PRESENCE phase (end of tick, housekeeping):
    for each agent whose next_check_time is due:
      lifecycle check → maybe transition (calls generator ACTIVATE / REMOVE(kind))
      roll next_check_time (+20–40 min)

Herd agent variation in the ACTION phase: instead of an endogenous decision procedure, it reads the active-tip slot → attention (notice-delay) → trust (act_prob coin) → size (f(aggressiveness) × affordable capital) → execution (delay + market/limit) → exit at my_exit_level. (Full path: LAYER_2_TIPPER_FOLLOWERS.md §4.4.)

6. What is defined vs what is still internals

Fully defined (this schema): identity, all frozen traits + their draws, the durable record + trade log, all per-session runtime fields (presence, timers, tip-state, offsets), the shared tip slot, and how they're read/written by generator / lifecycle / scheduler / tipper-follower.

Still internals (deferred — the next major body):

the endogenous decision procedure (the black box the scheduler calls): disposition consumed as mixing weights [approach A], order sizing, exit / position-management, downside/panic exit, P&L-reactivity direction (panic vs tilt), and the derived view of the trade log reactivity reads (window/cumulative/streak).
tipper tip-content (what values go in each tip field, from the tipper's own view).
stop_loss usage, downside/panic herd exit, endogenous tip-subscribers.
consumption of own-position signals (held-too-long, too-concentrated).

Parked (infra, not built for prototype): X ≤ active ≤ N balancing; concrete persistence mechanism + cross-layer crash recovery.

End of consolidated agent schema. This is a reference assembled from the six Layer 2 docs — not a new design. Use it as the single agent definition when starting the internals (decision-procedure) work.