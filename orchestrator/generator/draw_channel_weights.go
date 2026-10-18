package generator

import (
	"math/rand/v2"
)

// drawChannelWeights produces the per-trait Dirichlet weight simplex
// over each trait's configured channel set. The mechanism is final:
// Dirichlet(concentration · ones(len(trait's channels))) with the
// per-population concentration dial from GenParams. The one placeholder
// is params.TraitChannelSets itself — the per-trait channel vectors —
// which gets replaced wholesale when the finalized channel-to-trait map
// lands. No code change in this file is needed when that map arrives;
// only the GenParams values flip.
//
// Weights are keyed inside each inner map by the channel name (sorted
// iteration over the trait's channel list for reproducibility). Entries
// are produced for every trait listed in TraitChannelSets regardless of
// whether the agent's own diet turns those channels on; the Tier-4
// reader is free to zero out weights for channels the agent does not
// perceive.
func drawChannelWeights(rng *rand.Rand, params GenParams) map[string]map[string]float64 {
	traits := sortedStringKeys(params.TraitChannelSets)
	weights := make(map[string]map[string]float64, len(traits))

	for _, traitName := range traits {
		channelList := params.TraitChannelSets[traitName]
		if len(channelList) == 0 {
			// Validate rejects this case; the guard is here only in
			// case a caller skipped Validate.
			continue
		}
		alphas := make([]float64, len(channelList))
		for index := range alphas {
			alphas[index] = params.ChannelWeightsDirichletConcentration
		}
		simplexSample := drawDirichlet(rng, alphas)

		perChannelWeights := make(map[string]float64, len(channelList))
		for index, channelName := range channelList {
			perChannelWeights[channelName] = simplexSample[index]
		}
		weights[traitName] = perChannelWeights
	}
	return weights
}
