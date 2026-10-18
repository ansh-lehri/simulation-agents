package scenario

import (
	"math"
	"testing"

	"pgregory.net/rapid"

	"simulation-agents/orchestrator/generator"
)

// baselineGenParams returns a minimally populated GenParams whose
// scalar-knob fields have known, calibration-realistic values. Only
// the dials the moments layer reads are populated — the rest stay
// zero because moments.go does not touch them. A trailing field
// outside this subset is deliberately irrelevant to the moment
// extraction, which keeps the test insulated from future GenParams
// additions.
func baselineGenParams() generator.GenParams {
	return generator.GenParams{
		HerdProbability:    0.3,
		SkillBeta:          generator.BetaPair{Alpha: 2, Beta: 3},
		AggressivenessBeta: generator.BetaPair{Alpha: 2, Beta: 2},
		PnlReactivityBeta:  generator.BetaPair{Alpha: 1.5, Beta: 4},
		PActBeta:           generator.BetaPair{Alpha: 1, Beta: 1},
		ReactivityBeta:     generator.BetaPair{Alpha: 2, Beta: 5},
		OptimismBeta:       generator.BetaPair{Alpha: 3, Beta: 3},
		CapitalSizeLogNormal: generator.LogNormalPair{
			Mu:    math.Log(1000),
			Sigma: 1.2,
		},
		DispositionDirichletAlphas: generator.DispositionAlphas{
			Trend:         2,
			MeanReversion: 1,
			Noise:         1,
		},
	}
}

// scalarKnobsUnderTest enumerates every scalar-moment knob the layer
// supports. Keeping the slice next to the tests means a new knob is
// obvious to add here when its catalogue entry lands.
func scalarKnobsUnderTest() []string {
	return []string{
		KnobAggressivenessMean,
		KnobSkillMean,
		KnobPnlReactivityMean,
		KnobReactivityMean,
		KnobPActMean,
		KnobHerdProbability,
		KnobCapitalMedian,
		KnobCapitalSpread,
		KnobOptimismCenter,
	}
}

// TestScalarMomentIdentityRoundTrip asserts the baseline invariant
// every shove step depends on: reading a moment and immediately
// writing it back must leave GenParams byte-identical. A drift here
// would mean the method-of-moments mapping is lossy, which would
// corrupt the baseline every time Effective("") runs through the full
// engine in later stages.
func TestScalarMomentIdentityRoundTrip(t *testing.T) {
	for _, knobName := range scalarKnobsUnderTest() {
		knobName := knobName
		t.Run(knobName, func(t *testing.T) {
			baseline := baselineGenParams()
			moment := scalarMoment(baseline, knobName)
			rewritten := baseline
			setScalarMoment(&rewritten, knobName, moment)
			recovered := scalarMoment(rewritten, knobName)
			if math.Abs(recovered-moment) > floatCloseEnough {
				t.Fatalf("scalar moment round-trip drifted: knob=%s baseline=%v recovered=%v",
					knobName, moment, recovered)
			}
		})
	}
}

// TestScalarMomentShiftRoundTrip asserts that writing an arbitrary
// new moment and reading it back recovers the written value. This is
// the property the merge engine relies on when inverting a shoved
// link value back into a distribution parameter.
func TestScalarMomentShiftRoundTrip(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		for _, knobName := range scalarKnobsUnderTest() {
			newMoment := drawPlausibleMomentFor(propertyRun, knobName)
			params := baselineGenParams()
			setScalarMoment(&params, knobName, newMoment)
			recovered := scalarMoment(params, knobName)
			if math.Abs(recovered-newMoment) > floatCloseEnough*math.Max(1, math.Abs(newMoment)) {
				propertyRun.Fatalf("shift round-trip drifted: knob=%s written=%v recovered=%v",
					knobName, newMoment, recovered)
			}
		}
	})
}

// drawPlausibleMomentFor samples a new moment in the valid domain of
// a given knob — a probability for a Logit knob, a positive scalar
// for a Log knob, and a (-1, +1) center for the Arctanh knob. Keeping
// the generator here (rather than inlining per-knob draws) centralizes
// the domain conventions each knob is tested against.
func drawPlausibleMomentFor(propertyRun *rapid.T, knobName string) float64 {
	switch knobName {
	case KnobCapitalMedian, KnobCapitalSpread:
		return rapid.Float64Range(0.01, 10000).Draw(propertyRun, knobName)
	case KnobOptimismCenter:
		return rapid.Float64Range(-0.99, 0.99).Draw(propertyRun, knobName)
	default:
		return rapid.Float64Range(0.01, 0.99).Draw(propertyRun, knobName)
	}
}

// TestSetBetaMeanPreservesConcentration pins the method-of-moments
// contract that keeps the Beta family's tightness stable under a
// shove: writing a new mean must leave α + β unchanged, so a shifted
// scenario tilts the distribution's location without widening or
// sharpening it.
func TestSetBetaMeanPreservesConcentration(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		alpha := rapid.Float64Range(0.5, 20).Draw(propertyRun, "alpha")
		beta := rapid.Float64Range(0.5, 20).Draw(propertyRun, "beta")
		newMean := rapid.Float64Range(0.01, 0.99).Draw(propertyRun, "new_mean")

		baselinePair := generator.BetaPair{Alpha: alpha, Beta: beta}
		baselineConcentration := baselinePair.Alpha + baselinePair.Beta
		updated := setBetaMean(baselinePair, newMean)
		updatedConcentration := updated.Alpha + updated.Beta
		if math.Abs(updatedConcentration-baselineConcentration) > floatCloseEnough*baselineConcentration {
			propertyRun.Fatalf("concentration changed: baseline=%v updated=%v", baselineConcentration, updatedConcentration)
		}
		if math.Abs(betaMean(updated)-newMean) > floatCloseEnough {
			propertyRun.Fatalf("new mean not realized: wanted=%v got=%v", newMean, betaMean(updated))
		}
	})
}

// TestSetOptimismCenterPreservesConcentration asserts the center-dial
// path reuses setBetaMean cleanly: the concentration survives, and
// the realized center matches what was written through the 2β - 1
// convention.
func TestSetOptimismCenterPreservesConcentration(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		alpha := rapid.Float64Range(0.5, 20).Draw(propertyRun, "alpha")
		beta := rapid.Float64Range(0.5, 20).Draw(propertyRun, "beta")
		newCenter := rapid.Float64Range(-0.95, 0.95).Draw(propertyRun, "new_center")

		baselinePair := generator.BetaPair{Alpha: alpha, Beta: beta}
		baselineConcentration := baselinePair.Alpha + baselinePair.Beta
		updated := setOptimismCenter(baselinePair, newCenter)
		updatedConcentration := updated.Alpha + updated.Beta
		if math.Abs(updatedConcentration-baselineConcentration) > floatCloseEnough*baselineConcentration {
			propertyRun.Fatalf("concentration changed: baseline=%v updated=%v", baselineConcentration, updatedConcentration)
		}
		if math.Abs(optimismCenter(updated)-newCenter) > floatCloseEnough {
			propertyRun.Fatalf("new center not realized: wanted=%v got=%v", newCenter, optimismCenter(updated))
		}
	})
}

// TestLogNormalKnobsAreIndependent asserts that shoving the median
// does not perturb σ and shoving the spread does not perturb μ — the
// two capital knobs must compose without a hidden cross-coupling.
func TestLogNormalKnobsAreIndependent(t *testing.T) {
	baseline := generator.LogNormalPair{Mu: math.Log(500), Sigma: 1.3}

	medianOnly := setLogNormalMedian(baseline, 1500)
	if medianOnly.Sigma != baseline.Sigma {
		t.Errorf("setLogNormalMedian changed Sigma: baseline=%v updated=%v", baseline.Sigma, medianOnly.Sigma)
	}
	if math.Abs(logNormalMedian(medianOnly)-1500) > floatCloseEnough*1500 {
		t.Errorf("setLogNormalMedian: median wrong: got=%v", logNormalMedian(medianOnly))
	}

	spreadOnly := setLogNormalSpread(baseline, 2.5)
	if spreadOnly.Mu != baseline.Mu {
		t.Errorf("setLogNormalSpread changed Mu: baseline=%v updated=%v", baseline.Mu, spreadOnly.Mu)
	}
	if logNormalSpread(spreadOnly) != 2.5 {
		t.Errorf("setLogNormalSpread: spread wrong: got=%v", logNormalSpread(spreadOnly))
	}
}

// TestDispositionProportionsSumToOne guards the two invariants the
// generator relies on when consuming the shifted Dirichlet mean: the
// proportions sum to 1 and each component is strictly positive.
func TestDispositionProportionsSumToOne(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		trendAlpha := rapid.Float64Range(0.1, 10).Draw(propertyRun, "trend_alpha")
		meanRevAlpha := rapid.Float64Range(0.1, 10).Draw(propertyRun, "mean_rev_alpha")
		noiseAlpha := rapid.Float64Range(0.1, 10).Draw(propertyRun, "noise_alpha")

		alphas := generator.DispositionAlphas{
			Trend:         trendAlpha,
			MeanReversion: meanRevAlpha,
			Noise:         noiseAlpha,
		}
		proportions := dispositionProportions(alphas)
		total := 0.0
		for _, proportion := range proportions {
			if !(proportion > 0) {
				propertyRun.Fatalf("non-positive disposition proportion: %v", proportions)
			}
			total += proportion
		}
		if math.Abs(total-1) > floatCloseEnough {
			propertyRun.Fatalf("disposition proportions do not sum to 1: sum=%v proportions=%v", total, proportions)
		}
	})
}

// TestSetDispositionProportionsPreservesConcentration asserts that
// writing a shifted proportion vector back into DispositionAlphas
// leaves the baseline α_total unchanged — the Dirichlet tightness
// the calibration chose must not drift when a scenario moves only
// the center of the simplex.
func TestSetDispositionProportionsPreservesConcentration(t *testing.T) {
	baseline := generator.DispositionAlphas{Trend: 2, MeanReversion: 1, Noise: 1}
	baselineTotal := baseline.Trend + baseline.MeanReversion + baseline.Noise

	shiftedProportions := []float64{0.1, 0.1, 0.8}
	updated := setDispositionProportions(baseline, shiftedProportions)
	updatedTotal := updated.Trend + updated.MeanReversion + updated.Noise
	if math.Abs(updatedTotal-baselineTotal) > floatCloseEnough*baselineTotal {
		t.Errorf("Dirichlet concentration drifted: baseline=%v updated=%v", baselineTotal, updatedTotal)
	}

	recovered := dispositionProportions(updated)
	for index := range shiftedProportions {
		if math.Abs(recovered[index]-shiftedProportions[index]) > floatCloseEnough {
			t.Errorf("proportion[%d] not realized: wanted=%v got=%v",
				index, shiftedProportions[index], recovered[index])
		}
	}
}

// TestScalarMomentPanicsOnUnknownKnob keeps the dispatcher honest: a
// misrouted knob name must blow up loudly, not silently return zero.
func TestScalarMomentPanicsOnUnknownKnob(t *testing.T) {
	assertPanics(t, "scalarMoment(unknown)",
		func() { _ = scalarMoment(baselineGenParams(), "not.a.real.knob") })
	assertPanics(t, "setScalarMoment(unknown)",
		func() {
			params := baselineGenParams()
			setScalarMoment(&params, "not.a.real.knob", 0.5)
		})
}

// TestScalarMomentPanicsOnDispositionKnob guards the one-way split
// between scalar and vector dispatch: disposition must never be
// routed through the scalar path.
func TestScalarMomentPanicsOnDispositionKnob(t *testing.T) {
	assertPanics(t, "scalarMoment(disposition)",
		func() { _ = scalarMoment(baselineGenParams(), KnobDisposition) })
	assertPanics(t, "setScalarMoment(disposition)",
		func() {
			params := baselineGenParams()
			setScalarMoment(&params, KnobDisposition, 0.5)
		})
}

// TestSetDispositionProportionsPanicsOnWrongLength pins the shape
// invariant: the caller must hand over a length-three vector or the
// bug surfaces here, not in a mis-sized GenParams.
func TestSetDispositionProportionsPanicsOnWrongLength(t *testing.T) {
	assertPanics(t, "setDispositionProportions(len=2)", func() {
		setDispositionProportions(generator.DispositionAlphas{Trend: 1, MeanReversion: 1, Noise: 1},
			[]float64{0.5, 0.5})
	})
}
