package scenario

// Disposition component names. The disposition simplex has three
// slots — trend, mean-reversion, and noise — and a scenario names
// the one to shift mass toward and the one to shift mass away from
// using these strings. Keeping the names as constants means a typo
// becomes a load-time error (the registry rejects it), not a silent
// mis-shove at session start.
const (
	DispositionComponentTrend         = "trend"
	DispositionComponentMeanReversion = "mean_reversion"
	DispositionComponentNoise         = "noise"
)

// Direction is a scenario's per-scalar-knob arrow set. The map key is
// a knob name registered in the catalogue; the value is a signed
// weight (positive = shove the knob's moment up on its ruler,
// negative = shove it down). Magnitudes are prototype-equal (±1) in
// the shipped presets and tuned later via the measurement loop.
//
// The disposition knob is vector-valued and does NOT appear in this
// map — it has its own DispositionDirection field on ScenarioBlob so
// a reader cannot accidentally hand it a scalar weight.
type Direction map[string]float64

// DispositionArrow names the two disposition components a scenario
// moves mass between. Toward names the component the shove increases;
// AwayFrom names the one the shove decreases. The third component is
// held neutral by the merge engine's log-ratio shove, which assigns
// zero to any component not named in the arrow.
//
// Toward and AwayFrom must be different disposition component names;
// the registry loader rejects a direction that names the same slot
// for both.
type DispositionArrow struct {
	Toward   string
	AwayFrom string
}

// BimodalConfig is the three-slot calibration shape of the one
// scenario-aware generator branch — the two-camp optimism switch.
// Separation names the distance between the bull and bear camp
// centers on the (-1, +1) axis; BullWeight is the Bernoulli weight
// of the bull camp; WithinSpread is the shared width around each
// camp center. All three are calibration slots filled in the
// registry presets as placeholder values.
type BimodalConfig struct {
	Separation   float64
	BullWeight   float64
	WithinSpread float64
}

// Switches carries the absolute (not shoved) overrides a scenario
// applies on top of the shifted root dials. Every field is a pointer
// so a nil entry leaves that switch at its baseline; a non-nil
// value overrides it unconditionally.
//
// PhiBimodal, when non-nil, overrides any optimism.center direction
// arrow — a bimodal scenario is defined by its two camps, not by a
// shifted single center. The merge engine enforces that precedence.
type Switches struct {
	Tipper        *bool
	MinNoiseFloor *bool
	PhiBimodal    *BimodalConfig
}

// MetricTarget is the measurable market outcome a scenario is
// defined by — the "thin market has median spread at least three
// times baseline" style predicate. The scenario's intensity is fit
// to this target by the offline measurement loop (not implemented
// here); the engine itself never reads this field, it is carried for
// provenance and for the fitting tool.
//
// Metric names the quantity (e.g. "median_spread"); Relation is a
// comparator string (e.g. ">="); Value is the target magnitude; Unit
// names the comparison basis (e.g. "x_baseline"). All four are
// calibration slots; the engine does not validate them beyond
// non-emptiness checks the registry loader performs.
type MetricTarget struct {
	Metric   string
	Relation string
	Value    float64
	Unit     string
}

// ScenarioBlob is the full description of one named scenario — its
// per-knob direction, its intensity, its switches, and its target
// metric. Everything downstream reads this struct and nothing else.
//
// Direction holds scalar-knob arrows only; DispositionDirection
// holds the one vector-valued disposition arrow (or nil, meaning no
// disposition shift). Intensity is the single scalar that scales
// every arrow in the shove.
type ScenarioBlob struct {
	Direction            Direction
	DispositionDirection *DispositionArrow
	Intensity            float64
	Switches             Switches
	Target               MetricTarget
}

// PresenceBaseline carries the four baseline lifecycle-curve rates
// the scenario engine needs to compute shifted presence rates. These
// rates live outside GenParams because they belong to the lifecycle
// curve builder (a separate consumer that reads them to shape agent
// arrival / step-out / completion / re-entry). The engine takes them
// as an injected sidecar at construction time and never mutates
// them.
//
// Every rate is in events per unit time; the Log ruler shoves them
// so a scenario moves a rate's magnitude without ever producing a
// negative rate.
type PresenceBaseline struct {
	EntryRate   float64
	StepOutRate float64
	DoneRate    float64
	ReentryRate float64
}

// validDispositionComponent reports whether a given string names one
// of the three disposition slots. The registry loader uses this to
// reject a typo in a DispositionArrow at startup.
func validDispositionComponent(componentName string) bool {
	switch componentName {
	case DispositionComponentTrend,
		DispositionComponentMeanReversion,
		DispositionComponentNoise:
		return true
	}
	return false
}

// dispositionComponentIndex maps a disposition component name to its
// position in the length-three proportion vector the moments layer
// uses. The merge engine consults this to translate a scenario's
// toward/away-from arrow into the two signed entries of the shove
// vector. Panics on an unknown name — callers must have validated
// via validDispositionComponent first (the registry loader does).
func dispositionComponentIndex(componentName string) int {
	switch componentName {
	case DispositionComponentTrend:
		return DispositionTrendIndex
	case DispositionComponentMeanReversion:
		return DispositionMeanReversionIndex
	case DispositionComponentNoise:
		return DispositionNoiseIndex
	}
	panic("scenario: dispositionComponentIndex called with unknown component: " + componentName)
}
