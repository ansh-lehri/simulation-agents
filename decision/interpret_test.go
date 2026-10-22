package decision

import "testing"

// TraitChannelRoles is a fixed design fact, not a generated value — this
// pins its exact shape so a future edit cannot silently add a noise
// entry back in or swap which channel is the lean versus the multiplier
// for an existing trait.
func TestTraitChannelRoles_Shape(t *testing.T) {
	if len(TraitChannelRoles) != 2 {
		t.Fatalf("TraitChannelRoles: want exactly 2 traits, got %d (%v)", len(TraitChannelRoles), TraitChannelRoles)
	}

	trend, ok := TraitChannelRoles["trend"]
	if !ok {
		t.Fatal("TraitChannelRoles: missing \"trend\" entry")
	}
	if trend.Lean != "chart" {
		t.Errorf("trend.Lean: got %q want %q", trend.Lean, "chart")
	}
	if len(trend.Mults) != 1 || trend.Mults[0] != "volume" {
		t.Errorf("trend.Mults: got %v want [\"volume\"]", trend.Mults)
	}

	meanRev, ok := TraitChannelRoles["mean_rev"]
	if !ok {
		t.Fatal("TraitChannelRoles: missing \"mean_rev\" entry")
	}
	if meanRev.Lean != "range" {
		t.Errorf("mean_rev.Lean: got %q want %q", meanRev.Lean, "range")
	}
	if len(meanRev.Mults) != 1 || meanRev.Mults[0] != "volume" {
		t.Errorf("mean_rev.Mults: got %v want [\"volume\"]", meanRev.Mults)
	}

	if _, hasNoise := TraitChannelRoles["noise"]; hasNoise {
		t.Error("TraitChannelRoles must not have a \"noise\" entry — noise reads no channel")
	}
}
