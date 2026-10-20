package session

import (
	"testing"

	"simulation-agents/orchestrator/generator"
)

// TestBuildLifecycleCurvesDeterministic: fixed seed → identical
// outputs across two independent builds. Any drift in the GP draw
// order or the closure math would break every regression baseline a
// calibration run depends on.
func TestBuildLifecycleCurvesDeterministic(t *testing.T) {
	t.Parallel()

	params := DefaultLifecycleCurveParams()
	curvesOne := buildLifecycleCurves(nil, params, seededRNG())
	curvesTwo := buildLifecycleCurves(nil, params, seededRNG())

	sampleTimes := []float64{0.0, 0.1, 0.25, 0.5, 0.75, 0.9, 1.0}
	for _, normalizedTime := range sampleTimes {
		if curvesOne.stepOutRate(normalizedTime) != curvesTwo.stepOutRate(normalizedTime) {
			t.Fatalf("stepOutRate(%.2f): run1=%v run2=%v — determinism broken",
				normalizedTime, curvesOne.stepOutRate(normalizedTime), curvesTwo.stepOutRate(normalizedTime))
		}
		if curvesOne.doneRate(normalizedTime) != curvesTwo.doneRate(normalizedTime) {
			t.Fatalf("doneRate(%.2f): run1=%v run2=%v — determinism broken",
				normalizedTime, curvesOne.doneRate(normalizedTime), curvesTwo.doneRate(normalizedTime))
		}
	}

	idleDurations := []float64{0, 300, 600, 1200, 1800, 2400, 3600}
	for _, idleDuration := range idleDurations {
		if curvesOne.reentryProb(idleDuration) != curvesTwo.reentryProb(idleDuration) {
			t.Fatalf("reentryProb(%.0f): run1=%v run2=%v — determinism broken",
				idleDuration, curvesOne.reentryProb(idleDuration), curvesTwo.reentryProb(idleDuration))
		}
		if curvesOne.giveUpProb(idleDuration) != curvesTwo.giveUpProb(idleDuration) {
			t.Fatalf("giveUpProb(%.0f): run1=%v run2=%v — determinism broken",
				idleDuration, curvesOne.giveUpProb(idleDuration), curvesTwo.giveUpProb(idleDuration))
		}
	}
}

// TestBuildLifecycleCurvesStepOutZeroAtOpen: at normalized time t=0
// the ramp factor (1 − e^0) evaluates exactly to zero, so the
// step-out intensity is zero at session open regardless of the GP
// realization. An agent cannot step out the instant the session
// begins — the lifecycle design guarantees that.
func TestBuildLifecycleCurvesStepOutZeroAtOpen(t *testing.T) {
	t.Parallel()

	curves := buildLifecycleCurves(nil, DefaultLifecycleCurveParams(), seededRNG())
	if rate := curves.stepOutRate(0.0); rate != 0.0 {
		t.Fatalf("stepOutRate(0) = %v, want exactly 0.0 (ramp is zero at open)", rate)
	}
}

// TestBuildLifecycleCurvesDoneRisesNearClose: the done-early function
// must be larger near session close than at session open. With the
// baseline τ_done=0.25, the decay factor at t=0 is e^(−4)≈0.018
// while at t=1 it is e^0=1.0 — a 56× structural lift that a
// reasonably-sized GP wiggle cannot overcome.
func TestBuildLifecycleCurvesDoneRisesNearClose(t *testing.T) {
	t.Parallel()

	curves := buildLifecycleCurves(nil, DefaultLifecycleCurveParams(), seededRNG())
	atOpen := curves.doneRate(0.0)
	atClose := curves.doneRate(1.0)
	if atClose <= atOpen {
		t.Fatalf("doneRate(1.0)=%v must be > doneRate(0.0)=%v; curve is not close-heavy", atClose, atOpen)
	}
}

// TestBuildLifecycleCurvesReentryPeakAtMeanBreak: the re-entry
// function is a Gaussian centered at μ_break, so it must be strictly
// larger at μ_break than at symmetric points on either side. No GP
// is involved; the function is analytic.
func TestBuildLifecycleCurvesReentryPeakAtMeanBreak(t *testing.T) {
	t.Parallel()

	params := DefaultLifecycleCurveParams()
	curves := buildLifecycleCurves(nil, params, seededRNG())

	meanBreak := params.ReentryMeanBreak
	atPeak := curves.reentryProb(meanBreak)
	atEarlier := curves.reentryProb(meanBreak - params.ReentrySigmaBreak)
	atLater := curves.reentryProb(meanBreak + params.ReentrySigmaBreak)

	if atPeak <= atEarlier {
		t.Fatalf("reentryProb(μ=%v)=%v must be > reentryProb(μ-σ=%v)=%v",
			meanBreak, atPeak, meanBreak-params.ReentrySigmaBreak, atEarlier)
	}
	if atPeak <= atLater {
		t.Fatalf("reentryProb(μ=%v)=%v must be > reentryProb(μ+σ=%v)=%v",
			meanBreak, atPeak, meanBreak+params.ReentrySigmaBreak, atLater)
	}
}

// TestBuildLifecycleCurvesGiveUpZeroBeforeMean: the give-up sigmoid
// is clamped to exactly 0 for any idle duration at or below μ_break.
// An agent that has not been idle long enough to reach the typical
// break length must have zero give-up probability.
func TestBuildLifecycleCurvesGiveUpZeroBeforeMean(t *testing.T) {
	t.Parallel()

	params := DefaultLifecycleCurveParams()
	curves := buildLifecycleCurves(nil, params, seededRNG())

	for _, idleDuration := range []float64{0, 300, 600, params.ReentryMeanBreak} {
		if prob := curves.giveUpProb(idleDuration); prob != 0.0 {
			t.Fatalf("giveUpProb(%.0f) = %v, want 0.0 (idle <= μ_break=%.0f)",
				idleDuration, prob, params.ReentryMeanBreak)
		}
	}
}

// TestBuildLifecycleCurvesGiveUpRisesAfterMean: once idle duration
// exceeds μ_break the sigmoid begins rising, and a duration well
// past the give-up time constant τ should reach a substantial fraction
// of q_max.
func TestBuildLifecycleCurvesGiveUpRisesAfterMean(t *testing.T) {
	t.Parallel()

	params := DefaultLifecycleCurveParams()
	curves := buildLifecycleCurves(nil, params, seededRNG())

	// One give-up τ past the mean: 1 − e^(−1) ≈ 0.63 of q_max.
	durationOneTau := params.ReentryMeanBreak + params.GiveUpTau
	probOneTau := curves.giveUpProb(durationOneTau)
	if probOneTau <= 0 {
		t.Fatalf("giveUpProb(μ+τ=%.0f) = %v, want > 0", durationOneTau, probOneTau)
	}

	// Five τ past the mean should be close to q_max (1−e^(−5)≈0.993).
	durationFiveTau := params.ReentryMeanBreak + 5*params.GiveUpTau
	probFiveTau := curves.giveUpProb(durationFiveTau)
	if probFiveTau < 0.99*params.GiveUpMax {
		t.Fatalf("giveUpProb(μ+5τ=%.0f) = %v, want ≥ 0.99×q_max=%.4f",
			durationFiveTau, probFiveTau, 0.99*params.GiveUpMax)
	}
}

// TestBuildLifecycleCurvesScenarioOverrideScalesRates: a non-nil
// curveEffective replaces the three rate params before the GP draw.
// Two builds from identical RNG states — one nil, one with doubled
// rates — must produce step-out and done-early values in exactly
// the 2× ratio at every sample point, and re-entry probability
// must also scale by 2× at the peak.
func TestBuildLifecycleCurvesScenarioOverrideScalesRates(t *testing.T) {
	t.Parallel()

	params := DefaultLifecycleCurveParams()
	const scaleFactor = 2.0

	// Use the same PCG seed for both builds so the GP realizations are
	// identical; the only difference is the rate multiplier.
	baseline := buildLifecycleCurves(nil, params, seededRNG())
	override := buildLifecycleCurves(&generator.CurveEffective{
		StepOutRate: params.StepOutRate * scaleFactor,
		DoneRate:    params.DoneRate * scaleFactor,
		ReentryRate: params.ReentryRate * scaleFactor,
	}, params, seededRNG())

	const tolerance = 1e-12
	for _, normalizedTime := range []float64{0.1, 0.5, 0.9} {
		baselineStep := baseline.stepOutRate(normalizedTime)
		overrideStep := override.stepOutRate(normalizedTime)
		if baselineStep == 0 {
			continue
		}
		ratio := overrideStep / baselineStep
		if ratio < scaleFactor-tolerance || ratio > scaleFactor+tolerance {
			t.Fatalf("stepOutRate ratio at t=%.1f: got %v, want %v (scaleFactor=%v)",
				normalizedTime, ratio, scaleFactor, scaleFactor)
		}

		baseDone := baseline.doneRate(normalizedTime)
		overDone := override.doneRate(normalizedTime)
		if baseDone == 0 {
			continue
		}
		doneRatio := overDone / baseDone
		if doneRatio < scaleFactor-tolerance || doneRatio > scaleFactor+tolerance {
			t.Fatalf("doneRate ratio at t=%.1f: got %v, want %v (scaleFactor=%v)",
				normalizedTime, doneRatio, scaleFactor, scaleFactor)
		}
	}

	// Re-entry peak: override pMax = 2× baseline pMax → 2× Gaussian value.
	meanBreak := params.ReentryMeanBreak
	reentryRatio := override.reentryProb(meanBreak) / baseline.reentryProb(meanBreak)
	if reentryRatio < scaleFactor-tolerance || reentryRatio > scaleFactor+tolerance {
		t.Fatalf("reentryProb ratio at μ_break: got %v, want %v", reentryRatio, scaleFactor)
	}
}

// TestBuildLifecycleCurvesNilEffectiveUsesBaseline: a nil
// curveEffective is not an error condition — it means "no scenario
// shift". The call must succeed and produce the same results as
// explicitly supplying the baseline rates.
func TestBuildLifecycleCurvesNilEffectiveUsesBaseline(t *testing.T) {
	t.Parallel()

	params := DefaultLifecycleCurveParams()

	nilCurves := buildLifecycleCurves(nil, params, seededRNG())
	baselineOverride := buildLifecycleCurves(&generator.CurveEffective{
		EntryRate:   1.0,
		StepOutRate: params.StepOutRate,
		DoneRate:    params.DoneRate,
		ReentryRate: params.ReentryRate,
	}, params, seededRNG())

	for _, normalizedTime := range []float64{0.1, 0.5, 0.9} {
		if nilCurves.stepOutRate(normalizedTime) != baselineOverride.stepOutRate(normalizedTime) {
			t.Fatalf("stepOutRate(%.1f): nil=%v baseline-override=%v should match",
				normalizedTime, nilCurves.stepOutRate(normalizedTime), baselineOverride.stepOutRate(normalizedTime))
		}
		if nilCurves.doneRate(normalizedTime) != baselineOverride.doneRate(normalizedTime) {
			t.Fatalf("doneRate(%.1f): nil=%v baseline-override=%v should match",
				normalizedTime, nilCurves.doneRate(normalizedTime), baselineOverride.doneRate(normalizedTime))
		}
	}
	if nilCurves.reentryProb(params.ReentryMeanBreak) != baselineOverride.reentryProb(params.ReentryMeanBreak) {
		t.Fatalf("reentryProb(μ_break): nil=%v baseline-override=%v should match",
			nilCurves.reentryProb(params.ReentryMeanBreak), baselineOverride.reentryProb(params.ReentryMeanBreak))
	}
}

// TestBuildLifecycleCurvesPanicOnBadParams: zero or negative values
// for the structural params the math requires are a composition-root
// bug; buildLifecycleCurves must panic with a message naming the
// offending field.
func TestBuildLifecycleCurvesPanicOnBadParams(t *testing.T) {
	t.Parallel()

	good := DefaultLifecycleCurveParams()

	cases := []struct {
		name   string
		mutate func(*LifecycleCurveParams)
	}{
		{"zero StepOutTauRamp", func(p *LifecycleCurveParams) { p.StepOutTauRamp = 0 }},
		{"zero DoneTauRamp", func(p *LifecycleCurveParams) { p.DoneTauRamp = 0 }},
		{"zero StepOutGPLengthScale", func(p *LifecycleCurveParams) { p.StepOutGPLengthScale = 0 }},
		{"zero DoneGPLengthScale", func(p *LifecycleCurveParams) { p.DoneGPLengthScale = 0 }},
		{"zero ReentrySigmaBreak", func(p *LifecycleCurveParams) { p.ReentrySigmaBreak = 0 }},
		{"zero GiveUpTau", func(p *LifecycleCurveParams) { p.GiveUpTau = 0 }},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			params := good
			testCase.mutate(&params)
			assertPanics(t, func() {
				buildLifecycleCurves(nil, params, seededRNG())
			})
		})
	}
}

// assertPanics asserts that calling fn causes a panic, failing the
// test if it returns normally.
func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic; got normal return")
		}
	}()
	fn()
}
