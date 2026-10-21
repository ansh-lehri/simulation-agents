package decision

// Direction is the signed directional reading a chart or range channel
// produces. It carries no opinion about whether to trade — that is 4b's job.
type Direction int8

const (
	DirFlat Direction = 0
	DirUp   Direction = 1
	DirDown Direction = -1
)

// VolumeLevel classifies current volume relative to the agent's rolling
// baseline. Three buckets only — agents care about whether volume is thin,
// normal, or elevated, not about a precise ratio.
type VolumeLevel int8

const (
	VolLow    VolumeLevel = -1
	VolNormal VolumeLevel = 0
	VolHigh   VolumeLevel = 1
)

// ChartReading is the output of the chart/technical channel. Direction and
// strength are the directional verdict; cleanliness and maturity add texture
// so two agents that agree on direction can still trade differently (a choppy
// young trend vs a clean mature one). KeyLevels are raw tick prices carried
// through to execution — not thresholded or mixed by 4b.
type ChartReading struct {
	Direction   Direction
	Strength    float64  // [0,1]: how pronounced the move
	Cleanliness float64  // [0,1]: path efficiency — 1=straight, ~0=choppy
	Maturity    float64  // [0,1]: how long the current directional run has held
	KeyLevels   []int64  // nearby support/resistance prices in integer ticks
	ReadOK      bool
}

// RangeReading is the output of the mean-reversion/range channel.
// PositionInRange and Stretch are the range verdict; RangeBounds are raw
// tick prices carried through to execution.
type RangeReading struct {
	PositionInRange float64   // [-1,+1]: -1=at bottom, 0=mid, +1=at top
	Stretch         float64   // [0,1]: how far beyond the normal band
	RangeBounds     [2]int64  // [low, high] tick prices of the current range
	ReadOK          bool
}

// LiquidityReading is the output of the order-book channel. All three fields
// are condition observations — none of them carries an inherent direction.
// Depth is absolute (no rolling normalization).
type LiquidityReading struct {
	Spread    float64 // (ask-bid)/midPrice, normalized
	Depth     float64 // absolute sum of top-N bid+ask qty
	Imbalance float64 // (bidSum-askSum)/(bidSum+askSum) ∈ [-1,+1]
	ReadOK    bool
}

// VolumeReading is the output of the volume channel. Level and Spike are
// condition observations about current volume relative to the agent's
// baseline. Confirming ties volume movement to a real price move — used by
// agents to distinguish genuine breakouts from low-conviction noise.
type VolumeReading struct {
	Level      VolumeLevel
	Spike      float64 // [0,1]: magnitude of a sudden volume burst this bar
	Confirming bool    // true when volume is at-or-above baseline on a real price move
	ReadOK     bool
}

// TimeReading is the output of the time-of-session channel. Nearly free to
// compute — just reads the clock and the exchange phase string.
type TimeReading struct {
	Phase           string
	SessionProgress float64 // [0,1]: 0=session open, 1=session close
	ReadOK          bool
}

// PerceptionBundle holds one tick's readings across all five perception
// channels for one instrument. A nil pointer means that channel's diet flag
// is off — absent, not neutral. A non-nil pointer with ReadOK=false means
// the channel is on but could not produce a valid reading this tick (thin
// bar history, no quote, instrument not yet present).
type PerceptionBundle struct {
	Chart     *ChartReading
	Range     *RangeReading
	Liquidity *LiquidityReading
	Volume    *VolumeReading
	Time      *TimeReading
}
