package scenario

import (
	"math"
	"math/rand/v2"
	"sort"

	"simulation-agents/orchestrator/generator"
)

// baselineSampleCount is the number of baseline draws the eta
// computation takes per knob. A few thousand is enough to pin a
// per-knob standard deviation to three or four significant figures,
// which is more than the shove math downstream can tell apart. The
// value is a package constant (not a knob) because changing it
// shifts eta by sampling noise, not by anything a scenario cares
// about.
const baselineSampleCount = 2048

// parameterKnobEta is the eta used for knobs that do not have a
// baseline trait distribution to sample from — flag-like or
// scale-like dials where the baseline is a point value, not a
// crowd. Using a unit value here means a shove of s * d on such a
// knob moves it by s * d units in ruler space (one logit for the
// herd probability, one log for a positive rate). This convention
// keeps the shove formula uniform across every knob while being
// honest about the distinction: trait-mean knobs get a measured
// crowd-width, parameter knobs get a documented unit scale.
const parameterKnobEta = 1.0

// BaselineLinkSpace holds every knob's baseline handle u0 in link
// space and its empirical crowd-width eta on the same axis. The
// scalar maps are keyed by knob name; the disposition fields are
// length-three vectors because the simplex is vector-valued and the
// merge engine shoves each component independently.
//
// Fields are unexported — the merge engine, which lives in the same
// package, reads them directly. External callers never need the
// internals; they get the engine's output through Effective.
type BaselineLinkSpace struct {
	scalarU0       map[string]float64
	scalarEta      map[string]float64
	dispositionU0  [DispositionComponentCount]float64
	dispositionEta [DispositionComponentCount]float64
}

// BuildBaseline computes u0 and eta for every knob the catalogue
// registers. It is called once at startup, with a fixed seed, and
// its output is cached for the process lifetime — the baseline does
// not change at runtime.
//
// Scalar knobs are walked in catalogue order (map iteration), and
// each one is dispatched to the sampler for its baseline
// distribution. The disposition knob is handled separately because
// it is vector-valued and uses the Dirichlet sampler.
//
// The seed is a dedicated calibration seed, independent of live
// session randomness — baseline building is tuning-side, not
// draw-time, and must be reproducible across restarts.
func BuildBaseline(
	params generator.GenParams,
	presence PresenceBaseline,
	catalogue KnobCatalogue,
	seed uint64,
) BaselineLinkSpace {
	// Two 64-bit words seed the PCG stream; mixing in a constant
	// into the second word avoids the degenerate (seed, seed) start
	// state the generator has for small seeds.
	baselineRNG := rand.New(rand.NewPCG(seed, seed^0xA5A5A5A5A5A5A5A5))

	// Walk the catalogue in sorted name order so the shared RNG
	// stream consumes draws in a reproducible sequence across builds.
	// Map iteration is otherwise non-deterministic, which would turn
	// a build with a fixed seed into one that returns a different eta
	// every run — the exact invariant this layer is required to hold.
	sortedKnobNames := make([]string, 0, len(catalogue))
	for knobName := range catalogue {
		sortedKnobNames = append(sortedKnobNames, knobName)
	}
	sort.Strings(sortedKnobNames)

	scalarU0 := make(map[string]float64, len(catalogue))
	scalarEta := make(map[string]float64, len(catalogue))
	for _, knobName := range sortedKnobNames {
		if knobName == KnobDisposition {
			continue
		}
		ruler := catalogue[knobName]
		moment := baselineScalarMoment(params, presence, knobName)
		scalarU0[knobName] = toLink(ruler, moment)
		scalarEta[knobName] = baselineScalarEta(baselineRNG, params, knobName, ruler)
	}

	dispositionU0 := baselineDispositionLogRatios(params.DispositionDirichletAlphas)
	dispositionEta := baselineDispositionEta(baselineRNG, params.DispositionDirichletAlphas)

	return BaselineLinkSpace{
		scalarU0:       scalarU0,
		scalarEta:      scalarEta,
		dispositionU0:  dispositionU0,
		dispositionEta: dispositionEta,
	}
}

// baselineScalarMoment routes a scalar knob to its baseline value
// source. Presence rates live on the injected PresenceBaseline
// sidecar; every other scalar knob lives on GenParams and is read
// through scalarMoment. The disposition knob is vector-valued and
// handled on its own path — passing it here is a programmer error.
func baselineScalarMoment(
	params generator.GenParams,
	presence PresenceBaseline,
	knobName string,
) float64 {
	switch knobName {
	case KnobPresenceEntryRate:
		return presence.EntryRate
	case KnobPresenceStepOutRate:
		return presence.StepOutRate
	case KnobPresenceDoneRate:
		return presence.DoneRate
	case KnobPresenceReentryRate:
		return presence.ReentryRate
	}
	return scalarMoment(params, knobName)
}

// baselineScalarEta dispatches per-knob eta computation. Trait-mean
// knobs backed by a drawn-trait distribution (Beta, LogNormal) get
// empirical eta from a sample pass; parameter knobs with no trait
// distribution to sample get the unit convention parameterKnobEta.
// A reader can tell which category a knob falls in by whether this
// function routes to a sampler or returns the constant.
func baselineScalarEta(
	rng *rand.Rand,
	params generator.GenParams,
	knobName string,
	ruler Ruler,
) float64 {
	switch knobName {
	case KnobAggressivenessMean:
		return empiricalEtaFromBeta(rng, params.AggressivenessBeta, ruler)
	case KnobSkillMean:
		return empiricalEtaFromBeta(rng, params.SkillBeta, ruler)
	case KnobPnlReactivityMean:
		return empiricalEtaFromBeta(rng, params.PnlReactivityBeta, ruler)
	case KnobReactivityMean:
		return empiricalEtaFromBeta(rng, params.ReactivityBeta, ruler)
	case KnobPActMean:
		return empiricalEtaFromBeta(rng, params.PActBeta, ruler)
	case KnobCapitalMedian:
		return empiricalEtaFromLogNormal(rng, params.CapitalSizeLogNormal, ruler)
	case KnobOptimismCenter:
		return empiricalEtaFromOptimismBeta(rng, params.OptimismBeta)
	case KnobHerdProbability,
		KnobCapitalSpread,
		KnobPresenceEntryRate,
		KnobPresenceStepOutRate,
		KnobPresenceDoneRate,
		KnobPresenceReentryRate:
		return parameterKnobEta
	}
	panic("scenario: baselineScalarEta called with unknown knob: " + knobName)
}

// empiricalEtaFromBeta samples the trait-mean knob's underlying
// Beta, pushes each sample through the knob's ruler, and returns the
// sample standard deviation on that axis. For the five Logit-ruler
// trait-mean knobs this is literally the std-dev of logit(X) under
// X ~ Beta(alpha, beta) — the "crowd width" the shove math is
// measured against.
func empiricalEtaFromBeta(
	rng *rand.Rand,
	pair generator.BetaPair,
	ruler Ruler,
) float64 {
	linkSamples := make([]float64, baselineSampleCount)
	for index := range linkSamples {
		betaSample := sampleBeta(rng, pair.Alpha, pair.Beta)
		linkSamples[index] = toLink(ruler, betaSample)
	}
	return standardDeviation(linkSamples)
}

// empiricalEtaFromLogNormal samples the capital.median knob's
// underlying log-normal and pushes each sample through the Log
// ruler. Analytically this converges to the log-normal's own sigma
// as sample size grows; sampling anyway (rather than returning sigma
// directly) keeps the eta pathway uniform with the other knobs and
// surfaces any numerical surprises in a single, testable spot.
func empiricalEtaFromLogNormal(
	rng *rand.Rand,
	pair generator.LogNormalPair,
	ruler Ruler,
) float64 {
	linkSamples := make([]float64, baselineSampleCount)
	for index := range linkSamples {
		logNormalSample := sampleLogNormal(rng, pair.Mu, pair.Sigma)
		linkSamples[index] = toLink(ruler, logNormalSample)
	}
	return standardDeviation(linkSamples)
}

// empiricalEtaFromOptimismBeta samples the optimism Beta, stretches
// each sample onto (-1, +1) with the 2 * beta - 1 convention the
// generator uses, pushes through Arctanh, and returns the sample
// standard deviation. This is the one knob whose ruler is Arctanh;
// the stretch step is why the sampler lives here instead of folding
// into empiricalEtaFromBeta.
func empiricalEtaFromOptimismBeta(
	rng *rand.Rand,
	pair generator.BetaPair,
) float64 {
	linkSamples := make([]float64, baselineSampleCount)
	for index := range linkSamples {
		betaSample := sampleBeta(rng, pair.Alpha, pair.Beta)
		linkSamples[index] = toLink(Arctanh, 2*betaSample-1)
	}
	return standardDeviation(linkSamples)
}

// baselineDispositionLogRatios returns the baseline log-ratios of
// the three disposition proportions — the vector-valued u0 the merge
// engine shoves component-wise before softmax-renormalizing back.
func baselineDispositionLogRatios(
	alphas generator.DispositionAlphas,
) [DispositionComponentCount]float64 {
	proportions := dispositionProportions(alphas)
	logRatios := toLogRatio(proportions)
	var fixedLengthRatios [DispositionComponentCount]float64
	copy(fixedLengthRatios[:], logRatios)
	return fixedLengthRatios
}

// baselineDispositionEta samples the Dirichlet, log-transforms each
// draw's components, and returns the per-component sample standard
// deviation. The three components get three etas because the shove
// is applied per component; a uniform shove across all three would
// cancel under softmax (log-ratio translation invariance).
func baselineDispositionEta(
	rng *rand.Rand,
	alphas generator.DispositionAlphas,
) [DispositionComponentCount]float64 {
	alphaSlice := []float64{alphas.Trend, alphas.MeanReversion, alphas.Noise}
	perComponentLogRatios := make([][]float64, DispositionComponentCount)
	for index := range perComponentLogRatios {
		perComponentLogRatios[index] = make([]float64, 0, baselineSampleCount)
	}

	for sampleIndex := 0; sampleIndex < baselineSampleCount; sampleIndex++ {
		proportions := sampleDirichlet(rng, alphaSlice)
		for componentIndex, proportion := range proportions {
			perComponentLogRatios[componentIndex] = append(
				perComponentLogRatios[componentIndex],
				math.Log(proportion),
			)
		}
	}

	var eta [DispositionComponentCount]float64
	for componentIndex := range eta {
		eta[componentIndex] = standardDeviation(perComponentLogRatios[componentIndex])
	}
	return eta
}
