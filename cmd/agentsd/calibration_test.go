package main

import (
	"io"
	"log/slog"
	"testing"

	"simulation-agents/scenario"
)

// TestScenarioEngineBuildsWithDefaults confirms the composition-root
// wiring between the default calibration fixtures and the scenario
// engine lines up end to end: the registry loads cleanly against the
// catalogue, and NewEngine accepts the default GenParams + presence
// baseline + registry + catalogue without complaint.
//
// If one of the default fixtures drifts (a knob renamed in the
// catalogue, a preset using a stale knob name, a GenParams field
// missed by Validate), this test fails at startup wiring rather than
// in production at the first bring-up.
func TestScenarioEngineBuildsWithDefaults(t *testing.T) {
	registry, err := scenario.Load(scenario.DefaultRegistry(), scenario.DefaultCatalogue())
	if err != nil {
		t.Fatalf("scenario.Load with defaults: %v", err)
	}

	engine, err := scenario.NewEngine(
		defaultGenParams(),
		defaultPresenceBaseline(),
		scenario.DefaultCatalogue(),
		registry,
		baselineScenarioSeed,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("scenario.NewEngine with defaults: %v", err)
	}

	// Resolving the empty name must stay the baseline identity path —
	// the composition root relies on this to leave the no-scenario
	// bring-up byte-identical to pre-scenario behaviour.
	effective, curve := engine.Effective("US", "")
	if effective != nil || curve != nil {
		t.Fatalf("empty name should produce (nil, nil), got (%v, %v)", effective, curve)
	}

	// Every shipped preset must resolve without panic — the full
	// pipeline runs through shove / invert / applySwitches / curveFrom
	// for each name.
	for _, scenarioName := range engine.RegistryNames() {
		effective, curve := engine.Effective("US", scenarioName)
		if effective == nil {
			t.Errorf("preset %q returned nil EffectiveParams", scenarioName)
		}
		if curve == nil {
			t.Errorf("preset %q returned nil CurveEffective", scenarioName)
		}
	}
}

// TestOrchestratorConfigAcceptsRegisteredScenarioNames confirms the
// handshake between the scenario registry and the orchestrator's
// intake accept-set: the names a composition-root run would feed into
// ValidScenarioNames must pass OrchestratorConfig.Validate without
// tripping the duplicate / empty-name guards.
func TestOrchestratorConfigAcceptsRegisteredScenarioNames(t *testing.T) {
	registry, err := scenario.Load(scenario.DefaultRegistry(), scenario.DefaultCatalogue())
	if err != nil {
		t.Fatalf("scenario.Load with defaults: %v", err)
	}

	config := defaultOrchestratorConfig()
	config.ValidScenarioNames = registry.Names()
	if err := config.Validate(); err != nil {
		t.Fatalf("default orchestrator config with registry names rejected: %v", err)
	}
}
