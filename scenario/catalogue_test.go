package scenario

import "testing"

// TestDefaultCatalogueCoversAllKnobConstants asserts that every knob
// name constant declared in catalogue.go is wired into the default
// catalogue with a ruler. A new knob added without a catalogue entry
// would otherwise appear valid at the string-constant level but
// silently fail the registry's name validation at startup.
func TestDefaultCatalogueCoversAllKnobConstants(t *testing.T) {
	expectedRulers := map[string]Ruler{
		KnobAggressivenessMean:  Logit,
		KnobSkillMean:           Logit,
		KnobPnlReactivityMean:   Logit,
		KnobReactivityMean:      Logit,
		KnobPActMean:            Logit,
		KnobHerdProbability:     Logit,
		KnobCapitalMedian:       Log,
		KnobCapitalSpread:       Log,
		KnobDisposition:         LogRatio,
		KnobOptimismCenter:      Arctanh,
		KnobPresenceEntryRate:   Log,
		KnobPresenceStepOutRate: Log,
		KnobPresenceDoneRate:    Log,
		KnobPresenceReentryRate: Log,
	}

	catalogue := DefaultCatalogue()
	if len(catalogue) != len(expectedRulers) {
		t.Fatalf("catalogue size mismatch: got %d, want %d", len(catalogue), len(expectedRulers))
	}
	for knobName, expectedRuler := range expectedRulers {
		actualRuler, present := catalogue.RulerFor(knobName)
		if !present {
			t.Errorf("knob %q missing from DefaultCatalogue", knobName)
			continue
		}
		if actualRuler != expectedRuler {
			t.Errorf("knob %q: ruler = %v, want %v", knobName, actualRuler, expectedRuler)
		}
	}
}

// TestCatalogueHasRejectsUnknownNames pins the behavior the registry
// loader depends on: a misspelled direction key reports absent so the
// loader can fail fast at startup with a specific error.
func TestCatalogueHasRejectsUnknownNames(t *testing.T) {
	catalogue := DefaultCatalogue()
	for _, unknownName := range []string{
		"",
		"aggressiveness",             // missing suffix
		"aggressiveness.MEAN",        // wrong case
		"optimism.centre",            // British spelling typo
		"presence.entry",             // missing _rate suffix
		"not.a.real.knob",
	} {
		if catalogue.Has(unknownName) {
			t.Errorf("Has(%q) = true, want false", unknownName)
		}
		if _, present := catalogue.RulerFor(unknownName); present {
			t.Errorf("RulerFor(%q) returned present=true, want false", unknownName)
		}
	}
}

// TestCatalogueHasAcceptsKnownNames round-trips every exported knob
// name through the lookup — a changed string value would be caught
// here before the registry loader got a chance to accept a bad name.
func TestCatalogueHasAcceptsKnownNames(t *testing.T) {
	catalogue := DefaultCatalogue()
	for _, knownName := range []string{
		KnobAggressivenessMean, KnobSkillMean, KnobPnlReactivityMean,
		KnobReactivityMean, KnobPActMean, KnobHerdProbability,
		KnobCapitalMedian, KnobCapitalSpread, KnobDisposition,
		KnobOptimismCenter,
		KnobPresenceEntryRate, KnobPresenceStepOutRate,
		KnobPresenceDoneRate, KnobPresenceReentryRate,
	} {
		if !catalogue.Has(knownName) {
			t.Errorf("Has(%q) = false, want true", knownName)
		}
	}
}
