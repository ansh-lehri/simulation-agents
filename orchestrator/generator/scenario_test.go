package generator

import "testing"

// TestNoOpScenarioProviderReturnsBaselineForAnyInput pins the one promise
// the no-op provider makes: every call returns the baseline no-shift pair
// so the generator runs against the baseline calibration until a real
// provider lands.
func TestNoOpScenarioProviderReturnsBaselineForAnyInput(t *testing.T) {
	provider := NoOpScenarioProvider{}

	cases := []struct {
		country, scenarioName string
	}{
		{"", ""},
		{"IN", ""},
		{"IN", "flash_crash"},
		{"US", "polarized_optimism"},
	}
	for _, testCase := range cases {
		effectiveShifts, lifecycleCurveShifts, _ := provider.Effective(testCase.country, testCase.scenarioName)
		if effectiveShifts != nil {
			t.Fatalf("NoOp returned non-nil trait shifts for (%q,%q): %+v",
				testCase.country, testCase.scenarioName, effectiveShifts)
		}
		if lifecycleCurveShifts != nil {
			t.Fatalf("NoOp returned non-nil lifecycle-curve shifts for (%q,%q): %+v",
				testCase.country, testCase.scenarioName, lifecycleCurveShifts)
		}
	}
}

// TestBimodalOptimismStructComposition is a compile-time witness: the
// shapes needed for the one scenario-aware branch of the trait draw —
// EffectiveParams with a BimodalOptimism — can be built and read. If a
// future refactor removes or renames these fields this test fails loudly
// at compile time rather than silently through a downstream nil deref.
func TestBimodalOptimismStructComposition(t *testing.T) {
	effectiveShifts := &EffectiveParams{
		PhiBimodal: &BimodalOptimism{
			MixWeight:      0.5,
			FirstModeBeta:  BetaPair{Alpha: 2, Beta: 8},
			SecondModeBeta: BetaPair{Alpha: 8, Beta: 2},
		},
	}
	if effectiveShifts.PhiBimodal == nil {
		t.Fatalf("PhiBimodal unexpectedly nil after construction")
	}
	if effectiveShifts.PhiBimodal.MixWeight != 0.5 {
		t.Fatalf("MixWeight = %v, want 0.5", effectiveShifts.PhiBimodal.MixWeight)
	}
}
