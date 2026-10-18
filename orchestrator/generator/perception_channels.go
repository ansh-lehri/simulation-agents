package generator

// Channel names used across the perception draw. These are the string
// labels that appear as map keys in GenParams and as sub-block keys on
// agent.PerceptionProfile. Keeping them in one place avoids drift
// between the draw code and the calibration maps.
const (
	channelNameChart         = "chart"
	channelNameRange         = "range"
	channelNameLiquidity     = "liquidity"
	channelNameVolume        = "volume"
	channelNameTimeOfSession = "time_of_session"
)

// Method family identifiers used by method selection on method-forked
// channels (chart, range). The family label — chart.ma / chart.rsi /
// chart.structure for chart; range.bollinger / range.channel / range.rsi
// for range — is the key under which the family's reactivity anchor and
// skill demand live in GenParams.MethodAnchorsAndDemands.
//
// The family identifier is NOT what ends up on the frozen trait vector.
// The vector's ChannelBlock.Method carries the FINAL method label —
// after the chart.ma family's ρ-coin resolves to "ema" or "sma", for
// instance — so the Tier-4 reader does not have to re-run the ρ-coin.
const (
	methodFamilyChartMA        = "chart.ma"
	methodFamilyChartRSI       = "chart.rsi"
	methodFamilyChartStructure = "chart.structure"

	methodFamilyRangeBollinger = "range.bollinger"
	methodFamilyRangeChannel   = "range.channel"
	methodFamilyRangeRSI       = "range.rsi"
)

// Final method labels — the strings that land in ChannelBlock.Method on
// the frozen trait vector. The chart family resolves to one of four
// labels (ma_type folds into the ema/sma split); range to one of three;
// volume carries the baseline_type coin result directly on Method. The
// Tier-4 reader branches on these string values.
const (
	methodLabelEMA       = "ema"
	methodLabelSMA       = "sma"
	methodLabelRSI       = "rsi"
	methodLabelStructure = "structure"

	methodLabelBollinger = "bollinger"
	methodLabelChannel   = "channel"

	methodLabelVolumeMean   = "mean"
	methodLabelVolumeMedian = "median"
)

// Directional perception fields — the four fields that bias applies to.
// The complementary (condition / levels / facts) fields carry no bias
// per the perception generation spec: "Scope: directional fields ONLY
// — chart.direction, chart.strength, range.position_in_range,
// range.stretch." These constants key both GenParams.BiasTiltStrengths
// and the output PerceptionProfile.Bias map.
const (
	directionalFieldChartDirection = "chart.direction"
	directionalFieldChartStrength  = "chart.strength"
	directionalFieldRangePosition  = "range.position_in_range"
	directionalFieldRangeStretch   = "range.stretch"
)

// chartMethodFamilies and rangeMethodFamilies list the family keys the
// method-selection sampler scores over. Order is fixed (sorted) so a
// seed replays identical selection outcomes.
var chartMethodFamilies = []string{methodFamilyChartMA, methodFamilyChartRSI, methodFamilyChartStructure}
var rangeMethodFamilies = []string{methodFamilyRangeBollinger, methodFamilyRangeChannel, methodFamilyRangeRSI}
