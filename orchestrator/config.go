package orchestrator

import (
	"fmt"

	"simulation-agents/agent"
)

// OrchestratorConfig carries the dial set the orchestrator needs beyond
// the generator's own GenParams. Kept small on purpose: generation
// calibration lives inside the generator; this struct holds only the
// orchestrator-level dials that could not sensibly live anywhere else.
type OrchestratorConfig struct {
	// PopulationSize is the target population per bring-up — the same
	// number both canonical and on-demand sessions target. Must be
	// strictly positive; a session with zero agents is a bug upstream.
	PopulationSize int

	// LifecycleBuffer is the channel buffer size for the lifecycle
	// decision stream between the per-exchange run goroutine and the
	// orchestrator-side writer. Buffered so a brief write-side stall
	// does not back up the tick loop. Must be non-negative; a value of
	// zero makes the channel unbuffered, which is a valid (synchronous)
	// choice for tests.
	LifecycleBuffer int

	// CapitalParams is the dial set the capital-resolver step passes
	// into agent.DrawTradingCapital. It is consumed per new agent (and
	// per leased-existing agent on an on-demand session) to produce the
	// session trading capital bounded by the broker's funding ceiling.
	// Validation of these dials is agent-package business; this struct
	// just carries them.
	CapitalParams agent.CapitalParams
}

// Validate returns a non-nil error if any orchestrator-level dial
// violates its structural invariant. Called by NewOrchestrator at
// construction time so a bad config fails startup instead of first
// bring-up.
func (config OrchestratorConfig) Validate() error {
	if config.PopulationSize <= 0 {
		return fmt.Errorf("orchestrator: PopulationSize %d must be positive", config.PopulationSize)
	}
	if config.LifecycleBuffer < 0 {
		return fmt.Errorf("orchestrator: LifecycleBuffer %d must be non-negative", config.LifecycleBuffer)
	}
	return nil
}
