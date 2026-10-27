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

	// AccountOpenConcurrency caps the fan-out of the account-open pass
	// (OpenAccount + GetAccountCash per agent). Each agent's two calls
	// run sequentially but all agents run in parallel up to this ceiling
	// so a large new-agent slice does not hammer Core. Must be positive.
	// Rule of thumb: ceil(PopulationSize / 50) gives ~50 ms latency per
	// batch at a 10 ms round-trip; 300 handles 13 000 agents in ~
	// 43 rounds (~430 ms total).
	AccountOpenConcurrency int

	// ValidScenarioNames is the accept-set the inbound bring-up
	// validator consults for BringUpRequest.Scenario. A non-empty
	// scenario name must be in this set or the bring-up is rejected
	// at intake — no silent fallback to baseline. The empty string
	// is always accepted (it signals a baseline on-demand bring-up)
	// and is not required to be listed.
	//
	// An empty slice is valid: it means "no scenarios registered",
	// in which case any non-empty Scenario field is rejected. The
	// composition root populates this from the loaded scenario
	// registry so the orchestrator does not need to import the
	// scenario package.
	ValidScenarioNames []string
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
	if config.AccountOpenConcurrency <= 0 {
		return fmt.Errorf("orchestrator: AccountOpenConcurrency %d must be positive", config.AccountOpenConcurrency)
	}
	seenScenario := make(map[string]struct{}, len(config.ValidScenarioNames))
	for _, scenarioName := range config.ValidScenarioNames {
		if scenarioName == "" {
			return fmt.Errorf("orchestrator: ValidScenarioNames contains an empty name")
		}
		if _, duplicate := seenScenario[scenarioName]; duplicate {
			return fmt.Errorf("orchestrator: ValidScenarioNames contains duplicate %q", scenarioName)
		}
		seenScenario[scenarioName] = struct{}{}
	}
	return nil
}
