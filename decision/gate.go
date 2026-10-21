package decision

import (
	"math"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// ShouldReperceive is the cheap re-read gate. It compares the current tick's
// market snapshot against what the agent last perceived (refs) and returns
// true when any on-diet channel has changed enough to warrant a full
// re-read. If refs is empty (first act for this instrument) it always
// returns true.
//
// Per-channel thresholds are drawn from traits.Perception.RereadSensitivity.
// A missing or zero threshold for a channel means that channel never triggers
// a reperceive on its own (safe calibration fallback, not a forced coast).
// Time-of-session uses a discrete phase-change comparison — no threshold.
//
// The range metric is "price move / recent window span" — a cheap
// approximation of position shift that does not re-run the full indicator.
func ShouldReperceive(
	traits agent.TraitVector,
	acc *Accessor,
	inst, tf string,
	refs store.PerceptionRefs,
) bool {
	// Empty refs → first act for this instrument → always reperceive.
	if refs == (store.PerceptionRefs{}) {
		return true
	}
	if !acc.Present(inst) {
		return false
	}
	if traits.Diet == nil || traits.Perception == nil {
		return false
	}

	diet := traits.Diet
	sensitivity := traits.Perception.RereadSensitivity

	price, priceOK := acc.CurrentPrice(inst, tf)

	// Chart channel: relative price change since last read.
	if diet.Chart && priceOK && refs.LastPrice > 0 {
		change := math.Abs(float64(price-refs.LastPrice)) / float64(refs.LastPrice)
		if threshold := sensitivity["chart"]; threshold > 0 && change > threshold {
			return true
		}
	}

	// Liquidity channel: relative spread change since last read.
	if diet.OrderBook {
		if spread, ok := acc.Spread(inst); ok && refs.LastSpread > 0 {
			change := math.Abs(float64(spread-refs.LastSpread)) / float64(refs.LastSpread)
			if threshold := sensitivity["liquidity"]; threshold > 0 && change > threshold {
				return true
			}
		}
	}

	// Volume channel: relative volume change on the last bar.
	if diet.Volume {
		if bars := acc.Candles(inst, tf); len(bars) > 0 && refs.LastVolume > 0 {
			currentVol := bars[len(bars)-1].Volume
			change := math.Abs(float64(currentVol-refs.LastVolume)) / float64(refs.LastVolume)
			if threshold := sensitivity["volume"]; threshold > 0 && change > threshold {
				return true
			}
		}
	}

	// Range channel: price move normalized by the recent close span. Cheap
	// approximation of how much the position-in-range may have shifted —
	// avoids running the full indicator.
	if diet.MeanReversion && priceOK && refs.LastPrice > 0 {
		barCount := acc.BarCount(inst, tf)
		windowSize := barCount
		if windowSize > 20 {
			windowSize = 20
		}
		if windowSize >= 2 {
			closes := acc.Closes(inst, tf, windowSize)
			span := closeSpan(closes)
			if span > 0 {
				priceMove := math.Abs(float64(price - refs.LastPrice))
				if threshold := sensitivity["range"]; threshold > 0 && priceMove/span > threshold {
					return true
				}
			}
		}
	}

	// Time-of-session channel: discrete phase change — no numeric threshold.
	if diet.TimeOfSession && acc.Phase(inst) != refs.LastPhase {
		return true
	}

	return false
}

// closeSpan returns max(closes) − min(closes). Returns 0 for empty or
// single-element slices. Used as a cheap scale estimate for the range gate.
func closeSpan(closes []float64) float64 {
	if len(closes) < 2 {
		return 0
	}
	hi, lo := closes[0], closes[0]
	for _, c := range closes[1:] {
		if c > hi {
			hi = c
		}
		if c < lo {
			lo = c
		}
	}
	return hi - lo
}
