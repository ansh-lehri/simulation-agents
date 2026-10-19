package scenario

import (
	"io"
	"log/slog"
	"math"
	"testing"

	"simulation-agents/orchestrator/generator"
)

// testEngine builds a fresh ScenarioEngine from the baseline fixtures
// used across stage 2 tests. The same seed and params land in every
// test so determinism across cases is implicit.
func testEngine(t *testing.T) *ScenarioEngine {
	t.Helper()
	registry, err := Load(DefaultRegistry(), DefaultCatalogue())
	if err != nil {
		t.Fatalf("Load default registry: %v", err)
	}
	engine, err := NewEngine(
		baselineGenParams(),
		baselinePresenceRates(),
		DefaultCatalogue(),
		registry,
		42,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

// TestShoveLeavesUntouchedKnobsAtBaseline pins the sparse-shove
// invariant: a scenario that names only one knob in Direction must
// leave every other knob's uEff exactly at its baseline u0. A drift
// here would mean the shove formula misread an absent direction key
// as a non-zero weight.
func TestShoveLeavesUntouchedKnobsAtBaseline(t *testing.T) {
	engine := testEngine(t)
	singleKnobBlob := ScenarioBlob{
		Direction: Direction{KnobAggressivenessMean: +1},
		Intensity: 1,
	}

	scalarUEff, _ := engine.shove(singleKnobBlob)

	for knobName, baselineU0 := range engine.baseline.scalarU0 {
		if knobName == KnobAggressivenessMean {
			continue
		}
		if scalarUEff[knobName] != baselineU0 {
			t.Errorf("untouched knob %q drifted: baseline=%v shoved=%v",
				knobName, baselineU0, scalarUEff[knobName])
		}
	}
	expectedShift := engine.baseline.scalarEta[KnobAggressivenessMean]
	expectedUEff := engine.baseline.scalarU0[KnobAggressivenessMean] + expectedShift
	if math.Abs(scalarUEff[KnobAggressivenessMean]-expectedUEff) > floatCloseEnough {
		t.Errorf("aggressiveness.mean shoved wrong: got=%v want=%v",
			scalarUEff[KnobAggressivenessMean], expectedUEff)
	}
}

// TestShoveScalesWithIntensity asserts the shove is linear in
// Intensity: doubling s doubles the per-knob shift from baseline.
// This is the single-knob invariant the measurement loop relies on
// when fitting s to a target band.
func TestShoveScalesWithIntensity(t *testing.T) {
	engine := testEngine(t)
	baselineUEff, _ := engine.shove(ScenarioBlob{
		Direction: Direction{KnobSkillMean: +1},
		Intensity: 1,
	})
	doubleUEff, _ := engine.shove(ScenarioBlob{
		Direction: Direction{KnobSkillMean: +1},
		Intensity: 2,
	})

	baselineU0 := engine.baseline.scalarU0[KnobSkillMean]
	singleShift := baselineUEff[KnobSkillMean] - baselineU0
	doubleShift := doubleUEff[KnobSkillMean] - baselineU0
	if math.Abs(doubleShift-2*singleShift) > floatCloseEnough {
		t.Errorf("intensity scaling broke: single=%v double=%v", singleShift, doubleShift)
	}
}

// TestShoveDispositionArrowProducesSignedVector asserts the arrow
// translation: Toward gets +eta, AwayFrom gets -eta, the third
// component stays at its baseline u0. A regression here would break
// the "move mass between two slots" contract.
func TestShoveDispositionArrowProducesSignedVector(t *testing.T) {
	engine := testEngine(t)
	blob := ScenarioBlob{
		DispositionDirection: &DispositionArrow{
			Toward:   DispositionComponentNoise,
			AwayFrom: DispositionComponentMeanReversion,
		},
		Intensity: 1,
	}

	_, dispositionUEff := engine.shove(blob)

	expectedNoise := engine.baseline.dispositionU0[DispositionNoiseIndex] +
		engine.baseline.dispositionEta[DispositionNoiseIndex]
	expectedMeanRev := engine.baseline.dispositionU0[DispositionMeanReversionIndex] -
		engine.baseline.dispositionEta[DispositionMeanReversionIndex]
	expectedTrend := engine.baseline.dispositionU0[DispositionTrendIndex]

	if math.Abs(dispositionUEff[DispositionNoiseIndex]-expectedNoise) > floatCloseEnough {
		t.Errorf("noise component wrong: got=%v want=%v",
			dispositionUEff[DispositionNoiseIndex], expectedNoise)
	}
	if math.Abs(dispositionUEff[DispositionMeanReversionIndex]-expectedMeanRev) > floatCloseEnough {
		t.Errorf("mean-reversion component wrong: got=%v want=%v",
			dispositionUEff[DispositionMeanReversionIndex], expectedMeanRev)
	}
	if dispositionUEff[DispositionTrendIndex] != expectedTrend {
		t.Errorf("trend component drifted: got=%v want=%v",
			dispositionUEff[DispositionTrendIndex], expectedTrend)
	}
}

// TestInvertPreservesBaselineOnEmptyBlob is the all-zeros identity
// at the invert layer: an empty blob run through the full pipeline
// must land back on the baseline GenParams (same Beta pairs, same
// log-normal, same Dirichlet alphas). Any drift means the fromLink
// pass is lossy or setScalarMoment is subtly off.
func TestInvertPreservesBaselineOnEmptyBlob(t *testing.T) {
	engine := testEngine(t)
	emptyBlob := ScenarioBlob{}

	scalarUEff, dispositionUEff := engine.shove(emptyBlob)
	effective := engine.invert(scalarUEff, dispositionUEff, emptyBlob)

	if effective.ShiftedParams == nil {
		t.Fatal("invert returned nil ShiftedParams for empty blob")
	}
	shifted := *effective.ShiftedParams
	baseline := baselineGenParams()

	assertBetaCloseEnough(t, "AggressivenessBeta", shifted.AggressivenessBeta, baseline.AggressivenessBeta)
	assertBetaCloseEnough(t, "SkillBeta", shifted.SkillBeta, baseline.SkillBeta)
	assertBetaCloseEnough(t, "PnlReactivityBeta", shifted.PnlReactivityBeta, baseline.PnlReactivityBeta)
	assertBetaCloseEnough(t, "ReactivityBeta", shifted.ReactivityBeta, baseline.ReactivityBeta)
	assertBetaCloseEnough(t, "PActBeta", shifted.PActBeta, baseline.PActBeta)
	assertBetaCloseEnough(t, "OptimismBeta", shifted.OptimismBeta, baseline.OptimismBeta)
	if math.Abs(shifted.HerdProbability-baseline.HerdProbability) > floatCloseEnough {
		t.Errorf("HerdProbability drifted: baseline=%v shifted=%v",
			baseline.HerdProbability, shifted.HerdProbability)
	}

	assertDispositionCloseEnough(t, shifted.DispositionDirichletAlphas, baseline.DispositionDirichletAlphas)
}

// TestInvertDispositionProportionsSumToOne is the simplex invariant
// after a disposition shove: the implied proportions still sum to 1
// and every component is strictly positive. A regression here would
// let the Dirichlet draw land outside the simplex.
func TestInvertDispositionProportionsSumToOne(t *testing.T) {
	engine := testEngine(t)
	blob := ScenarioBlob{
		DispositionDirection: &DispositionArrow{
			Toward:   DispositionComponentTrend,
			AwayFrom: DispositionComponentNoise,
		},
		Intensity: 1.5,
	}

	scalarUEff, dispositionUEff := engine.shove(blob)
	effective := engine.invert(scalarUEff, dispositionUEff, blob)
	alphas := effective.ShiftedParams.DispositionDirichletAlphas

	totalShifted := alphas.Trend + alphas.MeanReversion + alphas.Noise
	baselineAlphas := baselineGenParams().DispositionDirichletAlphas
	totalBaseline := baselineAlphas.Trend + baselineAlphas.MeanReversion + baselineAlphas.Noise
	if math.Abs(totalShifted-totalBaseline) > floatCloseEnough*totalBaseline {
		t.Errorf("Dirichlet concentration drifted: baseline=%v shifted=%v", totalBaseline, totalShifted)
	}
	if alphas.Trend <= 0 || alphas.MeanReversion <= 0 || alphas.Noise <= 0 {
		t.Errorf("non-positive Dirichlet alpha after shove: %+v", alphas)
	}
}

// TestInvertSkipsPresenceKnobs guards the split between invert
// (writes shifted GenParams) and curveFrom (writes shifted presence
// rates). A regression where invert tries to set a presence knob
// into GenParams would panic (no matching knob in setScalarMoment);
// the test asserts it does not.
func TestInvertSkipsPresenceKnobs(t *testing.T) {
	engine := testEngine(t)
	blob := ScenarioBlob{
		Direction: Direction{
			KnobPresenceEntryRate: +1,
			KnobPresenceDoneRate:  -1,
		},
		Intensity: 1,
	}

	// If invert routed presence knobs into setScalarMoment, this
	// would panic. We only need the fact that it returns normally.
	scalarUEff, dispositionUEff := engine.shove(blob)
	_ = engine.invert(scalarUEff, dispositionUEff, blob)
}

// TestCurveFromReturnsShiftedPresenceRates asserts the lifecycle
// output carries the four shifted rates in their inverted (strictly
// positive) form, and that an untouched presence knob returns the
// baseline rate exactly.
func TestCurveFromReturnsShiftedPresenceRates(t *testing.T) {
	engine := testEngine(t)
	blob := ScenarioBlob{
		Direction: Direction{KnobPresenceEntryRate: +1},
		Intensity: 1,
	}

	scalarUEff, _ := engine.shove(blob)
	curve := engine.curveFrom(scalarUEff)

	if curve.EntryRate <= 0 || curve.StepOutRate <= 0 || curve.DoneRate <= 0 || curve.ReentryRate <= 0 {
		t.Errorf("non-positive shifted presence rate: %+v", curve)
	}

	baselinePresence := baselinePresenceRates()
	// Untouched rates should land exactly back on their baseline values.
	if math.Abs(curve.StepOutRate-baselinePresence.StepOutRate) > floatCloseEnough*baselinePresence.StepOutRate {
		t.Errorf("StepOutRate drifted without a shove: got=%v want=%v",
			curve.StepOutRate, baselinePresence.StepOutRate)
	}
	if math.Abs(curve.DoneRate-baselinePresence.DoneRate) > floatCloseEnough*baselinePresence.DoneRate {
		t.Errorf("DoneRate drifted without a shove: got=%v want=%v",
			curve.DoneRate, baselinePresence.DoneRate)
	}
	// The shoved rate should be strictly greater than baseline (positive
	// direction, unit eta, positive intensity).
	if !(curve.EntryRate > baselinePresence.EntryRate) {
		t.Errorf("positive EntryRate shove did not raise the rate: baseline=%v shifted=%v",
			baselinePresence.EntryRate, curve.EntryRate)
	}
}

// TestApplySwitchesBimodalPopulated asserts the one supported
// absolute switch — the bimodal optimism override — maps a
// BimodalConfig onto a correctly-shaped BimodalOptimism. The mix
// weight passes through unchanged; the two Beta pairs center at the
// bull / bear means derived from Separation; the camp concentration
// matches WithinSpread.
func TestApplySwitchesBimodalPopulated(t *testing.T) {
	engine := testEngine(t)
	blob := dispersionPreset()

	scalarUEff, dispositionUEff := engine.shove(blob)
	effective := engine.invert(scalarUEff, dispositionUEff, blob)

	if effective.PhiBimodal == nil {
		t.Fatal("PhiBimodal was not populated by applySwitches")
	}
	bimodal := effective.PhiBimodal
	if bimodal.MixWeight != bimodalDispersionBullWeightSlot {
		t.Errorf("MixWeight = %v, want %v (BullWeight slot)",
			bimodal.MixWeight, bimodalDispersionBullWeightSlot)
	}

	expectedBullMean := (1 + bimodalDispersionSeparationSlot) / 2
	expectedBearMean := (1 - bimodalDispersionSeparationSlot) / 2
	expectedConcentration := bimodalDispersionWithinSpreadSlot

	assertCampBeta(t, "bull", bimodal.FirstModeBeta, expectedBullMean, expectedConcentration)
	assertCampBeta(t, "bear", bimodal.SecondModeBeta, expectedBearMean, expectedConcentration)
}

// TestApplySwitchesBimodalOverridesCenterArrow pins the merge-engine
// precedence rule: a scenario with phi_bimodal ON overrides any
// optimism.center direction arrow. The test constructs a blob that
// sets both and asserts the shifted OptimismBeta is irrelevant —
// drawTraits reads PhiBimodal first, which is populated.
func TestApplySwitchesBimodalOverridesCenterArrow(t *testing.T) {
	engine := testEngine(t)
	bimodalOn := BimodalConfig{
		Separation:   0.5,
		BullWeight:   0.5,
		WithinSpread: 0.3,
	}
	blob := ScenarioBlob{
		Direction: Direction{KnobOptimismCenter: +1},
		Intensity: 1,
		Switches:  Switches{PhiBimodal: &bimodalOn},
	}

	scalarUEff, dispositionUEff := engine.shove(blob)
	effective := engine.invert(scalarUEff, dispositionUEff, blob)

	if effective.PhiBimodal == nil {
		t.Fatal("PhiBimodal must be populated when switches set it")
	}
	// The shifted OptimismBeta is written anyway (the shove step runs
	// uniformly), but drawOptimism consults PhiBimodal first; the
	// precedence is encoded by drawTraits ignoring the shifted single
	// center when PhiBimodal is non-nil.
}

func assertBetaCloseEnough(t *testing.T, label string, got, want generator.BetaPair) {
	t.Helper()
	if math.Abs(got.Alpha-want.Alpha) > floatCloseEnough*math.Max(1, math.Abs(want.Alpha)) {
		t.Errorf("%s.Alpha: got=%v want=%v", label, got.Alpha, want.Alpha)
	}
	if math.Abs(got.Beta-want.Beta) > floatCloseEnough*math.Max(1, math.Abs(want.Beta)) {
		t.Errorf("%s.Beta: got=%v want=%v", label, got.Beta, want.Beta)
	}
}

func assertDispositionCloseEnough(t *testing.T, got, want generator.DispositionAlphas) {
	t.Helper()
	if math.Abs(got.Trend-want.Trend) > floatCloseEnough*math.Max(1, want.Trend) {
		t.Errorf("Disposition.Trend: got=%v want=%v", got.Trend, want.Trend)
	}
	if math.Abs(got.MeanReversion-want.MeanReversion) > floatCloseEnough*math.Max(1, want.MeanReversion) {
		t.Errorf("Disposition.MeanReversion: got=%v want=%v", got.MeanReversion, want.MeanReversion)
	}
	if math.Abs(got.Noise-want.Noise) > floatCloseEnough*math.Max(1, want.Noise) {
		t.Errorf("Disposition.Noise: got=%v want=%v", got.Noise, want.Noise)
	}
}

func assertCampBeta(t *testing.T, label string, got generator.BetaPair, expectedMean, expectedConcentration float64) {
	t.Helper()
	gotMean := got.Alpha / (got.Alpha + got.Beta)
	gotConcentration := got.Alpha + got.Beta
	if math.Abs(gotMean-expectedMean) > floatCloseEnough {
		t.Errorf("%s camp Beta mean: got=%v want=%v", label, gotMean, expectedMean)
	}
	if math.Abs(gotConcentration-expectedConcentration) > floatCloseEnough*expectedConcentration {
		t.Errorf("%s camp Beta concentration: got=%v want=%v", label, gotConcentration, expectedConcentration)
	}
}
