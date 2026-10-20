package session

import (
	"fmt"
	"math"
	"math/rand/v2"

	"simulation-agents/orchestrator/generator"
)

// lifecycleGPGridPoints is the resolution of the Gaussian-process
// realization grid for both time-based lifecycle curves. Matched to
// the entry curve default so the smoothness of the lifecycle wiggle
// is consistent with the session arrival rhythm.
const lifecycleGPGridPoints = 64

// buildLifecycleCurves realizes the four lifecycle intensity and
// probability functions for one session. The two time-based functions
// (step-out and done-early) each draw a fresh Gaussian-process
// realization from rng at build time; all subsequent queries are pure
// closures over the frozen grid. The two idle-duration functions
// (re-entry and give-up) are analytic and consume no RNG.
//
// When curveEffective is non-nil, the three rate parameters it carries
// replace the corresponding baseline values in params before any math
// runs. EntryRate in curveEffective belongs to the pre-open phase and
// is not consumed here.
func buildLifecycleCurves(curveEffective *generator.CurveEffective, params LifecycleCurveParams, rng *rand.Rand) *lifecycleCurves {
	validateLifecycleCurveParams(params)

	if curveEffective != nil {
		params.StepOutRate = curveEffective.StepOutRate
		params.DoneRate = curveEffective.DoneRate
		params.ReentryRate = curveEffective.ReentryRate
	}

	// Both GP draws happen unconditionally and in this order so the
	// RNG sequence is deterministic regardless of scenario presence.
	stepOutGPGrid := sampleLifecycleGP(rng, params.StepOutGPVariance, params.StepOutGPLengthScale, lifecycleGPGridPoints)
	doneGPGrid := sampleLifecycleGP(rng, params.DoneGPVariance, params.DoneGPLengthScale, lifecycleGPGridPoints)

	return &lifecycleCurves{
		stepOutRate: buildStepOutFunc(stepOutGPGrid, params.StepOutRate, params.StepOutTauRamp),
		doneRate:    buildDoneFunc(doneGPGrid, params.DoneRate, params.DoneTauRamp),
		reentryProb: buildReentryFunc(params.ReentryRate, params.ReentryMeanBreak, params.ReentrySigmaBreak),
		giveUpProb:  buildGiveUpFunc(params.GiveUpMax, params.ReentryMeanBreak, params.GiveUpTau),
	}
}

// buildStepOutFunc returns the step-out intensity function λ_out(t).
// Shape: s · (1 − e^(−t/τ)) · e^(g(t)). The ramp factor holds the
// rate at zero at session open and lets it rise as the session
// progresses; the GP factor adds session-specific local variation.
// At normalized time t=0 the ramp is zero, so no agent steps out
// immediately at the open.
func buildStepOutFunc(gpGrid []float64, stepOutRate, tauRamp float64) func(float64) float64 {
	return func(normalizedTime float64) float64 {
		ramp := 1.0 - math.Exp(-normalizedTime/tauRamp)
		wiggle := math.Exp(interpolateLifecycleGrid(gpGrid, normalizedTime))
		return stepOutRate * ramp * wiggle
	}
}

// buildDoneFunc returns the done-early intensity function λ_done(t).
// Shape: D · e^(−(1−t)/τ) · e^(g(t)). As normalized time approaches
// 1 (session close), the exponential approaches 1, making this
// function close-heavy: most done-early decisions happen in the
// final stretch of the session.
func buildDoneFunc(gpGrid []float64, doneRate, tauRamp float64) func(float64) float64 {
	return func(normalizedTime float64) float64 {
		closeDecay := math.Exp(-(1.0 - normalizedTime) / tauRamp)
		wiggle := math.Exp(interpolateLifecycleGrid(gpGrid, normalizedTime))
		return doneRate * closeDecay * wiggle
	}
}

// buildReentryFunc returns the re-entry probability function r(d).
// Shape: p_max · exp(−(d−μ)²/(2σ²)). A Gaussian centered at the
// typical break length, peaking when an idle agent has been away
// roughly that long and decaying on both sides.
func buildReentryFunc(pMax, meanBreak, sigmaBreak float64) func(float64) float64 {
	twoVariance := 2.0 * sigmaBreak * sigmaBreak
	return func(idleDuration float64) float64 {
		delta := idleDuration - meanBreak
		return pMax * math.Exp(-(delta*delta)/twoVariance)
	}
}

// buildGiveUpFunc returns the give-up probability function q(d).
// Shape: q_max · (1 − e^(−(d−μ)/τ)) for d > μ; 0 otherwise. The
// sigmoid rises from zero once idle duration exceeds the re-entry
// mean break, bounding how long an agent can stay idle before it
// decides it is done for the session.
func buildGiveUpFunc(qMax, meanBreak, tau float64) func(float64) float64 {
	return func(idleDuration float64) float64 {
		if idleDuration <= meanBreak {
			return 0.0
		}
		return qMax * (1.0 - math.Exp(-(idleDuration-meanBreak)/tau))
	}
}

// sampleLifecycleGP draws one zero-mean Gaussian-process realization
// on a uniform grid of gridPoints points on [0, 1], using an RBF
// kernel with the given variance and length scale. It factors the
// kernel matrix via the package's choleskyDecompose and multiplies by
// a vector of i.i.d. standard normals drawn from rng.
func sampleLifecycleGP(rng *rand.Rand, variance, lengthScale float64, gridPoints int) []float64 {
	kernelMatrix := buildLifecycleRBFKernel(variance, lengthScale, gridPoints)
	choleskyFactor := choleskyDecompose(kernelMatrix)

	standardNormals := make([]float64, gridPoints)
	for drawIndex := 0; drawIndex < gridPoints; drawIndex++ {
		standardNormals[drawIndex] = rng.NormFloat64()
	}

	gpSample := make([]float64, gridPoints)
	for rowIndex := 0; rowIndex < gridPoints; rowIndex++ {
		accumulator := 0.0
		for columnIndex := 0; columnIndex <= rowIndex; columnIndex++ {
			accumulator += choleskyFactor[rowIndex][columnIndex] * standardNormals[columnIndex]
		}
		gpSample[rowIndex] = accumulator
	}
	return gpSample
}

// buildLifecycleRBFKernel constructs the RBF covariance matrix on a
// uniform grid of gridPoints points on [0, 1]. A small diagonal nugget
// (variance × 1e-6) is added for numerical stability.
func buildLifecycleRBFKernel(variance, lengthScale float64, gridPoints int) [][]float64 {
	normalizer := float64(gridPoints - 1)
	nugget := variance * 1e-6
	twoLengthScaleSquared := 2.0 * lengthScale * lengthScale

	matrix := make([][]float64, gridPoints)
	for rowIndex := range matrix {
		matrix[rowIndex] = make([]float64, gridPoints)
	}
	for rowIndex := 0; rowIndex < gridPoints; rowIndex++ {
		rowTime := float64(rowIndex) / normalizer
		for columnIndex := 0; columnIndex <= rowIndex; columnIndex++ {
			columnTime := float64(columnIndex) / normalizer
			delta := rowTime - columnTime
			kernelValue := variance * math.Exp(-(delta*delta)/twoLengthScaleSquared)
			if rowIndex == columnIndex {
				kernelValue += nugget
			}
			matrix[rowIndex][columnIndex] = kernelValue
			matrix[columnIndex][rowIndex] = kernelValue
		}
	}
	return matrix
}

// interpolateLifecycleGrid returns the GP value at normalizedTime via
// linear interpolation between the two bracketing grid points. The
// grid is uniform on [0, 1], so indexing is a scale-and-floor.
func interpolateLifecycleGrid(grid []float64, normalizedTime float64) float64 {
	highestIndex := len(grid) - 1
	scaled := normalizedTime * float64(highestIndex)
	lowerIndex := int(scaled)
	if lowerIndex >= highestIndex {
		return grid[highestIndex]
	}
	if lowerIndex < 0 {
		return grid[0]
	}
	fraction := scaled - float64(lowerIndex)
	return grid[lowerIndex]*(1.0-fraction) + grid[lowerIndex+1]*fraction
}

// validateLifecycleCurveParams panics on any param that would make
// the lifecycle math degenerate. Called once at build time.
func validateLifecycleCurveParams(params LifecycleCurveParams) {
	if params.StepOutTauRamp <= 0 {
		panic(fmt.Sprintf("session: LifecycleCurveParams.StepOutTauRamp must be > 0, got %g", params.StepOutTauRamp))
	}
	if params.DoneTauRamp <= 0 {
		panic(fmt.Sprintf("session: LifecycleCurveParams.DoneTauRamp must be > 0, got %g", params.DoneTauRamp))
	}
	if params.StepOutGPLengthScale <= 0 {
		panic(fmt.Sprintf("session: LifecycleCurveParams.StepOutGPLengthScale must be > 0, got %g", params.StepOutGPLengthScale))
	}
	if params.DoneGPLengthScale <= 0 {
		panic(fmt.Sprintf("session: LifecycleCurveParams.DoneGPLengthScale must be > 0, got %g", params.DoneGPLengthScale))
	}
	if params.StepOutGPVariance < 0 {
		panic(fmt.Sprintf("session: LifecycleCurveParams.StepOutGPVariance must be >= 0, got %g", params.StepOutGPVariance))
	}
	if params.DoneGPVariance < 0 {
		panic(fmt.Sprintf("session: LifecycleCurveParams.DoneGPVariance must be >= 0, got %g", params.DoneGPVariance))
	}
	if params.ReentrySigmaBreak <= 0 {
		panic(fmt.Sprintf("session: LifecycleCurveParams.ReentrySigmaBreak must be > 0, got %g", params.ReentrySigmaBreak))
	}
	if params.GiveUpTau <= 0 {
		panic(fmt.Sprintf("session: LifecycleCurveParams.GiveUpTau must be > 0, got %g", params.GiveUpTau))
	}
}
