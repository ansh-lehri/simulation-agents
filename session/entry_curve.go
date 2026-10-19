package session

import (
	"fmt"
	"math"
	"math/rand/v2"
)

// EntryCurve is a per-session arrival-intensity shape. It is built
// once per bring-up (the session's "rhythm") and sampled against by
// each agent to produce that agent's entry fraction over the
// normalized session interval. A later stage maps the fraction onto
// a wall-clock time via start + fraction·(end-start); the fraction
// representation lets the same math serve a thirty-minute practice
// session and a six-hour canonical session with identical params.
//
// Shape: an inhomogeneous-Poisson rate λ(t) = heightA ·
// exp(-t/tauDecay) · exp(g(t)). The exponential base concentrates
// arrivals near the open and decays through the session; the
// Gaussian-process factor g(t) wiggles the rate up and down, giving
// each session a few organic local peaks. The whole shape is
// positive (an exponential around g(t)), and sampling via thinning
// turns the rate into a per-agent fraction without ever integrating
// the rate analytically.
//
// The Gaussian process is drawn once at build time (one shared
// rhythm per session) rather than once per agent — the lifecycle
// design specifies the GP is drawn fresh each SESSION, so every
// bring-up gets its own rhythm and every agent in that bring-up
// samples from the same rhythm.
type EntryCurve struct {
	// rateGrid holds λ(t) at a uniform grid of normalized times on
	// [0, 1]. Thinning interpolates between adjacent grid points,
	// so a modest grid is enough to recover the smooth shape.
	rateGrid []float64

	// rateMax is the maximum of rateGrid; thinning accepts a
	// proposal with probability λ(t)/rateMax, so this value sits
	// on the hot path and is cached once at build time.
	rateMax float64
}

// BuildEntryCurve draws one Gaussian-process realization against the
// params, composes it with the exponential base, and returns the
// precomputed rate grid ready for DrawEntryFraction. The RNG is
// consumed only during the build; subsequent per-agent draws use
// their own RNG argument, so the caller can thread a single live
// *rand.Rand through both the build and the N agent draws.
//
// Invalid params (non-positive TauDecay, non-positive HeightA,
// non-positive LengthScale, negative VarianceSigmaSquared, fewer
// than two grid points) are an invariant violation at the composition
// root; building a session rhythm against them cannot proceed, so
// the function panics with a clear message rather than returning a
// silently degenerate curve.
func BuildEntryCurve(rng *rand.Rand, params EntryCurveParams) EntryCurve {
	validateEntryCurveParams(params)

	gaussianProcessSample := sampleGaussianProcessOnGrid(rng, params)
	rateGrid := make([]float64, params.GridPoints)
	rateMax := 0.0
	normalizer := float64(params.GridPoints - 1)
	for gridIndex := 0; gridIndex < params.GridPoints; gridIndex++ {
		normalizedTime := float64(gridIndex) / normalizer
		rate := params.HeightA *
			math.Exp(-normalizedTime/params.TauDecay) *
			math.Exp(gaussianProcessSample[gridIndex])
		rateGrid[gridIndex] = rate
		if rate > rateMax {
			rateMax = rate
		}
	}
	return EntryCurve{rateGrid: rateGrid, rateMax: rateMax}
}

// DrawEntryFraction returns one agent's arrival fraction on [0, 1]
// sampled from this curve via thinning. The caller is responsible
// for mapping the fraction to a wall-clock time; the curve itself
// is session-length-agnostic.
//
// Thinning procedure: propose a candidate time uniformly in [0, 1],
// evaluate the rate at the candidate by interpolating the grid, and
// accept with probability rate/rateMax. The expected number of
// proposals per returned fraction is rateMax / mean(rate); with the
// baseline params this stays small enough that an unbounded loop is
// safe in practice. Guard: if the acceptance test ever fails to
// find a draw within a very large number of attempts (a sign the
// RNG or params are degenerate in a way validation missed), the
// loop panics rather than hang the caller forever.
func (curve EntryCurve) DrawEntryFraction(rng *rand.Rand) float64 {
	const maximumThinningAttempts = 1 << 20
	for attempt := 0; attempt < maximumThinningAttempts; attempt++ {
		candidateFraction := rng.Float64()
		acceptanceDraw := rng.Float64()
		if acceptanceDraw*curve.rateMax < curve.rateAtFraction(candidateFraction) {
			return candidateFraction
		}
	}
	panic("session: EntryCurve.DrawEntryFraction exhausted thinning attempts; params or RNG are degenerate")
}

// rateAtFraction returns λ at a normalized time via linear
// interpolation between the two bracketing grid points. The grid is
// uniform on [0, 1], so the interpolation is a cheap scale-and-floor
// without a search.
func (curve EntryCurve) rateAtFraction(normalizedTime float64) float64 {
	highestIndex := len(curve.rateGrid) - 1
	scaled := normalizedTime * float64(highestIndex)
	lowerIndex := int(scaled)
	if lowerIndex >= highestIndex {
		return curve.rateGrid[highestIndex]
	}
	if lowerIndex < 0 {
		return curve.rateGrid[0]
	}
	fractionBetween := scaled - float64(lowerIndex)
	return curve.rateGrid[lowerIndex]*(1.0-fractionBetween) +
		curve.rateGrid[lowerIndex+1]*fractionBetween
}

// sampleGaussianProcessOnGrid returns one realization of a zero-mean
// Gaussian process with RBF kernel, evaluated at a uniform grid of
// GridPoints points on [0, 1]. The RBF kernel is
//
//	k(ti, tj) = varianceSigmaSquared · exp(-(ti-tj)^2 / (2·lengthScale^2))
//
// which produces smooth draws whose typical bump width is set by
// lengthScale and whose typical bump height is set by
// varianceSigmaSquared.
//
// Sampling uses the Cholesky factor L of the kernel matrix K: a draw
// z of i.i.d. standard normals becomes g = L · z, a joint sample
// from N(0, K). A tiny diagonal nugget is added to K for numerical
// stability — the far-apart grid points have kernel values near
// zero, so the matrix is near-degenerate without the nugget.
func sampleGaussianProcessOnGrid(rng *rand.Rand, params EntryCurveParams) []float64 {
	gridSize := params.GridPoints
	kernelMatrix := buildRBFKernelMatrix(params)
	choleskyFactor := choleskyDecompose(kernelMatrix)

	standardNormalDraws := make([]float64, gridSize)
	for drawIndex := 0; drawIndex < gridSize; drawIndex++ {
		standardNormalDraws[drawIndex] = rng.NormFloat64()
	}

	gaussianProcessSample := make([]float64, gridSize)
	for rowIndex := 0; rowIndex < gridSize; rowIndex++ {
		accumulator := 0.0
		for columnIndex := 0; columnIndex <= rowIndex; columnIndex++ {
			accumulator += choleskyFactor[rowIndex][columnIndex] * standardNormalDraws[columnIndex]
		}
		gaussianProcessSample[rowIndex] = accumulator
	}
	return gaussianProcessSample
}

// buildRBFKernelMatrix constructs the RBF covariance matrix on the
// uniform grid, with a small diagonal nugget for numerical stability.
// The matrix is symmetric and positive definite (post-nugget) so the
// Cholesky factorization below cannot fail on realistic params.
func buildRBFKernelMatrix(params EntryCurveParams) [][]float64 {
	gridSize := params.GridPoints
	normalizer := float64(gridSize - 1)
	nuggetTerm := params.VarianceSigmaSquared * 1e-6
	twoLengthScaleSquared := 2.0 * params.LengthScale * params.LengthScale

	kernelMatrix := make([][]float64, gridSize)
	for rowIndex := range kernelMatrix {
		kernelMatrix[rowIndex] = make([]float64, gridSize)
	}
	for rowIndex := 0; rowIndex < gridSize; rowIndex++ {
		rowTime := float64(rowIndex) / normalizer
		for columnIndex := 0; columnIndex <= rowIndex; columnIndex++ {
			columnTime := float64(columnIndex) / normalizer
			delta := rowTime - columnTime
			kernelValue := params.VarianceSigmaSquared *
				math.Exp(-(delta*delta)/twoLengthScaleSquared)
			if rowIndex == columnIndex {
				kernelValue += nuggetTerm
			}
			kernelMatrix[rowIndex][columnIndex] = kernelValue
			kernelMatrix[columnIndex][rowIndex] = kernelValue
		}
	}
	return kernelMatrix
}

// choleskyDecompose returns the lower-triangular Cholesky factor L
// of a symmetric positive-definite matrix: L·L^T = matrix. Upper-
// triangular entries of L are left zero. The input matrix is read
// but not modified. A non-positive diagonal pivot during the
// factorization is a sign the matrix is not positive definite — a
// bug in the caller (bad params) rather than a runtime failure, so
// the function panics with a clear message.
func choleskyDecompose(matrix [][]float64) [][]float64 {
	size := len(matrix)
	lowerFactor := make([][]float64, size)
	for rowIndex := range lowerFactor {
		lowerFactor[rowIndex] = make([]float64, size)
	}
	for rowIndex := 0; rowIndex < size; rowIndex++ {
		for columnIndex := 0; columnIndex <= rowIndex; columnIndex++ {
			sum := matrix[rowIndex][columnIndex]
			for innerIndex := 0; innerIndex < columnIndex; innerIndex++ {
				sum -= lowerFactor[rowIndex][innerIndex] * lowerFactor[columnIndex][innerIndex]
			}
			if rowIndex == columnIndex {
				if sum <= 0 {
					panic(fmt.Sprintf(
						"session: Cholesky encountered non-positive pivot %g at index %d; kernel is not positive definite",
						sum, rowIndex,
					))
				}
				lowerFactor[rowIndex][columnIndex] = math.Sqrt(sum)
			} else {
				lowerFactor[rowIndex][columnIndex] = sum / lowerFactor[columnIndex][columnIndex]
			}
		}
	}
	return lowerFactor
}

// validateEntryCurveParams asserts the structural invariants the
// curve math requires. A violation here is a composition-root bug,
// not a runtime failure the caller could do anything about, so the
// function panics. Keeping the checks here rather than in NewRunner
// keeps the curve math responsible for its own preconditions.
func validateEntryCurveParams(params EntryCurveParams) {
	if params.HeightA <= 0 {
		panic(fmt.Sprintf("session: EntryCurveParams.HeightA must be > 0, got %g", params.HeightA))
	}
	if params.TauDecay <= 0 {
		panic(fmt.Sprintf("session: EntryCurveParams.TauDecay must be > 0, got %g", params.TauDecay))
	}
	if params.LengthScale <= 0 {
		panic(fmt.Sprintf("session: EntryCurveParams.LengthScale must be > 0, got %g", params.LengthScale))
	}
	if params.VarianceSigmaSquared < 0 {
		panic(fmt.Sprintf("session: EntryCurveParams.VarianceSigmaSquared must be >= 0, got %g", params.VarianceSigmaSquared))
	}
	if params.GridPoints < 2 {
		panic(fmt.Sprintf("session: EntryCurveParams.GridPoints must be >= 2, got %d", params.GridPoints))
	}
}
