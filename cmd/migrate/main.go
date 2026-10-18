// Command migrate applies pending Postgres migrations and exits. It is a
// deliberately separate binary from the service so schema changes are an
// explicit operational step, not a side effect of booting the daemon.
//
// Usage: configuration is loaded from the same environment variables as
// the service (see connectivity/config); point POSTGRES_DSN at the target
// database and run:
//
//	go run ./cmd/migrate
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"simulation-agents/connectivity/config"
	"simulation-agents/connectivity/db"
	"simulation-agents/store"
)

// migrateTimeout bounds the whole run. Migrations are a few DDL statements
// in one transaction; a minute is plenty, and well under any CI deployment
// job's wall-clock window.
const migrateTimeout = 60 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("migrate exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	connectivityConfig, err := config.Load()
	if err != nil {
		return fmt.Errorf("migrate: load connectivity config: %w", err)
	}

	rootCtx, stopSignalWatch := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM,
	)
	defer stopSignalWatch()

	ctx, cancel := context.WithTimeout(rootCtx, migrateTimeout)
	defer cancel()

	pool, err := db.NewPostgres(ctx, connectivityConfig.Postgres)
	if err != nil {
		return fmt.Errorf("migrate: open postgres pool: %w", err)
	}
	defer pool.Close()

	logger.Info("migrate: applying pending migrations")
	if err := store.Apply(ctx, pool); err != nil {
		return fmt.Errorf("migrate: apply: %w", err)
	}
	logger.Info("migrate: done")
	return nil
}
