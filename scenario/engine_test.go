package scenario

import (
	"bytes"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"simulation-agents/orchestrator/generator"
)

// buildEngine is the per-test engine factory for the engine tests —
// uses the same baseline fixtures as the merge tests so determinism
// across tests is implicit.
func buildEngine(t *testing.T, logger *slog.Logger) *ScenarioEngine {
	t.Helper()
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	registry, err := Load(DefaultRegistry(), DefaultCatalogue())
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	engine, err := NewEngine(
		baselineGenParams(),
		baselinePresenceRates(),
		DefaultCatalogue(),
		registry,
		42,
		logger,
	)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

// TestEffectiveEmptyNameIsIdentity pins the no-scenario identity
// path: a blank name returns a (nil, nil) pair with no allocation,
// no log entry, and no mutation of engine state. This is the
// baseline-on-demand pathway and must stay byte-identical to the
// pre-scenario behavior.
func TestEffectiveEmptyNameIsIdentity(t *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	engine := buildEngine(t, logger)

	effective, curve := engine.Effective("US", "")
	if effective != nil {
		t.Errorf("Effective(\"\") returned non-nil EffectiveParams")
	}
	if curve != nil {
		t.Errorf("Effective(\"\") returned non-nil CurveEffective")
	}
	if logBuffer.Len() != 0 {
		t.Errorf("Effective(\"\") wrote a log entry; buffer: %q", logBuffer.String())
	}
}

// TestEffectiveUnknownNamePanics guards the "no silent fallback"
// invariant. A name that was not validated at intake reaches the
// engine as a programming error and must surface loudly, not resolve
// to baseline. The panic message must name the bad input so the
// crash dump is actionable.
func TestEffectiveUnknownNamePanics(t *testing.T) {
	engine := buildEngine(t, nil)
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("expected panic for unknown scenario name")
		}
		message, ok := recovered.(string)
		if !ok {
			t.Fatalf("panic value was not a string: %v", recovered)
		}
		if !strings.Contains(message, "unregistered name") || !strings.Contains(message, "thinnn") {
			t.Errorf("panic message missed context: %q", message)
		}
	}()
	engine.Effective("US", "thinnn")
}

// TestEffectiveLogsCountryAndName asserts the resolve log line
// carries both correlation ids. This is the one boundary log the
// engine owns; a change to its field names would hide a session's
// provenance from a forensic scan.
func TestEffectiveLogsCountryAndName(t *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	engine := buildEngine(t, logger)

	_, _ = engine.Effective("JP", ScenarioNameThin)

	logOutput := logBuffer.String()
	if !strings.Contains(logOutput, "scenario=thin") {
		t.Errorf("resolve log missing scenario field: %q", logOutput)
	}
	if !strings.Contains(logOutput, "country=JP") {
		t.Errorf("resolve log missing country field: %q", logOutput)
	}
}

// TestEffectiveEveryPresetReturnsNonNil is a shape smoke-test over
// the five shipped presets. Each one must produce a non-nil
// EffectiveParams (with ShiftedParams populated) and a non-nil
// CurveEffective — the empty "normal" scenario included, because it
// still runs through the engine (unlike Effective("")).
func TestEffectiveEveryPresetReturnsNonNil(t *testing.T) {
	engine := buildEngine(t, nil)
	for _, scenarioName := range engine.RegistryNames() {
		effective, curve := engine.Effective("US", scenarioName)
		if effective == nil {
			t.Errorf("preset %q returned nil EffectiveParams", scenarioName)
			continue
		}
		if effective.ShiftedParams == nil {
			t.Errorf("preset %q returned nil ShiftedParams", scenarioName)
		}
		if curve == nil {
			t.Errorf("preset %q returned nil CurveEffective", scenarioName)
		}
	}
}

// TestEffectiveIsDeterministic pins the standing reproducibility
// invariant for the engine's output: the same engine resolving the
// same scenario twice yields byte-identical EffectiveParams and
// CurveEffective. A regression here would mean a hidden
// non-determinism (map iteration, time, fresh rng) crept into
// Effective's execution path.
func TestEffectiveIsDeterministic(t *testing.T) {
	engine := buildEngine(t, nil)
	for _, scenarioName := range engine.RegistryNames() {
		firstEffective, firstCurve := engine.Effective("US", scenarioName)
		secondEffective, secondCurve := engine.Effective("US", scenarioName)

		if !reflect.DeepEqual(firstEffective, secondEffective) {
			t.Errorf("EffectiveParams drift for %q", scenarioName)
		}
		if !reflect.DeepEqual(firstCurve, secondCurve) {
			t.Errorf("CurveEffective drift for %q", scenarioName)
		}
	}
}

// TestEffectiveNormalPresetMatchesBaseline asserts the control
// preset returns a ShiftedParams byte-equal to the baseline. The
// normal preset has empty direction, zero intensity, and no
// switches — the shove reduces to u0, the invert maps back to the
// baseline moments, and setScalarMoment / setDispositionProportions
// hand back the exact starting values. This is also an indirect
// identity check on the whole pipeline.
func TestEffectiveNormalPresetMatchesBaseline(t *testing.T) {
	engine := buildEngine(t, nil)
	effective, _ := engine.Effective("US", ScenarioNameNormal)
	if effective == nil || effective.ShiftedParams == nil {
		t.Fatalf("normal preset returned nil")
	}

	shifted := *effective.ShiftedParams
	baseline := baselineGenParams()

	assertBetaCloseEnough(t, "AggressivenessBeta", shifted.AggressivenessBeta, baseline.AggressivenessBeta)
	assertBetaCloseEnough(t, "SkillBeta", shifted.SkillBeta, baseline.SkillBeta)
	assertBetaCloseEnough(t, "OptimismBeta", shifted.OptimismBeta, baseline.OptimismBeta)
	assertDispositionCloseEnough(t, shifted.DispositionDirichletAlphas, baseline.DispositionDirichletAlphas)
}

// TestEffectiveThinPresetShiftsAggressivenessUp is a direction check
// for one preset: thin names KnobAggressivenessMean with +1, so the
// shifted mean must exceed the baseline mean. This asserts the shove
// sign reaches the output correctly; the magnitude is calibration.
func TestEffectiveThinPresetShiftsAggressivenessUp(t *testing.T) {
	engine := buildEngine(t, nil)
	effective, _ := engine.Effective("US", ScenarioNameThin)
	if effective == nil || effective.ShiftedParams == nil {
		t.Fatalf("thin preset returned nil")
	}

	baselineAggressiveness := betaMean(baselineGenParams().AggressivenessBeta)
	shiftedAggressiveness := betaMean(effective.ShiftedParams.AggressivenessBeta)
	if !(shiftedAggressiveness > baselineAggressiveness) {
		t.Errorf("thin preset did not raise aggressiveness.mean: baseline=%v shifted=%v",
			baselineAggressiveness, shiftedAggressiveness)
	}
}

// TestEffectiveDispersionPresetSetsPhiBimodal asserts the dispersion
// preset flips the one SHAPE override — PhiBimodal is populated and
// has the right camp means from the preset's slot constants.
func TestEffectiveDispersionPresetSetsPhiBimodal(t *testing.T) {
	engine := buildEngine(t, nil)
	effective, _ := engine.Effective("US", ScenarioNameDispersion)
	if effective == nil || effective.PhiBimodal == nil {
		t.Fatal("dispersion preset did not populate PhiBimodal")
	}
	expectedBullMean := (1 + bimodalDispersionSeparationSlot) / 2
	expectedBearMean := (1 - bimodalDispersionSeparationSlot) / 2
	assertCampBeta(t, "bull", effective.PhiBimodal.FirstModeBeta, expectedBullMean, bimodalDispersionWithinSpreadSlot)
	assertCampBeta(t, "bear", effective.PhiBimodal.SecondModeBeta, expectedBearMean, bimodalDispersionWithinSpreadSlot)
}

// TestEngineSatisfiesScenarioProviderInterface is a compile-time
// witness that ScenarioEngine implements generator.ScenarioProvider.
// A method-shape drift would break this at build time rather than at
// composition root wiring.
func TestEngineSatisfiesScenarioProviderInterface(t *testing.T) {
	var _ generator.ScenarioProvider = (*ScenarioEngine)(nil)
}

// TestNewEngineRejectsNilLogger asserts the constructor fails fast
// when a required dependency is missing — matches the generator /
// orchestrator conventions for required-arg validation.
func TestNewEngineRejectsNilLogger(t *testing.T) {
	registry, _ := Load(DefaultRegistry(), DefaultCatalogue())
	_, err := NewEngine(baselineGenParams(), baselinePresenceRates(), DefaultCatalogue(), registry, 1, nil)
	if err == nil {
		t.Fatal("NewEngine accepted nil logger")
	}
}

// TestNewEngineRejectsEmptyCatalogue asserts a missing catalogue
// also fails fast — nothing downstream can meaningfully run with
// zero knobs registered.
func TestNewEngineRejectsEmptyCatalogue(t *testing.T) {
	registry, _ := Load(DefaultRegistry(), DefaultCatalogue())
	_, err := NewEngine(baselineGenParams(), baselinePresenceRates(), KnobCatalogue{}, registry, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("NewEngine accepted empty catalogue")
	}
}

// TestNewEngineRejectsInvalidGenParams asserts a bad calibration is
// surfaced at construction via generator.GenParams.Validate — the
// composition root sees the failure at startup rather than at first
// bring-up.
func TestNewEngineRejectsInvalidGenParams(t *testing.T) {
	badParams := baselineGenParams()
	badParams.HerdProbability = 1.5 // outside [0,1]

	registry, _ := Load(DefaultRegistry(), DefaultCatalogue())
	_, err := NewEngine(badParams, baselinePresenceRates(), DefaultCatalogue(), registry, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("NewEngine accepted invalid HerdProbability")
	}
}
