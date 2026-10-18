Layer 2 — Overview & Topology (Implementation Reference, Orientation Doc)

What this doc is. The first doc a developer reads before implementing Layer 2 (the AI-agent system). It orients: what L2 is, how its process sits relative to the rest of the simulator, the stores it owns, the two session populations, the tier map, and the standing rules that govern every lower doc. It does not contain the detailed designs — those are in the tier docs this one points to. Everything here is locked (from the project decision log) unless explicitly marked OPEN.

Relationship to the older L2 docs. The original six L2 docs (LAYER_2_AGENTS, LAYER_2_POPULATOR, LAYER_2_ORCHESTRATOR, LAYER_2_LIFECYCLE, LAYER_2_SCHEDULER, LAYER_2_TIPPER_FOLLOWERS) plus LAYER_2_AGENT_SCHEMA are the principle / crux layer — they define the axes, the lifecycle math, the population-generator philosophy, the tipper mechanics. They remain the authority for the why and for the underlying math. But their function signatures, persistence model, and infra assumptions are stale where this doc set says so — much changed once the broker, economy, exchange, and L7 became real and L2 was split into its own process. Rule for the developer: for mechanism/philosophy → the old docs; for what to actually build → this doc set, which supersedes them on specifics and cites them for grounding.

1. What Layer 2 is

L2 is the AI-agent system: it generates a population of heuristic trading agents, stands them up on the market, and runs them through a session so that a realistic, diverse crowd trades against (and alongside) humans. Its realism target is psychological — does trading against this crowd feel like real, diverse people — not price-series replication (the instruments are fictional; there is no real tape to match).

L2 is not a set of agent "types." It is a population generator over axes of human variation — each agent is a point sampled from distributions over traits (disposition, skill, capital, aggressiveness, reactivity, …). Richness is an aggregate property of the population spread, not of any single agent; every agent is a cheap heuristic (runs thousands of times × ~22,500 ticks/session). No LLMs anywhere in the agents.

2. Process topology (LOCKED — do not re-litigate)

There are two processes / servers:

The L2 agent process — standalone. Owns agent generation, identity, persistence, the per-session runtime, and the goroutines that run live sessions.
The Core process — L7 + broker + economy + exchange, all in one process together. Their internal calls are in-process Go calls.

L2 ↔ Core = network calls. From L2's side, every interaction with L7 / broker / economy / exchange is just an API call to Core. Whether Core routes a given request internally through L7 or the broker is Core's business, invisible to L2.

Consequences that ripple through the whole design:

L2 can crash independently of Core. Core keeps running (holding agents' live broker sessions, positions, reservations) while L2 restarts. This is why L2 keeps a Redis runtime snapshot and a restart-reload path — so a restarted L2 re-attaches to broker sessions that never died.
The cross-layer boundary rule (below) applies to every L2→Core call.

Historical note for the reader: an older broker doc states "the whole sim is one process (incl. L2)." That line is stale only as to L2 — L7/broker/economy/exchange genuinely are one in-process unit; L2 was split out later. Trust this doc on the topology.

(The exact nature of L2's network surface is Tier-0 and out of scope for this doc set.)

3. The ownership spine (LOCKED — who owns which data)

This table is the single most load-bearing fact in L2. Every storage and read decision descends from it.

Data	Owner	L2 stores it?	How L2 gets it
Durable agent identity (traits, provenance, capital handle)	L2	Yes — L2 Postgres	written at mint
Per-session runtime (presence, timers, decision/position working memory, tip-state, lease state, session's trading-capital, market-data window)	L2	Yes — L2 Redis (snapshot, rebuildable)	written during the session
Market data (OHLCV candles, quote/depth)	broker (market-data)	Cached — L2 Redis	pulled (history) + streamed (live), re-fetchable
Own positions / cash / P&L / trades	broker	No — never stored by L2	read fresh from broker, per use

Two corollaries the developer must internalize:

The per-agent trade log is NOT in L2. The old LAYER_2_ORCHESTRATOR design put it in L2's durable record; that is superseded. P&L and trade history are broker-owned and read fresh. L2 persists no performance/P&L data.
Positions are never snapshotted by L2. On restart, re-read them fresh from the broker.
4. The two populations (LOCKED)

L2 serves two session types. They share ~90% of the pipeline; the deltas are called out everywhere in the tier docs.

Canonical — the real, evolving market.

Generated once ever per country, then evolved via churn across sessions.
Agents persist: durable identity in L2 Postgres, reused and revived session to session.
trading_capital (the agent's session money) persists and evolves with the agent's realized P&L across sessions.
One static tipper per country.

On-demand — a throwaway practice/teaching session, a full copy of the canonical market (same instruments/rules), its own exchange instance.

Draws its population from a per-country inventory/pool of pre-built on-demand agents + mints any shortfall.
Reuses the economy account (to skip account-open); redraws traits fresh each session. (This is the scenario-driven refinement — see §6 and the scenario doc.)
Persists nothing durable about the session itself: PersistOnClose=false (the economy's session-type gate) suppresses all trade/cost/balance writeback. trading_capital is drawn fresh each session, Redis-only, never persisted.
Tipper assigned per session at runtime (not static).
Its live market data lives under its own exchange-id namespace, never colliding with canonical's. History seed may be read from canonical's immutable past; live bars are the on-demand instance's own.
5. The tier map (what each tier is; this doc set covers Tiers 1–2 fully)

L2's design is split into dependency-ordered tiers:

Tier 1 — Identity & Persistence. The stores: durable agents table (Postgres), per-session runtime (Redis), market-data cache (Redis), the capital model, the on-demand lease model. Fully documented here (design + impl).
Tier 2 — Bring-up. Trigger → generation → account-open (2a); persist → snapshot → spawn the goroutine (2b); the goroutine's pre-open run → go-live (2c). Fully documented here (design + impl).
Tier 3 — Runtime loop. The per-exchange goroutine's post-go-live tick loop: the ACTION phase (3a) and the PRESENCE/lifecycle phase (3b). Downstream — referenced here as the consumer of what Tier 2 hands over; designed in its own docs.
Tier 4 — Agent internals. The decision procedure (decide()) the tick loop calls: perception (4a), decision/mixer (4b), execution (4c), position/exit management (4d), P&L-reactivity (4e), tipper content (4f). Downstream — referenced here as the consumer of the traits Tier 1/2 produce.
Tier 5 — Herding. Tipper + follower friction stack. Downstream.
Tier 6 — Teardown & resilience. Session-end, lease release, cross-layer crash recovery, the pending_mint drain execution, failBringUp abort cleanup, respawn-on-restart. All L2 resilience is consolidated here; Tier 1/2 define the mechanisms and name what they hand to Tier 6.

Scope rule for THIS doc set (important). Tiers 1 and 2 are the base that Tiers 3–6 build on. "Fully documented" therefore means: anything Tier 1/2 produce, draw, persist, hand over, or pass down through the generator — is in scope here, even when the eventual consumer lives in Tier 3 or Tier 4. Concretely, the complete frozen trait vector (including the Tier-4 perception_profile / exit_profile / rho / phi / max_hold fields), the effective scenario params carried to 3b (curve_eff), and the per-agent runtime working-memory slots are all documented here, because Tier 1/2 are what create and carry them. Tier 3/4 are named only as "this is where X gets consumed."

6. The scenario layer (LOCKED concept; on-demand only)

On-demand sessions can be scenario-based: the user picks a named preset (e.g. "thin liquidity," "calm," "busy") and the market must behave that way — without scripting it. The founding principle (LAYER_2_AGENTS §11, LAYER_2_TIER4_FRAMING §5) is emergence, not inducement: no if scenario == X branch forces market conditions.

The mechanism is parameter-level: a scenario is a sparse override that shifts the population-generation dials and the lifecycle-curve dials, then the generator draws a differently-shaped crowd from the shifted dials — and the requested market character emerges from who is in the room. No agent ever knows its scenario; it only feels the conditions the crowd produces. Because on-demand redraws traits fresh, the scenario reaches the whole population.

The scenario layer is a full sub-system (registry, a link-space math transform, a merge engine, two plug-points into generation and the lifecycle curves). It is documented in its own design + impl docs. It touches the schema (the rho/phi latents) and reopens one Tier-1 lock (on-demand leased agents redraw traits rather than reuse stored ones).

7. Standing rules (LOCKED — enforced in every lower doc)
Cross-layer boundary rule. For any L2→Core call, L2 specifies only its own two sides — the request it sends and the response it expects — never how Core fulfills it. No designing the broker's/economy's internals.
Generic population. Agents are generated generic, not per-instrument — they self-select instruments later. The instruments list from the trigger is carried, not consumed by generation.
Reproducibility. Every random draw uses a seeded RNG. Live = fresh randomness; tuning/calibration = fixed seed. No seed is derived from identifiers like exchange_id.
Mechanism vs calibration. The docs design the rule shape; the actual numbers (distribution parameters, thresholds, scenario intensities) are calibration dials, never fixed in design.
DECIDE / WRITE split. Lifecycle decides presence transitions; the orchestrator writes durable state. Refined by the Tier-3 write-target rule: Redis runtime → the exchange goroutine writes directly; Postgres → only via the orchestrator.
No-LLM. Agents are sophisticated heuristics. "Intelligent, not LLM": composed heuristics, risk management, P&L reaction — yes; reasoning/planning/interpreting-meaning — no.
8. Reading guide (the rest of this doc set)

Design docs (the "what" and "why"):

L2_OVERVIEW_AND_TOPOLOGY (this doc) — orientation.
L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN — stores, the full agents schema (both stores, full trait vector), capital model, lease model.
L2_TIER2_BRINGUP_DESIGN — 2a/2b/2c end to end: trigger, generation, account-open, persist, snapshot, goroutine spawn + handover, pre-open ops, go-live.
L2_SCENARIOS_DESIGN — the scenario sub-system: concept, the surfaces, the link-space math, the registry.

Implementation docs (the "how to build it," point-by-point, with structures, formulae, pseudocode, decisions — same format as the BROKER_*_IMPLEMENTATION_DESIGN docs): 5. L2_TIER1_IMPLEMENTATION_DESIGN 6. L2_TIER2_IMPLEMENTATION_DESIGN 7. L2_SCENARIOS_IMPLEMENTATION_DESIGN

A developer implementing bring-up reads 1 → 2 → 3 → 4 for understanding, then 5 → 6 → 7 to build, cross-referencing the old L2 docs for the underlying math/philosophy.

9. Open / not-yet-designed items referenced by this set (so nothing is mistaken for buildable)

These are flagged wherever they appear; collected here so the developer knows what is not ready:

theta_add (an agent add-gate dial) — reserved in the schema, not designed (blocked on theta_act, a Tier-4 4b-2 item).
The channel→trait map (which market channels each disposition trait consumes) — the mechanism around it is set, but the per-trait channel lists are config still owed; perception_profile.channel_weights contents can't be finalized without it.
Scenario calibration values — every scenario intensity and target threshold is a calibration slot, fit later; none are fixed in design.
The s-fitting / session-statistics measurement tool — owed at calibration time; the automated version is future Monte-Carlo work.
All resilience execution (pending_mint drain, failBringUp cleanup, lease reconcile, respawn) — mechanisms defined in Tier 1/2, execution owned by Tier 6.

End of overview. Next: L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN.