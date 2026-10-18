package scenario

import (
	"math"
	"testing"

	"pgregory.net/rapid"
)

// floatCloseEnough is the tolerance for round-trip assertions. The link
// transforms are smooth and well-conditioned on the interior of each
// ruler's domain; the only error source is double-precision rounding
// across two function calls, which stays well under 1e-9 for the
// inputs the engine ever sees.
const floatCloseEnough = 1e-9

// TestLogitRoundTrip asserts that fromLink reverses toLink on the open
// interval (0, 1). The property-test range excludes the endpoints
// because the forward map blows up there — a knob with a moment at
// exactly 0 or 1 is a miscalibration the generator's Validate step
// rejects long before a shove reaches this layer.
func TestLogitRoundTrip(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		mean := rapid.Float64Range(1e-6, 1-1e-6).Draw(propertyRun, "mean")
		recovered := fromLink(Logit, toLink(Logit, mean))
		if math.Abs(recovered-mean) > floatCloseEnough {
			propertyRun.Fatalf("logit round-trip drifted: input=%v recovered=%v", mean, recovered)
		}
	})
}

// TestLogRoundTrip asserts that fromLink reverses toLink on the
// strictly positive reals. The range stops short of float64 overflow
// so exp(toLink(x)) stays finite.
func TestLogRoundTrip(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		positiveMoment := rapid.Float64Range(1e-6, 1e6).Draw(propertyRun, "positive_moment")
		recovered := fromLink(Log, toLink(Log, positiveMoment))
		if math.Abs(recovered-positiveMoment) > floatCloseEnough*math.Max(1, positiveMoment) {
			propertyRun.Fatalf("log round-trip drifted: input=%v recovered=%v", positiveMoment, recovered)
		}
	})
}

// TestArctanhRoundTrip asserts that fromLink reverses toLink on the
// open interval (-1, +1), the domain of the optimism center knob.
func TestArctanhRoundTrip(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		center := rapid.Float64Range(-1+1e-6, 1-1e-6).Draw(propertyRun, "center")
		recovered := fromLink(Arctanh, toLink(Arctanh, center))
		if math.Abs(recovered-center) > floatCloseEnough {
			propertyRun.Fatalf("arctanh round-trip drifted: input=%v recovered=%v", center, recovered)
		}
	})
}

// TestLogitNoClampUnderShift is the invariant that the whole link-space
// scheme exists to buy: shifting the link value by any plausible amount
// cannot push the inverse out of (0, 1). A failure here means a clamp
// has been reintroduced upstream (wrong ruler, wrong moment extraction)
// or the ruler itself was miscoded.
func TestLogitNoClampUnderShift(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		mean := rapid.Float64Range(1e-6, 1-1e-6).Draw(propertyRun, "mean")
		shift := rapid.Float64Range(-10, 10).Draw(propertyRun, "shift")
		shifted := fromLink(Logit, toLink(Logit, mean)+shift)
		if !(shifted > 0 && shifted < 1) {
			propertyRun.Fatalf("logit shift left domain: input=%v shift=%v shifted=%v", mean, shift, shifted)
		}
	})
}

// TestLogNoClampUnderShift asserts strict positivity of the Log
// inverse under any finite shift. exp never produces zero or a
// negative number for a finite input, so the test range covers the
// full plausible shove window.
func TestLogNoClampUnderShift(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		positiveMoment := rapid.Float64Range(1e-3, 1e3).Draw(propertyRun, "positive_moment")
		shift := rapid.Float64Range(-10, 10).Draw(propertyRun, "shift")
		shifted := fromLink(Log, toLink(Log, positiveMoment)+shift)
		if !(shifted > 0) {
			propertyRun.Fatalf("log shift left domain: input=%v shift=%v shifted=%v", positiveMoment, shift, shifted)
		}
	})
}

// TestArctanhNoClampUnderShift asserts the inverse stays strictly in
// (-1, +1) under any shift tanh can process without saturating to the
// endpoints at float64 precision.
func TestArctanhNoClampUnderShift(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		center := rapid.Float64Range(-1+1e-3, 1-1e-3).Draw(propertyRun, "center")
		shift := rapid.Float64Range(-5, 5).Draw(propertyRun, "shift")
		shifted := fromLink(Arctanh, toLink(Arctanh, center)+shift)
		if !(shifted > -1 && shifted < 1) {
			propertyRun.Fatalf("arctanh shift left domain: input=%v shift=%v shifted=%v", center, shift, shifted)
		}
	})
}

// TestLogRatioRoundTrip asserts that fromLogRatio reverses toLogRatio
// on the open simplex. The property draws three strictly positive
// values and normalizes them to a simplex before the round-trip, so
// every input is a valid Dirichlet mean.
func TestLogRatioRoundTrip(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		rawFirst := rapid.Float64Range(0.01, 10).Draw(propertyRun, "raw_first")
		rawSecond := rapid.Float64Range(0.01, 10).Draw(propertyRun, "raw_second")
		rawThird := rapid.Float64Range(0.01, 10).Draw(propertyRun, "raw_third")
		rawSum := rawFirst + rawSecond + rawThird
		simplex := []float64{rawFirst / rawSum, rawSecond / rawSum, rawThird / rawSum}
		recovered := fromLogRatio(toLogRatio(simplex))
		for index := range simplex {
			if math.Abs(recovered[index]-simplex[index]) > floatCloseEnough {
				propertyRun.Fatalf("log-ratio round-trip drifted at %d: input=%v recovered=%v",
					index, simplex, recovered)
			}
		}
	})
}

// TestLogRatioSoftmaxSumsToOne guards the simplex invariant that the
// generator relies on: after any shove the renormalized proportions
// must still sum to 1 and each component must be strictly positive.
func TestLogRatioSoftmaxSumsToOne(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		first := rapid.Float64Range(-10, 10).Draw(propertyRun, "first_logratio")
		second := rapid.Float64Range(-10, 10).Draw(propertyRun, "second_logratio")
		third := rapid.Float64Range(-10, 10).Draw(propertyRun, "third_logratio")
		simplex := fromLogRatio([]float64{first, second, third})
		total := 0.0
		for _, proportion := range simplex {
			if !(proportion > 0) {
				propertyRun.Fatalf("log-ratio softmax produced non-positive component: %v", simplex)
			}
			total += proportion
		}
		if math.Abs(total-1) > floatCloseEnough {
			propertyRun.Fatalf("log-ratio softmax did not sum to 1: sum=%v simplex=%v", total, simplex)
		}
	})
}

// TestLogRatioTranslationInvariant pins the softmax identity the
// merge engine depends on: shifting every log-ratio by the same
// constant leaves the simplex unchanged. This is why the vector form
// does not need a designated reference component — any uniform drift
// cancels out, so direction arrows that touch every disposition slot
// uniformly produce no change.
func TestLogRatioTranslationInvariant(t *testing.T) {
	rapid.Check(t, func(propertyRun *rapid.T) {
		first := rapid.Float64Range(-5, 5).Draw(propertyRun, "first")
		second := rapid.Float64Range(-5, 5).Draw(propertyRun, "second")
		third := rapid.Float64Range(-5, 5).Draw(propertyRun, "third")
		uniformShift := rapid.Float64Range(-10, 10).Draw(propertyRun, "uniform_shift")

		baseline := fromLogRatio([]float64{first, second, third})
		shifted := fromLogRatio([]float64{
			first + uniformShift,
			second + uniformShift,
			third + uniformShift,
		})
		for index := range baseline {
			if math.Abs(baseline[index]-shifted[index]) > floatCloseEnough {
				propertyRun.Fatalf("uniform shift changed softmax at %d: baseline=%v shifted=%v",
					index, baseline, shifted)
			}
		}
	})
}

// TestScalarRulersRejectLogRatio asserts both scalar entry points
// panic when handed the vector-valued ruler. A missed dispatch at the
// merge-engine layer would otherwise silently return a meaningless
// number instead of surfacing the bug.
func TestScalarRulersRejectLogRatio(t *testing.T) {
	assertPanics(t, "toLink(LogRatio)", func() { _ = toLink(LogRatio, 0.5) })
	assertPanics(t, "fromLink(LogRatio)", func() { _ = fromLink(LogRatio, 0.0) })
}

// assertPanics runs the given thunk and fails the test if the call
// returns normally. Reads clearer than littering tests with manual
// defer/recover blocks.
func assertPanics(t *testing.T, label string, thunk func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatalf("%s: expected panic, got no panic", label)
		}
	}()
	thunk()
}
