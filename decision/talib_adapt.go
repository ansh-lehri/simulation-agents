package decision

import "simulation-agents/store"

// candlesToFloat64 returns the close prices of a candle slice as float64.
// go-talib takes []float64 inputs; all price series enter through this
// boundary. The original int64 ticks are exact-representable as float64
// for any value within the trading price range.
func candlesToFloat64(candles []store.Candle) []float64 {
	out := make([]float64, len(candles))
	for idx, bar := range candles {
		out[idx] = float64(bar.Close)
	}
	return out
}

// highsToFloat64 returns the high prices as float64.
func highsToFloat64(candles []store.Candle) []float64 {
	out := make([]float64, len(candles))
	for idx, bar := range candles {
		out[idx] = float64(bar.High)
	}
	return out
}

// lowsToFloat64 returns the low prices as float64.
func lowsToFloat64(candles []store.Candle) []float64 {
	out := make([]float64, len(candles))
	for idx, bar := range candles {
		out[idx] = float64(bar.Low)
	}
	return out
}

// volumesToFloat64 returns the volumes as float64.
func volumesToFloat64(candles []store.Candle) []float64 {
	out := make([]float64, len(candles))
	for idx, bar := range candles {
		out[idx] = float64(bar.Volume)
	}
	return out
}

// hasMinBars reports whether barCount meets the minimum required before
// go-talib's output is valid. go-talib fills the warm-up region with 0.0
// rather than NaN — callers must gate on this before consuming any result.
func hasMinBars(barCount, requiredPeriod int) bool {
	return barCount >= requiredPeriod
}
