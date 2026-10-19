package scenario

import (
	"math"
	"math/rand/v2"
)

// This file holds the random-draw primitives the baseline builder
// uses to measure each knob's crowd-width (eta) in link space. The
// algorithms mirror the trait-draw engine's samplers; they are
// duplicated here (rather than shared through a common package) so
// the baseline computation is self-contained and does not reach into
// another package's unexported helpers. Both sets of samplers are
// deterministic under a fixed seed.

// sampleGamma draws from Gamma(shape, 1) via Marsaglia and Tsang's
// rejection method, with Johnk's boost for shape < 1. The algorithm
// is well-known; the implementation uses contextual names
// (shiftedShape, coefficient, standardNormalSample, cubeOfBase) so a
// reader following the published paper can match variables without
// a cheat sheet. The caller passes positive shape.
func sampleGamma(rng *rand.Rand, shape float64) float64 {
	if shape < 1 {
		boosted := sampleGamma(rng, shape+1)
		return boosted * math.Pow(rng.Float64(), 1/shape)
	}

	shiftedShape := shape - 1.0/3.0
	coefficient := 1 / math.Sqrt(9*shiftedShape)

	for {
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

		if uniformForRejection < 1-0.0331*normalSquared*normalSquared {
			return shiftedShape * cubeOfBase
		}
		if math.Log(uniformForRejection) <
			0.5*normalSquared+shiftedShape*(1-cubeOfBase+math.Log(cubeOfBase)) {
			return shiftedShape * cubeOfBase
		}
	}
}

// sampleBeta draws from Beta(alpha, beta) via the standard
// X / (X + Y) construction over two independent Gamma draws. Both
// shape parameters must be positive.
func sampleBeta(rng *rand.Rand, alpha, beta float64) float64 {
	gammaFromAlpha := sampleGamma(rng, alpha)
	gammaFromBeta := sampleGamma(rng, beta)
	return gammaFromAlpha / (gammaFromAlpha + gammaFromBeta)
}

// sampleLogNormal draws from a log-normal distribution whose
// underlying normal has mean mu and standard deviation sigma. The
// result is strictly positive; sigma must be positive.
func sampleLogNormal(rng *rand.Rand, mu, sigma float64) float64 {
	return math.Exp(mu + sigma*rng.NormFloat64())
}

// sampleDirichlet draws a point on the K-simplex from
// Dirichlet(alphas) via the independent-gamma construction. The
// returned slice has the same length as alphas and sums to one up to
// floating-point rounding.
func sampleDirichlet(rng *rand.Rand, alphas []float64) []float64 {
	samples := make([]float64, len(alphas))
	total := 0.0
	for index, alpha := range alphas {
		samples[index] = sampleGamma(rng, alpha)
		total += samples[index]
	}
	for index := range samples {
		samples[index] /= total
	}
	return samples
}

// standardDeviation returns the sample standard deviation of a slice
// of floats using the two-pass formula. The baseline builder calls
// this with sample sizes in the low thousands; the double traversal
// is irrelevant at that scale and the formula reads cleanly.
func standardDeviation(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	mean := sum / float64(len(values))
	sumOfSquaredDeviations := 0.0
	for _, value := range values {
		deviation := value - mean
		sumOfSquaredDeviations += deviation * deviation
	}
	return math.Sqrt(sumOfSquaredDeviations / float64(len(values)))
}
