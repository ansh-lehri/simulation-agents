package generator

import (
	"testing"
)

// TestThetaActDirectionFollowsAggressiveness pins the (1−aggressiveness)
// direction: cautious agents (low aggressiveness) land on a higher commit bar
// than aggressive agents (high aggressiveness) on average across many draws.
func TestThetaActDirectionFollowsAggressiveness(t *testing.T) {
	params := completeGenParams()

	cautiousTotal := 0.0
	aggressiveTotal := 0.0
	const sampleCount = 500

	for seedWord := uint64(0); seedWord < sampleCount; seedWord++ {
		cautiousTotal += drawThetaAct(seededRNG(seedWord, 41), 0.1, params)
		aggressiveTotal += drawThetaAct(seededRNG(seedWord, 43), 0.9, params)
	}

	if cautiousTotal <= aggressiveTotal {
		t.Fatalf("cautious agents should land on a higher commit bar than aggressive ones: "+
			"cautious-total=%v aggressive-total=%v (over %d seeds each)",
			cautiousTotal, aggressiveTotal, sampleCount)
	}
}

// TestThetaActStaysInBox covers the clamp step: even with jitter the draw
// must stay within [ThetaActRange.Low, ThetaActRange.High].
func TestThetaActStaysInBox(t *testing.T) {
	params := completeGenParams()

	for seedWord := uint64(0); seedWord < 500; seedWord++ {
		value := drawThetaAct(seededRNG(seedWord, 45), 0.5, params)
		if value < params.ThetaActRange.Low || value > params.ThetaActRange.High {
			t.Fatalf("drawThetaAct escaped box: got %v, want in [%v,%v] (seed %d)",
				value, params.ThetaActRange.Low, params.ThetaActRange.High, seedWord)
		}
	}
}

// TestNeedsConfirmRateTracksAggressiveness verifies the coin direction: the
// fraction of immediate actors (needs_confirm=false) across many seeds must
// be noticeably higher for aggressive agents than for cautious agents.
func TestNeedsConfirmRateTracksAggressiveness(t *testing.T) {
	immediateAtHighAggr := 0
	immediateAtLowAggr := 0
	const sampleCount = 1000

	for seedWord := uint64(0); seedWord < sampleCount; seedWord++ {
		if !drawNeedsConfirm(seededRNG(seedWord, 47), 0.9) {
			immediateAtHighAggr++
		}
		if !drawNeedsConfirm(seededRNG(seedWord, 49), 0.1) {
			immediateAtLowAggr++
		}
	}

	if immediateAtHighAggr <= immediateAtLowAggr {
		t.Fatalf("aggressive agents should be immediate more often than cautious ones: "+
			"high-aggr-immediate=%d low-aggr-immediate=%d (over %d seeds each)",
			immediateAtHighAggr, immediateAtLowAggr, sampleCount)
	}
}

// TestConfirmsNeededDirectionFollowsAggressiveness pins the direction:
// cautious agents (low aggressiveness) require more confirmations on average
// than aggressive agents (high aggressiveness).
func TestConfirmsNeededDirectionFollowsAggressiveness(t *testing.T) {
	params := completeGenParams()

	cautiousTotal := 0
	aggressiveTotal := 0
	const sampleCount = 500

	for seedWord := uint64(0); seedWord < sampleCount; seedWord++ {
		cautiousTotal += drawConfirmsNeeded(seededRNG(seedWord, 51), 0.1, params)
		aggressiveTotal += drawConfirmsNeeded(seededRNG(seedWord, 53), 0.9, params)
	}

	if cautiousTotal <= aggressiveTotal {
		t.Fatalf("cautious agents should require more confirmations than aggressive ones: "+
			"cautious-total=%d aggressive-total=%d (over %d seeds each)",
			cautiousTotal, aggressiveTotal, sampleCount)
	}
}

// TestConfirmsNeededStaysInBox covers the clamp step: the result must always
// be an integer within [ConfirmsNeededRange.Low, ConfirmsNeededRange.High].
func TestConfirmsNeededStaysInBox(t *testing.T) {
	params := completeGenParams()

	for seedWord := uint64(0); seedWord < 500; seedWord++ {
		value := drawConfirmsNeeded(seededRNG(seedWord, 55), 0.5, params)
		if value < params.ConfirmsNeededRange.Low || value > params.ConfirmsNeededRange.High {
			t.Fatalf("drawConfirmsNeeded escaped box: got %d, want in [%d,%d] (seed %d)",
				value, params.ConfirmsNeededRange.Low, params.ConfirmsNeededRange.High, seedWord)
		}
	}
}

// TestDrawCommitGateTraitsReturnOrder confirms that the three-value return
// from the wrapper matches the order (thetaAct, needsConfirm, confirmsNeeded)
// and that calling the wrapper produces the same values as calling each
// function individually from the same seed.
func TestDrawCommitGateTraitsReturnOrder(t *testing.T) {
	params := completeGenParams()
	const aggressiveness = 0.6

	// Wrapper path.
	thetaAct, needsConfirm, confirmsNeeded := drawCommitGateTraits(
		seededRNG(7, 57), aggressiveness, params,
	)

	// Individual path — must consume the RNG in the same order.
	rng := seededRNG(7, 57)
	wantThetaAct := drawThetaAct(rng, aggressiveness, params)
	wantNeedsConfirm := drawNeedsConfirm(rng, aggressiveness)
	wantConfirmsNeeded := drawConfirmsNeeded(rng, aggressiveness, params)

	if thetaAct != wantThetaAct {
		t.Errorf("thetaAct mismatch: wrapper=%v individual=%v", thetaAct, wantThetaAct)
	}
	if needsConfirm != wantNeedsConfirm {
		t.Errorf("needsConfirm mismatch: wrapper=%v individual=%v", needsConfirm, wantNeedsConfirm)
	}
	if confirmsNeeded != wantConfirmsNeeded {
		t.Errorf("confirmsNeeded mismatch: wrapper=%v individual=%v", confirmsNeeded, wantConfirmsNeeded)
	}
}
