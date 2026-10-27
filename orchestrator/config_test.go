package orchestrator

import (
	"strings"
	"testing"

	"simulation-agents/agent"
)

// completeOrchestratorConfig returns an OrchestratorConfig whose every
// field passes Validate. Each mutation case below changes exactly one
// field. The ValidScenarioNames list carries the two placeholder names
// the existing bring-up tests exercise, so a request bearing either
// name passes intake and the test can assert the downstream behaviour
// (warn-on-canonical, no-warn-on-on-demand) that was its real target.
func completeOrchestratorConfig() OrchestratorConfig {
	return OrchestratorConfig{
		PopulationSize:         100,
		LifecycleBuffer:        16,
		AccountOpenConcurrency: 10,
		CapitalParams: agent.CapitalParams{
			Floor:             1_000,
			JitterSpread:      0.1,
			DefaultFundedCash: 100_000,
		},
		ValidScenarioNames: []string{"flash_crash", "polarized_optimism"},
	}
}

func TestValidateAcceptsCompleteOrchestratorConfig(t *testing.T) {
	if err := completeOrchestratorConfig().Validate(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestValidateRejectsNonPositivePopulationSize(t *testing.T) {
	config := completeOrchestratorConfig()
	config.PopulationSize = 0
	err := config.Validate()
	if err == nil || !strings.Contains(err.Error(), "PopulationSize") {
		t.Fatalf("expected PopulationSize error, got %v", err)
	}
}

func TestValidateRejectsNegativeLifecycleBuffer(t *testing.T) {
	config := completeOrchestratorConfig()
	config.LifecycleBuffer = -1
	err := config.Validate()
	if err == nil || !strings.Contains(err.Error(), "LifecycleBuffer") {
		t.Fatalf("expected LifecycleBuffer error, got %v", err)
	}
}

func TestValidateAcceptsZeroLifecycleBuffer(t *testing.T) {
	// Zero makes the channel unbuffered. That is a valid (synchronous)
	// choice for tests; Validate must not reject it.
	config := completeOrchestratorConfig()
	config.LifecycleBuffer = 0
	if err := config.Validate(); err != nil {
		t.Fatalf("expected zero buffer to validate, got %v", err)
	}
}

func TestValidateAcceptsEmptyValidScenarioNames(t *testing.T) {
	// An empty slice is valid and means "no scenarios registered";
	// any non-empty Scenario field is then rejected at intake. A
	// process without a scenario engine wired must still start.
	config := completeOrchestratorConfig()
	config.ValidScenarioNames = nil
	if err := config.Validate(); err != nil {
		t.Fatalf("expected empty ValidScenarioNames to validate, got %v", err)
	}
}

func TestValidateRejectsEmptyScenarioName(t *testing.T) {
	// An empty string in the slice would make the intake accept-set
	// contain the empty name, overlapping with the "baseline" sentinel.
	// The validator must flag this as a calibration bug.
	config := completeOrchestratorConfig()
	config.ValidScenarioNames = []string{"thin", ""}
	err := config.Validate()
	if err == nil || !strings.Contains(err.Error(), "empty name") {
		t.Fatalf("expected empty-name error, got %v", err)
	}
}

func TestValidateRejectsDuplicateScenarioName(t *testing.T) {
	// A duplicate in the slice would silently mask one of the entries
	// in the accept-set; surface it at Validate so a bad registry does
	// not reach the orchestrator.
	config := completeOrchestratorConfig()
	config.ValidScenarioNames = []string{"thin", "calm", "thin"}
	err := config.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}
