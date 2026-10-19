package scenario

import (
	"reflect"
	"strings"
	"testing"
)

// TestDefaultRegistryLoadsClean asserts the shipped presets validate
// against the default catalogue. A direction key that drifted out of
// the catalogue would surface here, before any live wiring.
func TestDefaultRegistryLoadsClean(t *testing.T) {
	registry, err := Load(DefaultRegistry(), DefaultCatalogue())
	if err != nil {
		t.Fatalf("Load(DefaultRegistry) returned error: %v", err)
	}

	expectedNames := []string{
		ScenarioNameBusy,
		ScenarioNameCalm,
		ScenarioNameDispersion,
		ScenarioNameNormal,
		ScenarioNameThin,
	}
	if got := registry.Names(); !reflect.DeepEqual(got, expectedNames) {
		t.Fatalf("Names() = %v, want %v (sorted)", got, expectedNames)
	}
}

// TestLoadRejectsUnknownDirectionKey proves the catalogue check is
// enforced: a typo in a direction key fails the load with an error
// that names both the scenario and the bad knob, so an operator
// debugging a startup failure has enough context to find the fix.
func TestLoadRejectsUnknownDirectionKey(t *testing.T) {
	bad := map[string]ScenarioBlob{
		"typo": {
			Direction: Direction{"aggressiveness.MEAN": +1},
			Intensity: 1,
		},
	}
	_, err := Load(bad, DefaultCatalogue())
	if err == nil {
		t.Fatal("Load accepted unknown direction key")
	}
	if !strings.Contains(err.Error(), "typo") ||
		!strings.Contains(err.Error(), "aggressiveness.MEAN") {
		t.Errorf("error wording missed context: %v", err)
	}
}

// TestLoadRejectsDispositionInDirection pins the one-way split
// between scalar directions and the vector-valued disposition arrow:
// putting disposition in Direction is a programmer error, not a
// shove, and must be rejected at load.
func TestLoadRejectsDispositionInDirection(t *testing.T) {
	bad := map[string]ScenarioBlob{
		"misplaced": {
			Direction: Direction{KnobDisposition: +1},
			Intensity: 1,
		},
	}
	_, err := Load(bad, DefaultCatalogue())
	if err == nil {
		t.Fatal("Load accepted disposition in Direction")
	}
	if !strings.Contains(err.Error(), "DispositionDirection") {
		t.Errorf("error should point to DispositionDirection field: %v", err)
	}
}

// TestLoadRejectsUnknownDispositionComponent asserts the two
// disposition arrow slots are both validated. A misspelled component
// name must not survive load — otherwise the merge engine would read
// an unknown index and panic deep inside session startup instead of
// cleanly at process start.
func TestLoadRejectsUnknownDispositionComponent(t *testing.T) {
	for _, scenarioToward := range []string{"trending", "", "mean_rev"} {
		arrow := &DispositionArrow{
			Toward:   scenarioToward,
			AwayFrom: DispositionComponentNoise,
		}
		bad := map[string]ScenarioBlob{
			"bad_toward": {
				Direction:            Direction{},
				DispositionDirection: arrow,
				Intensity:            1,
			},
		}
		_, err := Load(bad, DefaultCatalogue())
		if err == nil {
			t.Fatalf("Load accepted bad Toward=%q", scenarioToward)
		}
	}

	bad := map[string]ScenarioBlob{
		"bad_away_from": {
			DispositionDirection: &DispositionArrow{
				Toward:   DispositionComponentTrend,
				AwayFrom: "noisey",
			},
			Intensity: 1,
		},
	}
	_, err := Load(bad, DefaultCatalogue())
	if err == nil || !strings.Contains(err.Error(), "AwayFrom") {
		t.Fatalf("Load should reject bad AwayFrom with specific error; got %v", err)
	}
}

// TestLoadRejectsSameTowardAndAwayFrom guards the "move mass between
// two different components" invariant — a toward==away_from arrow is
// a no-op dressed up as a shove and must be caught at load.
func TestLoadRejectsSameTowardAndAwayFrom(t *testing.T) {
	bad := map[string]ScenarioBlob{
		"self_cancel": {
			DispositionDirection: &DispositionArrow{
				Toward:   DispositionComponentTrend,
				AwayFrom: DispositionComponentTrend,
			},
			Intensity: 1,
		},
	}
	_, err := Load(bad, DefaultCatalogue())
	if err == nil {
		t.Fatal("Load accepted toward == away_from")
	}
}

// TestLoadValidatesBimodalShape covers the three BimodalConfig
// invariants Load enforces: separation in [0, 1), bull weight in
// [0, 1], within-spread strictly positive. Each is checked in its
// own sub-case so a failure diagnostic names the specific bad field.
func TestLoadValidatesBimodalShape(t *testing.T) {
	validRest := ScenarioBlob{Intensity: 1}

	cases := []struct {
		label  string
		bad    BimodalConfig
		expect string
	}{
		{"separation_negative", BimodalConfig{Separation: -0.1, BullWeight: 0.5, WithinSpread: 0.2}, "Separation"},
		{"separation_at_one", BimodalConfig{Separation: 1.0, BullWeight: 0.5, WithinSpread: 0.2}, "Separation"},
		{"bull_weight_above_one", BimodalConfig{Separation: 0.5, BullWeight: 1.1, WithinSpread: 0.2}, "BullWeight"},
		{"bull_weight_negative", BimodalConfig{Separation: 0.5, BullWeight: -0.01, WithinSpread: 0.2}, "BullWeight"},
		{"within_spread_zero", BimodalConfig{Separation: 0.5, BullWeight: 0.5, WithinSpread: 0}, "WithinSpread"},
	}
	for _, oneCase := range cases {
		t.Run(oneCase.label, func(t *testing.T) {
			bimodal := oneCase.bad
			blob := validRest
			blob.Switches = Switches{PhiBimodal: &bimodal}

			_, err := Load(map[string]ScenarioBlob{"x": blob}, DefaultCatalogue())
			if err == nil {
				t.Fatalf("Load accepted bad bimodal: %+v", oneCase.bad)
			}
			if !strings.Contains(err.Error(), oneCase.expect) {
				t.Errorf("error should mention %q: got %v", oneCase.expect, err)
			}
		})
	}
}

// TestRegistryGetReturnsRegisteredBlob asserts the lookup pathway
// hands back the stored blob by name, which is the merge engine's
// one point of registry contact.
func TestRegistryGetReturnsRegisteredBlob(t *testing.T) {
	registry, err := Load(DefaultRegistry(), DefaultCatalogue())
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	for _, scenarioName := range registry.Names() {
		got := registry.Get(scenarioName)
		if got.Intensity == 0 && scenarioName != ScenarioNameNormal {
			t.Errorf("preset %q has zero intensity; only %q should",
				scenarioName, ScenarioNameNormal)
		}
	}
}

// TestRegistryHasRejectsUnknownNames confirms the intake-side check
// returns false for anything that was not loaded.
func TestRegistryHasRejectsUnknownNames(t *testing.T) {
	registry, _ := Load(DefaultRegistry(), DefaultCatalogue())
	if registry.Has("thinnn") {
		t.Error("Has(thinnn) = true; want false")
	}
	if !registry.Has(ScenarioNameThin) {
		t.Error("Has(thin) = false; want true")
	}
}

// TestLoadIsDefensivelyCopied guards against the caller mutating the
// input map after load and seeing those changes through the
// Registry — the engine's blobs must be stable across the process.
func TestLoadIsDefensivelyCopied(t *testing.T) {
	input := DefaultRegistry()
	registry, err := Load(input, DefaultCatalogue())
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}

	// Mutate the caller's view after load.
	delete(input, ScenarioNameThin)

	if !registry.Has(ScenarioNameThin) {
		t.Error("deleting from the input map after Load affected the Registry")
	}
}
