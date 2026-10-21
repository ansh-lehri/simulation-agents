package decision

import (
	"math"
	"math/rand/v2"

	"github.com/markcheno/go-talib"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// perceiveChart computes a trend/directional reading using the agent's frozen
// chart method (ema, sma, rsi, or structure). Bias and skill-noise perturb
// the continuous pre-cutoff value for direction and the raw magnitude for
// strength before cutoffs and clamping are applied.
//
// Cleanliness (path efficiency) and maturity (run length) are
// method-independent, computed over the method's own window.
func perceiveChart(
	block *agent.ChannelBlock,
	acc *Accessor,
	inst, tf string,
	traits agent.TraitVector,
	agentRNG *rand.Rand,
	params PerceptionParams,
) ChartReading {
	if !acc.Present(inst) || block == nil {
		return ChartReading{}
	}

	switch block.Method {
	case "ema", "sma":
		return perceiveChartMA(block, acc, inst, tf, traits, agentRNG, params)
	case "rsi":
		return perceiveChartRSI(block, acc, inst, tf, traits, agentRNG, params)
	case "structure":
		return perceiveChartStructure(block, acc, inst, tf, traits, agentRNG, params)
	}
	return ChartReading{}
}

// perceiveChartMA computes direction and strength from a fast-minus-slow MA
// spread. Bias+noise perturb the spread before the flat_band cutoff; strength
// saturates at m × flat_band where m = PerceptionParams.MASaturationMultiple.
func perceiveChartMA(
	block *agent.ChannelBlock,
	acc *Accessor,
	inst, tf string,
	traits agent.TraitVector,
	agentRNG *rand.Rand,
	params PerceptionParams,
) ChartReading {
	fastPeriod := int(block.ReadingParams["fast_period"])
	slowPeriod := int(block.ReadingParams["slow_period"])
	flatBand := block.ReadingCutoffs["flat_band"]

	barCount := acc.BarCount(inst, tf)
	if !hasMinBars(barCount, slowPeriod) {
		return ChartReading{}
	}

	closes := acc.Closes(inst, tf, barCount)

	var fastMA, slowMA float64
	if block.Method == "ema" {
		fastOut := talib.Ema(closes, fastPeriod)
		slowOut := talib.Ema(closes, slowPeriod)
		fastMA = fastOut[len(fastOut)-1]
		slowMA = slowOut[len(slowOut)-1]
	} else {
		fastOut := talib.Sma(closes, fastPeriod)
		slowOut := talib.Sma(closes, slowPeriod)
		fastMA = fastOut[len(fastOut)-1]
		slowMA = slowOut[len(slowOut)-1]
	}

	if slowMA <= 0 {
		return ChartReading{}
	}

	rawSpread := (fastMA - slowMA) / slowMA

	// Bias+noise perturb the spread before direction cutoff (categorical
	// pre-cutoff perturbation).
	perturbedSpread := biasedAndNoisy(rawSpread, "chart.direction", traits, agentRNG, params)
	direction := directionFromSpread(perturbedSpread, flatBand)

	saturation := params.MASaturationMultiple * flatBand
	rawStrength := 0.0
	if saturation > 0 {
		rawStrength = math.Abs(rawSpread) / saturation
	}
	strength := clamp01(biasedAndNoisy(rawStrength, "chart.strength", traits, agentRNG, params))

	cleanCloses := acc.Closes(inst, tf, slowPeriod)
	cleanliness := pathEfficiency(cleanCloses)
	maturity := directionRunFraction(acc.Closes(inst, tf, slowPeriod+1), direction)

	highs := acc.Highs(inst, tf, slowPeriod)
	lows := acc.Lows(inst, tf, slowPeriod)
	keyLevels := priceBoundsFromWindow(highs, lows, slowPeriod)

	return ChartReading{
		Direction:   direction,
		Strength:    strength,
		Cleanliness: cleanliness,
		Maturity:    maturity,
		KeyLevels:   keyLevels,
		ReadOK:      true,
	}
}

// perceiveChartRSI computes direction and strength from RSI relative to 50.
// mid_band is the RSI deviation (from 50) required for a non-flat reading.
func perceiveChartRSI(
	block *agent.ChannelBlock,
	acc *Accessor,
	inst, tf string,
	traits agent.TraitVector,
	agentRNG *rand.Rand,
	params PerceptionParams,
) ChartReading {
	period := int(block.ReadingParams["period"])
	midBand := block.ReadingCutoffs["mid_band"]

	barCount := acc.BarCount(inst, tf)
	if !hasMinBars(barCount, period+1) {
		return ChartReading{}
	}

	closes := acc.Closes(inst, tf, barCount)
	rsiOut := talib.Rsi(closes, period)
	rsiVal := rsiOut[len(rsiOut)-1]

	rawDev := rsiVal - 50 // deviation from midpoint, in RSI units

	perturbedDev := biasedAndNoisy(rawDev, "chart.direction", traits, agentRNG, params)
	direction := directionFromSpread(perturbedDev, midBand)

	rawStrength := clamp01(math.Abs(rawDev) / 50)
	strength := clamp01(biasedAndNoisy(rawStrength, "chart.strength", traits, agentRNG, params))

	cleanCloses := acc.Closes(inst, tf, period)
	cleanliness := pathEfficiency(cleanCloses)
	maturity := directionRunFraction(acc.Closes(inst, tf, period+1), direction)

	highs := acc.Highs(inst, tf, period)
	lows := acc.Lows(inst, tf, period)
	keyLevels := priceBoundsFromWindow(highs, lows, period)

	return ChartReading{
		Direction:   direction,
		Strength:    strength,
		Cleanliness: cleanliness,
		Maturity:    maturity,
		KeyLevels:   keyLevels,
		ReadOK:      true,
	}
}

// perceiveChartStructure classifies trend from fractal swing pivots.
// Bar i is a swing-high if high[i] is strictly the max over [i−swingWindow,
// i+swingWindow]; confirmed swing_window bars after the pivot bar so the
// pivot only becomes usable once swing_window additional bars have closed.
// Pivots are kept only when they differ from the last opposite-type kept
// pivot by ≥ min_swing_pct (filters out noise wiggles). Direction is the
// majority of HH/HL comparisons across the last confirm_swings of each type.
// Strength is the fraction of comparisons agreeing with the direction.
func perceiveChartStructure(
	block *agent.ChannelBlock,
	acc *Accessor,
	inst, tf string,
	traits agent.TraitVector,
	agentRNG *rand.Rand,
	params PerceptionParams,
) ChartReading {
	swingWindow := int(block.ReadingParams["swing_window"])
	confirmSwings := int(block.ReadingParams["confirm_swings"])
	minSwingPct := block.ReadingCutoffs["min_swing_pct"]

	bars := acc.Candles(inst, tf)
	if swingWindow < 1 || confirmSwings < 2 {
		return ChartReading{}
	}
	minBarsNeeded := 2*swingWindow + 1
	if len(bars) < minBarsNeeded {
		return ChartReading{}
	}

	detectedHighs, detectedLows := detectSwingPivots(bars, swingWindow, minSwingPct)

	if len(detectedHighs) < confirmSwings || len(detectedLows) < confirmSwings {
		return ChartReading{}
	}

	direction, rawStrength := swingDirectionAndStrength(detectedHighs, detectedLows, confirmSwings)

	strength := clamp01(biasedAndNoisy(rawStrength, "chart.strength", traits, agentRNG, params))

	// Cleanliness and maturity over the immediate swing-detection window.
	cleanCloses := acc.Closes(inst, tf, swingWindow)
	cleanliness := pathEfficiency(cleanCloses)
	maturity := directionRunFraction(acc.Closes(inst, tf, swingWindow+1), direction)

	// Key levels: most recent detected swing high and low.
	var keyLevels []int64
	lastLow := detectedLows[len(detectedLows)-1]
	lastHigh := detectedHighs[len(detectedHighs)-1]
	if lastLow <= lastHigh {
		keyLevels = []int64{lastLow, lastHigh}
	} else {
		keyLevels = []int64{lastHigh, lastLow}
	}

	return ChartReading{
		Direction:   direction,
		Strength:    strength,
		Cleanliness: cleanliness,
		Maturity:    maturity,
		KeyLevels:   keyLevels,
		ReadOK:      true,
	}
}

// detectSwingPivots scans a bar series for confirmed swing highs and lows.
// Bar i is a swing-high candidate if bars[i].High is strictly greater than
// every other bar's high in [i−swingWindow, i+swingWindow]. Only candidates
// at or before len(bars)−swingWindow−1 are processed (confirmed lag).
// A candidate is kept only when it differs from the last kept opposite-type
// pivot by ≥ minSwingPct × lastOpposite (noise filter).
func detectSwingPivots(bars []store.Candle, swingWindow int, minSwingPct float64) (highs []int64, lows []int64) {
	n := len(bars)
	var lastKeptHigh, lastKeptLow float64 = -1, -1

	for i := swingWindow; i < n-swingWindow; i++ {
		barHigh := float64(bars[i].High)
		barLow := float64(bars[i].Low)

		// Swing-high check: strictly max in symmetric window.
		isHigh := true
		for j := i - swingWindow; j <= i+swingWindow; j++ {
			if j != i && float64(bars[j].High) >= barHigh {
				isHigh = false
				break
			}
		}
		if isHigh {
			keepHigh := lastKeptLow < 0 ||
				(barHigh-lastKeptLow)/lastKeptLow >= minSwingPct
			if keepHigh {
				highs = append(highs, bars[i].High)
				lastKeptHigh = barHigh
			}
		}

		// Swing-low check: strictly min in symmetric window.
		isLow := true
		for j := i - swingWindow; j <= i+swingWindow; j++ {
			if j != i && float64(bars[j].Low) <= barLow {
				isLow = false
				break
			}
		}
		if isLow {
			keepLow := lastKeptHigh < 0 ||
				(lastKeptHigh-barLow)/lastKeptHigh >= minSwingPct
			if keepLow {
				lows = append(lows, bars[i].Low)
				lastKeptLow = barLow
			}
		}
	}
	return
}

// swingDirectionAndStrength classifies trend direction and strength from the
// last confirmSwings entries in each pivot list. Direction is the majority of
// consecutive HH/HL comparisons across both pivot types; strength is the
// fraction of comparisons agreeing with the direction.
func swingDirectionAndStrength(highs, lows []int64, confirmSwings int) (Direction, float64) {
	lastHighs := lastN(highs, confirmSwings)
	lastLows := lastN(lows, confirmSwings)

	totalComparisons := 0
	agreeUp := 0
	agreeDown := 0

	for i := 1; i < len(lastHighs); i++ {
		totalComparisons++
		if lastHighs[i] > lastHighs[i-1] {
			agreeUp++
		} else if lastHighs[i] < lastHighs[i-1] {
			agreeDown++
		}
	}
	for i := 1; i < len(lastLows); i++ {
		totalComparisons++
		if lastLows[i] > lastLows[i-1] {
			agreeUp++
		} else if lastLows[i] < lastLows[i-1] {
			agreeDown++
		}
	}

	if totalComparisons == 0 {
		return DirFlat, 0
	}

	fracUp := float64(agreeUp) / float64(totalComparisons)
	fracDown := float64(agreeDown) / float64(totalComparisons)

	switch {
	case fracUp > fracDown:
		return DirUp, fracUp
	case fracDown > fracUp:
		return DirDown, fracDown
	default:
		return DirFlat, 0
	}
}

// directionRunFraction returns how long (as a fraction of the close series)
// the given direction has held as a trailing consecutive run.
// Uses trailingRunFraction from bias_noise.go on the close-to-close booleans.
func directionRunFraction(closes []float64, direction Direction) float64 {
	if len(closes) < 2 || direction == DirFlat {
		return 0
	}
	matches := make([]bool, len(closes)-1)
	for i := 1; i < len(closes); i++ {
		switch direction {
		case DirUp:
			matches[i-1] = closes[i] > closes[i-1]
		case DirDown:
			matches[i-1] = closes[i] < closes[i-1]
		}
	}
	return trailingRunFraction(matches)
}

// priceBoundsFromWindow returns [min_low, max_high] from the given high/low
// slices as integer tick levels. Returns nil when either slice is empty.
// Uses talib.Max / talib.Min per the spec recommendation.
func priceBoundsFromWindow(highs, lows []float64, window int) []int64 {
	if len(highs) == 0 || len(lows) == 0 || window < 1 {
		return nil
	}
	// Clamp window to available data.
	w := window
	if w > len(highs) {
		w = len(highs)
	}
	if w > len(lows) {
		w = len(lows)
	}
	maxOut := talib.Max(highs, w)
	minOut := talib.Min(lows, w)
	maxHigh := maxOut[len(maxOut)-1]
	minLow := minOut[len(minOut)-1]
	return []int64{int64(math.Round(minLow)), int64(math.Round(maxHigh))}
}

// lastN returns the last n elements of the slice. If n >= len, returns the
// whole slice. Avoids an allocation when the slice is already short enough.
func lastN(s []int64, n int) []int64 {
	if n >= len(s) {
		return s
	}
	return s[len(s)-n:]
}

// perceiveChartDiet is the diet-gated entry point.
func perceiveChartDiet(
	diet *agent.Diet,
	block *agent.ChannelBlock,
	acc *Accessor,
	inst, tf string,
	traits agent.TraitVector,
	agentRNG *rand.Rand,
	params PerceptionParams,
) *ChartReading {
	if diet == nil || !diet.Chart {
		return nil
	}
	reading := perceiveChart(block, acc, inst, tf, traits, agentRNG, params)
	return &reading
}
