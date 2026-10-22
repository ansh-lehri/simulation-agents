package decision

import (
	"math"
	"math/rand/v2"
	"testing"

	"pgregory.net/rapid"

	"simulation-agents/agent"
)

func testMixerParams() MixerParams {
	return MixerParams{
		SecondarySensitivity:   0.5,
		NoiseImpulseConviction: 0.5,
	}
}

func floatPtr(value float64) *float64 { return &value }

// A disposition whose Noise weight is 1 always takes the bypass
// branch regardless of what the RNG draws — this is what lets the
// bypass tests be deterministic without needing to reason about the
// exact stream of float64 values a given seed produces.
func TestMix_NoiseBypass_AlwaysTriggersAndReturnsAnImpulse(t *testing.T) {
	disposition := agent.Disposition{Trend: 0, MeanRev: 0, Noise: 1}
	params := testMixerParams()

	sawPositive, sawNegative := false, false
	for seed := uint64(0); seed < 50; seed++ {
		agentRNG := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
		opinion, bypass := mix(traitVerdicts{}, disposition, agentRNG, params)

		if !bypass {
			t.Fatalf("seed %d: bypass=false, want true (Noise=1 must always trigger)", seed)
		}
		if opinion == nil {
			t.Fatalf("seed %d: opinion=nil on a bypass, want a random impulse", seed)
		}
		if opinion.Conviction != params.NoiseImpulseConviction {
			t.Errorf("seed %d: Conviction=%v, want %v", seed, opinion.Conviction, params.NoiseImpulseConviction)
		}
		switch opinion.Direction {
		case 1:
			sawPositive = true
		case -1:
			sawNegative = true
		default:
			t.Errorf("seed %d: Direction=%v, want +1 or -1", seed, opinion.Direction)
		}
	}
	if !sawPositive || !sawNegative {
		t.Errorf("noise impulse direction never varied across 50 seeds (positive=%v negative=%v) — the direction coin may be hardcoded", sawPositive, sawNegative)
	}
}

// A disposition whose Noise weight is 0 can never take the bypass
// branch, regardless of what the RNG draws — the complementary
// determinism anchor to the Noise=1 case above.
func TestMix_NoiseBypass_NeverTriggersWhenNoiseWeightIsZero(t *testing.T) {
	disposition := agent.Disposition{Trend: 0.5, MeanRev: 0.5, Noise: 0}
	params := testMixerParams()

	for seed := uint64(0); seed < 20; seed++ {
		agentRNG := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
		_, bypass := mix(traitVerdicts{trend: floatPtr(0.5)}, disposition, agentRNG, params)
		if bypass {
			t.Fatalf("seed %d: bypass=true, want false (Noise=0 must never trigger)", seed)
		}
	}
}

func TestMix_ZeroVerdicts_ReturnsNilOpinion(t *testing.T) {
	disposition := agent.Disposition{Trend: 0.5, MeanRev: 0.5, Noise: 0}
	agentRNG := rand.New(rand.NewPCG(1, 2))

	opinion, bypass := mix(traitVerdicts{}, disposition, agentRNG, testMixerParams())
	if opinion != nil {
		t.Errorf("opinion: got %+v, want nil", opinion)
	}
	if bypass {
		t.Error("bypass: got true, want false — zero verdicts is a SKIP, not an impulse")
	}
}

func TestMix_OneVerdict_UsedVerbatimWithNoMultiplier(t *testing.T) {
	disposition := agent.Disposition{Trend: 0.9, MeanRev: 0.1, Noise: 0}
	params := testMixerParams()

	cases := []struct {
		name     string
		verdicts traitVerdicts
	}{
		{"trend only", traitVerdicts{trend: floatPtr(-0.6)}},
		{"meanRev only", traitVerdicts{meanRev: floatPtr(0.3)}},
	}
	for _, testCase := range cases {
		agentRNG := rand.New(rand.NewPCG(5, 6))
		opinion, bypass := mix(testCase.verdicts, disposition, agentRNG, params)
		if bypass {
			t.Errorf("%s: bypass=true, want false", testCase.name)
		}
		if opinion == nil {
			t.Fatalf("%s: opinion=nil, want a present opinion", testCase.name)
		}

		var onlyVerdict float64
		if testCase.verdicts.trend != nil {
			onlyVerdict = *testCase.verdicts.trend
		} else {
			onlyVerdict = *testCase.verdicts.meanRev
		}
		wantConviction := math.Abs(onlyVerdict)
		if opinion.Conviction != wantConviction {
			t.Errorf("%s: Conviction=%v, want %v (no multiplier applied when only one trait has a verdict)", testCase.name, opinion.Conviction, wantConviction)
		}
		if opinion.Direction != directionOf(onlyVerdict) {
			t.Errorf("%s: Direction=%v, want %v", testCase.name, opinion.Direction, directionOf(onlyVerdict))
		}
	}
}

// Property test: across many random (trend verdict, mean-rev verdict,
// trend weight, mean-rev weight, seed) combinations, the secondary
// trait may dampen or boost conviction but must never flip which way
// the opinion points. Noise is forced to 0 so every draw lands on the
// dominant/secondary combine path, never the bypass branch.
func TestMix_SecondaryNeverFlipsDirection(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		trendVerdict := rapid.Float64Range(-1, 1).Draw(rt, "trend_verdict")
		meanRevVerdict := rapid.Float64Range(-1, 1).Draw(rt, "mean_rev_verdict")
		trendWeight := rapid.Float64Range(0, 1).Draw(rt, "trend_weight")
		meanRevWeight := rapid.Float64Range(0, 1).Draw(rt, "mean_rev_weight")
		secondarySensitivity := rapid.Float64Range(0, 5).Draw(rt, "secondary_sensitivity")
		seed := rapid.Uint64().Draw(rt, "seed")

		disposition := agent.Disposition{Trend: trendWeight, MeanRev: meanRevWeight, Noise: 0}
		params := MixerParams{SecondarySensitivity: secondarySensitivity}
		agentRNG := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))

		opinion, bypass := mix(traitVerdicts{trend: &trendVerdict, meanRev: &meanRevVerdict}, disposition, agentRNG, params)
		if bypass {
			rt.Fatal("bypass=true with Noise=0 — the coin must never trigger")
		}
		if opinion == nil {
			rt.Fatal("opinion=nil with two present verdicts — SKIP must only occur with zero verdicts")
		}
		if opinion.Conviction < 0 || opinion.Conviction > 1 {
			rt.Fatalf("Conviction=%v out of [0,1]", opinion.Conviction)
		}

		dominantVerdict := trendVerdict
		if meanRevWeight > trendWeight {
			dominantVerdict = meanRevVerdict
		} else if meanRevWeight == trendWeight {
			return // tie-break is RNG-dependent; sign(dominant) isn't predictable from inputs alone, covered separately
		}
		if directionOf(opinion.Direction) != directionOf(dominantVerdict) {
			rt.Fatalf("Direction=%v does not match the dominant verdict's sign (dominant=%v): secondary flipped direction", opinion.Direction, dominantVerdict)
		}
	})
}

// The dominance tie-break must be deterministic under a fixed seed
// (same seed, same inputs, same choice every time) but must actually
// consult the RNG rather than always favoring one trait — verified by
// sweeping seeds and confirming both choices occur.
func TestMix_TieBreak_DeterministicButActuallyRandom(t *testing.T) {
	disposition := agent.Disposition{Trend: 0.5, MeanRev: 0.5, Noise: 0}
	params := testMixerParams()
	// Opposite-signed verdicts make the tie-break's choice observable
	// directly from the resulting Direction.
	verdicts := traitVerdicts{trend: floatPtr(0.8), meanRev: floatPtr(-0.6)}

	for seed := uint64(0); seed < 10; seed++ {
		first, _ := mix(verdicts, disposition, rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)), params)
		second, _ := mix(verdicts, disposition, rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)), params)
		if first.Direction != second.Direction {
			t.Fatalf("seed %d: tie-break not deterministic — first=%v second=%v", seed, first.Direction, second.Direction)
		}
	}

	sawTrendChosen, sawMeanRevChosen := false, false
	for seed := uint64(0); seed < 50; seed++ {
		opinion, _ := mix(verdicts, disposition, rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)), params)
		if opinion.Direction == 1 {
			sawTrendChosen = true
		} else {
			sawMeanRevChosen = true
		}
	}
	if !sawTrendChosen || !sawMeanRevChosen {
		t.Errorf("tie-break never varied across 50 seeds (trend=%v meanRev=%v) — the coin may be hardcoded to one side", sawTrendChosen, sawMeanRevChosen)
	}
}
