package orchestrator

import (
	"strings"
	"testing"

	"simulation-agents/agent"
)

// completeOrchestratorConfig returns an OrchestratorConfig whose every
// field passes Validate. Each mutation case below changes exactly one
// field.
func completeOrchestratorConfig() OrchestratorConfig {
	return OrchestratorConfig{
		PopulationSize:  100,
		LifecycleBuffer: 16,
		CapitalParams: agent.CapitalParams{
			Floor:             1_000,
			JitterSpread:      0.1,
			DefaultFundedCash: 100_000,
		},
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
