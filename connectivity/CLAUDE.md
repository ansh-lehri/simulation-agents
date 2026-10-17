# connectivity — Config & Connectivity (infra stage)

Spec: `docs/connectivity/INFRA_Connectivity.md` — the authority for this stage.

## What connectivity IS

Standalone infra: env-driven config loader + verified Postgres/Redis connection constructors.
Zero domain logic. No domain packages ever imported.

## Owns / public surface

- `config/` — `Config`, `PostgresConfig`, `RedisConfig` structs; `Load() (Config, error)`.
- `db/` — `NewPostgres(ctx, PostgresConfig) (*pgxpool.Pool, error)`; `NewRedis(ctx, RedisConfig) (*redis.Client, error)`.

## Dependency rule (non-negotiable)

- `config/` imports stdlib only (`time`, `os`, `net/url`, `fmt`, `errors`, `strconv`).
- `db/` imports `config/`, `pgx/pgxpool`, `go-redis/v9`, stdlib only.
- **Neither package imports any domain package** (`economy/`, `exchange/`, `broker/`, `contract/`). Ever.
- Dependency flows one way: domain packages may import this; this imports no domain package.

## Invariants

- `Load()` is the **only** place env vars are read. Constructors take config values, not env.
- Constructors ping on startup — fail fast here, not on the first real query.
- Caller owns `Close()` on returned handles. No package-level global, no singleton.
- No secret (DSN, URL, password) appears in any log line — log host/db/pool only.
- `go test ./... -race` stays green.

## Do NOT

- Read env vars inside `db/` constructors.
- Log DSNs, full URLs, or any credential field.
- Import `economy/`, `exchange/`, `broker/`, `contract/`, or any domain type.
- Add schema, migrations, queries, or reconnect/health-loop logic (later stages).
- Create package-level connection singletons.
