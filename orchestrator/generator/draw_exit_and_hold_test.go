package generator

import (
	"testing"
)

// TestDrawExitProfileTargetAboveStop pins the one invariant the four
// numeric exit dials must preserve: target_pct > stop_pct, by
// construction (target = stop × a reward-to-risk multiplier drawn in
// [1, N]). A regression that flipped the derivation would ship
// agents that give up gains before they appear.
func TestDrawExitProfileTargetAboveStop(t *testing.T) {
	params := completeGenParams()

	for seedCounter := uint64(0); seedCounter < 500; seedCounter++ {
		profile := drawExitProfile(seededRNG(seedCounter, 19), params, 0.5)
		if profile.TargetPct <= profile.StopPct {
			t.Fatalf("exit profile target not strictly above stop (seed %d): %+v",
				seedCounter, profile)
		}
		if profile.StopPct < params.ExitStopPctRange.Low || profile.StopPct > params.ExitStopPctRange.High {
			t.Fatalf("exit stop pct out of range (seed %d): %v not in [%v,%v]",
				seedCounter, profile.StopPct, params.ExitStopPctRange.Low, params.ExitStopPctRange.High)
		}
		if profile.StopBuffer < params.ExitStopBufferRange.Low || profile.StopBuffer > params.ExitStopBufferRange.High {
			t.Fatalf("exit stop buffer out of range (seed %d): %v", seedCounter, profile.StopBuffer)
		}
		if profile.TargetBuffer < params.ExitTargetBufferRange.Low || profile.TargetBuffer > params.ExitTargetBufferRange.High {
			t.Fatalf("exit target buffer out of range (seed %d): %v", seedCounter, profile.TargetBuffer)
		}
	}
}

// TestDrawExitModeShiftsWithSkill confirms the directional claim of the
// skill-anchor kernel: low-skill agents concentrate on the distance
// anchor (0.2 in the complete params), high-skill agents on the level
// anchor (0.85). We don't assert exact fractions; we assert the ordering
// of mode frequencies between the two skill extremes.
func TestDrawExitModeShiftsWithSkill(t *testing.T) {
	params := completeGenParams()

	lowSkillCounts := map[string]int{}
	highSkillCounts := map[string]int{}
	const sampleCount = 3000

	for seedCounter := uint64(0); seedCounter < sampleCount; seedCounter++ {
		lowMode := drawExitMode(
			seededRNG(seedCounter, 21),
			params.ExitModeAnchors, params.ExitModeSelectionTau, 0.05,
		)
		lowSkillCounts[lowMode]++

		highMode := drawExitMode(
			seededRNG(seedCounter, 23),
			params.ExitModeAnchors, params.ExitModeSelectionTau, 0.95,
		)
		highSkillCounts[highMode]++
	}

	if lowSkillCounts["distance"] <= lowSkillCounts["level"] {
		t.Fatalf("low-skill agents should prefer distance over level: distance=%d level=%d",
			lowSkillCounts["distance"], lowSkillCounts["level"])
	}
	if highSkillCounts["level"] <= highSkillCounts["distance"] {
		t.Fatalf("high-skill agents should prefer level over distance: level=%d distance=%d",
			highSkillCounts["level"], highSkillCounts["distance"])
	}
}

// TestDrawExitModeSpreadsAcrossModes checks that the mode choice is
// SAMPLED, not argmax: at a mid skill where no mode dominates the score,
// every mode must appear at least once across many seeds. An argmax
// implementation would collapse the whole batch onto one mode (the one
// whose anchor is nearest to skill) and this test would fail.
func TestDrawExitModeSpreadsAcrossModes(t *testing.T) {
	params := completeGenParams()
	// A skill value equidistant from distance (0.2) and level (0.85) with
	// hybrid (0.55) in the middle; no mode should dominate.
	midSkill := 0.5
	counts := map[string]int{}
	const sampleCount = 2000
	for seedCounter := uint64(0); seedCounter < sampleCount; seedCounter++ {
		mode := drawExitMode(seededRNG(seedCounter, 25), params.ExitModeAnchors, params.ExitModeSelectionTau, midSkill)
		counts[mode]++
	}
	for _, modeName := range []string{"distance", "hybrid", "level"} {
		if counts[modeName] == 0 {
			t.Fatalf("mode %q never selected at mid skill — exit-mode selection collapsed to argmax",
				modeName)
		}
	}
}

// TestDrawMaxHoldReactivityDirection pins the (1-reactivity) direction:
// a jumpy (high reactivity) agent lands on a shorter max-hold than a
// deliberate (low reactivity) agent on average, inside the same range
// box.
func TestDrawMaxHoldReactivityDirection(t *testing.T) {
	intRange := IntRange{Low: 10, High: 1000}
	jitter := 0.05

	jumpyTotal := 0
	deliberateTotal := 0
	const sampleCount = 500

	for seedCounter := uint64(0); seedCounter < sampleCount; seedCounter++ {
		jumpyTotal += drawMaxHold(seededRNG(seedCounter, 27), intRange, 0.9, jitter)
		deliberateTotal += drawMaxHold(seededRNG(seedCounter, 29), intRange, 0.1, jitter)
	}

	if jumpyTotal >= deliberateTotal {
		t.Fatalf("jumpy agents should land on shorter max-holds than deliberate ones: "+
			"jumpy-total=%d deliberate-total=%d (over %d seeds each)",
			jumpyTotal, deliberateTotal, sampleCount)
	}
}

// TestDrawMaxHoldStaysInBox covers the clamp step: even with aggressive
// jitter the draw must stay inside the configured range.
func TestDrawMaxHoldStaysInBox(t *testing.T) {
	intRange := IntRange{Low: 50, High: 100}
	for seedCounter := uint64(0); seedCounter < 500; seedCounter++ {
		value := drawMaxHold(seededRNG(seedCounter, 31), intRange, 0.5, 0.4)
		if value < intRange.Low || value > intRange.High {
			t.Fatalf("drawMaxHold escaped box: got %d, want in [%d,%d] (seed %d)",
				value, intRange.Low, intRange.High, seedCounter)
		}
	}
}

// TestSortedKeysIsDeterministic covers the small helper that neutralises
// Go's randomized map iteration. Called many times on the same map and
// always returns the same slice.
func TestSortedKeysIsDeterministic(t *testing.T) {
	mapping := map[string]float64{"beta": 1, "alpha": 2, "gamma": 3}
	first := sortedKeys(mapping)
	for i := 0; i < 50; i++ {
		next := sortedKeys(mapping)
		for index := range first {
			if first[index] != next[index] {
				t.Fatalf("sortedKeys drifted at index %d: first=%q next=%q",
					index, first[index], next[index])
			}
		}
	}
	// Also assert it is actually sorted.
	for index := 1; index < len(first); index++ {
		if first[index-1] > first[index] {
			t.Fatalf("sortedKeys output not sorted: %v", first)
		}
	}
}
