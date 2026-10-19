package scenario

// Calibration slots. Every numeric below is a placeholder the shipped
// presets expose for later fitting — a reader opening this file sees
// at a glance which numbers are design values (the ±1 arrows in each
// preset's direction map) and which are "to be fit" (every constant
// below). The names end in "Slot" so a grep for "Slot" lists the
// entire inventory of un-calibrated values in one shot.
//
// How these get filled in: for each scenario the measurement loop
// runs an offline session at a candidate intensity, measures the
// target metric from cached market data, nudges intensity until the
// metric lands in the target band, and records the fit value. That
// tool is not implemented in this package; it reads this file as its
// input and writes back the fit values at calibration time.
const (
	intensityNormalSlot     = 0.0
	intensityThinSlot       = 1.0
	intensityCalmSlot       = 1.0
	intensityBusySlot       = 1.0
	intensityDispersionSlot = 1.0

	targetThinSpreadMultipleSlot          = 3.0
	targetCalmRangeWidthMultipleSlot      = 0.5
	targetBusyTradesPerMinuteMultipleSlot = 2.0
	targetDispersionVolMultipleSlot       = 2.0

	bimodalDispersionSeparationSlot   = 0.6
	bimodalDispersionBullWeightSlot   = 0.5
	bimodalDispersionWithinSpreadSlot = 0.2
)

// Preset metric names. Four known target metrics the measurement
// tool computes from a session's cached market data. The engine does
// not interpret these strings; they live on each blob's MetricTarget
// for the fitting tool and operator logs.
const (
	metricMedianSpread      = "median_spread"
	metricPriceRangeWidth   = "price_range_width"
	metricTradesPerMinute   = "trades_per_minute"
	metricRealizedVolatility = "realized_volatility"

	targetUnitBaselineMultiple = "x_baseline"
)

// Scenario names. Keeping them as constants makes the composition
// root's intake validation grep for them cleanly and keeps the
// transport-side rejection error wording stable across changes.
const (
	ScenarioNameNormal     = "normal"
	ScenarioNameThin       = "thin"
	ScenarioNameCalm       = "calm"
	ScenarioNameBusy       = "busy"
	ScenarioNameDispersion = "dispersion"
)

// DefaultRegistry returns the shipped placeholder scenarios in a
// form Load can validate: a map from scenario name to ScenarioBlob.
// Direction magnitudes are ±1 (the prototype's equal-magnitude
// convention — the "which sliders up, which down" shape only, no
// hand-tuned weights). Intensity values and bimodal shape parameters
// are the slot constants above. The directions themselves encode
// market cause→effect — defensible per scenario and spelled out in
// each blob's declaration.
//
// New scenarios should be added here (not loaded dynamically) so the
// catalogue-check at load time runs over every shipped preset at the
// same package-build moment the names become reachable.
func DefaultRegistry() map[string]ScenarioBlob {
	return map[string]ScenarioBlob{
		ScenarioNameNormal:     normalPreset(),
		ScenarioNameThin:       thinPreset(),
		ScenarioNameCalm:       calmPreset(),
		ScenarioNameBusy:       busyPreset(),
		ScenarioNameDispersion: dispersionPreset(),
	}
}

// normalPreset is the control scenario — no shove, no switches, no
// target. Effective("normal") still runs through the full engine
// (unlike Effective(""), which short-circuits to baseline identity),
// which is useful as an operator-visible "run the registry pathway
// but produce baseline results" check.
func normalPreset() ScenarioBlob {
	return ScenarioBlob{
		Direction: Direction{},
		Intensity: intensityNormalSlot,
	}
}

// thinPreset — the "thin liquidity" shape. The crowd shifts toward
// aggressive, shallow-pocket, fast-step-out participants; mean
// disposition tilts away from mean-reversion toward noise. The
// emergent book thins because the drawn crowd provides less resting
// liquidity and crosses more.
func thinPreset() ScenarioBlob {
	return ScenarioBlob{
		Direction: Direction{
			KnobAggressivenessMean:  +1,
			KnobCapitalMedian:       -1,
			KnobCapitalSpread:       -1,
			KnobPresenceStepOutRate: +1,
			KnobPresenceDoneRate:    +1,
		},
		DispositionDirection: &DispositionArrow{
			Toward:   DispositionComponentNoise,
			AwayFrom: DispositionComponentMeanReversion,
		},
		Intensity: intensityThinSlot,
		Target: MetricTarget{
			Metric:   metricMedianSpread,
			Relation: ">=",
			Value:    targetThinSpreadMultipleSlot,
			Unit:     targetUnitBaselineMultiple,
		},
	}
}

// calmPreset — the "calm market" shape. The crowd shifts toward
// patient, deeper-pocket participants; disposition tilts toward
// mean-reversion. P&L reactivity drops, aggressiveness drops, and
// step-out rates fall. Low spread and narrow range emerge from the
// deeper, less-twitchy book.
func calmPreset() ScenarioBlob {
	return ScenarioBlob{
		Direction: Direction{
			KnobCapitalMedian:       +1,
			KnobPnlReactivityMean:   -1,
			KnobSkillMean:           +1,
			KnobAggressivenessMean:  -1,
			KnobPresenceStepOutRate: -1,
		},
		DispositionDirection: &DispositionArrow{
			Toward:   DispositionComponentMeanReversion,
			AwayFrom: DispositionComponentNoise,
		},
		Intensity: intensityCalmSlot,
		Target: MetricTarget{
			Metric:   metricPriceRangeWidth,
			Relation: "<=",
			Value:    targetCalmRangeWidthMultipleSlot,
			Unit:     targetUnitBaselineMultiple,
		},
	}
}

// busyPreset — the "busy market" shape. More arrivals, fewer
// completions, higher aggressiveness. The emergent floor is thicker
// and trades per minute rises against the baseline rate.
func busyPreset() ScenarioBlob {
	return ScenarioBlob{
		Direction: Direction{
			KnobPresenceEntryRate:  +1,
			KnobPresenceDoneRate:   -1,
			KnobAggressivenessMean: +1,
		},
		Intensity: intensityBusySlot,
		Target: MetricTarget{
			Metric:   metricTradesPerMinute,
			Relation: ">=",
			Value:    targetBusyTradesPerMinuteMultipleSlot,
			Unit:     targetUnitBaselineMultiple,
		},
	}
}

// dispersionPreset — the "two camps" shape. Aggressiveness and
// P&L reactivity rise (both camps are opinionated), and the
// bimodal switch flips on, splitting optimism into a bull hump and
// a bear hump centered at ±Separation. The engine's merge step
// ensures the bimodal switch overrides any optimism.center
// direction (so a reader does not need to add one here, and the
// precedence rule stays explicit).
func dispersionPreset() ScenarioBlob {
	bimodalEnabled := BimodalConfig{
		Separation:   bimodalDispersionSeparationSlot,
		BullWeight:   bimodalDispersionBullWeightSlot,
		WithinSpread: bimodalDispersionWithinSpreadSlot,
	}
	return ScenarioBlob{
		Direction: Direction{
			KnobAggressivenessMean: +1,
			KnobPnlReactivityMean:  +1,
		},
		Intensity: intensityDispersionSlot,
		Switches: Switches{
			PhiBimodal: &bimodalEnabled,
		},
		Target: MetricTarget{
			Metric:   metricRealizedVolatility,
			Relation: ">=",
			Value:    targetDispersionVolMultipleSlot,
			Unit:     targetUnitBaselineMultiple,
		},
	}
}
