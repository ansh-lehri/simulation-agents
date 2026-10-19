package session

import (
	"math/rand/v2"
	"testing"
)

// TestEntryCurveBuildAndDrawDeterministic pins the "live = fresh,
// tuning = fixed-seed" rule: with the same seeded RNG, two separate
// Build+Draw sequences must produce identical rhythms and identical
// draws. A seeded run that drifts from itself breaks every regression
// baseline a tuning run depends on.
func TestEntryCurveBuildAndDrawDeterministic(t *testing.T) {
	t.Parallel()

	params := DefaultEntryCurveParams()
	populationSize := 128

	drawOne := drawAllFractions(seededRNG(), params, populationSize)
	drawTwo := drawAllFractions(seededRNG(), params, populationSize)

	if len(drawOne) != populationSize || len(drawTwo) != populationSize {
		t.Fatalf("want %d draws in each run; got len(one)=%d len(two)=%d",
			populationSize, len(drawOne), len(drawTwo))
	}
	for index, fractionOne := range drawOne {
		if fractionOne != drawTwo[index] {
			t.Fatalf("determinism broken at index %d: run one = %v, run two = %v",
				index, fractionOne, drawTwo[index])
		}
	}
}

// TestEntryCurveDrawsStayInRange is the hard contract DrawEntryFraction
// promises: every returned fraction is in [0, 1]. A fraction outside
// that range would silently mis-stamp an agent's entry time at a
// wall-clock before the open or after the close — a kind of bug that
// only shows up as mysteriously-active agents in the tick loop.
func TestEntryCurveDrawsStayInRange(t *testing.T) {
	t.Parallel()

	params := DefaultEntryCurveParams()
	rng := seededRNG()
	curve := BuildEntryCurve(rng, params)

	const drawCount = 10000
	for drawIndex := 0; drawIndex < drawCount; drawIndex++ {
		fraction := curve.DrawEntryFraction(rng)
		if fraction < 0.0 || fraction > 1.0 {
			t.Fatalf("fraction %v (draw %d) is outside [0, 1]", fraction, drawIndex)
		}
	}
}

// TestEntryCurveIsOpenWeighted asserts the shape promise the design
// doc writes down: with the baseline exponential-decay base rate,
// arrivals must concentrate early in the session. A session where
// arrivals are flat or close-weighted would feel wrong (no open
// rush) and would hide a bug in the base-rate wiring (sign flip on
// tauDecay, swapped exponent, etc).
//
// The GP factor adds per-session variance, so a single draw is not
// a reliable shape test; we average over many sessions by rebuilding
// the curve under different seeds and pooling the draws. The
// threshold is loose on purpose: a correct implementation beats it
// comfortably; a broken base rate flunks by a wide margin.
func TestEntryCurveIsOpenWeighted(t *testing.T) {
	t.Parallel()

	params := DefaultEntryCurveParams()
	const sessionCount = 32
	const drawsPerSession = 512
	openSideCount := 0
	closeSideCount := 0
	for sessionIndex := 0; sessionIndex < sessionCount; sessionIndex++ {
		rng := seededRNGWithOffset(uint64(sessionIndex))
		curve := BuildEntryCurve(rng, params)
		for drawIndex := 0; drawIndex < drawsPerSession; drawIndex++ {
			fraction := curve.DrawEntryFraction(rng)
			switch {
			case fraction <= 0.3:
				openSideCount++
			case fraction >= 0.7:
				closeSideCount++
			}
		}
	}
	if closeSideCount == 0 {
		t.Fatalf("no close-side draws observed; the pool is too thin to compare")
	}
	ratio := float64(openSideCount) / float64(closeSideCount)
	if ratio < 2.0 {
		t.Fatalf(
			"baseline curve must be open-weighted (open count / close count >= 2.0); got open=%d close=%d ratio=%v",
			openSideCount, closeSideCount, ratio,
		)
	}
}

// TestEntryCurveRejectsInvalidParams walks every validated invariant
// on EntryCurveParams and asserts BuildEntryCurve panics with a
// message naming the offending field. Silently accepting a
// non-positive TauDecay would produce a divide-by-zero or inverted
// curve; silently accepting GridPoints < 2 would divide by zero
// building the uniform grid. Both are coupling bugs at the
// composition root, so panic is the right exit.
func TestEntryCurveRejectsInvalidParams(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		fieldName  string
		mutate     func(*EntryCurveParams)
		substring  string
	}{
		{"HeightA_zero", func(params *EntryCurveParams) { params.HeightA = 0 }, "HeightA"},
		{"HeightA_negative", func(params *EntryCurveParams) { params.HeightA = -1 }, "HeightA"},
		{"TauDecay_zero", func(params *EntryCurveParams) { params.TauDecay = 0 }, "TauDecay"},
		{"TauDecay_negative", func(params *EntryCurveParams) { params.TauDecay = -1 }, "TauDecay"},
		{"LengthScale_zero", func(params *EntryCurveParams) { params.LengthScale = 0 }, "LengthScale"},
		{"LengthScale_negative", func(params *EntryCurveParams) { params.LengthScale = -1 }, "LengthScale"},
		{"VarianceSigmaSquared_negative", func(params *EntryCurveParams) { params.VarianceSigmaSquared = -1 }, "VarianceSigmaSquared"},
		{"GridPoints_zero", func(params *EntryCurveParams) { params.GridPoints = 0 }, "GridPoints"},
		{"GridPoints_one", func(params *EntryCurveParams) { params.GridPoints = 1 }, "GridPoints"},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.fieldName, func(t *testing.T) {
			t.Parallel()
			params := DefaultEntryCurveParams()
			testCase.mutate(&params)
			defer func() {
				recovered := recover()
				if recovered == nil {
					t.Fatalf("BuildEntryCurve must panic on bad %s; got no panic", testCase.fieldName)
				}
				panicMessage, isString := recovered.(string)
				if !isString {
					t.Fatalf("panic value must be a string with the field name; got %T %v", recovered, recovered)
				}
				if !containsSubstring(panicMessage, testCase.substring) {
					t.Fatalf("panic on bad %s must mention %q; got %q",
						testCase.fieldName, testCase.substring, panicMessage)
				}
			}()
			_ = BuildEntryCurve(seededRNG(), params)
		})
	}
}

// TestDefaultEntryCurveParamsAreTheBaseline pins the baseline-tuned
// values. A drift here changes the shape of every session that does
// not override the dials — the test fails loudly so an unintended
// change is caught in review rather than appearing as mysteriously
// different arrival patterns in a tuning run.
func TestDefaultEntryCurveParamsAreTheBaseline(t *testing.T) {
	t.Parallel()

	params := DefaultEntryCurveParams()
	if params.HeightA != 1.0 {
		t.Fatalf("HeightA baseline must be 1.0; got %v", params.HeightA)
	}
	if params.TauDecay != 0.35 {
		t.Fatalf("TauDecay baseline must be 0.35; got %v", params.TauDecay)
	}
	if params.LengthScale != 0.15 {
		t.Fatalf("LengthScale baseline must be 0.15; got %v", params.LengthScale)
	}
	if params.VarianceSigmaSquared != 0.25 {
		t.Fatalf("VarianceSigmaSquared baseline must be 0.25; got %v", params.VarianceSigmaSquared)
	}
	if params.GridPoints != 64 {
		t.Fatalf("GridPoints baseline must be 64; got %d", params.GridPoints)
	}
}

// --- helpers

// seededRNG returns a *rand.Rand with a fixed PCG seed shared across
// deterministic tests. Giving every determinism test the same source
// keeps the recorded failures reproducible.
func seededRNG() *rand.Rand {
	return rand.New(rand.NewPCG(0xC0FFEE, 0xDECADE))
}

// seededRNGWithOffset returns a *rand.Rand seeded off the shared base
// plus an offset, so pooled tests can walk a reproducible sequence
// of independent sessions.
func seededRNGWithOffset(offset uint64) *rand.Rand {
	return rand.New(rand.NewPCG(0xC0FFEE+offset, 0xDECADE+offset))
}

// drawAllFractions builds one curve and draws populationSize
// fractions off it, so the determinism test can compare two full
// (build + draw) sequences under the same seed.
func drawAllFractions(rng *rand.Rand, params EntryCurveParams, populationSize int) []float64 {
	curve := BuildEntryCurve(rng, params)
	fractions := make([]float64, populationSize)
	for index := 0; index < populationSize; index++ {
		fractions[index] = curve.DrawEntryFraction(rng)
	}
	return fractions
}

// containsSubstring is a small substring check without pulling in
// the strings package just for this one usage.
func containsSubstring(whole, part string) bool {
	if len(part) == 0 {
		return true
	}
	for startIndex := 0; startIndex+len(part) <= len(whole); startIndex++ {
		if whole[startIndex:startIndex+len(part)] == part {
			return true
		}
	}
	return false
}
