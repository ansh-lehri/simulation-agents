package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"simulation-agents/connectivity/config"
)

// NewPostgres opens a pgxpool, pings within pgCfg.PingTimeout, and returns the
// live pool. The caller owns pool.Close(). Fails at startup if the DB is
// unreachable — never defers the error to the first real query.
// MaxConns/MinConns on pgCfg override the DSN pool query params when non-zero.
func NewPostgres(ctx context.Context, pgCfg config.PostgresConfig) (*pgxpool.Pool, error) {
	slog.Debug("connectivity/db: opening postgres pool", "dsn_scheme", "postgres")

	poolCfg, err := pgxpool.ParseConfig(pgCfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("connectivity/db: postgres: parse DSN: %w", err)
	}

	if pgCfg.MaxConns > 0 {
		poolCfg.MaxConns = pgCfg.MaxConns
	}
	if pgCfg.MinConns > 0 {
		poolCfg.MinConns = pgCfg.MinConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connectivity/db: postgres: create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, pgCfg.PingTimeout)
	defer cancel()

	if pingErr := pool.Ping(pingCtx); pingErr != nil {
		pool.Close()
		return nil, fmt.Errorf("connectivity/db: postgres: ping failed (host=%s db=%s timeout=%s): %w",
			poolCfg.ConnConfig.Host, poolCfg.ConnConfig.Database, pgCfg.PingTimeout, pingErr)
	}

	slog.Info("connectivity/db: postgres pool opened",
		"host", poolCfg.ConnConfig.Host,
		"db", poolCfg.ConnConfig.Database,
		"max_conns", poolCfg.MaxConns,
		"min_conns", poolCfg.MinConns,
	)

	return pool, nil
}
