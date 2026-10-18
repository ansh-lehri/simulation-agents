package orchestrator

import (
	cryptoRand "crypto/rand"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
)

// RNGSource builds a fresh seeded random source per bring-up call. Live
// sessions call Fresh() each time so one session's randomness cannot
// correlate with another's; calibration and tuning runs inject a
// fixed-seed source so a bring-up replays identically for comparison.
//
// Interface over a concrete type so the orchestrator can be constructed
// with either source without a package-level feature flag — the choice
// is a composition-root concern.
type RNGSource interface {
	Fresh() *rand.Rand
}

// NewFreshEntropySource returns an RNGSource whose Fresh() calls draw
// 128 bits of entropy from the system's crypto source and seed a fresh
// PCG generator. The returned sources are statistically independent run
// to run — the right choice for live sessions, where seeded replay is
// not a goal and correlation between sessions would be a leak.
func NewFreshEntropySource() RNGSource {
	return freshEntropySource{}
}

// NewFixedSeedSource returns an RNGSource whose Fresh() calls all seed
// a fresh PCG generator with the same (seedWordOne, seedWordTwo) pair.
// Two Fresh() calls from the same fixed source replay the same sequence
// — the right choice for tuning runs that need bring-up outcomes to be
// comparable across code changes.
//
// The two-word API matches the underlying PCG constructor. Both words
// contribute to the seed state; passing (0, 0) is accepted and produces
// a valid (if trivial) replayable source.
func NewFixedSeedSource(seedWordOne, seedWordTwo uint64) RNGSource {
	return fixedSeedSource{seedWordOne: seedWordOne, seedWordTwo: seedWordTwo}
}

type freshEntropySource struct{}

// Fresh draws a 128-bit seed from the system crypto source and returns
// a new PCG-backed *rand.Rand seeded with it. crypto/rand reading from
// a trusted system source is treated as infallible — a failure here
// means the host is in a state where any further work would be unsafe,
// so panic rather than silently fall back to a weak seed.
func (freshEntropySource) Fresh() *rand.Rand {
	var seedBytes [16]byte
	if _, err := cryptoRand.Read(seedBytes[:]); err != nil {
		panic(fmt.Sprintf("orchestrator: crypto/rand failed to seed RNG: %v", err))
	}
	seedWordOne := binary.LittleEndian.Uint64(seedBytes[0:8])
	seedWordTwo := binary.LittleEndian.Uint64(seedBytes[8:16])
	return rand.New(rand.NewPCG(seedWordOne, seedWordTwo))
}

type fixedSeedSource struct {
	seedWordOne uint64
	seedWordTwo uint64
}

// Fresh returns a new PCG-backed *rand.Rand seeded with the stored seed
// pair. Two Fresh() calls on the same fixedSeedSource produce two
// independent generators that nonetheless replay the identical sequence
// — this is the whole point of the fixed-seed path.
func (source fixedSeedSource) Fresh() *rand.Rand {
	return rand.New(rand.NewPCG(source.seedWordOne, source.seedWordTwo))
}
