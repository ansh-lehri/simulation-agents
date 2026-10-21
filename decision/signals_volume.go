package decision

import (
	"github.com/markcheno/go-talib"

	"simulation-agents/agent"
)

// perceiveVolume classifies current bar volume against the agent's rolling
// baseline. The baseline method ("mean" or "median") is frozen in
// block.Method at generation; parameters are in block.ReadingParams.
//
// Confirming is §8.4: a real price move (|close−open|>0) on at-or-above
// baseline volume. The volume channel carries no directional bias.
func perceiveVolume(block *agent.ChannelBlock, acc *Accessor, inst, tf string) VolumeReading {
	if !acc.Present(inst) {
		return VolumeReading{}
	}
	if block == nil || block.ReadingParams == nil {
		return VolumeReading{}
	}

	avgWindow := int(block.ReadingParams["avg_window"])
	if avgWindow < 1 {
		avgWindow = 1
	}
	spikeMultiple := block.ReadingParams["spike_multiple"]
	bandWidth := block.ReadingParams["band_width"]

	// Fetch avgWindow baseline bars plus the current bar.
	vols := acc.Volumes(inst, tf, avgWindow+1)
	if len(vols) < 2 {
		return VolumeReading{}
	}
	currentVol := vols[len(vols)-1]
	baselineVols := vols[:len(vols)-1]

	if len(baselineVols) < avgWindow {
		return VolumeReading{}
	}

	var baseline float64
	if block.Method == "median" {
		baseline = medianFloat64(baselineVols)
	} else {
		// "mean" — SMA of the baseline window; go-talib Sma on exactly
		// avgWindow values produces a valid last element.
		smaOut := talib.Sma(baselineVols, avgWindow)
		baseline = smaOut[len(smaOut)-1]
	}
	if baseline <= 0 {
		return VolumeReading{}
	}

	ratio := currentVol / baseline

	// Three-bucket level via symmetric band around ratio = 1.0.
	halfBand := bandWidth / 2
	level := VolNormal
	if ratio < 1-halfBand {
		level = VolLow
	} else if ratio > 1+halfBand {
		level = VolHigh
	}

	// Spike: how far above baseline × spike_multiple, clamped to [0,1].
	spike := 0.0
	if spikeMultiple > 1 {
		spike = clamp01((ratio - 1) / (spikeMultiple - 1))
	}

	// Confirming §8.4: real price move on at-or-above-baseline volume.
	bars := acc.Candles(inst, tf)
	confirming := false
	if len(bars) > 0 {
		lastBar := bars[len(bars)-1]
		confirming = lastBar.Close != lastBar.Open && ratio >= 1.0
	}

	return VolumeReading{
		Level:      level,
		Spike:      spike,
		Confirming: confirming,
		ReadOK:     true,
	}
}

// perceiveVolumeDiet is the diet-gated entry point.
func perceiveVolumeDiet(diet *agent.Diet, block *agent.ChannelBlock, acc *Accessor, inst, tf string) *VolumeReading {
	if diet == nil || !diet.Volume {
		return nil
	}
	reading := perceiveVolume(block, acc, inst, tf)
	return &reading
}
