package generator

import (
	"math"
	"math/rand/v2"
	"testing"

	"pgregory.net/rapid"
)

// seededRNG builds a deterministic *rand.Rand from two seed words.
// Determinism is a standing invariant of every trait draw, so every test
// in this package runs through this helper rather than touching
// package-level randomness.
func seededRNG(seedWordOne, seedWordTwo uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seedWordOne, seedWordTwo))
}

// TestDrawBetaStaysInUnitInterval checks the structural promise of the
// Beta draw. The property is the one guarantee the trait draws built on
// top rely on; magnitudes are calibration-dependent and not tested here.
func TestDrawBetaStaysInUnitInterval(t *testing.T) {
	rapid.Check(t, func(testRun *rapid.T) {
		alpha := rapid.Float64Range(0.1, 10).Draw(testRun, "alpha")
		beta := rapid.Float64Range(0.1, 10).Draw(testRun, "beta")
		seedWord := rapid.Uint64().Draw(testRun, "seed")

		rng := seededRNG(seedWord, 1)
		sample := drawBeta(rng, alpha, beta)

		if sample < 0 || sample > 1 {
			testRun.Fatalf("drawBeta(%v, %v) = %v, want in [0,1]", alpha, beta, sample)
		}
	})
}

// TestDrawDirichletLandsOnSimplex checks that every drawn vector sums to
// one (within a small floating-point tolerance) and that every component
// is non-negative. These are the two properties that make a Dirichlet
// draw usable as a disposition simplex.
func TestDrawDirichletLandsOnSimplex(t *testing.T) {
	rapid.Check(t, func(testRun *rapid.T) {
		alphaCount := rapid.IntRange(2, 6).Draw(testRun, "alphaCount")
		alphas := make([]float64, alphaCount)
		for index := range alphas {
			alphas[index] = rapid.Float64Range(0.1, 5).Draw(testRun, "alpha")
		}
		seedWord := rapid.Uint64().Draw(testRun, "seed")

		rng := seededRNG(seedWord, 2)
		sample := drawDirichlet(rng, alphas)

		if len(sample) != len(alphas) {
			testRun.Fatalf("drawDirichlet returned %d entries, want %d", len(sample), len(alphas))
		}
		sum := 0.0
		for index, value := range sample {
			if value < 0 {
				testRun.Fatalf("drawDirichlet[%d] = %v, want non-negative", index, value)
			}
			sum += value
		}
		if math.Abs(sum-1) > 1e-9 {
			testRun.Fatalf("drawDirichlet sum = %v, want ~1", sum)
		}
	})
}

// TestDrawLogNormalIsPositive is the single structural property the
// log-normal draw promises its callers: a positive scale, suitable as a
// capital size.
func TestDrawLogNormalIsPositive(t *testing.T) {
	rapid.Check(t, func(testRun *rapid.T) {
		logMean := rapid.Float64Range(-5, 15).Draw(testRun, "logMean")
		logStdDev := rapid.Float64Range(0.01, 3).Draw(testRun, "logStdDev")
		seedWord := rapid.Uint64().Draw(testRun, "seed")

		rng := seededRNG(seedWord, 3)
		sample := drawLogNormal(rng, logMean, logStdDev)

		if sample <= 0 {
			testRun.Fatalf("drawLogNormal(%v, %v) = %v, want > 0", logMean, logStdDev, sample)
		}
	})
}

// TestUniformInRangeStaysInRange covers the one invariant other primitives
// rely on; it is cheap and catches off-by-one swaps on the sampling formula.
func TestUniformInRangeStaysInRange(t *testing.T) {
	rapid.Check(t, func(testRun *rapid.T) {
		low := rapid.Float64Range(-100, 100).Draw(testRun, "low")
		high := rapid.Float64Range(low, low+100).Draw(testRun, "high")
		seedWord := rapid.Uint64().Draw(testRun, "seed")

		rng := seededRNG(seedWord, 4)
		sample := uniformInRange(rng, low, high)

		if sample < low || sample > high {
			testRun.Fatalf("uniformInRange(%v, %v) = %v, out of range", low, high, sample)
		}
	})
}

// TestCenterFromReactivityDirectionAtEndpoints pins the directional
// contract: without jitter, reactivity 0 lands on the high end, reactivity
// 1 on the low end. Everything downstream (shorter windows for jumpy
// agents, longer for deliberate ones) rides on this orientation.
func TestCenterFromReactivityDirectionAtEndpoints(t *testing.T) {
	rng := seededRNG(1, 1)

	highEnd := centerFromReactivity(rng, 5, 20, 0, 0)
	if highEnd != 20 {
		t.Fatalf("reactivity 0 with zero jitter should land on high: got %v, want 20", highEnd)
	}

	lowEnd := centerFromReactivity(rng, 5, 20, 1, 0)
	if lowEnd != 5 {
		t.Fatalf("reactivity 1 with zero jitter should land on low: got %v, want 5", lowEnd)
	}
}

// TestCenterFromReactivityStaysInRangeUnderJitter verifies the clamp
// catches jitter excursions. The jitter is Gaussian so a few sigma out
// is routine; the clamp is the whole reason the field keeps its box.
func TestCenterFromReactivityStaysInRangeUnderJitter(t *testing.T) {
	rapid.Check(t, func(testRun *rapid.T) {
		low := rapid.Float64Range(-100, 100).Draw(testRun, "low")
		high := rapid.Float64Range(low+0.01, low+200).Draw(testRun, "high")
		reactivity := rapid.Float64Range(0, 1).Draw(testRun, "reactivity")
		jitterFraction := rapid.Float64Range(0, 0.5).Draw(testRun, "jitterFraction")
		seedWord := rapid.Uint64().Draw(testRun, "seed")

		rng := seededRNG(seedWord, 5)
		sample := centerFromReactivity(rng, low, high, reactivity, jitterFraction)

		if sample < low || sample > high {
			testRun.Fatalf("centerFromReactivity escaped box: sample %v not in [%v,%v]", sample, low, high)
		}
	})
}

// TestClampFloatBehavior covers the three regions: below, inside, above.
func TestClampFloatBehavior(t *testing.T) {
	cases := []struct {
		value, low, high, want float64
	}{
		{-5, 0, 10, 0},
		{5, 0, 10, 5},
		{15, 0, 10, 10},
		{0, 0, 10, 0},
		{10, 0, 10, 10},
	}
	for _, testCase := range cases {
		got := clampFloat(testCase.value, testCase.low, testCase.high)
		if got != testCase.want {
			t.Fatalf("clampFloat(%v, %v, %v) = %v, want %v",
				testCase.value, testCase.low, testCase.high, got, testCase.want)
		}
	}
}

// TestClampIntBehavior mirrors the float coverage for the integer path.
func TestClampIntBehavior(t *testing.T) {
	cases := []struct {
		value, low, high, want int
	}{
		{-5, 0, 10, 0},
		{5, 0, 10, 5},
		{15, 0, 10, 10},
		{0, 0, 10, 0},
		{10, 0, 10, 10},
	}
	for _, testCase := range cases {
		got := clampInt(testCase.value, testCase.low, testCase.high)
		if got != testCase.want {
			t.Fatalf("clampInt(%v, %v, %v) = %v, want %v",
				testCase.value, testCase.low, testCase.high, got, testCase.want)
		}
	}
}

// TestDeterministicReplay checks the standing invariant for the whole
// package: a fixed seed replays identical sequences across runs. The
// primitives are the only place this is pinned at the primitive level;
// higher-level draws re-check it through their own golden tests.
func TestDeterministicReplay(t *testing.T) {
	firstPass := []float64{}
	rng := seededRNG(42, 7)
	for i := 0; i < 100; i++ {
		firstPass = append(firstPass, drawBeta(rng, 2, 5))
	}

	secondPass := []float64{}
	rng = seededRNG(42, 7)
	for i := 0; i < 100; i++ {
		secondPass = append(secondPass, drawBeta(rng, 2, 5))
	}

	for index := range firstPass {
		if firstPass[index] != secondPass[index] {
			t.Fatalf("determinism broken at index %d: %v vs %v", index, firstPass[index], secondPass[index])
		}
	}
}
