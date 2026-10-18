# Simulation-Agents (Layer 2) — Project Constitution
<!-- Root CLAUDE.md: loaded every session. Universal rules only; component detail
     lives in each package's own CLAUDE.md. Target < 200 lines. -->

Go implementation of **Layer 2 — the AI-agent system**: it generates a population of
heuristic trading agents, stands them up on the market, and runs them through a session
so a realistic, diverse crowd trades against humans. Realism target is **psychological**
(does trading against this crowd feel like real, diverse people), NOT price-series
replication — the instruments are fictional, there is no real tape to match. **No LLMs
in agents, ever** — sophisticated heuristics only.

The `docs/agents/` design + implementation docs are the spec. This repo is a **separate
process** from Core; it speaks to Core only over the network.

## Stack
- Go 1.27.1 (version per `go.mod`). Logging: standard `log/slog`.
- Postgres via `jackc/pgx/v5` (`pgxpool`); Redis via `redis/go-redis/v9`.
- Property tests: `pgregory.net/rapid`. Format/lint: `gofmt`, `go vet`, `golangci-lint`.

## Commands
- Build: `go build ./...`
- Test (standing gate): `go test ./... -race`
- Lint: `go vet ./...` && `golangci-lint run`

## Process topology (LOCKED — do not re-litigate)
- Two processes: **this** (the L2 agent process) and **Core** (conductor + broker + economy +
  exchange, all one process). Every L2↔Core interaction is a **network call** through the
  one client `o.core`. Whether Core routes a request to conductor or the broker is Core's
  business, invisible to L2.
- L2 can crash independently of Core. Core keeps agents' live sessions/positions alive
  while L2 restarts — which is why L2 keeps a Redis runtime snapshot + a restart-reload
  path. (Spec: `docs/agents/L2_OVERVIEW_AND_TOPOLOGY.md` §2.)

## The ownership spine (LOCKED — the single most load-bearing fact)
| Data | Owner | L2 stores it? |
|---|---|---|
| Durable agent identity (traits, provenance, capital handle) | **L2** | Yes — Postgres |
| Per-session runtime (presence, timers, working memory, tip-state, lease) | **L2** | Yes — Redis (rebuildable) |
| Market data (OHLCV, quote/depth) | **broker** | Cached in Redis, re-fetchable |
| Positions / cash / P&L / trades | **broker** | **NEVER** — read fresh per use |
- **There is NO trade-log / P&L table in L2.** The old `LAYER_2_ORCHESTRATOR`
  trade-log-in-L2 design is **superseded**. **Positions are never snapshotted** — re-read
  fresh from the broker on restart.

## Standing rules (LOCKED — enforced in every lower doc)
- **Cross-layer boundary rule.** For any L2→Core call, L2 specs ONLY its own two sides —
  the request it sends, the response it expects — never how Core fulfills it. Never design
  the broker's/economy's internals.
- **Generic population.** Agents are generated generic, NOT per-instrument — they
  self-select instruments later. The trigger's `instruments` list is carried, not consumed
  by generation.
- **Reproducibility.** Every random draw uses a seeded RNG. **Live = fresh randomness;
  tuning/calibration = fixed seed.** No seed is ever derived from an identifier like
  `exchange_id`.
- **Mechanism vs calibration.** The docs design the rule *shape*; the actual numbers
  (distribution params, thresholds, scenario intensities) are **calibration dials**, never
  fixed in code.
- **DECIDE / WRITE split.** Lifecycle *decides* presence transitions; the orchestrator
  *writes* durable state. Refinement: Redis runtime → the exchange goroutine writes
  directly; Postgres → only via the orchestrator.
- **No-LLM.** Composed heuristics, risk management, P&L reaction — yes. Reasoning /
  planning / interpreting-meaning — no.

## Value discipline (correctness, not style)
- All money is integer coins (`int64`); all prices integer ticks (`int64`). **Never
  float** — floats cross the wire only as display percentages (`change_pct`, `pnl_pct`).
- Amount/quantity arguments are strictly positive; direction is carried by which call and
  which field, never by a negative argument.

## Errors vs panics
- **Invariant violation → panic** (dev-fatal): a broken coupling invariant, missing
  hot-path state, a guard failing after the caller already proved the precondition.
- **Expected/business failure → typed error** to the caller: account-open failure,
  lease-exhausted, a Core call returning a reject.
- **Never partially mutate:** validate fully before the first write; on any error, state
  is untouched.

## Logging
- Structured only (`log/slog`). Log at **boundaries** — the inbound BringUp/WindDown seam,
  each Core call, generation start/end, persist, snapshot, spawn, go-live, teardown.
- **No secret** (DSN, URL, password) in any log line — log host/db/pool only.
- Boundary logs carry correlation ids where they apply: `ExchangeID`, `AgentID`,
  `Country`, `SessionType`.

## The spec is the authority
- The component's `docs/agents/` file **is** the specification. Implement what it says; do NOT
  invent behavior it doesn't state.
- Decisions marked **LOCKED** are not reopened. Anything crossing a defined interface (the
  Core API contract, the store schemas, the seam) is **verified against the doc/contract,
  never reconstructed.** If something looks wrong or missing, **stop and ask** — don't
  silently fix it. (Project rule: clear doubts, don't assume.)

## Testing policy
- Tests ship **in the same stage** as the code they cover.
- A property test for every invariant the doc lists; a golden/table test for each
  deterministic behavior; table-driven units for pure functions.
- Assertions trace to the spec, never to the code's own output.
- `go test ./... -race` is a standing gate; add a determinism check (fixed seed → run
  twice → identical output) for every seeded/stateful part.

## Workflow (every session)
1. **Orientation first, no code:** read the component's doc(s) + its `CLAUDE.md`,
   summarize what it owns, propose a file layout + test plan. **Make no changes.**
2. Implement **one stage, then stop and report.** No unrequested refactors; never reach
   into another package.
3. Human reviews the `git diff` (code *and* tests) before every commit.

## Structure
- One package per component. `docs/agents/` holds the specs (not inside packages). Nested
  CLAUDE.md: root (this) → package → sub-package.
- Packages: `connectivity/` (config + db, vendored from the sibling, not installable),
  `transport/` (HTTPS server + inbound BringUp/WindDown seam), `core/` (the `o.core`
  client to Core), `agent/` (Agent + TraitVector records), `store/` (Postgres + Redis
  persistence), `orchestrator/` (bring-up + its `generator/` sub-package), `scenario/`
  (param/lifecycle manipulation), `cmd/agentsd/` (composition root).

## Doc map (the spec for each package — read the right one in orientation)
- orientation (everyone) → `docs/agents/L2_OVERVIEW_AND_TOPOLOGY.md`
- `transport` + `core` → `docs/agents/L2_TIER0_CORE_API_CONTRACT.md` (seam = §4)
- `store` → `L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN.md` + `L2_TIER1_IMPLEMENTATION_DESIGN.md`
- `orchestrator` → `L2_TIER2_BRINGUP_DESIGN.md` + `L2_TIER2_IMPLEMENTATION_DESIGN.md`
- `scenario` → `L2_SCENARIOS_DESIGN.md` + `L2_SCENARIOS_IMPLEMENTATION_DESIGN.md`
- generation internals (trait draw) → `LAYER_2_POPULATOR.md`, `LAYER_2_AGENT_SCHEMA.md`,
  `LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md`, `LAYER_2_TIER4_FRAMING.md`
<!-- Old six L2 docs (LAYER_2_AGENTS etc.) = authority for the WHY and the math only;
     this doc set supersedes them on signatures/persistence/infra. -->