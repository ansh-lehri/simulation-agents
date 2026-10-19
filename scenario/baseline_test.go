package scenario

import (
	"math"
	"testing"

	"simulation-agents/orchestrator/generator"
)

// baselinePresenceRates returns a small, non-degenerate presence
// baseline fixture used by every baseline test. The values are
// unitless placeholders — the baseline builder cares only that each
// rate is positive (so the Log ruler is defined) and that the four
// values are distinguishable (so a knob mis-routed to the wrong
// slot would show up as a wrong u0).
func baselinePresenceRates() PresenceBaseline {
	return PresenceBaseline{
		EntryRate:   10,
		StepOutRate: 2,
		DoneRate:    1,
		ReentryRate: 4,
	}
}

// TestBuildBaselinePopulatesEveryScalarKnob asserts every scalar
// catalogue entry gets both a u0 and an eta — a missing entry would
// mean the shove step reads from a zero map and silently applies a
// zero shift, which is one of the specific "lies" the no-clamp
// design was built to avoid.
func TestBuildBaselinePopulatesEveryScalarKnob(t *testing.T) {
	baseline := BuildBaseline(baselineGenParams(), baselinePresenceRates(), DefaultCatalogue(), 42)

	for knobName := range DefaultCatalogue() {
		if knobName == KnobDisposition {
			continue
		}
		if _, present := baseline.scalarU0[knobName]; !present {
			t.Errorf("scalar u0 missing for knob %q", knobName)
		}
		if _, present := baseline.scalarEta[knobName]; !present {
			t.Errorf("scalar eta missing for knob %q", knobName)
		}
	}
	if len(baseline.scalarU0) != len(DefaultCatalogue())-1 {
		t.Errorf("scalar u0 size: got %d, want %d (catalogue minus disposition)",
			len(baseline.scalarU0), len(DefaultCatalogue())-1)
	}
}

// TestBuildBaselineIsDeterministic pins the reproducibility
// invariant: the same seed, params, and presence yields identical
// u0 and eta across two builds. A regression here would mean a
// non-deterministic map iteration (which the shove consumes in
// catalogue order) or a time-based input had slipped into the
// baseline path.
func TestBuildBaselineIsDeterministic(t *testing.T) {
	firstBuild := BuildBaseline(baselineGenParams(), baselinePresenceRates(), DefaultCatalogue(), 7)
	secondBuild := BuildBaseline(baselineGenParams(), baselinePresenceRates(), DefaultCatalogue(), 7)

	for knobName, firstU0 := range firstBuild.scalarU0 {
		secondU0 := secondBuild.scalarU0[knobName]
		if firstU0 != secondU0 {
			t.Errorf("u0 drift for %q: first=%v second=%v", knobName, firstU0, secondU0)
		}
	}
	for knobName, firstEta := range firstBuild.scalarEta {
		secondEta := secondBuild.scalarEta[knobName]
		if firstEta != secondEta {
			t.Errorf("eta drift for %q: first=%v second=%v", knobName, firstEta, secondEta)
		}
	}
	for componentIndex := range firstBuild.dispositionU0 {
		if firstBuild.dispositionU0[componentIndex] != secondBuild.dispositionU0[componentIndex] {
			t.Errorf("disposition u0 drift at %d: first=%v second=%v",
				componentIndex, firstBuild.dispositionU0[componentIndex], secondBuild.dispositionU0[componentIndex])
		}
		if firstBuild.dispositionEta[componentIndex] != secondBuild.dispositionEta[componentIndex] {
			t.Errorf("disposition eta drift at %d: first=%v second=%v",
				componentIndex, firstBuild.dispositionEta[componentIndex], secondBuild.dispositionEta[componentIndex])
		}
	}
}

// TestBuildBaselineEtaIsStrictlyPositive asserts every knob's
// crowd-width is strictly greater than zero. A zero eta would make
// the shove formula return the baseline unchanged for that knob
// regardless of intensity — a silent no-op the design forbids.
func TestBuildBaselineEtaIsStrictlyPositive(t *testing.T) {
	baseline := BuildBaseline(baselineGenParams(), baselinePresenceRates(), DefaultCatalogue(), 11)

	for knobName, eta := range baseline.scalarEta {
		if !(eta > 0) {
			t.Errorf("eta non-positive for %q: %v", knobName, eta)
		}
	}
	for componentIndex, eta := range baseline.dispositionEta {
		if !(eta > 0) {
			t.Errorf("disposition eta non-positive at %d: %v", componentIndex, eta)
		}
	}
}

// TestBuildBaselineParameterKnobsUseUnitEta pins the documented
// convention for knobs that have no baseline distribution to
// sample: their eta is exactly parameterKnobEta. A reader looking at
// the shove math for a parameter knob can then read s * d * 1 == s * d
// and understand that intensity moves the knob by exactly s * d
// ruler units.
func TestBuildBaselineParameterKnobsUseUnitEta(t *testing.T) {
	baseline := BuildBaseline(baselineGenParams(), baselinePresenceRates(), DefaultCatalogue(), 3)

	parameterKnobs := []string{
		KnobHerdProbability,
		KnobCapitalSpread,
		KnobPresenceEntryRate,
		KnobPresenceStepOutRate,
		KnobPresenceDoneRate,
		KnobPresenceReentryRate,
	}
	for _, knobName := range parameterKnobs {
		eta := baseline.scalarEta[knobName]
		if eta != parameterKnobEta {
			t.Errorf("parameter knob %q: eta=%v, want %v (one ruler-unit per unit of s*d)",
				knobName, eta, parameterKnobEta)
		}
	}
}

// TestBuildBaselineU0MatchesLinkOfBaselineMoment asserts a specific
// value identity: for every scalar knob, the baseline u0 is exactly
// toLink(ruler, baseline moment). A mis-wired u0 would silently
// shift the baseline center every call, independent of any shove.
func TestBuildBaselineU0MatchesLinkOfBaselineMoment(t *testing.T) {
	params := baselineGenParams()
	presence := baselinePresenceRates()
	baseline := BuildBaseline(params, presence, DefaultCatalogue(), 123)

	for knobName, ruler := range DefaultCatalogue() {
		if knobName == KnobDisposition {
			continue
		}
		expectedMoment := baselineScalarMoment(params, presence, knobName)
		expectedU0 := toLink(ruler, expectedMoment)
		if baseline.scalarU0[knobName] != expectedU0 {
			t.Errorf("u0 mismatch for %q: got=%v want=%v",
				knobName, baseline.scalarU0[knobName], expectedU0)
		}
	}
}

// TestBuildBaselineDispositionU0MatchesLogOfBaselineProportions
// asserts the vector-valued disposition u0 pathway is exact: the
// three stored log-ratios equal the component-wise log of the
// baseline Dirichlet proportions.
func TestBuildBaselineDispositionU0MatchesLogOfBaselineProportions(t *testing.T) {
	params := baselineGenParams()
	baseline := BuildBaseline(params, baselinePresenceRates(), DefaultCatalogue(), 55)

	baselineProportions := dispositionProportions(params.DispositionDirichletAlphas)
	expected := toLogRatio(baselineProportions)
	for componentIndex := range expected {
		if math.Abs(baseline.dispositionU0[componentIndex]-expected[componentIndex]) > floatCloseEnough {
			t.Errorf("disposition u0 mismatch at %d: got=%v want=%v",
				componentIndex, baseline.dispositionU0[componentIndex], expected[componentIndex])
		}
	}
}

// TestBuildBaselineUsesPresenceBaselineForPresenceKnobs guards the
// routing split between GenParams-sourced and PresenceBaseline-
// sourced moments: a changed presence rate must change the
// corresponding u0 and nothing else.
func TestBuildBaselineUsesPresenceBaselineForPresenceKnobs(t *testing.T) {
	params := baselineGenParams()
	basePresence := baselinePresenceRates()
	alteredPresence := basePresence
	alteredPresence.EntryRate = basePresence.EntryRate * 2

	baseBuild := BuildBaseline(params, basePresence, DefaultCatalogue(), 1)
	alteredBuild := BuildBaseline(params, alteredPresence, DefaultCatalogue(), 1)

	if baseBuild.scalarU0[KnobPresenceEntryRate] == alteredBuild.scalarU0[KnobPresenceEntryRate] {
		t.Errorf("doubling EntryRate did not change u0 for %q", KnobPresenceEntryRate)
	}
	// Every other scalar knob should land on the exact same u0 — a
	// crossed wire would show up here.
	for knobName, basedU0 := range baseBuild.scalarU0 {
		if knobName == KnobPresenceEntryRate {
			continue
		}
		if alteredBuild.scalarU0[knobName] != basedU0 {
			t.Errorf("knob %q changed when only EntryRate was altered: base=%v altered=%v",
				knobName, basedU0, alteredBuild.scalarU0[knobName])
		}
	}
}

// TestEmpiricalEtaFromLogNormalApproximatesSigma pins the analytic
// expectation for the one knob with a closed-form eta: the Log
// ruler on a log-normal collapses to the normal's standard
// deviation. Sampling noise at the chosen sample count stays well
// under five percent of sigma for the plausible sigma range.
func TestEmpiricalEtaFromLogNormalApproximatesSigma(t *testing.T) {
	for _, sigma := range []float64{0.5, 1.0, 1.5, 2.0} {
		params := baselineGenParams()
		params.CapitalSizeLogNormal = generator.LogNormalPair{Mu: math.Log(500), Sigma: sigma}
		baseline := BuildBaseline(params, baselinePresenceRates(), DefaultCatalogue(), 97)
		got := baseline.scalarEta[KnobCapitalMedian]
		if math.Abs(got-sigma) > 0.05*sigma {
			t.Errorf("CapitalMedian eta did not track sigma: sigma=%v got=%v (diff > 5%%)", sigma, got)
		}
	}
}
