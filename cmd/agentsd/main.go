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
	"simulation-agents/transport"
)

// shutdownTimeout bounds how long graceful shutdown waits for in-flight
// requests to drain before the server is torn down forcibly. Long enough
// to finish any plausible inbound call to the orchestrator seam, short
// enough that an operator pressing Ctrl-C a second time will see the
// process exit without a protracted hang.
const shutdownTimeout = 15 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("agentsd exited with error", "error", err)
		os.Exit(1)
	}
}

// run owns the composition root: it loads configuration, opens the
// Postgres pool and Redis client, builds the placeholder orchestrator,
// starts the HTTP server, and waits for either a SIGINT/SIGTERM or an
// unexpected serve error. Resource cleanup is handled by deferred Close
// calls so any failure path unwinds every already-opened resource in
// reverse order of acquisition.
func run(logger *slog.Logger) error {
	listenAddr, err := loadListenAddr()
	if err != nil {
		return err
	}

	connectivityConfig, err := config.Load()
	if err != nil {
		return fmt.Errorf("agentsd: load connectivity config: %w", err)
	}

	rootCtx, stopSignalWatch := signal.NotifyContext(
		context.Background(), syscall.SIGINT, syscall.SIGTERM,
	)
	defer stopSignalWatch()

	postgresPool, err := db.NewPostgres(rootCtx, connectivityConfig.Postgres)
	if err != nil {
		return fmt.Errorf("agentsd: open postgres pool: %w", err)
	}
	defer postgresPool.Close()

	redisClient, err := db.NewRedis(rootCtx, connectivityConfig.Redis)
	if err != nil {
		return fmt.Errorf("agentsd: open redis client: %w", err)
	}
	defer func() {
		if closeErr := redisClient.Close(); closeErr != nil {
			logger.Warn("agentsd: redis client close returned error", "error", closeErr)
		}
	}()

	placeholderOrchestrator := transport.NewPlaceholderOrchestrator(logger)
	server := transport.New(listenAddr, placeholderOrchestrator, logger)

	// Run the HTTP server on a background goroutine so main can wait on
	// either its exit (unexpected serve failure) or the signal context
	// (operator-requested shutdown) through a single select.
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Start()
	}()

	select {
	case startupErr := <-serveResult:
		if startupErr != nil {
			return fmt.Errorf("agentsd: http server failed before shutdown signal: %w", startupErr)
		}
		// A nil return before any signal means the server shut itself down
		// cleanly through some other path — unusual, but not an error.
		return nil
	case <-rootCtx.Done():
		logger.Info("agentsd: shutdown signal received, draining server")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		return fmt.Errorf("agentsd: shutdown failed: %w", shutdownErr)
	}

	// Drain the serve result so a late Serve-side error surfaces instead of
	// being silently lost when main returns.
	if postShutdownErr := <-serveResult; postShutdownErr != nil {
		return fmt.Errorf("agentsd: server exit error after shutdown: %w", postShutdownErr)
	}

	logger.Info("agentsd: shutdown complete")
	return nil
}
