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

// TraitChannelSets must no longer carry a "noise" entry — noise reads
// no perception channel, so there is nothing for the generator to draw
// a Dirichlet channel_weights vector over. Dropping the entry must not
// break GenParams.Validate(), which only requires every LISTED trait to
// have a non-empty channel list, not that every trait be listed.
func TestDefaultGenParams_TraitChannelSetsHasNoNoiseEntry(t *testing.T) {
	params := defaultGenParams()

	if _, hasNoise := params.TraitChannelSets["noise"]; hasNoise {
		t.Error("defaultGenParams: TraitChannelSets must not have a \"noise\" entry")
	}
	wantTraits := map[string][]string{
		"trend":    {"chart", "volume"},
		"mean_rev": {"range", "volume"},
	}
	if len(params.TraitChannelSets) != len(wantTraits) {
		t.Fatalf("TraitChannelSets: got %d traits, want %d (%v)", len(params.TraitChannelSets), len(wantTraits), params.TraitChannelSets)
	}

	if err := params.Validate(); err != nil {
		t.Fatalf("defaultGenParams with noise dropped from TraitChannelSets failed Validate: %v", err)
	}
}

// defaultSessionParams wires this into session.Params.Decision now, but
// this test still pins the basic sanity every shave/mixer constant must
// satisfy regardless of the exact placeholder magnitude: floors sit in
// (0,1] (a shave can shrink conviction hard but never force it to
// exactly zero on its own), and the two threshold-shaped dials are
// strictly positive.
func TestDefaultDecisionParams_FieldsAreSane(t *testing.T) {
	params := defaultDecisionParams()

	floors := map[string]float64{
		"TimeShave.Floor":     params.TimeShave.Floor,
		"SpreadShave.Floor":   params.SpreadShave.Floor,
		"CooldownShave.Floor": params.CooldownShave.Floor,
	}
	for name, floor := range floors {
		if floor <= 0 || floor > 1 {
			t.Errorf("%s: got %v, want in (0,1]", name, floor)
		}
	}

	if params.Mixer.SecondarySensitivity <= 0 {
		t.Errorf("Mixer.SecondarySensitivity: got %v, want > 0", params.Mixer.SecondarySensitivity)
	}
	if params.Mixer.NoiseImpulseConviction <= 0 || params.Mixer.NoiseImpulseConviction > 1 {
		t.Errorf("Mixer.NoiseImpulseConviction: got %v, want in (0,1]", params.Mixer.NoiseImpulseConviction)
	}
	if params.Gate.WaitReassessThreshold <= 0 {
		t.Errorf("Gate.WaitReassessThreshold: got %v, want > 0", params.Gate.WaitReassessThreshold)
	}
	if params.ThetaAddMargin < 0 {
		t.Errorf("ThetaAddMargin: got %v, want >= 0", params.ThetaAddMargin)
	}
}
