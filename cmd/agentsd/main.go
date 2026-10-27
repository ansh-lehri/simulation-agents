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
	"simulation-agents/core"
	"simulation-agents/orchestrator"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/scenario"
	"simulation-agents/session"
	"simulation-agents/store"
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
// Postgres pool, Redis client, and Core client, builds the agent-side
// stores and the bring-up orchestrator over them, starts the HTTP
// server, and waits for either a SIGINT/SIGTERM or an unexpected
// serve error. Resource cleanup is handled by deferred Close calls so
// any failure path unwinds every already-opened resource in reverse
// order of acquisition.
func run(logger *slog.Logger) error {
	listenAddr, err := loadListenAddr()
	if err != nil {
		return err
	}

	connectivityConfig, err := config.Load()
	if err != nil {
		return fmt.Errorf("agentsd: load connectivity config: %w", err)
	}

	coreClientConfig, err := loadCoreClientConfig()
	if err != nil {
		return err
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

	coreClient, err := core.NewClient(coreClientConfig, logger)
	if err != nil {
		return fmt.Errorf("agentsd: build core client: %w", err)
	}
	defer coreClient.Close()

	// Build the agent-side stores over the pool/client handles. Each
	// store carves out just the surface the orchestrator and generator
	// consume: durable identity + per-session Redis state + the lease
	// pool bookkeeping + the write-ahead log + the market-data cache
	// + the sessions:active hash the bring-up tail writes.
	agentStore := store.NewPostgresAgentStore(postgresPool)
	runtimeStore := store.NewRedisRuntimeStore(redisClient)
	leaseAllocator := store.NewRedisLeaseAllocator(redisClient)
	pendingMintWAL := store.NewRedisPendingMintWAL(redisClient)
	marketDataStore := store.NewRedisMarketDataStore(redisClient)
	activeSessionStore := store.NewRedisActiveSessionStore(redisClient)

	// Build the scenario engine. The registry is loaded from the
	// in-code default blobs (validated against the catalogue at load
	// time); the baseline link-space + eta are computed once at
	// startup from the GenParams + presence baseline. The engine
	// satisfies generator.ScenarioProvider — it is the production
	// replacement for the no-op provider the generator was wired to
	// in the previous phase.
	scenarioRegistry, err := scenario.Load(scenario.DefaultRegistry(), scenario.DefaultCatalogue())
	if err != nil {
		return fmt.Errorf("agentsd: load scenario registry: %w", err)
	}
	scenarioEngine, err := scenario.NewEngine(
		defaultGenParams(),
		defaultPresenceBaseline(),
		scenario.DefaultCatalogue(),
		scenarioRegistry,
		baselineScenarioSeed,
		logger,
	)
	if err != nil {
		return fmt.Errorf("agentsd: build scenario engine: %w", err)
	}

	// Build the trait-draw engine. GenParams values are calibration
	// placeholders defined alongside this file; the generator package
	// holds mechanism only. The scenario engine is passed in as the
	// provider — a non-empty scenario name on an on-demand bring-up
	// now reaches the real shove / invert / switches pipeline.
	populationGenerator, err := generator.NewGenerator(
		agentStore,
		leaseAllocator,
		pendingMintWAL,
		scenarioEngine,
		defaultGenParams(),
		newUUIDAgentID(),
		systemClock,
		logger,
	)
	if err != nil {
		return fmt.Errorf("agentsd: build generator: %w", err)
	}

	// Build orchestratorConfig first — AccountOpenConcurrency is needed
	// by the account-open client constructor below.
	orchestratorConfig := defaultOrchestratorConfig()
	// Hand the loaded registry's names into the orchestrator so its
	// intake rejects any BringUp whose scenario field is non-empty
	// and not one of the registered names.
	orchestratorConfig.ValidScenarioNames = scenarioEngine.RegistryNames()

	// Build the bring-up spine helpers over the core client. The
	// account-open client runs the per-agent OpenAccount + GetAccountCash
	// pass; the capital resolver runs the three-case dispatch over the
	// in-flight population after the handles are back.
	accountOpenClient, err := orchestrator.NewAccountOpenClient(
		coreClient, orchestratorConfig.AccountOpenConcurrency, logger,
	)
	if err != nil {
		return fmt.Errorf("agentsd: build account-open client: %w", err)
	}
	capitalResolver, err := orchestrator.NewCapitalResolver(orchestratorConfig.CapitalParams, logger)
	if err != nil {
		return fmt.Errorf("agentsd: build capital resolver: %w", err)
	}

	// Build the per-exchange session runner. The core-client adapter
	// below bridges *core.Client's concrete WS return type into
	// session.WSConnection so the runner can hold the interface.
	//
	// The failure reporter is a thin adapter that resolves to the
	// orchestrator's own FailBringUp / ReportReady methods — the
	// orchestrator is the single failure surface for both tail
	// steps and the goroutine's pre-open ops. The adapter's inner
	// pointer is nil at construction and gets patched below once
	// the orchestrator is built; no bring-up can fire in that
	// window because the server has not yet been started.
	sessionScheduler := session.NewLiveScheduler(
		&sessionCoreClientAdapter{inner: coreClient},
		runtimeStore,
		marketDataStore,
		defaultSessionParams(orchestratorConfig.CapitalParams.DefaultFundedCash),
		logger,
		orchestrator.NewFreshEntropySource(),
	)
	orchestratorFailureAdapter := &orchestratorFailureReporterAdapter{}
	sessionRunner, err := session.NewRunner(session.Dependencies{
		Core:       &sessionCoreClientAdapter{inner: coreClient},
		Runtime:    runtimeStore,
		MarketData: marketDataStore,
		Scheduler:  sessionScheduler,
		Failure:    orchestratorFailureAdapter,
		Clock:      session.NewRealClock(),
		RNGSource:  orchestrator.NewFreshEntropySource(),
		Params:     defaultSessionParams(orchestratorConfig.CapitalParams.DefaultFundedCash),
		Logger:     logger,
	})
	if err != nil {
		return fmt.Errorf("agentsd: build session runner: %w", err)
	}

	// Compose the orchestrator. Live sessions use the fresh-entropy
	// seed source so one bring-up's randomness is independent of
	// another's.
	bringUpOrchestrator, err := orchestrator.NewOrchestrator(
		populationGenerator,
		accountOpenClient,
		capitalResolver,
		orchestrator.NewFreshEntropySource(),
		agentStore,
		runtimeStore,
		leaseAllocator,
		pendingMintWAL,
		activeSessionStore,
		scenarioEngine,
		sessionRunner,
		orchestratorConfig,
		logger,
	)
	if err != nil {
		return fmt.Errorf("agentsd: build orchestrator: %w", err)
	}
	// Close the two-phase wiring loop: patch the failure adapter
	// now that the orchestrator exists, so the session runner's
	// FailBringUp / ReportReady calls reach the orchestrator's
	// methods on the next bring-up.
	orchestratorFailureAdapter.inner = bringUpOrchestrator

	server := transport.New(listenAddr, bringUpOrchestrator, logger)

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
