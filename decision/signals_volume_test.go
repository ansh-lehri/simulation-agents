package decision

import (
	"math"
	"testing"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// makeVolumeAccessor builds an Accessor from a flat candle slice (volumes
// are set via the volume argument to makeCandles in accessor_test.go, but here
// we build manually to control open/close for confirming tests).
func makeVolumeAccessor(inst, tf string, candles []store.Candle) *Accessor {
	return NewAccessor(
		map[string]map[string][]store.Candle{inst: {tf: candles}},
		map[string]store.Quote{inst: {HasLast: true, LastPrice: candles[len(candles)-1].Close}},
		map[string]bool{inst: true},
		TickContext{},
	)
}

func volumeBlock(method string, avgWindow int, spikeMultiple, bandWidth float64) *agent.ChannelBlock {
	return &agent.ChannelBlock{
		Method: method,
		ReadingParams: map[string]float64{
			"avg_window":     float64(avgWindow),
			"spike_multiple": spikeMultiple,
			"band_width":     bandWidth,
		},
	}
}

// buildVolumeCandles creates candles where volumes follow the given list and
// open==close (flat bars, non-confirming).
func buildFlatVolumeCandles(vols ...int64) []store.Candle {
	out := make([]store.Candle, len(vols))
	for i, v := range vols {
		out[i] = store.Candle{Open: 100, High: 101, Low: 99, Close: 100, Volume: v}
	}
	return out
}

// buildMovingVolumeCandles creates candles where close != open (confirming).
func buildMovingVolumeCandles(vols ...int64) []store.Candle {
	out := make([]store.Candle, len(vols))
	for i, v := range vols {
		out[i] = store.Candle{Open: 100, High: 102, Low: 98, Close: 101, Volume: v}
	}
	return out
}

func TestPerceiveVolume_MeanBaseline_Normal(t *testing.T) {
	// 5 baseline bars at vol=100, current bar at vol=100 → ratio=1.0 → VolNormal.
	candles := buildFlatVolumeCandles(100, 100, 100, 100, 100, 100) // avgWindow=5 + 1 current
	acc := makeVolumeAccessor("X", "1m", candles)
	block := volumeBlock("mean", 5, 2.0, 0.4)

	r := perceiveVolume(block, acc, "X", "1m")
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	if r.Level != VolNormal {
		t.Errorf("expected VolNormal, got %v", r.Level)
	}
	if r.Spike != 0 {
		t.Errorf("no spike when ratio=1.0, got %v", r.Spike)
	}
}

func TestPerceiveVolume_LowLevel(t *testing.T) {
	// Baseline = 100, current = 70 → ratio = 0.7 < 1 - 0.2 = 0.8 → VolLow.
	candles := buildFlatVolumeCandles(100, 100, 100, 100, 100, 70)
	acc := makeVolumeAccessor("X", "1m", candles)
	block := volumeBlock("mean", 5, 2.0, 0.4)

	r := perceiveVolume(block, acc, "X", "1m")
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	if r.Level != VolLow {
		t.Errorf("expected VolLow, got %v", r.Level)
	}
}

func TestPerceiveVolume_HighLevel(t *testing.T) {
	// Baseline = 100, current = 150 → ratio = 1.5 > 1 + 0.2 = 1.2 → VolHigh.
	candles := buildFlatVolumeCandles(100, 100, 100, 100, 100, 150)
	acc := makeVolumeAccessor("X", "1m", candles)
	block := volumeBlock("mean", 5, 3.0, 0.4)

	r := perceiveVolume(block, acc, "X", "1m")
	if r.Level != VolHigh {
		t.Errorf("expected VolHigh, got %v", r.Level)
	}
}

func TestPerceiveVolume_SpikeComputation(t *testing.T) {
	// Baseline = 100, spikeMultiple = 3.0.
	// At ratio=3.0: spike=(3-1)/(3-1)=1.0.
	// At ratio=2.0: spike=(2-1)/(3-1)=0.5.
	candles := buildFlatVolumeCandles(100, 100, 100, 100, 100, 300) // ratio=3.0
	acc := makeVolumeAccessor("X", "1m", candles)
	block := volumeBlock("mean", 5, 3.0, 0.4)

	r := perceiveVolume(block, acc, "X", "1m")
	if math.Abs(r.Spike-1.0) > 0.01 {
		t.Errorf("spike at ratio=3 with multiple=3: want 1.0, got %v", r.Spike)
	}
}

func TestPerceiveVolume_SpikeMidPoint(t *testing.T) {
	// ratio=2.0, multiple=3.0 → spike=(2-1)/(3-1)=0.5.
	candles := buildFlatVolumeCandles(100, 100, 100, 100, 100, 200)
	acc := makeVolumeAccessor("X", "1m", candles)
	block := volumeBlock("mean", 5, 3.0, 0.4)

	r := perceiveVolume(block, acc, "X", "1m")
	if math.Abs(r.Spike-0.5) > 0.01 {
		t.Errorf("spike at ratio=2 with multiple=3: want 0.5, got %v", r.Spike)
	}
}

func TestPerceiveVolume_Confirming_TrueWhenMoving(t *testing.T) {
	// Open != Close on last bar AND ratio >= 1.0 → confirming=true.
	candles := buildMovingVolumeCandles(100, 100, 100, 100, 100, 100)
	acc := makeVolumeAccessor("X", "1m", candles)
	block := volumeBlock("mean", 5, 2.0, 0.4)

	r := perceiveVolume(block, acc, "X", "1m")
	if !r.Confirming {
		t.Fatal("Confirming must be true when price moved and vol >= baseline")
	}
}

func TestPerceiveVolume_Confirming_FalseWhenFlat(t *testing.T) {
	// Open == Close on last bar → confirming=false.
	candles := buildFlatVolumeCandles(100, 100, 100, 100, 100, 100)
	acc := makeVolumeAccessor("X", "1m", candles)
	block := volumeBlock("mean", 5, 2.0, 0.4)

	r := perceiveVolume(block, acc, "X", "1m")
	if r.Confirming {
		t.Fatal("Confirming must be false when open==close (flat bar)")
	}
}

func TestPerceiveVolume_Confirming_FalseWhenThinVolume(t *testing.T) {
	// Price moves but volume is below baseline → confirming=false.
	candles := buildMovingVolumeCandles(100, 100, 100, 100, 100, 50) // 50 < baseline 100
	acc := makeVolumeAccessor("X", "1m", candles)
	block := volumeBlock("mean", 5, 2.0, 0.4)

	r := perceiveVolume(block, acc, "X", "1m")
	if r.Confirming {
		t.Fatal("Confirming must be false when volume < baseline")
	}
}

func TestPerceiveVolume_MedianBaseline(t *testing.T) {
	// Median of [100,100,100,50,200] = 100. Current = 200 → ratio=2.0 → VolHigh.
	candles := buildFlatVolumeCandles(100, 100, 100, 50, 200, 200)
	acc := makeVolumeAccessor("X", "1m", candles)
	block := volumeBlock("median", 5, 3.0, 0.4)

	r := perceiveVolume(block, acc, "X", "1m")
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	if r.Level != VolHigh {
		t.Errorf("expected VolHigh with median baseline, got %v", r.Level)
	}
}

func TestPerceiveVolume_ThinHistory(t *testing.T) {
	// Only 1 candle — cannot form baseline.
	candles := buildFlatVolumeCandles(100)
	acc := makeVolumeAccessor("X", "1m", candles)
	block := volumeBlock("mean", 5, 2.0, 0.4)

	r := perceiveVolume(block, acc, "X", "1m")
	if r.ReadOK {
		t.Fatal("ReadOK must be false with fewer bars than avgWindow+1")
	}
}

func TestPerceiveVolume_Absent(t *testing.T) {
	acc := NewAccessor(
		map[string]map[string][]store.Candle{},
		map[string]store.Quote{},
		map[string]bool{"X": false},
		TickContext{},
	)
	r := perceiveVolume(volumeBlock("mean", 5, 2.0, 0.4), acc, "X", "1m")
	if r.ReadOK {
		t.Fatal("ReadOK must be false when instrument absent")
	}
}
