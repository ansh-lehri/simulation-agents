Layer 2 — Tier 2: Bring-up (Design)

What this doc is. The design of how a session comes alive: from the BRING_UP trigger to "agents live on the broker, market data flowing, scheduler running." It is three sub-steps — 2a (trigger → generation → account-open), 2b (persist → snapshot → spawn the goroutine), 2c (the goroutine's pre-open run → go-live). It covers both populations (canonical / on-demand), deltas marked. Read L2_OVERVIEW_AND_TOPOLOGY and L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN first — this doc fills the Tier-1 stores and hands off to Tier 3.

Grounding. All locked in the project decision log (the 2a/2b/2c entries, the account-open contract, the pending_mint WAL, the capital-resolution and market-data entries) and the cross-layer boundary rule. Scenario-specific additions to bring-up (the optional scenario trigger field, curve_eff carry-through) are noted here and designed in L2_SCENARIOS_DESIGN.

Scope. In: the trigger, the orchestrator spine, both generation paths, account-open (L2's side only), the WAL, capital resolution, persist/snapshot/spawn, the handover, the three pre-open ops (L2's side), go-live. Out: the trait draw internals (generation mechanics live in the Tier-2 impl doc + the old POPULATOR/SCHEMA math), the tick loop (Tier 3), the decision procedure (Tier 4), teardown/release/resilience execution (Tier 6).

1. The two actors (the frame everything hangs on)

Bring-up has two actors, separated in time by the 2b goroutine spawn:

The orchestrator — runs 2a and 2b. Does generation, account-open, all durable + Redis writes, and finally spawns the per-exchange goroutine.
The per-exchange goroutine (runAgentPopulation) — created at the end of 2b. From spawn onward it owns the running session: it runs 2c (pre-open), then the Tier-3 tick loop. One goroutine per exchange_id.

This separation is why some things computed early (in 2a/2b) must be carried across the spawn to be used later by the goroutine (see §6, and the scenario curve_eff in §8).

2. The whole shape
Core ──BRING_UP(exchange_id, session_type, country, start, end, instruments, broker_id[, scenario])──► L2 (ASYNC)
   │
   │  HandleBringUp: validate → run 2a spine → RETURN once account-open done ──► Core unblocked
   │                                                      │
   │                                              go continueBringUp (2b, async tail)
   ▼ 2a — ORCHESTRATOR                                    ▼ 2b — ORCHESTRATOR (all L2-internal, no Core call)
   STAGE A assemble identities                           P2 persist NEW agents rows (+ HDEL pending_mint)
     canonical: reuse-query → existing ; mint → new      P3 thin DORMANT snapshot per agent
     on-demand: lease-select → existing ; mint → new     P4 (on-demand) HSET leased new ; assign runtime tipper
   STAGE B bulk account-open (NEW only) → handles+funded  P5 spawn goroutine, register sessions:active,
   STAGE C resolve session capital (A)                        hand over Population + lifecycle channel
                                                              │
                                                              ▼ 2c — GOROUTINE (runAgentPopulation)
                                                         1 stamp entry_time (L2-internal)
                                                         2 CONCURRENTLY: session-load · history pull · WS subscribe  (Core)
                                                         3 JOIN all three
                                                         4 go-live at start_time → start Tier-3 tick loop

Async contract: BRING_UP validates, runs the 2a spine synchronously, and returns once account-open (Stage B) completes (accounts now exist); 2b/2c run as an async tail inside L2, readiness surfaced via L7's poll-status path. (This supersedes the older "BRING_UP is SYNC / blocks till populated" lock.)

Trigger inputs consumed vs carried (2a): country keys generation; session_type dispatches the path; exchange_id tags the set (becomes the Redis/market-data namespace); start/end are scheduler-clock inputs (carried, used in 2c/3b, not 2a); instruments carried for 2c feed subscription (not consumed by generation — population is generic); broker_id reserved (multi-broker), carried. scenario (optional, on-demand only) → the scenario layer (L2_SCENARIOS_DESIGN). N (population size) is an L2-owned generation param, NOT a trigger input.

3. 2a — Trigger → Generation → Account-open

Boundary: from receiving BRING_UP to a fully-assembled Population{existing, new} with every agent carrying a capital handle and a session trading_capital. An orchestrator spine sequences three stages over one shared Population record; stage order is forced by data dependency (A makes identities → B needs identities to open accounts and yields funded_cash → C needs funded_cash to bound A).

Stage A — assemble identities (the {existing, new} split)

Two paths by session_type; each returns agents tagged existing (already have a durable row + account) or new (freshly minted, need an account).

Canonical — revive-first, then mint: read the country's live members (SELECT … WHERE country=? AND population='canonical' AND retired_at IS NULL) → these are existing (revived; already carry capital_handle + persisted, P&L-evolved A; they skip account-open). Mint the shortfall N − |existing| → new. Exactly one canonical agent carries the static is_tipper (promoted if no revived agent has it).
On-demand — lease-then-mint: lease-select up to N from lease:{country}:available (atomic pop + mark leased so a concurrent on-demand session can't double-lease), load those durable rows → existing (they carry an account; per the scenario design their traits are redrawn fresh this session rather than reused — see L2_SCENARIOS_DESIGN; their A is drawn fresh in Stage C). Mint the shortfall → new. New on-demand agents are enlisted into leased after account-open (2b). No tipper here — on-demand tipper is a 2b runtime assignment.

Mint draws the frozen trait vector + an agent_id (the draw mechanics are generation-internal — impl doc + the old POPULATOR/SCHEMA math). capital_size (the frozen scale) is drawn here; the session money A is Stage C (needs funded_cash).

The pending_mint WAL (crash-recovery, locked amendment). At mint, before account-open, each new agent is staged to a Redis hash pending_mint (agent_id → {traits, population, country, exchange_id}). It is cleared (HDEL) in 2b after the durable row is written. Because staging lands before account-open, no Core account is ever orphaned — every opened account already has a recoverable pending_mint record. The only residual window is mint → HSET (in-memory, a clean loss of a few un-opened agents). trading_capital is not staged (recovery redraws it; a crashed bring-up never traded). Recovery contract: drain pending_mint at L2 startup before serving new BRING_UPs (re-open idempotently, re-persist idempotently by PK, HDEL) — execution is Tier 6.

Stage B — account-open (bulk, new agents only)

One bulk network call L2 → Core, for the new set only (revived/leased-existing already hold their handle). Per the cross-layer boundary rule, L2 specs only its two sides:

Request L2 sends: session_type/mode + a list of {owner_type: agent, owner_id: agent_id}, one per new agent. Thin — capital is broker-side; nothing else crosses.
Response L2 expects: per agent {owner_id, account_id, funded_cash?}.
What L2 does with it: set capital_handle = account_id; carry funded_cash (or default_funded_cash if absent) into Stage C.

How Core batches/fulfills it is Core's business. (EnsureAccount is get-or-create → re-sends are safe; a network bulk variant is a Core-side dependency.)

Stage C — resolve session capital (A)

For each agent, produce trading_capital A per the capital model (Tier 1 §4): new → draw A ≤ funded_cash; on-demand existing → draw fresh A ≤ default_funded_cash (skipped account-open); canonical existing → keep persisted A. Draw order is account-open THEN A.

Output of 2a: Population{existing, new}, every agent carrying capital_handle + session A, tagged to exchange_id → handed to 2b.

4. 2b — Persist → Snapshot → Spawn

Runs as the async tail continueBringUp (orchestrator-owned, off the BRING_UP return path). Entirely L2-internal — no Core/broker/L7 call. Fork(a) (locked): the orchestrator performs all 2b writes, then spawns the goroutine as the final handoff; the goroutine wakes already-persisted + snapshotted and enters 2c. Steps:

P2 — persist + WAL clear. Insert the new agents' durable agents rows (canonical: trading_capital persisted; on-demand: NULL). HDEL pending_mint after each row is durable (so a crash between persist and HDEL is safe — recovery re-persists idempotently by PK then clears). Existing agents are already durable.
P3 — thin initial snapshot. Write a per-agent Redis runtime snapshot carrying just {presence=DORMANT, trading_capital=A}, for existing + new alike. entry_time is deliberately NOT written here — the goroutine stamps it in 2c (entry-curve is lifecycle, owned by the goroutine). The snapshot is thin by design.
P4 — on-demand only: enlist + tipper. HSET leased the new on-demand agents (they were minted after lease-select, so weren't in it; they'll be released at teardown, Tier 6). Assign exactly one agent of the on-demand population a runtime is_tipper flag in its snapshot (per-session; canonical's was set static in 2a). Canonical sessions skip P4.
P5 — spawn + handover. Create the lifecycle→orchestrator channel; register the session in sessions:active (so a restart knows which goroutines to respawn and with what params); spawn one runAgentPopulation goroutine per exchange_id, handing it the in-memory Population (traits = the "specs") + the channel. The goroutine owns the session from here.
5. 2c — The goroutine's pre-open run → go-live

The body of runAgentPopulation. Sequence:

Stamp entry_time (L2-internal, the goroutine's first act). Draw each agent's entry_time from the inhomogeneous-Poisson entry curve over [start, end] (LAYER_2_LIFECYCLE §4) → write into the runtime snapshot (so a restart doesn't redraw it and change the entry pattern).
Three pre-open ops, run CONCURRENTLY (all via o.core, L2's network client to Core; each specs L2's side only):
Session-load — one bulk L2→Core call (StartSession → session_load → install per agent); Mode = Canonical | OnDemand (on-demand sets PersistOnClose=false broker-side). Loads fine during PhaseClosed (punches just bounce at the exchange phase gate until open). L2 stores nothing from the response but per-agent success/fail — participant_id stays broker/L7-side (the broker resolves the bound participant itself at order time; L2 identifies an agent by its capital_handle).
History pull — GET /candles per instrument, tf=1m, over the history window → cache in Redis (marketdata:{exchange_id}:{instrument}:candles:1m). On-demand: history is pulled from the canonical market's candles (the broker never persists on-demand candles); live comes from the on-demand instance.
WS subscribe — one WS per exchange_id, level full, all instruments → cache in Redis (snapshot-on-subscribe, then a long-lived ingest goroutine appends candles / overwrites quote for the session's life). L2 does no rollup — 1-min only; serving any timeframe is the broker's job.
JOIN all three. Nothing goes live until all three succeed.
Go-live at start_time (wall-clock). Start the Tier-3 scheduler/tick loop (loop body = Tier 3). Agents remain DORMANT through all of 2c — none are flipped ACTIVE here; they activate at their own entry_time inside the Tier-3 loop.
6. The handover contract (orchestrator → goroutine)

What crosses the 2b spawn into the goroutine:

the in-memory Population (each agent carries its frozen traits = its "specs", its capital_handle, its session A);
the lifecycle→orchestrator channel (the goroutine's lifecycle decides presence transitions; the orchestrator writes the durable ones — the DECIDE/WRITE split; see Tier-3 3b for which transitions go over the channel vs are written inline);
the sessions:active record (durable-ish session params: country, session_type, instruments, start, end, broker_id — plus the scenario curve_eff, §8), which also lets a restart respawn the goroutine with its params.

The goroutine needs no Postgres read to run: identity/traits arrive in Population; runtime lives in Redis; positions/P&L are read fresh from the broker.

7. Failure policy (locked)
Validation failure (bad trigger) → return before anything is created.
Generation / account-open failure (2a) → surface; nothing is spawned.
2b failure before spawn → failBringUp; nothing left live (abort-cleanup mechanics = Tier 6).
2c join: history or WS failure is fatal (agents with no data are useless → fail the bring-up); partial session-load failure proceeds with the installed agents (the population is statistical). All failures surface via L7's poll-status path.
8. Scenario carry-through (brief; full design in L2_SCENARIOS_DESIGN)

A scenario (on-demand only) shifts generation params and lifecycle-curve params. The generation half (θ_eff) is consumed in 2a (it shapes what Stage A draws). The lifecycle half (curve_eff) is consumed later, in 3b, by the goroutine when it builds the presence curves — so curve_eff rides the 2b handover (in sessions:active, so a restart rebuilds scenario-shaped curves, not baseline ones). All scenario calculation happens once, in 2a; curve_eff is merely carried. This is the only part of the scenario machinery that reaches past 2a into the running session.

9. What 2a–2c hand to Tier 3, and what's carried to Tier 6

To Tier 3: a live goroutine owning the population, all agents DORMANT with entry_time stamped, market data flowing into Redis, sessions installed on the broker, scheduler loop started. Tier 3 runs the tick (3a action phase + 3b presence/lifecycle). Agents are identified to the broker at order time by capital_handle.

Carried to Tier 6 (named here, executed there): the pending_mint drain execution; failBringUp abort-cleanup; lease release (release-after-discard ordering); lease reconcile on restart; respawn-on-restart semantics; the canonical P&L→A fold at session end.

End of Tier 2 design. Next: L2_SCENARIOS_DESIGN.