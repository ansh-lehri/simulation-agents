package generator

import "testing"

// TestDrawActProbStaysInUnitInterval pins the sole structural property
// the herd credulity trait has to satisfy: a probability in [0, 1]. The
// Beta primitive is already tested; this is the smoke test for the
// trait-level call site.
func TestDrawActProbStaysInUnitInterval(t *testing.T) {
	pair := BetaPair{Alpha: 2, Beta: 2}
	for seedCounter := uint64(0); seedCounter < 500; seedCounter++ {
		sample := drawActProb(seededRNG(seedCounter, 33), pair)
		if sample < 0 || sample > 1 {
			t.Fatalf("drawActProb out of [0,1]: got %v (seed %d)", sample, seedCounter)
		}
	}
}
