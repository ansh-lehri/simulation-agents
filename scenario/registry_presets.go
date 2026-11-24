package scenario

import "simulation-agents/orchestrator/generator"

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
//
// Expected market behavior: a representative baseline day. Price
// drifts gradually in one direction by 1–3 % over the session with
// occasional small retracements. Spread is tight, book has moderate
// depth, and trade frequency is steady. There is no dramatic spike or
// pronounced trend; volatility is low and the chart looks like a
// healthy "normal" trading day.
func normalPreset() ScenarioBlob {
	return ScenarioBlob{
		Direction: Direction{},
		Intensity: intensityNormalSlot,
		Session: &generator.SessionEffective{
			SellerCount:          15,
			BuyerCount:           15,
			OrderSizeMin:         5,
			OrderSizeMax:         20,
			RandomLevelSpreadBps: 7,
			ThetaMarket:          0.62,
			ContinuousFraction:   0.82,
		},
		// Session nil: normal uses the composition-root defaults unchanged.
	}
}

// thinPreset — the "thin liquidity" shape. The crowd shifts toward
// aggressive, shallow-pocket, fast-step-out participants; mean
// disposition tilts away from mean-reversion toward noise. The
// emergent book thins because the drawn crowd provides less resting
// liquidity and crosses more.
//
// Expected market behavior: the bid-ask spread widens noticeably
// (target ≥ 3× baseline). Resting book depth on both sides is
// shallow — a single moderate-sized order can jump price by 10–30 bps.
// Sharp short-lived spikes and gaps are frequent. There is no
// sustained trend; the chart shows many wicks and erratic tick-by-tick
// motion. Trade count may be lower than normal because limit orders
// do not find counterparties easily.
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
		// Thinner seeder book + wider spread + lower ThetaMarket (more crossing).
		// All values are starting-point calibration placeholders; tune until
		// median spread reaches the target multiple above baseline.
		Session: &generator.SessionEffective{
			SellerCount:          15,
			BuyerCount:           15,
			OrderSizeMin:         5,
			OrderSizeMax:         50,
			RandomLevelSpreadBps: 12,
			ContinuousFraction:   0.60,
			ThetaMarket:          0.62,
			PivotLevelCount:      0,
		},
	}
}

// calmPreset — the "calm market" shape. The crowd shifts toward
// patient, deeper-pocket participants; disposition tilts toward
// mean-reversion. P&L reactivity drops, aggressiveness drops, and
// step-out rates fall. Low spread and narrow range emerge from the
// deeper, less-twitchy book.
//
// Expected market behavior: price barely moves over the session —
// target high-to-low range ≤ 0.5× baseline. Candles are small-bodied
// with tiny wicks. Spread is tight and the book is thick on both
// sides. Most agents rest passive limits; very few cross the spread.
// The chart looks like a flat, quiet day with only slow mean-reverting
// drift. Consecutive candles will have nearly identical open/close.
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
		// Deeper seeder book + tighter spread + higher ThetaMarket (fewer market orders).
		// All values are starting-point calibration placeholders; tune until
		// price range width reaches the target fraction of baseline.
		Session: &generator.SessionEffective{
			SellerCount:          12,
			BuyerCount:           12,
			OrderSizeMin:         5,
			OrderSizeMax:         20,
			RandomLevelSpreadBps: 5,
			ThetaMarket:          0.65,
			ContinuousFraction:   0.92,
			PivotLevelCount:      0,
		},
	}
}

// busyPreset — the "busy market" shape. More arrivals, fewer
// completions, higher aggressiveness. The emergent floor is thicker
// and trades per minute rises against the baseline rate.
//
// Expected market behavior: high trade frequency — target ≥ 2× trades
// per minute versus baseline. The book refreshes quickly; both sides
// stay well-populated. Spread may be slightly tighter than normal due
// to competition. Price can trend moderately in either direction with
// meaningful intraday range, but moves are orderly (no flash gaps).
// The chart shows many candles with visible bodies and consistent
// volume, like an active mid-cap stock on a news day.
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
		// More seeder capacity + larger orders + lower ThetaMarket (more market orders).
		// All values are starting-point calibration placeholders; tune until
		// trades-per-minute reaches the target multiple above baseline.
		Session: &generator.SessionEffective{
			SellerCount:          15,
			BuyerCount:           15,
			OrderSizeMin:         5,
			OrderSizeMax:         20,
			RandomLevelSpreadBps: 10,
			ThetaMarket:          0.58,
			ContinuousFraction:   0.85,
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
//
// Expected market behavior: two opinionated sides clash at the book.
// Price trends sharply in one direction for a stretch, then reverses
// as the opposing camp overwhelms — the chart shows a clear V-shape
// or inverted-V-shape within the session. Realized volatility is high
// (target ≥ 2× baseline). Both bid and ask sides are active with
// conviction; the spread can widen when the camps are evenly matched
// but snaps tight when one side dominates momentarily.
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
		// Moderate seeder + wider spread + low ThetaMarket (both camps willing to cross).
		// All values are starting-point calibration placeholders; tune until
		// realized volatility reaches the target multiple above baseline.
		Session: &generator.SessionEffective{
			SellerCount:          20,
			BuyerCount:           20,
			OrderSizeMin:         5,
			OrderSizeMax:         400,
			RandomLevelSpreadBps: 20,
			ThetaMarket:          0.35,
		},
	}
}
