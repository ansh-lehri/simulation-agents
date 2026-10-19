package session

import (
	"context"
	"fmt"
	"log/slog"

	"simulation-agents/orchestrator/generator"
	"simulation-agents/transport"
)

// NoOpScheduler is the Scheduler implementation this phase ships. Its
// StartScheduler logs the handoff and returns; the real tick loop
// (the per-agent action phase + the lifecycle presence phase) lands
// in a later phase and replaces this implementation at the
// composition root with no caller change.
//
// Composition-root responsibility: build one NoOpScheduler per
// process and inject it on the Runner's Dependencies so the Runner's
// go-live step has a scheduler to call. The log line marks the point
// in a bring-up where the next phase's tick loop will start.
type NoOpScheduler struct {
	logger *slog.Logger
}

// NewNoOpScheduler validates the logger and returns a NoOpScheduler.
// A nil logger is rejected so the handoff log line is never silently
// dropped — losing that line hides the point at which the next
// phase's tick loop would have started.
func NewNoOpScheduler(logger *slog.Logger) (*NoOpScheduler, error) {
	if logger == nil {
		return nil, fmt.Errorf("session: NewNoOpScheduler requires a non-nil logger")
	}
	return &NoOpScheduler{logger: logger}, nil
}

// StartScheduler logs the handoff and returns. The signature matches
// Scheduler exactly so the real tick loop drops in later without any
// caller change in the Runner.
func (scheduler *NoOpScheduler) StartScheduler(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
	lifecycleChannel chan<- LifecycleDecision,
) {
	_ = ctx
	_ = lifecycleChannel
	scheduler.logger.Info(
		"scheduler stub reached (next phase replaces body)",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"existing_count", len(population.Existing),
		"new_count", len(population.New),
	)
}

// Compile-time witness: the no-op implementation satisfies the
// Scheduler interface. Signature drift between the two would fail
// the build here rather than at first bring-up spawn.
var _ Scheduler = (*NoOpScheduler)(nil)
