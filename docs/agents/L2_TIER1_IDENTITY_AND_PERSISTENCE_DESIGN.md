Layer 2 — Tier 1: Identity & Persistence (Design)

What this doc is. The design of everything L2 stores and how: the durable agent-identity store (Postgres agents table, including the full frozen trait vector), the per-session runtime store (Redis), the market-data cache, the capital model, and the on-demand lease model. It answers what persists, where, keyed how, and with what shape — not the bring-up flow that fills these stores (that is Tier 2) nor the runtime loop that reads them (Tier 3/4). Read L2_OVERVIEW_AND_TOPOLOGY first.

Grounding. The store split and ownership come from the locked ownership spine (overview §3). The agents schema and capital/lease models are the locked Tier-1 decisions (project decision log). The trait vector is the consolidated agent schema (LAYER_2_AGENT_SCHEMA §1) plus the Tier-4 trait additions (LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION, LAYER_2_TIER4_FRAMING, and the 4b/4d decision-log entries) — included here because Tier 1 is what persists them, even though Tier 4 consumes them. Distribution families are locked; parameter values are calibration.

Scope. In: the three stores, the agents DDL + full traits JSONB, the Redis key set, the capital model, the lease model. Out: generation/mint flow (Tier 2 — 2a), snapshot-write timing & reload execution (Tier 2 — 2b/2c, Tier 6), the trait draw mechanics (Tier 2 generation + the old POPULATOR/SCHEMA math), scenario trait-shifting (L2_SCENARIOS_DESIGN).

1. The three-way store split
Stratum	Store	Holds	Lifetime
Durable identity	L2 Postgres	agent identity + frozen trait vector + reuse keys + capital handle + session capital (canonical)	forever; survives process restart and Redis flush
Per-session runtime	L2 Redis	presence, lifecycle timers, decision/position working memory, tip-state, session capital (on-demand), lease state, active-session registry	per-session; survives L2 process restart; rebuildable/reconcilable on Redis flush
Market-data cache	L2 Redis	OHLCV window + latest quote/depth	per-exchange; broker-owned, re-fetchable
Never stored	—	positions, cash, P&L, trades, order ids	re-read fresh from broker every use

Two consequences restated from the ownership spine because they shape this whole tier: there is no trade-log / P&L table in L2 (broker-owned; the old LAYER_2_ORCHESTRATOR trade-log-in-L2 design is superseded), and positions are never snapshotted (re-read fresh on restart).

2. Durable store — the agents table (Postgres)

One table, both populations, discriminated by population. The durable identity shape is identical for canonical and on-demand; only the reuse mechanism differs (canonical: churn + revive; on-demand: lease), and lease state is runtime, not durable.

sql
CREATE TABLE agents (
    agent_id          TEXT        PRIMARY KEY,          -- L2's stable id; also = economy owner_id
    population        TEXT        NOT NULL CHECK (population IN ('canonical','ondemand')),
    country           TEXT        NOT NULL,             -- keys canonical reuse AND the on-demand pool
    capital_handle    TEXT        NOT NULL,             -- economy account_id handed back at account-open (opaque to L2)
    trading_capital   BIGINT,                           -- A, the session money (§4).
                                                        --   canonical: set at mint, evolves with realized P&L.
                                                        --   on-demand: NULL (drawn fresh per-session, Redis-only).
    traits            JSONB       NOT NULL,             -- the FROZEN trait vector — full contents in §3.
    traits_version    INT         NOT NULL DEFAULT 1,   -- bumped only by a future adaptation pass (never the hot path)
    traits_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    origin            TEXT        NOT NULL CHECK (origin IN ('boot','mint')),  -- provenance
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at        TIMESTAMPTZ                        -- NULL = live member; set = RETIRED (for_good; never revived)
);

-- the canonical reuse / on-demand pool query hits exactly this:
CREATE INDEX idx_agents_reuse ON agents (country, population) WHERE retired_at IS NULL;

Design rationale (locked):

agent_id = economy owner_id. The economy account links back via owner_type='agent', owner_id=agent_id (UNIQUE on the economy side). capital_handle holds the returned account_id, opaque to L2. These two ids are the only economy fields L2 touches — L2's table is shaped around L2's own jobs, never around the economy accounts table.
trading_capital is a typed column, not inside traits — it is numeric, bounded, changes often (canonical), and is the one field L2 might ever validate on. Everything frozen lives in traits; this mutable money is a column. (Full semantics in §4.)
traits is JSONB, not typed columns — it is read as a whole when an agent is loaded to run; L2 never filters agents by an individual trait value (reuse keys on country, not on skill/disposition). The vector is kind-dependent and sparse (herd lacks disposition/diet/perception_profile; endogenous lacks act_prob), so typed columns would be half-null. And the Tier-4 expansion added many fields with no migration — the whole reason JSONB was chosen.
"Frozen" = write-once at mint, rewritten only by a future adaptation pass, never by the trading hot path. traits_version/traits_updated_at exist so that when that adaptation tier eventually runs, a changed vector is traceable and reproducible. Immutability on the hot path is enforced by L2 code, not a DB constraint.
retired_at is the only lifecycle field in the durable row: NULL = live, set = RETIRED (the for_good removal; never revived). All other presence lives in Redis runtime.
Per-population semantics of the row (important)

Both populations store the full trait vector and identity. What differs is how the row is used:

Canonical: the row is the agent's real, persistent identity — reused and revived across sessions, its trading_capital persisted and evolving with realized P&L. is_tipper (if set) is static here.
On-demand: the pool's durable rows exist so the agent's economy account is reusable (avoiding account-open). Per the scenario design, on-demand leased agents reuse the account but redraw their traits fresh each session (so a scenario shapes the whole population — see L2_SCENARIOS_DESIGN). Consequence: an on-demand row's stored traits are written at mint but not read back on reuse — accepted dead weight, kept (by decision) so a future "persistent on-demand identity" capability isn't foreclosed. trading_capital is NULL (drawn fresh each session, Redis-only). The on-demand tipper is assigned at runtime, not stored here.

This refines the original Tier-1/2 lease lock (which reused stored traits). The refinement is scenario-driven and documented in L2_SCENARIOS_DESIGN.

3. The frozen trait vector (contents of traits JSONB)

Grouped by who carries it — the vector is kind-dependent. The Tier-4 perception/exit expansion is endogenous-only; herd agents stay lean (LAYER_2_TIER4_FRAMING §8.4). Distribution families are locked (Beta / log-normal / Dirichlet / Bernoulli); values are calibration.

Universal — every agent, both kinds (LAYER_2_AGENT_SCHEMA §1):

agent_kind      : "endogenous" | "herd"
skill           : [0,1]
aggressiveness  : [0,1]
capital_size    : >0     -- the frozen SCALE (whale↔minnow). The session money A is trading_capital (§4), NOT here.
pnl_reactivity  : [0,1]
p_act           : per-tick act probability

Endogenous-only (herd omit all of these):

disposition     : {trend, mean_rev, noise}  (Dirichlet simplex, sums to 1; Option A = deterministic blend)
diet            : 5 bool flags {chart, order_book, mean_reversion, volume, time_of_session}, coupled from disposition

-- Tier-4 latents (LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION §2) — frozen, stored:
rho             : [0,1]    -- reactivity latent (calm↔jumpy); drives most perception fields
phi             : [-1,+1]  -- optimism latent (bear↔bull); drives bias

-- Tier-4 perception_profile — nested object, a block PER ON diet-channel only:
perception_profile: {
    <method-forked channel (chart/range)>: { method, reading_params{…}, reading_cutoffs{…} },  -- the CHOSEN method's block
    <fixed-calc channel (liquidity/volume/time)>: { … its params … },
    bias              : { per directional field },        -- derived from phi
    channel_weights   : { per trait → weights },          -- Dirichlet (see open item, §7)
    reread_sensitivity: { per ON channel },               -- 4a re-perceive gate dial (market-units)
    decide_sensitivity: { per consumed directional field }-- 4b re-decide gate dial (reading-units); distinct from reread_sensitivity
}

-- Tier-4 exit/position traits (4d; parallel to perception_profile):
exit_profile    : { mode: distance|level|hybrid, stop_pct, target_pct, stop_buffer, target_buffer }
max_hold        : ticks
-- theta_add     : RESERVED — 4d add-gate dial; NOT designed (blocked on theta_act, a 4b-2 item). Not populated yet. (§7)

Herd-only:

act_prob        : [0,1]   -- tip credulity; NOT derived from other axes

Canonical-only flag:

is_tipper       : bool    -- set on exactly one ENDOGENOUS agent per country; canonical only.
                          --   On-demand assigns its tipper at 2b RUNTIME (Redis), NOT in durable traits.

Precision notes (so the shape isn't mis-built):

perception_profile sub-shape is method-dependent — a chart block holds the chosen method's params (MA / structure / RSI each differ); only ON diet channels get a block (OFF channels absent, not null). This variability is exactly why JSONB is right here.
rho/phi are stored even though runtime reads the derived fields — they are the frozen latents (kept for reproducibility and as the hook a future adaptation pass re-derives from). Cheap (two floats).
The Tier-4 runtime working-memory (position_context, standing_opinion, …) is NOT in traits — it is per-session Redis state (§5). Only frozen traits persist here.
4. The capital model

Two genuinely separate numbers:

capital_size — a frozen trait (in traits), the agent's persistent scale (log-normal draw; whale↔minnow). Frozen on purpose: a whale stays a whale by nature even after a losing streak shrinks its actual stack.
trading_capital (A) — a mutable typed column, the agent's actual money this session, that the agent sizes against.

Provisioning = CAP-TO-A (locked). The broker's account-open (EnsureAccount(ownerType, ownerID)) takes no capital amount — starting capital is a broker-side economy-config dial. So every account is funded to the same broker figure funded_cash; L2 draws each agent's A ≤ funded_cash internally, the agent self-sizes to A, and the broker's Gate 4 @ funded_cash is a loose backstop.

The draw (locked): A = clamp(capital_size × jitter, floor, funded_cash). funded_cash comes from the account-open response; fallback is default_funded_cash (config) when the response omits it. Draw order: account-open THEN A (A can't be bounded until funded_cash is known). The clamp/jitter formula detail is generation-internal (Tier 2 — 2a).

Three cases at resolution time:

NEW agent → draw A ≤ funded_cash (from its account-open response).
ON-DEMAND existing (leased) → draw fresh A ≤ default_funded_cash (it skipped account-open this session).
CANONICAL existing (revived) → keep the persisted, P&L-evolved A; no draw.

Evolution & persistence:

Canonical: A is persisted in agents.trading_capital, set at mint, and evolves session-to-session with realized P&L (a session-end fold reads realized P&L from the broker and updates A — the fold mechanism is a later tier; the field supports it).
On-demand: A is drawn fresh each session, lives in Redis runtime only, never persisted → trading_capital is NULL in Postgres for on-demand rows.

The session's A is also mirrored into the per-agent Redis runtime key (§5) — the sole home for on-demand, and a no-Postgres-read convenience for the canonical goroutine.

5. Per-session runtime store (Redis)

All per-session, mutable, rebuildable state. No frozen traits here (those are read from Postgres / the handed-over Population). No positions/cash/P&L (read fresh from broker). Key set:

5.1 Per-agent runtime key
runtime:{exchange_id}:agent:{agent_id} = {
    -- session identity values (written at bring-up, 2b/2c):
    trading_capital,        -- A for this session (on-demand: sole home; canonical: convenience copy)
    is_tipper,              -- ON-DEMAND only (per-session tipper); canonical's is_tipper is in Postgres traits

    -- presence / lifecycle (Tier-1 + 3b):
    presence,               -- DORMANT | ACTIVE | IDLE | DONE | RETIRED
    entry_time,             -- [0,1] normalized over [start,end]; one-shot DORMANT→ACTIVE trigger (stamped in 2c)
    next_check_time,        -- staggered 20–40 min lifecycle check (3b, Clock Model A)
    idle_since,             -- idle_duration clock for the re-entry roll

    -- 4b decision working memory:
    standing_opinion,       -- current directional opinion, carried between ticks
    standing_stance,        -- FLAT_IDLE | WAIT | HOLDING
    wait_streak,            -- consecutive ticks in WAIT (forces re-decide past a global threshold)
    last_decided_readings,  -- consumed directional-field values at last decision (the decide_sensitivity baseline)

    -- 4d position/exit working memory (present only while holding):
    position_context: { entry_dir, entry_channel, entry_tick },  -- written at entry by whoever opened/added; read by 4d

    -- tip-following state — HERD agents only:
    last_acted_tip_id,      -- acts once per tip (id, not bool)
    tracking: { tracking_tip_id, target, notice_at },            -- the one tip mid-"noticing"
    my_exit_level,          -- tip target + exit_offset (mechanical upside exit)
    submit_at               -- pending execution-delay stamp, if mid-act
}

Herd vs endogenous: endogenous carry the 4b/4d blocks and no tip-state; herd carry the tip-state block and none of the 4b/4d decision memory. trading_capital + presence are common.

5.2 Other runtime keys
runtime:{exchange_id}:tipslot          -- shared active-tip slot (per exchange): { tip_id, direction, conviction, target, stop_loss }
marketdata:{exchange_id}:{instrument}:candles:{tf}  -- 1-min OHLCV window (only tf=1m populated now); append/range
marketdata:{exchange_id}:{instrument}:quote         -- latest quote/depth/price; overwrite, last-writer-wins
lease:{country}:available              -- SET of free on-demand agent_ids (§6)
lease:{country}:leased                 -- HASH agent_id → exchange_id (who holds each busy one) (§6)
sessions:active                        -- HASH exchange_id → { country, session_type, instruments, start, end, broker_id }
                                       --   (so a restarted orchestrator knows which goroutines to respawn and with what params)

Keying note (isolation for free): market-data keyed by exchange_id gives canonical/on-demand isolation automatically — an on-demand instance has its own exchange_id, so its live tape never collides with canonical's. For on-demand, history is seeded from the canonical market's candles (broker never persists on-demand candles) while live comes from the on-demand instance; both cached under the on-demand exchange_id namespace.

Redis-flush consequence (accepted): canonical market data is re-pullable from the broker's persisted candles; an on-demand session's live tape is lost & unrecoverable on a flush (throwaway session, acceptable). Runtime per-agent state survives a process restart (it's in Redis); on a Redis flush it is rebuilt/reconciled (execution = Tier 6).

6. The on-demand lease model

The on-demand pool for a country is "all on-demand agents ever built for that country" — the durable agents rows with population='ondemand'. That is membership: large, durable, Postgres. At any moment some are busy (running inside a live on-demand session) and the rest free. The lease is the free/busy bookkeeping, so two concurrent on-demand sessions never grab the same agent (which would hit the broker's ErrSessionAlreadyLive backstop).

Why Redis, not Postgres: busy/free flips many times a day and is pure transient state — if L2 restarts, "busy" must be recomputed anyway (the goroutines holding agents are gone). Membership stays durable (Postgres); only availability is runtime (Redis).

The two keys (locked):

lease:{country}:available   -- Redis SET of currently-free agent_ids   (select N = pop from this set)
lease:{country}:leased      -- Redis HASH agent_id → exchange_id        (who holds each busy one; the teardown/reclaim lookup)

The model answers two questions fast and survives restart: "give me N free agents, mark them busy" (session start) and "these are done, mark them free" (session end). available is a SET because the core op is "take N free agents" (a native pop). leased is a HASH so teardown and crash-reclaim know which session held each agent.

Operation ownership across tiers (design-level here; mechanics elsewhere):

Select + enlist (lease N at session start; enlist newly-minted on-demand agents) — Tier 2 (2a select, 2b enlist).
Release (return a session's agents to available, after the broker has discarded the session) — Tier 6. Ordering is load-bearing: broker session_discard first, then release, else the next lease's session-load hits ErrSessionAlreadyLive.
Reconcile (after an L2 restart: leased lists agents as busy whose goroutines are gone → return orphans to available; also rebuild available after a Redis flush as "durable members − legitimately-still-leased") — Tier 6.
7. Open / flagged items (not buildable as-is)
theta_add — reserved key in traits; the 4d add-gate dial, not designed (blocked on theta_act, a Tier-4 4b-2 item). Do not invent its structure.
perception_profile.channel_weights contents — the mechanism (Dirichlet per trait) is set, but the channel→trait map (which channels each disposition trait consumes) is config still owed (LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION §9/§12). channel_weights can't be finalized without it.
On-demand stored-traits dead weight — kept by decision (keeps the persistent-on-demand-identity door open); revisit if/when that capability is actually wanted.
All distribution parameter values — calibration, never fixed in design.

End of Tier 1 design. Next: L2_TIER2_BRINGUP_DESIGN.