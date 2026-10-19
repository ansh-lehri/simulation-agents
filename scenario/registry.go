package scenario

import (
	"fmt"
	"sort"
)

// Registry is the validated set of named scenarios the engine can
// resolve. It is immutable at runtime: Load builds it once at
// startup from a map of blobs, every blob is validated against the
// catalogue, and callers read through Get / Has / Names.
//
// A Registry built by Load guarantees:
//   - Every Direction key is a scalar knob known to the catalogue.
//   - The disposition knob never appears in Direction (it has its
//     own vector-valued arrow field).
//   - Any non-nil DispositionDirection names valid, distinct slots.
//   - A non-nil BimodalConfig has sane shape parameters.
//
// With these invariants, the merge engine can shove blindly — a
// stored direction key is a safe lookup key for the baseline's u0
// and eta maps.
type Registry struct {
	blobs map[string]ScenarioBlob
}

// Load builds a validated Registry from a map of named blobs. The
// catalogue is consulted to validate every direction key against
// the known scalar-knob set. On any violation Load returns an error
// naming the specific scenario and knob; the composition root fails
// the process at startup rather than letting an unknown knob reach
// the merge engine at session time.
func Load(blobs map[string]ScenarioBlob, catalogue KnobCatalogue) (Registry, error) {
	for scenarioName, blob := range blobs {
		if err := validateBlob(scenarioName, blob, catalogue); err != nil {
			return Registry{}, err
		}
	}

	// Defensive copy of the input map so a later mutation of the
	// caller's map (an edit of a global, for example) cannot change
	// the Registry's contents mid-run.
	cloned := make(map[string]ScenarioBlob, len(blobs))
	for scenarioName, blob := range blobs {
		cloned[scenarioName] = blob
	}
	return Registry{blobs: cloned}, nil
}

// validateBlob runs the per-blob invariants. Split out from Load so
// the error path reads as one named check per rule.
func validateBlob(
	scenarioName string,
	blob ScenarioBlob,
	catalogue KnobCatalogue,
) error {
	for knobName := range blob.Direction {
		if knobName == KnobDisposition {
			return fmt.Errorf(
				"scenario %q: use DispositionDirection for the disposition knob, not Direction",
				scenarioName,
			)
		}
		if !catalogue.Has(knobName) {
			return fmt.Errorf(
				"scenario %q: unknown knob %q in Direction",
				scenarioName, knobName,
			)
		}
	}

	if blob.DispositionDirection != nil {
		arrow := blob.DispositionDirection
		if !validDispositionComponent(arrow.Toward) {
			return fmt.Errorf(
				"scenario %q: unknown disposition component %q in DispositionDirection.Toward",
				scenarioName, arrow.Toward,
			)
		}
		if !validDispositionComponent(arrow.AwayFrom) {
			return fmt.Errorf(
				"scenario %q: unknown disposition component %q in DispositionDirection.AwayFrom",
				scenarioName, arrow.AwayFrom,
			)
		}
		if arrow.Toward == arrow.AwayFrom {
			return fmt.Errorf(
				"scenario %q: DispositionDirection Toward and AwayFrom must name different components (both %q)",
				scenarioName, arrow.Toward,
			)
		}
	}

	if blob.Switches.PhiBimodal != nil {
		bimodal := blob.Switches.PhiBimodal
		if !(bimodal.Separation >= 0 && bimodal.Separation < 1) {
			return fmt.Errorf(
				"scenario %q: PhiBimodal.Separation %v must be in [0, 1)",
				scenarioName, bimodal.Separation,
			)
		}
		if !(bimodal.BullWeight >= 0 && bimodal.BullWeight <= 1) {
			return fmt.Errorf(
				"scenario %q: PhiBimodal.BullWeight %v must be in [0, 1]",
				scenarioName, bimodal.BullWeight,
			)
		}
		if !(bimodal.WithinSpread > 0) {
			return fmt.Errorf(
				"scenario %q: PhiBimodal.WithinSpread %v must be positive",
				scenarioName, bimodal.WithinSpread,
			)
		}
	}

	return nil
}

// Get returns the scenario blob for the given name. The caller must
// have validated presence via Has (or at intake); Get returns the
// zero blob for an unknown name, which the engine treats as an
// invariant violation.
func (registry Registry) Get(scenarioName string) ScenarioBlob {
	return registry.blobs[scenarioName]
}

// Has reports whether a scenario name is registered. Intake uses it
// to reject unknown names at the earliest layer so a bring-up
// cannot silently fall through to baseline.
func (registry Registry) Has(scenarioName string) bool {
	_, present := registry.blobs[scenarioName]
	return present
}

// Names returns every registered scenario name in sorted order.
// Sorted so transport-level validation and operator-facing listings
// read in a stable order and so tests comparing name lists do not
// depend on map iteration.
func (registry Registry) Names() []string {
	names := make([]string, 0, len(registry.blobs))
	for scenarioName := range registry.blobs {
		names = append(names, scenarioName)
	}
	sort.Strings(names)
	return names
}
