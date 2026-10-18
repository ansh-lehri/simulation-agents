package scenario

import "math"

// Ruler names the four domain-safe coordinate transforms a knob can be
// shifted on. A ruler is a property of the knob (a mean lives on
// Logit, a positive rate lives on Log) and the merge engine selects
// which transform to apply before and after a shove. The transforms
// are chosen so the inverse always lands in the knob's valid domain
// by construction — no clamp is ever needed to keep a shoved value
// in range.
type Ruler int

const (
	// Logit is the ruler for a knob whose moment is a probability or
	// proportion in the open interval (0, 1). The forward map is
	// log(p / (1 - p)); the inverse is the logistic function, which
	// returns a value strictly in (0, 1) for every finite input.
	Logit Ruler = iota

	// Log is the ruler for a strictly positive knob — a rate, a
	// standard deviation, a scale, a concentration. The forward map is
	// log; the inverse is exp, which returns a strictly positive
	// value for every finite input.
	Log

	// Arctanh is the ruler for a knob whose moment lives on the open
	// interval (-1, +1) — the optimism center, for example. The
	// forward map is 0.5 * log((1 + m) / (1 - m)); the inverse is
	// tanh, which returns a value strictly in (-1, +1) for every
	// finite input.
	Arctanh

	// LogRatio is the ruler for a knob whose moment is a vector of
	// proportions on the simplex (a Dirichlet mean, in practice the
	// three disposition proportions summing to 1). Scalar toLink /
	// fromLink reject this ruler — the merge engine routes it through
	// toLogRatio / fromLogRatio instead, which operate on the whole
	// vector and renormalize back to a simplex via softmax.
	LogRatio
)

// toLink pushes a scalar baseline moment onto its ruler's unbounded
// axis so a shift can be applied additively without an edge to fall
// off. The LogRatio case panics: the simplex is vector-valued and the
// scalar path has nothing meaningful to return.
func toLink(ruler Ruler, moment float64) float64 {
	switch ruler {
	case Logit:
		return math.Log(moment / (1 - moment))
	case Log:
		return math.Log(moment)
	case Arctanh:
		return 0.5 * math.Log((1+moment)/(1-moment))
	case LogRatio:
		panic("scenario: toLink called with LogRatio; use toLogRatio for simplex-valued knobs")
	}
	panic("scenario: toLink called with unknown ruler")
}

// fromLink pulls a shifted link-space value back onto its ruler's
// bounded domain. Each inverse is in-range by construction, so no
// caller ever needs to clamp — a clamp here would silently hide a
// miscoded knob (wrong ruler, wrong units) that the engine should
// instead let fail loudly in a test. Panics for LogRatio for the same
// reason toLink does.
func fromLink(ruler Ruler, linkValue float64) float64 {
	switch ruler {
	case Logit:
		return 1 / (1 + math.Exp(-linkValue))
	case Log:
		return math.Exp(linkValue)
	case Arctanh:
		return math.Tanh(linkValue)
	case LogRatio:
		panic("scenario: fromLink called with LogRatio; use fromLogRatio for simplex-valued knobs")
	}
	panic("scenario: fromLink called with unknown ruler")
}

// toLogRatio pushes a vector of simplex proportions onto an unbounded
// per-component axis so each component can be shoved additively. The
// forward map is the plain component-wise log; softmax (the inverse)
// is translation-invariant, so taking a shared reference component is
// unnecessary — the simplex is recovered symmetrically from any shift.
// The caller is expected to pass strictly positive proportions; a
// zero or negative component would come from a corrupt baseline and
// is left to produce a non-finite value so the fault is loud.
func toLogRatio(proportions []float64) []float64 {
	logRatios := make([]float64, len(proportions))
	for index, proportion := range proportions {
		logRatios[index] = math.Log(proportion)
	}
	return logRatios
}

// fromLogRatio renormalizes a shoved log-ratio vector back onto the
// simplex via softmax. The output is strictly positive and sums to 1
// by construction — the two invariants the generator relies on when
// consuming a Dirichlet mean. The softmax is written in the numerically
// stable form (subtract the max before exponentiating) so an extreme
// shove does not overflow for one component and underflow the rest.
func fromLogRatio(logRatios []float64) []float64 {
	if len(logRatios) == 0 {
		return nil
	}
	maxLogRatio := logRatios[0]
	for _, value := range logRatios[1:] {
		if value > maxLogRatio {
			maxLogRatio = value
		}
	}
	exponentials := make([]float64, len(logRatios))
	exponentialSum := 0.0
	for index, value := range logRatios {
		exponential := math.Exp(value - maxLogRatio)
		exponentials[index] = exponential
		exponentialSum += exponential
	}
	proportions := make([]float64, len(logRatios))
	for index, exponential := range exponentials {
		proportions[index] = exponential / exponentialSum
	}
	return proportions
}
