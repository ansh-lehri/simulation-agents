package generator

import (
	"fmt"
	"math"
	"math/rand/v2"

	"simulation-agents/agent"
)

// drawPerceptionProfile builds the per-ON-channel perception block for
// one endogenous agent. OFF channels are deliberately absent from the
// returned struct (not JSON null) so a reader can tell at a glance which
// channels the agent perceives at all.
//
// The three shared drivers — skill, reactivity, and optimism — all come
// from the agent's own already-drawn trait vector so a coherent
// personality threads through every window / cutoff / tilt. The caller
// (the top-level trait draw) is responsible for passing in those three
// scalars; nothing in this file re-draws them.
func drawPerceptionProfile(
	rng *rand.Rand,
	skill, reactivity, optimism float64,
	diet agent.Diet,
	params GenParams,
) agent.PerceptionProfile {
	profile := agent.PerceptionProfile{}

	if diet.Chart {
		block := drawChartChannel(rng, skill, reactivity, params)
		profile.Chart = &block
	}
	if diet.MeanReversion {
		block := drawRangeChannel(rng, skill, reactivity, params)
		profile.Range = &block
	}
	if diet.OrderBook {
		block := drawLiquidityChannel(rng, reactivity, params)
		profile.Liquidity = &block
	}
	if diet.Volume {
		block := drawVolumeChannel(rng, reactivity, params)
		profile.Volume = &block
	}
	if diet.TimeOfSession {
		// The time-of-session channel has no per-agent params and no
		// method label — the perception generation spec locks it as "no
		// method, no params" with globally shared phase boundaries. An
		// empty block is written so the on-wire presence signals that
		// the agent reads this channel at all.
		empty := agent.ChannelBlock{}
		profile.TimeOfSession = &empty
	}

	profile.Bias = buildBias(optimism, params.BiasTiltStrengths)
	profile.RereadSensitivity = buildRereadSensitivity(rng, diet, reactivity, params)
	profile.DecideSensitivity = buildDecideSensitivity(rng, diet, reactivity, params)

	return profile
}

// drawChartChannel samples the chart channel block: pick a method
// family, run that family's parameter + cutoff draws, fold the ma_type
// ρ-coin into the final method label so the Tier-4 reader can branch on
// one string.
func drawChartChannel(rng *rand.Rand, skill, reactivity float64, params GenParams) agent.ChannelBlock {
	family := selectMethodFamily(rng, chartMethodFamilies, params, skill, reactivity)

	switch family {
	case methodFamilyChartMA:
		return drawChartMAParams(rng, reactivity, params)
	case methodFamilyChartRSI:
		return drawChartRSIParams(rng, reactivity, params)
	case methodFamilyChartStructure:
		return drawChartStructureParams(rng, reactivity, params)
	}
	panic(fmt.Sprintf("generator: unreachable chart method family %q", family))
}

// drawRangeChannel is the symmetric counterpart on the range channel.
func drawRangeChannel(rng *rand.Rand, skill, reactivity float64, params GenParams) agent.ChannelBlock {
	family := selectMethodFamily(rng, rangeMethodFamilies, params, skill, reactivity)

	switch family {
	case methodFamilyRangeBollinger:
		return drawRangeBollingerParams(rng, reactivity, params)
	case methodFamilyRangeChannel:
		return drawRangeChannelParams(rng, reactivity, params)
	case methodFamilyRangeRSI:
		return drawRangeRSIParams(rng, reactivity, params)
	}
	panic(fmt.Sprintf("generator: unreachable range method family %q", family))
}

// selectMethodFamily samples one family key from the given list using the
// proximity × skill-gate kernel: nearer the agent's reactivity → higher
// proximity; skill-heavy methods suppressed for low-skill agents. Sampled
// (not argmax) so same-reactivity agents do not collapse onto one method.
//
// Iteration order over the family list is caller-provided and already
// sorted (chartMethodFamilies / rangeMethodFamilies), so a seed replays
// identically.
func selectMethodFamily(
	rng *rand.Rand,
	familyKeys []string,
	params GenParams,
	skill, reactivity float64,
) string {
	if len(familyKeys) == 0 {
		panic("generator: selectMethodFamily called with empty family list")
	}
	scores := make([]float64, len(familyKeys))
	totalScore := 0.0
	for index, familyKey := range familyKeys {
		anchor, ok := params.MethodAnchorsAndDemands[familyKey]
		if !ok {
			panic(fmt.Sprintf("generator: MethodAnchorsAndDemands missing %q", familyKey))
		}
		proximityGap := reactivity - anchor.ReactivityAnchor
		proximity := math.Exp(-(proximityGap * proximityGap) / (2 * params.MethodProximityTau * params.MethodProximityTau))
		// Skill gate: skill ^ (eta × skill_demand). skill_demand of 0
		// gives a flat gate of 1 regardless of skill; a positive demand
		// suppresses low-skill agents on that method.
		gate := math.Pow(skill, params.MethodSkillGateEta*anchor.SkillDemand)
		scores[index] = proximity * gate
		totalScore += scores[index]
	}
	if totalScore <= 0 {
		// All methods scored zero — happens only at skill = 0 with
		// positive demands on every method. Fall back to the first
		// family so the mint can proceed; the calibration shouldn't
		// land here on realistic ranges.
		return familyKeys[0]
	}

	threshold := rng.Float64() * totalScore
	cumulative := 0.0
	for index, score := range scores {
		cumulative += score
		if threshold < cumulative {
			return familyKeys[index]
		}
	}
	return familyKeys[len(familyKeys)-1]
}

// drawChartMAParams samples the moving-average method parameters. The
// ρ-coin picks EMA (jumpy agents) over SMA; the chosen label lands on
// Method directly so the Tier-4 reader does not re-run the coin. The
// slow / fast period ordering is enforced by swap so the invariant
// slow > fast cannot be violated by independent jitter.
func drawChartMAParams(rng *rand.Rand, reactivity float64, params GenParams) agent.ChannelBlock {
	fast := drawIntPerceptionField(rng, "chart.ma.fast_period", reactivity, params)
	slow := drawIntPerceptionField(rng, "chart.ma.slow_period", reactivity, params)
	if slow <= fast {
		slow, fast = fast, slow
		// After the swap we still need fast strictly less than slow;
		// if the two happen to be equal (both landed on the same
		// integer after rounding), bump slow by one to preserve the
		// strict ordering.
		if slow == fast {
			slow++
		}
	}
	flatBand := drawFloatPerceptionField(rng, "chart.ma.flat_band", reactivity, params)

	methodLabel := methodLabelSMA
	if rng.Float64() < reactivity {
		methodLabel = methodLabelEMA
	}

	return agent.ChannelBlock{
		Method: methodLabel,
		ReadingParams: map[string]float64{
			"fast_period": float64(fast),
			"slow_period": float64(slow),
		},
		ReadingCutoffs: map[string]float64{
			"flat_band": flatBand,
		},
	}
}

// drawChartRSIParams samples the chart-RSI method parameters.
func drawChartRSIParams(rng *rand.Rand, reactivity float64, params GenParams) agent.ChannelBlock {
	period := drawIntPerceptionField(rng, "chart.rsi.period", reactivity, params)
	midBand := drawFloatPerceptionField(rng, "chart.rsi.mid_band", reactivity, params)
	return agent.ChannelBlock{
		Method:         methodLabelRSI,
		ReadingParams:  map[string]float64{"period": float64(period)},
		ReadingCutoffs: map[string]float64{"mid_band": midBand},
	}
}

// drawChartStructureParams samples the chart-structure method parameters.
func drawChartStructureParams(rng *rand.Rand, reactivity float64, params GenParams) agent.ChannelBlock {
	swingWindow := drawIntPerceptionField(rng, "chart.structure.swing_window", reactivity, params)
	minSwing := drawFloatPerceptionField(rng, "chart.structure.min_swing_pct", reactivity, params)
	confirmSwings := drawIntPerceptionField(rng, "chart.structure.confirm_swings", reactivity, params)
	return agent.ChannelBlock{
		Method: methodLabelStructure,
		ReadingParams: map[string]float64{
			"swing_window":   float64(swingWindow),
			"confirm_swings": float64(confirmSwings),
		},
		ReadingCutoffs: map[string]float64{
			"min_swing_pct": minSwing,
		},
	}
}

// drawRangeBollingerParams samples the Bollinger method parameters.
func drawRangeBollingerParams(rng *rand.Rand, reactivity float64, params GenParams) agent.ChannelBlock {
	period := drawIntPerceptionField(rng, "range.bollinger.period", reactivity, params)
	sigmaMult := drawFloatPerceptionField(rng, "range.bollinger.sigma_mult", reactivity, params)
	obOsBand := drawFloatPerceptionField(rng, "range.bollinger.ob_os_band", reactivity, params)
	return agent.ChannelBlock{
		Method: methodLabelBollinger,
		ReadingParams: map[string]float64{
			"period":     float64(period),
			"sigma_mult": sigmaMult,
		},
		ReadingCutoffs: map[string]float64{
			"ob_os_band": obOsBand,
		},
	}
}

// drawRangeChannelParams samples the min-max / Donchian channel method
// parameters.
func drawRangeChannelParams(rng *rand.Rand, reactivity float64, params GenParams) agent.ChannelBlock {
	window := drawIntPerceptionField(rng, "range.channel.window", reactivity, params)
	extremeBand := drawFloatPerceptionField(rng, "range.channel.extreme_band", reactivity, params)
	return agent.ChannelBlock{
		Method:         methodLabelChannel,
		ReadingParams:  map[string]float64{"window": float64(window)},
		ReadingCutoffs: map[string]float64{"extreme_band": extremeBand},
	}
}

// drawRangeRSIParams samples the range-RSI method parameters.
func drawRangeRSIParams(rng *rand.Rand, reactivity float64, params GenParams) agent.ChannelBlock {
	period := drawIntPerceptionField(rng, "range.rsi.period", reactivity, params)
	obOsLevels := drawFloatPerceptionField(rng, "range.rsi.ob_os_levels", reactivity, params)
	return agent.ChannelBlock{
		Method:         methodLabelRSI,
		ReadingParams:  map[string]float64{"period": float64(period)},
		ReadingCutoffs: map[string]float64{"ob_os_levels": obOsLevels},
	}
}

// drawLiquidityChannel samples the liquidity fixed-calc channel block.
// Only one param (depth_levels, an integer) today; no cutoffs, no method
// label. Depth is read without a rolling normalization baseline — that
// normalization choice is explicitly open in the perception generation
// spec, and the trait draw simply writes the per-agent depth preference.
func drawLiquidityChannel(rng *rand.Rand, reactivity float64, params GenParams) agent.ChannelBlock {
	depthLevels := drawIntPerceptionField(rng, "liquidity.depth_levels", reactivity, params)
	return agent.ChannelBlock{
		ReadingParams: map[string]float64{"depth_levels": float64(depthLevels)},
	}
}

// drawVolumeChannel samples the volume fixed-calc channel block. The
// baseline_type coin is a free 50/50 and its result lands directly on
// Method ("mean" or "median") — the one bit of categorical information
// the channel carries, same convention used for ma_type on chart MA.
func drawVolumeChannel(rng *rand.Rand, reactivity float64, params GenParams) agent.ChannelBlock {
	avgWindow := drawIntPerceptionField(rng, "volume.avg_window", reactivity, params)
	spikeMultiple := drawFloatPerceptionField(rng, "volume.spike_multiple", reactivity, params)
	bandWidth := drawFloatPerceptionField(rng, "volume.band_width", reactivity, params)

	baseline := methodLabelVolumeMean
	if rng.Float64() < 0.5 {
		baseline = methodLabelVolumeMedian
	}

	return agent.ChannelBlock{
		Method: baseline,
		ReadingParams: map[string]float64{
			"avg_window":     float64(avgWindow),
			"spike_multiple": spikeMultiple,
			"band_width":     bandWidth,
		},
	}
}

// drawFloatPerceptionField looks up the given field's calibration box in
// PerceptionRanges and runs the shared (1−reactivity) placement rule.
// Panics with a clear message when the key is missing — a missing key
// is a calibration bug, caught at first mint rather than silently
// returning zero.
func drawFloatPerceptionField(rng *rand.Rand, fieldKey string, reactivity float64, params GenParams) float64 {
	rangeBox, ok := params.PerceptionRanges[fieldKey]
	if !ok {
		panic(fmt.Sprintf("generator: PerceptionRanges missing %q", fieldKey))
	}
	return centerFromReactivity(rng, rangeBox.Low, rangeBox.High, reactivity, params.SigmaIdio)
}

// drawIntPerceptionField runs the same (1−reactivity) placement rule
// and rounds the result to an integer, clamped back into the field's
// box so a rounding excursion cannot escape it. The spec rule is
// "integer params rounded after ε".
func drawIntPerceptionField(rng *rand.Rand, fieldKey string, reactivity float64, params GenParams) int {
	rangeBox, ok := params.PerceptionRanges[fieldKey]
	if !ok {
		panic(fmt.Sprintf("generator: PerceptionRanges missing %q", fieldKey))
	}
	centered := centerFromReactivity(rng, rangeBox.Low, rangeBox.High, reactivity, params.SigmaIdio)
	rounded := int(math.Round(centered))
	return clampInt(rounded, int(math.Round(rangeBox.Low)), int(math.Round(rangeBox.High)))
}

// buildBias produces the per-directional-field tilt map. Only the four
// directional fields appear in the output; non-directional fields and
// absent fields are not keyed. The result is intentionally deterministic
// (no RNG consumed) because bias is a straight linear function of the
// agent's optimism latent and the fixed per-field tilt strength.
//
// Entries whose key is not present in BiasTiltStrengths are silently
// skipped rather than being filled with zeros; a bias of zero is
// semantically "no tilt" and distinguishing "not configured" from "zero
// tilt" is a calibration question we leave to the GenParams author.
func buildBias(optimism float64, tiltStrengths map[string]float64) map[string]float64 {
	bias := make(map[string]float64)
	for _, fieldKey := range directionalFields {
		tilt, ok := tiltStrengths[fieldKey]
		if !ok {
			continue
		}
		bias[fieldKey] = optimism * tilt
	}
	return bias
}

// directionalFields names the four fields bias is applied to in sorted
// order. Used both by buildBias and by tests that assert bias scope.
var directionalFields = []string{
	directionalFieldChartDirection,
	directionalFieldChartStrength,
	directionalFieldRangePosition,
	directionalFieldRangeStretch,
}

// buildRereadSensitivity produces the per-ON-channel re-read threshold
// map. The time-of-session channel has no threshold at all (phase
// boundaries are discrete events, not continuous changes), so it is
// skipped even when on.
func buildRereadSensitivity(
	rng *rand.Rand,
	diet agent.Diet,
	reactivity float64,
	params GenParams,
) map[string]float64 {
	thresholds := map[string]float64{}
	onChannels := onChannelsFromDiet(diet)
	for _, channelKey := range onChannels {
		if channelKey == channelNameTimeOfSession {
			continue
		}
		rangeBox, ok := params.RereadSensitivityRanges[channelKey]
		if !ok {
			continue
		}
		thresholds[channelKey] = centerFromReactivity(
			rng, rangeBox.Low, rangeBox.High, reactivity, params.SigmaIdio,
		)
	}
	return thresholds
}

// buildDecideSensitivity produces the per-consumed-directional-field
// re-decide threshold map. The caller's directional-field coverage is
// implicit in which keys live in DecideSensitivityRanges.
func buildDecideSensitivity(
	rng *rand.Rand,
	diet agent.Diet,
	reactivity float64,
	params GenParams,
) map[string]float64 {
	thresholds := map[string]float64{}
	for _, fieldKey := range directionalFields {
		if !directionalFieldIsOn(diet, fieldKey) {
			continue
		}
		rangeBox, ok := params.DecideSensitivityRanges[fieldKey]
		if !ok {
			continue
		}
		thresholds[fieldKey] = centerFromReactivity(
			rng, rangeBox.Low, rangeBox.High, reactivity, params.SigmaIdio,
		)
	}
	return thresholds
}

// onChannelsFromDiet maps the five diet flags to the five perception
// channel names, in a fixed order so iteration is deterministic.
func onChannelsFromDiet(diet agent.Diet) []string {
	channels := []string{}
	if diet.Chart {
		channels = append(channels, channelNameChart)
	}
	if diet.MeanReversion {
		channels = append(channels, channelNameRange)
	}
	if diet.OrderBook {
		channels = append(channels, channelNameLiquidity)
	}
	if diet.Volume {
		channels = append(channels, channelNameVolume)
	}
	if diet.TimeOfSession {
		channels = append(channels, channelNameTimeOfSession)
	}
	return channels
}

// directionalFieldIsOn reports whether the agent's diet enables the
// channel a given directional field belongs to. Chart-prefixed fields
// require the chart channel; range-prefixed fields require the range
// channel.
func directionalFieldIsOn(diet agent.Diet, fieldKey string) bool {
	switch fieldKey {
	case directionalFieldChartDirection, directionalFieldChartStrength:
		return diet.Chart
	case directionalFieldRangePosition, directionalFieldRangeStretch:
		return diet.MeanReversion
	}
	return false
}
