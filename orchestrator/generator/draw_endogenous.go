package generator

import (
	"math/rand/v2"

	"simulation-agents/agent"
)

// This file holds the trait draws that only an endogenous agent carries:
// the disposition simplex, the coupled information diet, the reactivity
// and optimism latents. Herd agents consume none of these — their
// branch exits before this file's draws run.

// dispositionLabel names a point on the disposition simplex. Values are
// the string literals persisted on the agent record. "mean_rev" matches
// the JSON tag on agent.Disposition and the dominant-trait label used by
// the diet-coupling rule.
const (
	dispositionLabelTrend   = "trend"
	dispositionLabelMeanRev = "mean_rev"
	dispositionLabelNoise   = "noise"
)

// drawDisposition samples the three-way disposition simplex from a
// Dirichlet with the configured concentration triple. The returned
// struct's three weights sum to one and name the mix over trading styles:
// trend-following, mean-reversion, and noise/impulse. The one with the
// largest weight is the agent's "dominant" style, which the diet coupling
// consults next.
func drawDisposition(rng *rand.Rand, alphas DispositionAlphas) agent.Disposition {
	samples := drawDirichlet(rng, []float64{alphas.Trend, alphas.MeanReversion, alphas.Noise})
	return agent.Disposition{
		Trend:   samples[0],
		MeanRev: samples[1],
		Noise:   samples[2],
	}
}

// dominantDisposition names the trait with the largest weight on the
// simplex. Ties are broken by the fixed order trend > mean_rev > noise so
// a mint under a fixed seed always lands on the same label even when two
// Dirichlet draws happen to land on identical weights — a reproducibility
// concern, not a modelling claim.
func dominantDisposition(disposition agent.Disposition) string {
	dominant := dispositionLabelTrend
	highestWeight := disposition.Trend
	if disposition.MeanRev > highestWeight {
		dominant = dispositionLabelMeanRev
		highestWeight = disposition.MeanRev
	}
	if disposition.Noise > highestWeight {
		dominant = dispositionLabelNoise
	}
	return dominant
}

// coupleDietFromDisposition produces the agent's information diet from
// its disposition using the coupled-sampling rule: draw a disposition,
// then force the channel its dominant trait structurally reads ON, and
// toss independent coins for the other four flags.
//
// Why coupled: a trend-follower that reads only news (never charts) is
// an incoherent agent; the dominant-forced mandatory flag rules that
// combination out. Why still coins on the rest: a trend-follower who
// also watches volume behaves differently on a fake breakout than one
// who does not, and that within-disposition texture is what keeps a
// population from being disposition-labeled clones.
//
// The five coin flips happen whichever disposition is dominant, so the
// RNG consumption is identical across endogenous agents and a shift in
// the disposition mix does not re-align every downstream draw.
func coupleDietFromDisposition(rng *rand.Rand, disposition agent.Disposition, optionalFlagProbability float64) agent.Diet {
	diet := agent.Diet{
		Chart:         coinFlip(rng, optionalFlagProbability),
		OrderBook:     coinFlip(rng, optionalFlagProbability),
		MeanReversion: coinFlip(rng, optionalFlagProbability),
		Volume:        coinFlip(rng, optionalFlagProbability),
		TimeOfSession: coinFlip(rng, optionalFlagProbability),
	}

	switch dominantDisposition(disposition) {
	case dispositionLabelTrend:
		// A trend-follower is defined by watching the price trend; the
		// chart channel is forced on so the agent perceives what it
		// decides on.
		diet.Chart = true
	case dispositionLabelMeanRev:
		// A mean-reversion trader reads range/mean-reversion signals;
		// the matching channel is forced on.
		diet.MeanReversion = true
	case dispositionLabelNoise:
		// A noise/impulse trader reads little by design — no channel is
		// mandatory. The five optional coins already decide its diet.
	}
	return diet
}

// drawReactivity samples the per-agent reactivity latent in [0, 1]:
// 0 is deliberate/calm, 1 is jumpy. One draw per agent, shared across
// every perception window / cutoff width / sensitivity threshold and the
// max-hold trait, so the agent reads as one coherent temperament rather
// than a jumpy-perceiver / calm-exit Frankenstein.
func drawReactivity(rng *rand.Rand, pair BetaPair) float64 {
	return drawBeta(rng, pair.Alpha, pair.Beta)
}

// drawOptimism samples the per-agent optimism latent in [-1, +1]: minus
// is permabear/skeptic, zero is neutral, plus is permabull/optimist.
// Baseline draws from a single symmetric Beta stretched onto [-1, +1];
// the scenario-aware branch replaces the single Beta with a two-mode
// mixture, which is the ONE scenario-aware spot inside the trait draw.
// The latent is used downstream to tilt directional perception fields
// (consumed in the perception stage).
func drawOptimism(rng *rand.Rand, baseline BetaPair, bimodal *BimodalOptimism) float64 {
	var scaledBetaSample float64
	if bimodal == nil {
		scaledBetaSample = drawBeta(rng, baseline.Alpha, baseline.Beta)
	} else {
		if rng.Float64() < bimodal.MixWeight {
			scaledBetaSample = drawBeta(rng, bimodal.FirstModeBeta.Alpha, bimodal.FirstModeBeta.Beta)
		} else {
			scaledBetaSample = drawBeta(rng, bimodal.SecondModeBeta.Alpha, bimodal.SecondModeBeta.Beta)
		}
	}
	return 2*scaledBetaSample - 1
}

// coinFlip returns true with the given probability. rng.Float64() lands
// in [0, 1) so probability 0 is always false and probability 1 is always
// true — the two endpoints of the dial behave the way a reader expects.
func coinFlip(rng *rand.Rand, probability float64) bool {
	return rng.Float64() < probability
}
