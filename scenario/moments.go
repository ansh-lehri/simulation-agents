package scenario

import (
	"math"

	"simulation-agents/orchestrator/generator"
)

// Disposition component ordering. The disposition simplex is carried
// as a length-three slice of proportions throughout this package; the
// index-to-component mapping is pinned here so every call site agrees.
// A caller that needs to read or write a specific component uses the
// name constant rather than a magic index.
const (
	DispositionTrendIndex         = 0
	DispositionMeanReversionIndex = 1
	DispositionNoiseIndex         = 2
	DispositionComponentCount     = 3
)

// scalarMoment extracts the interpretable baseline moment of a
// scalar-valued knob from a GenParams struct — the mean for a Beta,
// the median for a log-normal, the standard deviation for a scale,
// the center-on-(-1, +1) for the optimism Beta, and the plain value
// for a direct-dial probability. Scalar in means scalar out; the one
// vector-valued knob (disposition) has its own getter.
//
// Panics on an unknown knob name: a miscoded dispatch is a bug and
// masking it with a zero value would hide the fault silently.
func scalarMoment(params generator.GenParams, knobName string) float64 {
	switch knobName {
	case KnobAggressivenessMean:
		return betaMean(params.AggressivenessBeta)
	case KnobSkillMean:
		return betaMean(params.SkillBeta)
	case KnobPnlReactivityMean:
		return betaMean(params.PnlReactivityBeta)
	case KnobReactivityMean:
		return betaMean(params.ReactivityBeta)
	case KnobPActMean:
		return betaMean(params.PActBeta)
	case KnobHerdProbability:
		return params.HerdProbability
	case KnobCapitalMedian:
		return logNormalMedian(params.CapitalSizeLogNormal)
	case KnobCapitalSpread:
		return logNormalSpread(params.CapitalSizeLogNormal)
	case KnobOptimismCenter:
		return optimismCenter(params.OptimismBeta)
	}
	panic("scenario: scalarMoment called with unknown or non-scalar knob: " + knobName)
}

// setScalarMoment writes a shifted scalar moment back into a GenParams
// struct, inverting whatever transformation scalarMoment extracted.
// For every Beta family (and the optimism Beta's center) the inverse
// preserves the baseline concentration α + β — a scenario moves the
// mean, not the tightness of the distribution around it. For the
// log-normal median only μ is rewritten; the log-space spread σ has
// its own knob and is updated independently.
//
// Panics on an unknown knob name for the same reason scalarMoment does.
func setScalarMoment(params *generator.GenParams, knobName string, newMoment float64) {
	switch knobName {
	case KnobAggressivenessMean:
		params.AggressivenessBeta = setBetaMean(params.AggressivenessBeta, newMoment)
	case KnobSkillMean:
		params.SkillBeta = setBetaMean(params.SkillBeta, newMoment)
	case KnobPnlReactivityMean:
		params.PnlReactivityBeta = setBetaMean(params.PnlReactivityBeta, newMoment)
	case KnobReactivityMean:
		params.ReactivityBeta = setBetaMean(params.ReactivityBeta, newMoment)
	case KnobPActMean:
		params.PActBeta = setBetaMean(params.PActBeta, newMoment)
	case KnobHerdProbability:
		params.HerdProbability = newMoment
	case KnobCapitalMedian:
		params.CapitalSizeLogNormal = setLogNormalMedian(params.CapitalSizeLogNormal, newMoment)
	case KnobCapitalSpread:
		params.CapitalSizeLogNormal = setLogNormalSpread(params.CapitalSizeLogNormal, newMoment)
	case KnobOptimismCenter:
		params.OptimismBeta = setOptimismCenter(params.OptimismBeta, newMoment)
	default:
		panic("scenario: setScalarMoment called with unknown or non-scalar knob: " + knobName)
	}
}

// dispositionProportions returns the three baseline disposition
// proportions in [trend, mean-reversion, noise] order, derived from
// the Dirichlet concentration parameters by the standard formula
// α_i / Σα. The baseline sum of concentrations (α_total) is the one
// property the scenario shove preserves: a shifted proportion vector
// is lifted back to concentrations by multiplying each proportion by
// the baseline α_total, which keeps the simplex tightness the
// calibration chose while moving only its location.
func dispositionProportions(alphas generator.DispositionAlphas) []float64 {
	total := alphas.Trend + alphas.MeanReversion + alphas.Noise
	proportions := make([]float64, DispositionComponentCount)
	proportions[DispositionTrendIndex] = alphas.Trend / total
	proportions[DispositionMeanReversionIndex] = alphas.MeanReversion / total
	proportions[DispositionNoiseIndex] = alphas.Noise / total
	return proportions
}

// setDispositionProportions writes a shifted proportion vector back
// into a Dirichlet-concentration triple, preserving the baseline
// α_total so the simplex tightness around the new center stays
// unchanged. The input must have length DispositionComponentCount
// and sum to 1 (the softmax inverse guarantees this when called from
// the merge engine).
func setDispositionProportions(
	alphas generator.DispositionAlphas,
	proportions []float64,
) generator.DispositionAlphas {
	if len(proportions) != DispositionComponentCount {
		panic("scenario: setDispositionProportions requires a length-three proportion vector")
	}
	totalConcentration := alphas.Trend + alphas.MeanReversion + alphas.Noise
	return generator.DispositionAlphas{
		Trend:         proportions[DispositionTrendIndex] * totalConcentration,
		MeanReversion: proportions[DispositionMeanReversionIndex] * totalConcentration,
		Noise:         proportions[DispositionNoiseIndex] * totalConcentration,
	}
}

// betaMean returns the mean of a Beta distribution expressed in the
// standard α / (α + β) form. Positive shape parameters are assumed —
// the generator's own Validate step rejects a bad calibration long
// before anything in this package runs.
func betaMean(pair generator.BetaPair) float64 {
	return pair.Alpha / (pair.Alpha + pair.Beta)
}

// setBetaMean produces a new Beta pair whose mean equals newMean and
// whose concentration (α + β) matches the input pair. This is the
// method-of-moments mapping that keeps the shape family and tightness
// stable under a shove — a scenario moves the mean, nothing else.
func setBetaMean(pair generator.BetaPair, newMean float64) generator.BetaPair {
	concentration := pair.Alpha + pair.Beta
	return generator.BetaPair{
		Alpha: newMean * concentration,
		Beta:  (1 - newMean) * concentration,
	}
}

// logNormalMedian returns the median of a log-normal distribution,
// which equals exp(μ) by definition.
func logNormalMedian(pair generator.LogNormalPair) float64 {
	return math.Exp(pair.Mu)
}

// setLogNormalMedian overwrites μ so the distribution's median equals
// newMedian, leaving σ untouched. The capital.spread knob has its own
// path (setLogNormalSpread) so each dial moves independently.
func setLogNormalMedian(pair generator.LogNormalPair, newMedian float64) generator.LogNormalPair {
	return generator.LogNormalPair{
		Mu:    math.Log(newMedian),
		Sigma: pair.Sigma,
	}
}

// logNormalSpread returns the log-space standard deviation σ of a
// log-normal distribution — the moment the Log ruler shoves for the
// capital.spread knob.
func logNormalSpread(pair generator.LogNormalPair) float64 {
	return pair.Sigma
}

// setLogNormalSpread overwrites σ with the shifted value and leaves μ
// alone, so the two capital knobs compose cleanly when a scenario
// touches both at once.
func setLogNormalSpread(pair generator.LogNormalPair, newSpread float64) generator.LogNormalPair {
	return generator.LogNormalPair{
		Mu:    pair.Mu,
		Sigma: newSpread,
	}
}

// optimismCenter returns the center-on-(-1, +1) of the optimism Beta
// under the stretched convention the generator uses: the per-agent
// optimism draw is 2 * β - 1, so the center of the latent is
// 2 * mean(β) - 1. The arctanh ruler then shifts this center.
func optimismCenter(pair generator.BetaPair) float64 {
	return 2*betaMean(pair) - 1
}

// setOptimismCenter produces a new Beta pair whose center equals
// newCenter under the 2β - 1 convention, preserving the baseline
// concentration so the within-camp spread stays what the calibration
// chose. The arctanh inverse delivers a center strictly in (-1, +1),
// so the derived Beta mean is strictly in (0, 1) and setBetaMean is
// in-range by construction.
func setOptimismCenter(pair generator.BetaPair, newCenter float64) generator.BetaPair {
	return setBetaMean(pair, (newCenter+1)/2)
}

