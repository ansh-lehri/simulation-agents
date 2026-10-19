package session

import "log/slog"

// Dependencies carries every collaborator the Runner needs to serve a
// bring-up spawn. The composition root is the sole constructor; the
// Runner knows the concrete implementations only through the narrow
// interfaces declared in interfaces.go.
//
// Every field is required. NewRunner rejects a nil in any of them so
// a misconfigured composition root fails at process start rather than
// at first bring-up — a nil dependency would otherwise panic deep in a
// pre-open op's call tree with no obvious root cause.
type Dependencies struct {
	// Core is the single outbound client to the Core process the
	// Runner consults for the three pre-open operations (session
	// install, candle history, live WebSocket subscribe).
	Core CoreClient

	// Runtime is the Redis-backed per-agent runtime state the Runner
	// writes directly: the entry-fraction stamp, the per-agent
	// session-load status, and later the lifecycle presence
	// transitions driven by the tick loop. It never writes Postgres.
	Runtime RuntimeStore

	// MarketData is the Redis-backed market-data cache the pre-open
	// history pull seeds and the live ingest goroutine extends.
	MarketData MarketDataStore

	// Scheduler is the tick-loop entry point the Runner hands off to
	// after go-live. The composition root wires a logging no-op this
	// phase; the real tick loop replaces it later.
	Scheduler Scheduler

	// Failure is the surface the Runner calls when a pre-open op
	// fails fatally and when the session reaches go-live. The
	// orchestrator provides the implementation; the Runner never
	// performs its own cleanup — the decide/write split holds for
	// failure handling exactly as it does for presence transitions.
	Failure FailureReporter

	// Clock is the wall-clock the Runner consults to wait until the
	// session's start time before handing off to the scheduler. A
	// fake clock replaces the real one in tests so they never sleep.
	Clock Clock

	// RNGSource builds a fresh seeded RNG the Runner uses to draw
	// each agent's entry fraction. Live sessions call Fresh() per
	// bring-up so one session's entry pattern does not correlate
	// with another's; tuning runs inject a fixed-seed source so a
	// bring-up replays identically.
	RNGSource RNGSource

	// Params carries the calibration and operational dials
	// (entry-curve shape, history-window length, concurrency caps).
	// Values live in the composition root, never hard-coded here.
	Params Params

	// Logger is the structured logger; every boundary call the
	// Runner makes produces a log line through it. Nil is rejected
	// so a boundary trail is never silently dropped.
	Logger *slog.Logger
}
