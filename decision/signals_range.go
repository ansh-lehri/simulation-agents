package decision

import (
	"math"
	"math/rand/v2"

	"github.com/markcheno/go-talib"

	"simulation-agents/agent"
)

// perceiveRange computes a mean-reversion reading for one instrument using
// the agent's chosen range method (bollinger, channel, or rsi). Bias and
// skill-noise are applied to position_in_range and stretch before clamping.
func perceiveRange(
	block *agent.ChannelBlock,
	acc *Accessor,
	inst, tf string,
	traits agent.TraitVector,
	agentRNG *rand.Rand,
	params PerceptionParams,
) RangeReading {
	if !acc.Present(inst) || block == nil {
		return RangeReading{}
	}

	switch block.Method {
	case "bollinger":
		return perceiveRangeBollinger(block, acc, inst, tf, traits, agentRNG, params)
	case "channel":
		return perceiveRangeChannel(block, acc, inst, tf, traits, agentRNG, params)
	case "rsi":
		return perceiveRangeRSI(block, acc, inst, tf, traits, agentRNG, params)
	}
	return RangeReading{}
}

// perceiveRangeBollinger uses Bollinger Bands to place price inside a
// volatility envelope. pos = (price − midBand) / (sigmaMult × sd); stretch
// measures how far beyond the ob_os_band zone.
func perceiveRangeBollinger(
	block *agent.ChannelBlock,
	acc *Accessor,
	inst, tf string,
	traits agent.TraitVector,
	agentRNG *rand.Rand,
	params PerceptionParams,
) RangeReading {
	period := int(block.ReadingParams["period"])
	sigmaMult := block.ReadingParams["sigma_mult"]
	obOsBand := block.ReadingCutoffs["ob_os_band"]

	barCount := acc.BarCount(inst, tf)
	if !hasMinBars(barCount, period) {
		return RangeReading{}
	}

	closes := acc.Closes(inst, tf, barCount)
	upper, mid, lower := talib.BBands(closes, period, sigmaMult, sigmaMult, talib.SMA)
	upperLast := upper[len(upper)-1]
	midLast := mid[len(mid)-1]
	lowerLast := lower[len(lower)-1]

	bandSpan := upperLast - lowerLast
	if bandSpan <= 0 {
		return RangeReading{}
	}

	priceInt, priceOK := acc.CurrentPrice(inst, tf)
	if !priceOK {
		return RangeReading{}
	}
	price := float64(priceInt)

	// Position: (price − mid) / half-band-width. ±1.0 when at the bands.
	halfBand := bandSpan / 2
	rawPos := (price - midLast) / halfBand

	pos := clampSymmetric(biasedAndNoisy(rawPos, "range.position_in_range", traits, agentRNG, params))

	// Stretch: how far outside the ob_os_band threshold, normalized to [0,1].
	rawStretch := 0.0
	if obOsBand < 1 {
		rawStretch = math.Max(0, math.Abs(rawPos)-obOsBand) / (1 - obOsBand)
	}
	stretch := clamp01(biasedAndNoisy(rawStretch, "range.stretch", traits, agentRNG, params))

	return RangeReading{
		PositionInRange: pos,
		Stretch:         stretch,
		RangeBounds:     [2]int64{int64(math.Round(lowerLast)), int64(math.Round(upperLast))},
		ReadOK:          true,
	}
}

// perceiveRangeChannel uses a Donchian (min-max) channel over the configured
// window. pos = (price − mid) / half-channel-width; stretch measures how
// close to the channel extremes beyond extreme_band.
func perceiveRangeChannel(
	block *agent.ChannelBlock,
	acc *Accessor,
	inst, tf string,
	traits agent.TraitVector,
	agentRNG *rand.Rand,
	params PerceptionParams,
) RangeReading {
	window := int(block.ReadingParams["window"])
	extremeBand := block.ReadingCutoffs["extreme_band"]

	barCount := acc.BarCount(inst, tf)
	if !hasMinBars(barCount, window) {
		return RangeReading{}
	}

	closes := acc.Closes(inst, tf, window)
	maxOut := talib.Max(closes, window)
	minOut := talib.Min(closes, window)
	channelHi := maxOut[len(maxOut)-1]
	channelLo := minOut[len(minOut)-1]

	channelRange := channelHi - channelLo
	if channelRange <= 0 {
		return RangeReading{}
	}

	priceInt, priceOK := acc.CurrentPrice(inst, tf)
	if !priceOK {
		return RangeReading{}
	}
	price := float64(priceInt)

	channelMid := (channelHi + channelLo) / 2
	rawPos := (price - channelMid) / (channelRange / 2)

	pos := clampSymmetric(biasedAndNoisy(rawPos, "range.position_in_range", traits, agentRNG, params))

	rawStretch := 0.0
	if extremeBand < 1 {
		rawStretch = math.Max(0, math.Abs(rawPos)-extremeBand) / (1 - extremeBand)
	}
	stretch := clamp01(biasedAndNoisy(rawStretch, "range.stretch", traits, agentRNG, params))

	return RangeReading{
		PositionInRange: pos,
		Stretch:         stretch,
		RangeBounds:     [2]int64{int64(math.Round(channelLo)), int64(math.Round(channelHi))},
		ReadOK:          true,
	}
}

// perceiveRangeRSI uses RSI as a bounded range proxy.
// pos = (rsi − 50) / 50 ∈ [−1,+1]; stretch measures how far beyond the
// ob_os_levels threshold (in raw RSI-deviation units). range_bounds are
// a Max/Min price-level fallback since RSI produces no price band.
func perceiveRangeRSI(
	block *agent.ChannelBlock,
	acc *Accessor,
	inst, tf string,
	traits agent.TraitVector,
	agentRNG *rand.Rand,
	params PerceptionParams,
) RangeReading {
	period := int(block.ReadingParams["period"])
	obOsLevels := block.ReadingCutoffs["ob_os_levels"] // in RSI-deviation units (e.g., 20 = RSI 30/70)

	barCount := acc.BarCount(inst, tf)
	if !hasMinBars(barCount, period+1) {
		return RangeReading{}
	}

	closes := acc.Closes(inst, tf, barCount)
	rsiOut := talib.Rsi(closes, period)
	rsiVal := rsiOut[len(rsiOut)-1]

	rawPos := (rsiVal - 50) / 50

	pos := clampSymmetric(biasedAndNoisy(rawPos, "range.position_in_range", traits, agentRNG, params))

	// Normalize ob_os_levels from RSI-deviation space to [0,1] position space.
	obOsBandNorm := obOsLevels / 50
	rawStretch := 0.0
	if obOsBandNorm < 1 {
		rawStretch = math.Max(0, math.Abs(rawPos)-obOsBandNorm) / (1 - obOsBandNorm)
	}
	stretch := clamp01(biasedAndNoisy(rawStretch, "range.stretch", traits, agentRNG, params))

	// Price bounds: Max/Min closes over the RSI period.
	boundsCloses := acc.Closes(inst, tf, period)
	maxOut := talib.Max(boundsCloses, period)
	minOut := talib.Min(boundsCloses, period)
	priceLo := int64(math.Round(minOut[len(minOut)-1]))
	priceHi := int64(math.Round(maxOut[len(maxOut)-1]))

	return RangeReading{
		PositionInRange: pos,
		Stretch:         stretch,
		RangeBounds:     [2]int64{priceLo, priceHi},
		ReadOK:          true,
	}
}

// perceiveRangeDiet is the diet-gated entry point.
func perceiveRangeDiet(
	diet *agent.Diet,
	block *agent.ChannelBlock,
	acc *Accessor,
	inst, tf string,
	traits agent.TraitVector,
	agentRNG *rand.Rand,
	params PerceptionParams,
) *RangeReading {
	if diet == nil || !diet.MeanReversion {
		return nil
	}
	reading := perceiveRange(block, acc, inst, tf, traits, agentRNG, params)
	return &reading
}
