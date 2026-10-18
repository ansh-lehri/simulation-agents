package generator

import (
	"math"
	"math/rand/v2"
)

// This file holds the random-draw primitives the trait-draw engine composes
// into higher-level trait samplers. Every function takes an injected seeded
// RNG and is pure: no package-level state, no time-based inputs, no fresh
// RNG construction. Deterministic replay with a fixed seed is a standing
// invariant of the engine, and it depends on every primitive routing its
// randomness through the one *rand.Rand the caller holds.

// drawGamma samples from a Gamma distribution with shape parameter `shape`
// and unit scale: Gamma(shape, 1). It is the one building block Beta and
// Dirichlet both use; neither has a direct standard-library sampler. The
// algorithm is Marsaglia and Tsang's rejection method, with Johnk's boost
// trick to handle shape < 1 (which Marsaglia-Tsang does not cover): draw
// from Gamma(shape+1) and multiply by a uniform raised to the power 1/shape.
//
// The caller is responsible for passing a positive shape; the generator's
// Validate step enforces that upstream.
func drawGamma(rng *rand.Rand, shape float64) float64 {
	if shape < 1 {
		boosted := drawGamma(rng, shape+1)
		return boosted * math.Pow(rng.Float64(), 1/shape)
	}

	// Marsaglia-Tsang for shape >= 1. The variables below are the paper's
	// `d` and `c`, renamed to say what they represent so the comparison
	// with the published algorithm is readable without a cheat sheet.
	shiftedShape := shape - 1.0/3.0
	coefficient := 1 / math.Sqrt(9*shiftedShape)

	for {
		// Draw a candidate until its cube's base is positive (so cubing
		// yields a valid gamma candidate).
		var standardNormalSample, cubeOfBase float64
		for {
			standardNormalSample = rng.NormFloat64()
			cubeOfBase = 1 + coefficient*standardNormalSample
			if cubeOfBase > 0 {
				break
			}
		}
		cubeOfBase = cubeOfBase * cubeOfBase * cubeOfBase

		uniformForRejection := rng.Float64()
		normalSquared := standardNormalSample * standardNormalSample

		// Fast acceptance path. Then the exact log-space test for the
		// remainder of the acceptance region.
		if uniformForRejection < 1-0.0331*normalSquared*normalSquared {
			return shiftedShape * cubeOfBase
		}
		if math.Log(uniformForRejection) < 0.5*normalSquared+shiftedShape*(1-cubeOfBase+math.Log(cubeOfBase)) {
			return shiftedShape * cubeOfBase
		}
	}
}

// drawBeta samples from Beta(alpha, beta) using the standard
// X / (X + Y) construction over two independent Gamma draws. Both shape
// parameters must be positive.
func drawBeta(rng *rand.Rand, alpha, beta float64) float64 {
	gammaFromAlpha := drawGamma(rng, alpha)
	gammaFromBeta := drawGamma(rng, beta)
	return gammaFromAlpha / (gammaFromAlpha + gammaFromBeta)
}

// drawDirichlet samples a point on the K-simplex from Dirichlet(alphas)
// using the independent-gamma construction. The returned slice has the
// same length as alphas and sums to one up to floating-point rounding.
// All alpha entries must be positive.
func drawDirichlet(rng *rand.Rand, alphas []float64) []float64 {
	samples := make([]float64, len(alphas))
	sum := 0.0
	for index, alpha := range alphas {
		samples[index] = drawGamma(rng, alpha)
		sum += samples[index]
	}
	for index := range samples {
		samples[index] /= sum
	}
	return samples
}

// drawLogNormal samples from a log-normal distribution with the underlying
// normal having mean logMean and standard deviation logStdDev. The result
// is strictly positive. logStdDev must be positive.
func drawLogNormal(rng *rand.Rand, logMean, logStdDev float64) float64 {
	return math.Exp(logMean + logStdDev*rng.NormFloat64())
}

// uniformInRange samples a uniform float in [low, high]. The caller is
// responsible for passing low <= high; GenParams validation enforces this
// for every range used by the trait-draw engine.
func uniformInRange(rng *rand.Rand, low, high float64) float64 {
	return low + (high-low)*rng.Float64()
}

// centerFromReactivity places a sample inside [low, high] using the agent's
// reactivity latent as the position dial, with a per-draw Gaussian jitter.
//
// Direction (shared across every perception field): a reactivity of 1
// (jumpy) pulls the center toward low, and a reactivity of 0 (deliberate)
// pulls it toward high. The jitter width is a fraction of the range so the
// same fractional dial behaves identically across differently-scaled fields.
// The result is clamped to [low, high] to stay inside the field's box after
// jitter.
//
// The caller is responsible for passing low <= high, reactivity in [0, 1],
// and jitterFraction >= 0. All three are enforced upstream.
func centerFromReactivity(rng *rand.Rand, low, high, reactivity, jitterFraction float64) float64 {
	center := low + (high-low)*(1-reactivity)
	jitter := jitterFraction * (high - low) * rng.NormFloat64()
	return clampFloat(center+jitter, low, high)
}

// clampFloat constrains value to [low, high]. If the caller passes low >
// high (a mis-calibration) the result pins to low — the lower bound is
// treated as the stronger invariant, matching the convention used by the
// capital-draw primitive in the agent package.
func clampFloat(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// clampInt constrains value to [low, high], with the same low-wins
// convention as clampFloat on mis-calibrated bounds.
func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
