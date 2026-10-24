# Running this service

This repo builds two binaries:

- **`cmd/agentsd`** — the long-running daemon. Serves the inbound HTTP seam
  (`/bringup`, `/winddown`, `/healthz`) and makes outbound calls to the Core
  process.
- **`cmd/migrate`** — a one-shot binary that applies pending Postgres
  migrations and exits. Deliberately separate from the daemon: schema changes
  are an explicit step you run, never something that happens silently on boot.

## Prerequisites

- Go, version per `go.mod` (`go 1.27.1`).
- A Postgres database you control (a dedicated one — migrations create the
  `agents` table there).
- A Redis instance you control.
- A reachable Core process (HTTP + WebSocket) — the daemon makes outbound
  calls to it, but does **not** ping it at startup, unlike Postgres/Redis
  below. A misconfigured or unreachable Core only surfaces when the first
  bring-up actually tries to call it, not at boot.

Both Postgres and Redis **do** get pinged at startup (bounded by
`DB_PING_TIMEOUT`, default 5s) — the daemon fails fast rather than booting
against a database it can't actually reach.

## 1. Apply database migrations (first run, and after any schema change)

```bash
export DATABASE_URL="postgres://user:pass@host:5432/dbname?sslmode=disable"
export REDIS_URL="redis://host:6379/0"
go run ./cmd/migrate
```

This runs `store/migrations/0001_agents.up.sql` (the `agents` table + its
partial reuse index). Note: `cmd/migrate`'s own doc comment mentions
`POSTGRES_DSN` — that's stale; the real variable it reads (via
`connectivity/config.Load()`) is `DATABASE_URL`, same as the daemon.

## 2. Environment variables for the daemon

### Required — Postgres / Redis (no default; `connectivity/config.Load()`)

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | Full pgx DSN, e.g. `postgres://user:pass@host:5432/db?sslmode=disable` |
| `REDIS_URL` | Full go-redis URL, e.g. `redis://:pass@host:6379/0` |

### Optional — Postgres / Redis tuning

| Variable | Default | Meaning |
|---|---|---|
| `DB_PING_TIMEOUT` | `5s` | Startup ping deadline for both Postgres and Redis |
| `PG_MAX_CONNS` / `PG_MIN_CONNS` | `0` (defer to DSN's `pool_max_conns`/`pool_min_conns` or library default) | Postgres pool size override |
| `REDIS_POOL_SIZE` | `0` (library default) | Redis pool size override |

### Required — Core client (no default on purpose; `cmd/agentsd/coreconfig.go`)

| Variable | Meaning |
|---|---|
| `AGENTSD_CORE_BASE_URL` | Core's HTTP origin, e.g. `http://core-host:9000` |
| `AGENTSD_CORE_WEBSOCKET_BASE_URL` | Core's WebSocket origin, e.g. `ws://core-host:9000` (different scheme, so it can't be inferred from the HTTP URL) |

### Optional — Core client call-class timeouts / retries

| Variable | Default | Meaning |
|---|---|---|
| `AGENTSD_CORE_TIMEOUT_PREOPEN_CALL` | `10s` | Slower setup calls (account-open, cash read, candle pull) |
| `AGENTSD_CORE_TIMEOUT_HOTPATH_CALL` | `500ms` | Live observation reads during trading |
| `AGENTSD_CORE_TIMEOUT_FIREANDFORGET_CALL` | `1s` | Order submission |
| `AGENTSD_CORE_MAX_IDEMPOTENT_RETRIES` | `2` | Retry cap for idempotent calls only (never for writes) |
| `AGENTSD_CORE_IDEMPOTENT_RETRY_BACKOFF` | `100ms` | Base delay between those retries |

### Optional — HTTP listen address

| Variable | Default | Meaning |
|---|---|---|
| `AGENTSD_LISTEN_ADDR` | `127.0.0.1:8080` | Loopback-only by default — set explicitly to bind elsewhere |

## 3. Start the daemon

```bash
export DATABASE_URL="postgres://user:pass@host:5432/dbname?sslmode=disable"
export REDIS_URL="redis://host:6379/0"
export AGENTSD_CORE_BASE_URL="http://localhost:9000"
export AGENTSD_CORE_WEBSOCKET_BASE_URL="ws://localhost:9000"

go run ./cmd/agentsd
# or: go build -o bin/agentsd ./cmd/agentsd && ./bin/agentsd
```

Startup order, so a failure is easy to place: load listen address → load
connectivity config → load Core client config → open Postgres pool (pings)
→ open Redis client (pings) → build Core client (no ping) → wire the stores,
generator, and orchestrator → start serving HTTP.

Shuts down on `SIGINT`/`SIGTERM`: stops accepting new requests, drains
in-flight ones for up to 15s, then exits.

## 4. Confirm it's up

```bash
curl -i http://127.0.0.1:8080/healthz   # 200, empty body
```

`POST /bringup` and `POST /winddown` are the inbound seam the conductor
calls — not something you'd normally curl by hand day to day.

## 5. Changing a behavior/calibration dial

Every tunable number (population size, lifecycle rates, capital-draw params,
perception/decision dials, the session scheduler's timing/quit-rate/broker-
default-funding dials, etc.) is a plain Go literal in the composition root —
**not** an environment variable:

- `cmd/agentsd/calibration.go` — generation (`defaultGenParams`), decision
  (`defaultDecisionParams`), orchestrator-level (`defaultOrchestratorConfig`:
  population size, lifecycle channel buffer, capital params), and the
  scenario presence baseline.
- `cmd/agentsd/session_wiring.go` — the per-session scheduler's dials
  (`defaultSessionParams`: history lookback, action/presence cadence,
  lookback depth, the quit-for-good rate, the broker-default funding figure).

To change one: edit the literal in the relevant `default*()` function, then
rebuild and restart — there's no live-reload. There is currently no
per-deployment override mechanism for these (unlike `DATABASE_URL` etc.,
which *are* env-driven) — if you need that, it would mean adding new env
plumbing at the composition root, not something already wired today.

## 6. Running the test suite (optional)

```bash
go test ./... -race
```

Tests that need a real Postgres/Redis additionally read:

| Variable | Requirement |
|---|---|
| `AGENTSD_TEST_DATABASE_URL` | Postgres DSN to a **dedicated test DB** — name must end in `_test` |
| `AGENTSD_TEST_REDIS_URL` | Redis URL on a **non-zero DB index** — tests `FLUSHDB` on every run |

Leaving either unset doesn't fail the suite — those specific tests skip.

## One thing to know before you rely on live trading behavior

`cmd/agentsd/main.go` currently wires a no-op scheduler
(`session.NewNoOpScheduler`) rather than the real tick-loop scheduler
(`session.NewLiveScheduler`). Bring-up/wind-down wiring is fully live, but no
actual per-tick trading or end-of-session effects run yet — that composition-
root swap is a deliberate, separate, not-yet-made decision (see the teardown
implementation doc under `docs/agents/` for the exact note), not an oversight
in this guide.
