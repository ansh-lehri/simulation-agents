package decision

import (
	"math"
	"math/rand/v2"
	"testing"

	"simulation-agents/agent"
)

// noiseRNG returns a seeded RNG for deterministic noise draws in tests.
func noiseRNG() *rand.Rand { return rand.New(rand.NewPCG(42, 0)) }

// zeroNoise returns a skillful agent whose noise sigma collapses to zero,
// so biasedAndNoisy returns raw+bias exactly.
func zeroNoise() agent.TraitVector { return agent.TraitVector{Skill: 1.0} }

// biasTraits builds a trait vector with one bias entry and skill=1 (no noise).
func biasTraits(key string, biasValue float64) agent.TraitVector {
	return agent.TraitVector{
		Skill: 1.0,
		Perception: &agent.PerceptionProfile{
			Bias: map[string]float64{key: biasValue},
		},
	}
}

func TestBiasedAndNoisy_NoEffect(t *testing.T) {
	raw := 0.5
	got := biasedAndNoisy(raw, "chart.direction", zeroNoise(), noiseRNG(), PerceptionParams{SigmaPerc: 0.1})
	if got != raw {
		t.Fatalf("skill=1 → sigma=0 → expected %v, got %v", raw, got)
	}
}

func TestBiasedAndNoisy_BiasAdded(t *testing.T) {
	raw := 0.5
	bias := 0.2
	traits := biasTraits("chart.strength", bias)
	got := biasedAndNoisy(raw, "chart.strength", traits, noiseRNG(), PerceptionParams{SigmaPerc: 0.1})
	if math.Abs(got-(raw+bias)) > 1e-12 {
		t.Fatalf("expected %v+%v=%v, got %v", raw, bias, raw+bias, got)
	}
}

func TestBiasedAndNoisy_AbsentKeyZeroBias(t *testing.T) {
	raw := 0.7
	got := biasedAndNoisy(raw, "absent.key", zeroNoise(), noiseRNG(), PerceptionParams{SigmaPerc: 0})
	if got != raw {
		t.Fatalf("absent bias key with zero sigma should return raw; got %v", got)
	}
}

func TestBiasedAndNoisy_LowSkillAddsNoise(t *testing.T) {
	// With skill=0 and SigmaPerc=1.0 the noise can be large; just verify
	// that the output differs from raw across multiple draws.
	raw := 0.0
	traits := agent.TraitVector{Skill: 0.0}
	params := PerceptionParams{SigmaPerc: 1.0}
	rng := rand.New(rand.NewPCG(1, 2))
	allSame := true
	prev := biasedAndNoisy(raw, "x", traits, rng, params)
	for i := 0; i < 20; i++ {
		v := biasedAndNoisy(raw, "x", traits, rng, params)
		if v != prev {
			allSame = false
			break
		}
	}
	if allSame {
		t.Fatal("low-skill agent should produce varying noise draws")
	}
}

func TestBiasedAndNoisy_NilPerception(t *testing.T) {
	raw := 0.3
	traits := agent.TraitVector{Skill: 1.0, Perception: nil}
	got := biasedAndNoisy(raw, "any", traits, noiseRNG(), PerceptionParams{SigmaPerc: 0})
	if got != raw {
		t.Fatalf("nil perception with zero sigma: expected %v, got %v", raw, got)
	}
}

func TestDirectionFromSpread(t *testing.T) {
	cases := []struct {
		spread, band float64
		want         Direction
	}{
		{0.005, 0.001, DirUp},
		{-0.005, 0.001, DirDown},
		{0.0005, 0.001, DirFlat},
		{0.001, 0.001, DirFlat},  // exactly at boundary → flat
		{-0.001, 0.001, DirFlat}, // same on negative side
	}
	for _, c := range cases {
		got := directionFromSpread(c.spread, c.band)
		if got != c.want {
			t.Errorf("directionFromSpread(%v, %v) = %v, want %v", c.spread, c.band, got, c.want)
		}
	}
}

func TestClamp01(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{-1, 0}, {0, 0}, {0.5, 0.5}, {1, 1}, {2, 1},
	}
	for _, c := range cases {
		if got := clamp01(c.in); got != c.want {
			t.Errorf("clamp01(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestClampSymmetric(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{-2, -1}, {-1, -1}, {0, 0}, {1, 1}, {2, 1},
	}
	for _, c := range cases {
		if got := clampSymmetric(c.in); got != c.want {
			t.Errorf("clampSymmetric(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPathEfficiency_Straight(t *testing.T) {
	closes := []float64{100, 101, 102, 103, 104}
	got := pathEfficiency(closes)
	if math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("straight up move: expected 1.0, got %v", got)
	}
}

func TestPathEfficiency_Chop(t *testing.T) {
	// Up then back to origin — displacement = 0.
	closes := []float64{100, 110, 100}
	got := pathEfficiency(closes)
	if got != 0 {
		t.Fatalf("full round-trip: expected 0, got %v", got)
	}
}

func TestPathEfficiency_ShortSlice(t *testing.T) {
	if got := pathEfficiency([]float64{100}); got != 1.0 {
		t.Fatalf("single element: expected 1.0, got %v", got)
	}
	if got := pathEfficiency(nil); got != 1.0 {
		t.Fatalf("nil: expected 1.0, got %v", got)
	}
}

func TestPathEfficiency_StillPrice(t *testing.T) {
	closes := []float64{100, 100, 100}
	got := pathEfficiency(closes)
	if got != 1.0 {
		t.Fatalf("still price: expected 1.0, got %v", got)
	}
}

func TestTrailingRunFraction_AllTrue(t *testing.T) {
	got := trailingRunFraction([]bool{true, true, true})
	if got != 1.0 {
		t.Fatalf("all true: expected 1.0, got %v", got)
	}
}

func TestTrailingRunFraction_AllFalse(t *testing.T) {
	got := trailingRunFraction([]bool{false, false})
	if got != 0 {
		t.Fatalf("all false: expected 0, got %v", got)
	}
}

func TestTrailingRunFraction_PartialTrailing(t *testing.T) {
	// Last 2 of 4 are true → 0.5
	got := trailingRunFraction([]bool{false, false, true, true})
	if math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("last-2-of-4 true: expected 0.5, got %v", got)
	}
}

func TestTrailingRunFraction_Empty(t *testing.T) {
	if got := trailingRunFraction(nil); got != 0 {
		t.Fatalf("nil: expected 0, got %v", got)
	}
}

func TestMedianFloat64_Odd(t *testing.T) {
	got := medianFloat64([]float64{3, 1, 2})
	if got != 2 {
		t.Fatalf("want 2, got %v", got)
	}
}

func TestMedianFloat64_Even(t *testing.T) {
	got := medianFloat64([]float64{1, 2, 3, 4})
	if got != 2.5 {
		t.Fatalf("want 2.5, got %v", got)
	}
}

func TestMedianFloat64_Empty(t *testing.T) {
	if got := medianFloat64(nil); got != 0 {
		t.Fatalf("empty: want 0, got %v", got)
	}
}

func TestMedianFloat64_PreservesInput(t *testing.T) {
	input := []float64{5, 1, 3}
	_ = medianFloat64(input)
	// Input must not be sorted in place.
	if input[0] != 5 {
		t.Fatal("medianFloat64 must not modify input slice")
	}
}
