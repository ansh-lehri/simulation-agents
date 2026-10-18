package generator

import (
	"bytes"
	"encoding/json"
	"testing"

	"simulation-agents/agent"
)

// allChannelsOnDiet is the diet the perception tests use as their "every
// channel on" baseline; the per-channel tests then zero out specific
// flags to assert OFF-channel behaviour.
func allChannelsOnDiet() agent.Diet {
	return agent.Diet{Chart: true, OrderBook: true, MeanReversion: true, Volume: true, TimeOfSession: true}
}

// TestPerceptionProfilePopulatesOnlyOnChannels pins the central shape
// contract: an OFF diet flag means the matching ChannelBlock pointer
// stays nil, which marshals to "key absent" (not JSON null). The
// Tier-4 reader distinguishes "channel off" from "channel on with zero
// signal" by presence, so this invariant is load-bearing.
func TestPerceptionProfilePopulatesOnlyOnChannels(t *testing.T) {
	params := completeGenParams()
	rng := seededRNG(1, 42)

	// Only Chart on; everything else off.
	diet := agent.Diet{Chart: true}
	profile := drawPerceptionProfile(rng, 0.5, 0.5, 0, diet, params)

	if profile.Chart == nil {
		t.Fatalf("Chart block missing with Chart on")
	}
	if profile.Range != nil {
		t.Fatalf("Range block present with MeanReversion off")
	}
	if profile.Liquidity != nil {
		t.Fatalf("Liquidity block present with OrderBook off")
	}
	if profile.Volume != nil {
		t.Fatalf("Volume block present with Volume off")
	}
	if profile.TimeOfSession != nil {
		t.Fatalf("TimeOfSession block present with TimeOfSession off")
	}

	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, offKey := range []string{"range", "liquidity", "volume", "time_of_session"} {
		if bytes.Contains(raw, []byte(`"`+offKey+`"`)) {
			t.Fatalf("OFF channel %q leaked into wire form: %s", offKey, raw)
		}
	}
}

// TestChartMethodLabelIsOneOfFourFinalLabels pins the "Method carries the
// final label directly" scheme — never a family identifier like "chart.ma".
func TestChartMethodLabelIsOneOfFourFinalLabels(t *testing.T) {
	params := completeGenParams()
	allowed := map[string]bool{
		methodLabelEMA:       true,
		methodLabelSMA:       true,
		methodLabelRSI:       true,
		methodLabelStructure: true,
	}
	for seedCounter := uint64(0); seedCounter < 200; seedCounter++ {
		rng := seededRNG(seedCounter, 43)
		block := drawChartChannel(rng, 0.5, 0.5, params)
		if !allowed[block.Method] {
			t.Fatalf("chart Method %q not in final label set (seed %d)", block.Method, seedCounter)
		}
	}
}

// TestRangeMethodLabelIsOneOfThreeFinalLabels mirrors the chart assertion
// for the range channel.
func TestRangeMethodLabelIsOneOfThreeFinalLabels(t *testing.T) {
	params := completeGenParams()
	allowed := map[string]bool{
		methodLabelBollinger: true,
		methodLabelChannel:   true,
		methodLabelRSI:       true,
	}
	for seedCounter := uint64(0); seedCounter < 200; seedCounter++ {
		rng := seededRNG(seedCounter, 44)
		block := drawRangeChannel(rng, 0.5, 0.5, params)
		if !allowed[block.Method] {
			t.Fatalf("range Method %q not in final label set (seed %d)", block.Method, seedCounter)
		}
	}
}

// TestVolumeMethodCarriesBaselineChoice confirms the volume channel's
// Method field carries "mean" or "median" (the baseline_type coin
// result), per the "Method is the one categorical choice" scheme.
func TestVolumeMethodCarriesBaselineChoice(t *testing.T) {
	params := completeGenParams()
	sawMean, sawMedian := false, false
	for seedCounter := uint64(0); seedCounter < 200 && !(sawMean && sawMedian); seedCounter++ {
		rng := seededRNG(seedCounter, 45)
		block := drawVolumeChannel(rng, 0.5, params)
		switch block.Method {
		case methodLabelVolumeMean:
			sawMean = true
		case methodLabelVolumeMedian:
			sawMedian = true
		default:
			t.Fatalf("volume Method %q is neither mean nor median (seed %d)", block.Method, seedCounter)
		}
	}
	if !(sawMean && sawMedian) {
		t.Fatalf("volume baseline coin never varied across 200 seeds: sawMean=%v sawMedian=%v", sawMean, sawMedian)
	}
}

// TestLiquidityAndTimeOfSessionHaveEmptyMethod pins the "no categorical
// = Method stays empty" half of the Method convention.
func TestLiquidityAndTimeOfSessionHaveEmptyMethod(t *testing.T) {
	params := completeGenParams()
	rng := seededRNG(5, 46)

	liquidity := drawLiquidityChannel(rng, 0.5, params)
	if liquidity.Method != "" {
		t.Fatalf("liquidity Method should be empty, got %q", liquidity.Method)
	}

	// TimeOfSession is produced in drawPerceptionProfile as an empty block;
	// confirm that path too.
	profile := drawPerceptionProfile(rng, 0.5, 0.5, 0, agent.Diet{TimeOfSession: true}, params)
	if profile.TimeOfSession == nil {
		t.Fatalf("TimeOfSession block missing with TimeOfSession on")
	}
	if profile.TimeOfSession.Method != "" {
		t.Fatalf("TimeOfSession Method should be empty, got %q", profile.TimeOfSession.Method)
	}
}

// TestChartMAEnforcesSlowStrictlyAboveFast locks in the one invariant
// the MA block must hold: slow_period > fast_period, even after the
// (1−reactivity) placement rule and jitter happen to swap them.
func TestChartMAEnforcesSlowStrictlyAboveFast(t *testing.T) {
	params := completeGenParams()
	seenMAAgents := 0
	for seedCounter := uint64(0); seedCounter < 500; seedCounter++ {
		rng := seededRNG(seedCounter, 47)
		block := drawChartChannel(rng, 0.5, 0.5, params)
		if block.Method != methodLabelEMA && block.Method != methodLabelSMA {
			continue
		}
		seenMAAgents++
		fast := block.ReadingParams["fast_period"]
		slow := block.ReadingParams["slow_period"]
		if slow <= fast {
			t.Fatalf("MA slow %v not strictly above fast %v (seed %d)",
				slow, fast, seedCounter)
		}
	}
	if seenMAAgents == 0 {
		t.Fatalf("no MA agents across 500 seeds — did the selection ever pick MA?")
	}
}

// TestChartMAMAtypeTracksReactivity confirms the ρ-coin direction:
// jumpy agents (high reactivity) land on EMA more often than deliberate
// ones. Fast-reaction method = EMA, matching "jumpy → faster EMA".
func TestChartMAMAtypeTracksReactivity(t *testing.T) {
	params := completeGenParams()
	// Pin reactivity so method-family selection is roughly stable, then
	// read the ma_type coin by filtering on MA agents.
	countEMAHighReactivity := 0
	countEMALowReactivity := 0
	countMAHighReactivity := 0
	countMALowReactivity := 0
	for seedCounter := uint64(0); seedCounter < 1500; seedCounter++ {
		highBlock := drawChartMAParams(seededRNG(seedCounter, 48), 0.95, params)
		lowBlock := drawChartMAParams(seededRNG(seedCounter, 49), 0.05, params)
		countMAHighReactivity++
		countMALowReactivity++
		if highBlock.Method == methodLabelEMA {
			countEMAHighReactivity++
		}
		if lowBlock.Method == methodLabelEMA {
			countEMALowReactivity++
		}
	}
	highRate := float64(countEMAHighReactivity) / float64(countMAHighReactivity)
	lowRate := float64(countEMALowReactivity) / float64(countMALowReactivity)
	if highRate <= lowRate {
		t.Fatalf("jumpy agents should prefer EMA: highRate=%.3f lowRate=%.3f", highRate, lowRate)
	}
}

// TestMethodFamilySelectionSpreadsAtMidReactivity confirms the sampled
// (not argmax) contract. At mid reactivity + mid skill, every chart
// family should appear at least once across many seeds; argmax would
// collapse the whole batch onto the family closest to the (reactivity,
// skill) point.
func TestMethodFamilySelectionSpreadsAtMidReactivity(t *testing.T) {
	params := completeGenParams()
	counts := map[string]int{}
	const sampleCount = 2000
	for seedCounter := uint64(0); seedCounter < sampleCount; seedCounter++ {
		family := selectMethodFamily(seededRNG(seedCounter, 51), chartMethodFamilies, params, 0.5, 0.55)
		counts[family]++
	}
	for _, family := range chartMethodFamilies {
		if counts[family] == 0 {
			t.Fatalf("family %q never selected at mid (reactivity, skill) — sampling collapsed to argmax", family)
		}
	}
}

// TestMethodFamilySelectionReactivityDirection confirms the proximity
// kernel direction: jumpy agents land on structure (the highest-anchor
// method) more often than deliberate ones; deliberate agents land on MA
// (the lowest-anchor method) more often than jumpy ones.
func TestMethodFamilySelectionReactivityDirection(t *testing.T) {
	params := completeGenParams()
	jumpyStructureCount := 0
	jumpyMACount := 0
	calmStructureCount := 0
	calmMACount := 0
	// skill=1 removes the skill-gate suppression on structure so the
	// test isolates the reactivity-proximity direction.
	const sampleCount = 2000
	for seedCounter := uint64(0); seedCounter < sampleCount; seedCounter++ {
		jumpy := selectMethodFamily(seededRNG(seedCounter, 53), chartMethodFamilies, params, 1.0, 0.95)
		calm := selectMethodFamily(seededRNG(seedCounter, 55), chartMethodFamilies, params, 1.0, 0.05)
		if jumpy == methodFamilyChartStructure {
			jumpyStructureCount++
		}
		if jumpy == methodFamilyChartMA {
			jumpyMACount++
		}
		if calm == methodFamilyChartStructure {
			calmStructureCount++
		}
		if calm == methodFamilyChartMA {
			calmMACount++
		}
	}
	if jumpyStructureCount <= calmStructureCount {
		t.Fatalf("jumpy agents should land on structure more than calm ones: jumpy=%d calm=%d",
			jumpyStructureCount, calmStructureCount)
	}
	if calmMACount <= jumpyMACount {
		t.Fatalf("calm agents should land on MA more than jumpy ones: calm=%d jumpy=%d",
			calmMACount, jumpyMACount)
	}
}

// TestMethodFamilySelectionSkillGate confirms the skill-gate direction:
// low-skill agents are pushed away from methods with high skill demand
// (structure in the complete params has demand = 1).
func TestMethodFamilySelectionSkillGate(t *testing.T) {
	params := completeGenParams()
	lowSkillStructureCount := 0
	highSkillStructureCount := 0
	const sampleCount = 2000
	for seedCounter := uint64(0); seedCounter < sampleCount; seedCounter++ {
		// Fix reactivity at the structure anchor so proximity does not
		// confound the test; the only remaining differentiator is skill.
		lowSkill := selectMethodFamily(seededRNG(seedCounter, 57), chartMethodFamilies, params, 0.05, 0.85)
		highSkill := selectMethodFamily(seededRNG(seedCounter, 59), chartMethodFamilies, params, 0.95, 0.85)
		if lowSkill == methodFamilyChartStructure {
			lowSkillStructureCount++
		}
		if highSkill == methodFamilyChartStructure {
			highSkillStructureCount++
		}
	}
	if highSkillStructureCount <= lowSkillStructureCount {
		t.Fatalf("high-skill agents should land on structure more than low-skill ones: high=%d low=%d",
			highSkillStructureCount, lowSkillStructureCount)
	}
}

// TestReactivityDirectionOnWindow pins the (1−reactivity) direction on
// a representative integer window field: jumpy agents land on shorter
// bollinger periods than deliberate ones on average.
func TestReactivityDirectionOnWindow(t *testing.T) {
	params := completeGenParams()
	jumpyTotal := 0
	deliberateTotal := 0
	const sampleCount = 500
	for seedCounter := uint64(0); seedCounter < sampleCount; seedCounter++ {
		jumpyBlock := drawRangeBollingerParams(seededRNG(seedCounter, 61), 0.9, params)
		deliberateBlock := drawRangeBollingerParams(seededRNG(seedCounter, 63), 0.1, params)
		jumpyTotal += int(jumpyBlock.ReadingParams["period"])
		deliberateTotal += int(deliberateBlock.ReadingParams["period"])
	}
	if jumpyTotal >= deliberateTotal {
		t.Fatalf("jumpy agents should land on shorter bollinger period than deliberate ones: jumpy-total=%d deliberate-total=%d",
			jumpyTotal, deliberateTotal)
	}
}

// TestBiasScopeIsDirectionalOnly pins the ONLY-directional-fields rule
// for bias — chart.direction, chart.strength, range.position_in_range,
// range.stretch and nothing else.
func TestBiasScopeIsDirectionalOnly(t *testing.T) {
	params := completeGenParams()
	bias := buildBias(0.5, params.BiasTiltStrengths)

	allowedKeys := map[string]bool{
		directionalFieldChartDirection: true,
		directionalFieldChartStrength:  true,
		directionalFieldRangePosition:  true,
		directionalFieldRangeStretch:   true,
	}
	for key := range bias {
		if !allowedKeys[key] {
			t.Fatalf("bias carries non-directional field %q", key)
		}
	}
	// Non-directional candidates that must never leak in: condition
	// fields (volume, liquidity, time) and levels.
	for _, forbidden := range []string{"volume", "liquidity", "time_of_session", "level"} {
		if _, present := bias[forbidden]; present {
			t.Fatalf("bias carries forbidden non-directional key %q", forbidden)
		}
	}
}

// TestBiasSignTracksOptimism confirms the direction: positive optimism
// produces positive bias on each directional field, negative produces
// negative, and zero produces zero.
func TestBiasSignTracksOptimism(t *testing.T) {
	params := completeGenParams()

	positiveBias := buildBias(0.8, params.BiasTiltStrengths)
	negativeBias := buildBias(-0.8, params.BiasTiltStrengths)
	neutralBias := buildBias(0, params.BiasTiltStrengths)

	for _, field := range directionalFields {
		if positiveBias[field] <= 0 {
			t.Fatalf("positive optimism should give positive bias on %q, got %v", field, positiveBias[field])
		}
		if negativeBias[field] >= 0 {
			t.Fatalf("negative optimism should give negative bias on %q, got %v", field, negativeBias[field])
		}
		if neutralBias[field] != 0 {
			t.Fatalf("zero optimism should give zero bias on %q, got %v", field, neutralBias[field])
		}
	}
}

// TestRereadSensitivityCoversOnChannelsExceptTime confirms the sensitivity
// map covers every ON channel except time_of_session (which has no
// re-read threshold by spec).
func TestRereadSensitivityCoversOnChannelsExceptTime(t *testing.T) {
	params := completeGenParams()
	rng := seededRNG(1, 67)
	thresholds := buildRereadSensitivity(rng, allChannelsOnDiet(), 0.5, params)

	for _, channel := range []string{channelNameChart, channelNameRange, channelNameLiquidity, channelNameVolume} {
		if _, present := thresholds[channel]; !present {
			t.Fatalf("reread sensitivity missing ON channel %q", channel)
		}
	}
	if _, present := thresholds[channelNameTimeOfSession]; present {
		t.Fatalf("reread sensitivity carries time_of_session entry; the channel has no threshold")
	}

	// Flipping a diet flag off removes the corresponding threshold.
	chartOnly := agent.Diet{Chart: true}
	thresholdsChartOnly := buildRereadSensitivity(rng, chartOnly, 0.5, params)
	for _, offChannel := range []string{channelNameRange, channelNameLiquidity, channelNameVolume} {
		if _, present := thresholdsChartOnly[offChannel]; present {
			t.Fatalf("reread sensitivity carries OFF channel %q", offChannel)
		}
	}
}

// TestDecideSensitivityCoversOnDirectionalFields verifies the per-field
// decide thresholds appear only for directional fields whose channel is
// on in the diet.
func TestDecideSensitivityCoversOnDirectionalFields(t *testing.T) {
	params := completeGenParams()
	rng := seededRNG(1, 69)

	// Chart on, range off → only chart.* directional fields appear.
	chartOnly := agent.Diet{Chart: true}
	thresholds := buildDecideSensitivity(rng, chartOnly, 0.5, params)

	if _, present := thresholds[directionalFieldChartDirection]; !present {
		t.Fatalf("decide sensitivity missing chart.direction with Chart on")
	}
	if _, present := thresholds[directionalFieldChartStrength]; !present {
		t.Fatalf("decide sensitivity missing chart.strength with Chart on")
	}
	if _, present := thresholds[directionalFieldRangePosition]; present {
		t.Fatalf("decide sensitivity carries range.position_in_range with MeanReversion off")
	}
	if _, present := thresholds[directionalFieldRangeStretch]; present {
		t.Fatalf("decide sensitivity carries range.stretch with MeanReversion off")
	}
}

// TestPerceptionDrawIsDeterministic pins the standing invariant: two
// draws from the same seed over the same inputs produce an identical
// perception profile.
func TestPerceptionDrawIsDeterministic(t *testing.T) {
	params := completeGenParams()
	diet := allChannelsOnDiet()

	first := drawPerceptionProfile(seededRNG(7, 71), 0.5, 0.5, 0.1, diet, params)
	second := drawPerceptionProfile(seededRNG(7, 71), 0.5, 0.5, 0.1, diet, params)

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("perception draw diverged under same seed:\nfirst = %s\nsecond = %s",
			firstJSON, secondJSON)
	}
}
