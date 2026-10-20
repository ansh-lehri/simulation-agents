package generator

import (
	"math"
	"testing"

	"simulation-agents/agent"
)

// TestDrawDispositionLandsOnSimplex pins the Dirichlet contract for the
// disposition draw: three non-negative weights that sum to one.
func TestDrawDispositionLandsOnSimplex(t *testing.T) {
	alphas := DispositionAlphas{Trend: 1, MeanReversion: 1, Noise: 1}

	for seedCounter := uint64(0); seedCounter < 100; seedCounter++ {
		disposition := drawDisposition(seededRNG(seedCounter, 3), alphas)
		if disposition.Trend < 0 || disposition.MeanRev < 0 || disposition.Noise < 0 {
			t.Fatalf("negative weight on simplex (seed %d): %+v", seedCounter, disposition)
		}
		sum := disposition.Trend + disposition.MeanRev + disposition.Noise
		if math.Abs(sum-1) > 1e-9 {
			t.Fatalf("simplex sum = %v, want ~1 (seed %d)", sum, seedCounter)
		}
	}
}

// TestDominantDispositionTiebreak locks in the fixed tiebreak order
// (trend > mean_rev > noise). The tiebreak is a reproducibility concern,
// not a modelling claim; the test exists so a refactor cannot silently
// flip the order and make seeded replays diverge.
func TestDominantDispositionTiebreak(t *testing.T) {
	allEqual := agent.Disposition{Trend: 1.0 / 3, MeanRev: 1.0 / 3, Noise: 1.0 / 3}
	if dominantDisposition(allEqual) != dispositionLabelTrend {
		t.Fatalf("tiebreak at equal weights must favor trend, got %q", dominantDisposition(allEqual))
	}
	meanRevTopTied := agent.Disposition{Trend: 0.4, MeanRev: 0.4, Noise: 0.2}
	if dominantDisposition(meanRevTopTied) != dispositionLabelTrend {
		t.Fatalf("trend-vs-mean_rev tie must favor trend, got %q", dominantDisposition(meanRevTopTied))
	}
}

// TestCoupleDietForcesMandatoryFlagByDominantDisposition is the core
// contract of the diet coupling rule: a trend-dominant agent always has
// chart ON; a mean-reversion-dominant agent always has mean_reversion
// ON; a noise-dominant agent has no mandatory channel. The other four
// flags vary across seeds (which the next test asserts).
func TestCoupleDietForcesMandatoryFlagByDominantDisposition(t *testing.T) {
	cases := []struct {
		name          string
		disposition   agent.Disposition
		assertDietHas func(diet agent.Diet) bool
		mustBeTrue    string
	}{
		{
			name:          "trend-dominant forces chart on",
			disposition:   agent.Disposition{Trend: 0.8, MeanRev: 0.1, Noise: 0.1},
			assertDietHas: func(diet agent.Diet) bool { return diet.Chart },
			mustBeTrue:    "chart",
		},
		{
			name:          "mean-rev-dominant forces mean_reversion on",
			disposition:   agent.Disposition{Trend: 0.1, MeanRev: 0.8, Noise: 0.1},
			assertDietHas: func(diet agent.Diet) bool { return diet.MeanReversion },
			mustBeTrue:    "mean_reversion",
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			for seedCounter := uint64(0); seedCounter < 200; seedCounter++ {
				diet := coupleDietFromDisposition(seededRNG(seedCounter, 5), testCase.disposition, 0.3)
				if !testCase.assertDietHas(diet) {
					t.Fatalf("diet missing mandatory %q flag (seed %d): %+v",
						testCase.mustBeTrue, seedCounter, diet)
				}
			}
		})
	}
}

// TestCoupleDietNoiseDominantHasNoMandatoryFlag confirms the noise
// branch: no channel is forced true, so the dominant flag may be false
// on at least one draw. Documented behaviour: a noise/impulse trader
// reads little, so no channel is mandatory.
func TestCoupleDietNoiseDominantHasNoMandatoryFlag(t *testing.T) {
	noiseDominant := agent.Disposition{Trend: 0.1, MeanRev: 0.1, Noise: 0.8}
	// With optional probability 0.5 and 400 seeds, each of the five
	// flags should be false at least once; that's the "nothing is
	// mandatory" assertion.
	sawFalseChart := false
	sawFalseMeanReversion := false
	sawFalseOrderBook := false
	sawFalseVolume := false
	sawFalseTimeOfSession := false
	for seedCounter := uint64(0); seedCounter < 400; seedCounter++ {
		diet := coupleDietFromDisposition(seededRNG(seedCounter, 7), noiseDominant, 0.5)
		if !diet.Chart {
			sawFalseChart = true
		}
		if !diet.MeanReversion {
			sawFalseMeanReversion = true
		}
		if !diet.OrderBook {
			sawFalseOrderBook = true
		}
		if !diet.Volume {
			sawFalseVolume = true
		}
		if !diet.TimeOfSession {
			sawFalseTimeOfSession = true
		}
	}
	if !sawFalseChart || !sawFalseMeanReversion || !sawFalseOrderBook || !sawFalseVolume || !sawFalseTimeOfSession {
		t.Fatalf("noise-dominant diet appears to force one of the five flags true: "+
			"chart=%v meanrev=%v orderbook=%v volume=%v timeofsession=%v",
			sawFalseChart, sawFalseMeanReversion, sawFalseOrderBook, sawFalseVolume, sawFalseTimeOfSession)
	}
}

// TestCoupleDietOptionalFlagsVary covers the "within-disposition variety"
// requirement: a trend-dominant agent's optional (non-mandatory) flags
// must not all be identical across seeds. Clone-agents (every
// trend-follower watches the same four extra channels) would be a
// coupling-rule bug.
func TestCoupleDietOptionalFlagsVary(t *testing.T) {
	trendDominant := agent.Disposition{Trend: 0.8, MeanRev: 0.1, Noise: 0.1}
	// For each of the four non-chart flags, we want to see it both true
	// and false across 200 seeds.
	sawTrueOrderBook, sawFalseOrderBook := false, false
	sawTrueVolume, sawFalseVolume := false, false
	for seedCounter := uint64(0); seedCounter < 200; seedCounter++ {
		diet := coupleDietFromDisposition(seededRNG(seedCounter, 13), trendDominant, 0.5)
		if diet.OrderBook {
			sawTrueOrderBook = true
		} else {
			sawFalseOrderBook = true
		}
		if diet.Volume {
			sawTrueVolume = true
		} else {
			sawFalseVolume = true
		}
	}
	if !sawTrueOrderBook || !sawFalseOrderBook {
		t.Fatalf("order_book flag never varied across 200 seeds (coupling clone)")
	}
	if !sawTrueVolume || !sawFalseVolume {
		t.Fatalf("volume flag never varied across 200 seeds (coupling clone)")
	}
}

// TestDrawOptimismBaselineInRange covers the [-1, +1] contract.
func TestDrawOptimismBaselineInRange(t *testing.T) {
	pair := BetaPair{Alpha: 2, Beta: 2}
	for seedCounter := uint64(0); seedCounter < 500; seedCounter++ {
		sample := drawOptimism(seededRNG(seedCounter, 17), pair, nil)
		if sample < -1 || sample > 1 {
			t.Fatalf("drawOptimism baseline out of [-1,1]: got %v (seed %d)", sample, seedCounter)
		}
	}
}
