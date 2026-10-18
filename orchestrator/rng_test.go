package orchestrator

import "testing"

// TestFixedSeedSourceReplaysIdenticalSequence pins the central property
// of the fixed-seed path: two Fresh() calls with the same seed produce
// generators that replay the same first-K draws. This is the whole
// reason the fixed-seed source exists — tuning runs compare bring-up
// outcomes across code changes under identical randomness.
func TestFixedSeedSourceReplaysIdenticalSequence(t *testing.T) {
	source := NewFixedSeedSource(42, 7)

	firstRng := source.Fresh()
	secondRng := source.Fresh()

	for drawIndex := 0; drawIndex < 100; drawIndex++ {
		firstDraw := firstRng.Uint64()
		secondDraw := secondRng.Uint64()
		if firstDraw != secondDraw {
			t.Fatalf("fixed-seed divergence at index %d: first=%d second=%d",
				drawIndex, firstDraw, secondDraw)
		}
	}
}

// TestFixedSeedSourceDifferentSeedsDiverge is the mirror property: two
// sources built from different seeds produce different first draws.
// Trivially true for a correct implementation; included because a bug
// that collapsed every seed onto one state would otherwise be silent.
func TestFixedSeedSourceDifferentSeedsDiverge(t *testing.T) {
	firstRng := NewFixedSeedSource(1, 2).Fresh()
	secondRng := NewFixedSeedSource(3, 4).Fresh()

	firstDraw := firstRng.Uint64()
	secondDraw := secondRng.Uint64()
	if firstDraw == secondDraw {
		t.Fatalf("different seeds collapsed to the same first draw: %d", firstDraw)
	}
}

// TestFreshEntropySourceProducesIndependentSequences is the central
// property of the live path: two Fresh() calls from the same source are
// seeded from independent entropy and produce different first draws
// with overwhelming probability. The test asserts the two 64-bit first
// draws differ; a collision on genuinely independent 64-bit values has
// probability 2^-64 and would be a stronger signal of a bug than of
// bad luck.
func TestFreshEntropySourceProducesIndependentSequences(t *testing.T) {
	source := NewFreshEntropySource()

	firstDraw := source.Fresh().Uint64()
	secondDraw := source.Fresh().Uint64()

	if firstDraw == secondDraw {
		t.Fatalf("two fresh-entropy sessions produced the same first draw %d — crypto seeding is likely broken",
			firstDraw)
	}
}
