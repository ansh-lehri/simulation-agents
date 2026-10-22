package decision

import (
	"math"
	"testing"

	"simulation-agents/agent"
)

// testInterpretationParams are fixed, hand-chosen coefficients used
// across every interpret table test below so each case's expected
// value can be hand-computed from the formula in the test comment.
func testInterpretationParams() InterpretationParams {
	return InterpretationParams{
		TrendCleanlinessBoost:        0.5,
		TrendVolumeConfirmBoost:      0.2,
		TrendVolumeLowDamp:           0.3,
		MeanRevVolumeLowExtremeBoost: 0.2,
	}
}

func verdictFloat(verdict *float64) float64 {
	if verdict == nil {
		return math.NaN() // never equals anything, including itself — forces a visible mismatch if a test wrongly compares a nil verdict by value
	}
	return *verdict
}

func TestInterpretTrend(t *testing.T) {
	params := testInterpretationParams()
	role := TraitChannelRoles["trend"]

	cases := []struct {
		name    string
		chart   *ChartReading
		volume  *VolumeReading
		wantNil bool
		want    float64
	}{
		{
			name:  "up, no cleanliness boost, no volume reading",
			chart: &ChartReading{Direction: DirUp, Strength: 0.8, Cleanliness: 0, ReadOK: true},
			want:  0.8, // lean=1*0.8=0.8; cleanlinessMult=1+0.5*0=1; volumeMult(nil)=1 → 0.8*1*1
		},
		{
			name:   "down, full cleanliness boost, confirming volume",
			chart:  &ChartReading{Direction: DirDown, Strength: 0.5, Cleanliness: 1, ReadOK: true},
			volume: &VolumeReading{Confirming: true, ReadOK: true},
			want:   -0.9, // lean=-0.5; cleanlinessMult=1.5; volumeMult=1.2 → sign(-1)*clamp(0.5*1.5*1.2)=-0.9
		},
		{
			name:  "flat direction returns a present, zero-magnitude verdict",
			chart: &ChartReading{Direction: DirFlat, Strength: 0.9, Cleanliness: 0.4, ReadOK: true},
			want:  0, // lean=0*0.9=0 regardless of strength/cleanliness — absent ≠ neutral means this must still be non-nil
		},
		{
			name:   "low volume dampens, cleanliness boost present",
			chart:  &ChartReading{Direction: DirUp, Strength: 1.0, Cleanliness: 1.0, ReadOK: true},
			volume: &VolumeReading{Level: VolLow, ReadOK: true},
			want:   1.0, // lean=1; cleanlinessMult=1.5; volumeMult=0.7 → clamp01(1*1.5*0.7)=clamp01(1.05)=1.0 (clamp triggers)
		},
		{
			name:   "unreadable volume treated as neutral, not as low",
			chart:  &ChartReading{Direction: DirUp, Strength: 0.4, Cleanliness: 0, ReadOK: true},
			volume: &VolumeReading{Level: VolLow, ReadOK: false},
			want:   0.4, // unreadable volume must not apply the low-volume damp
		},
		{
			name:    "nil chart is absent, not neutral",
			chart:   nil,
			wantNil: true,
		},
		{
			name:    "unreadable chart is absent, not neutral",
			chart:   &ChartReading{Direction: DirUp, Strength: 0.9, ReadOK: false},
			wantNil: true,
		},
	}

	for _, testCase := range cases {
		bundle := PerceptionBundle{Chart: testCase.chart, Volume: testCase.volume}
		got := interpretTrend(bundle, role, params)
		if testCase.wantNil {
			if got != nil {
				t.Errorf("%s: got %v, want nil", testCase.name, *got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%s: got nil, want %v", testCase.name, testCase.want)
			continue
		}
		if math.Abs(*got-testCase.want) > 1e-9 {
			t.Errorf("%s: got %v, want %v", testCase.name, *got, testCase.want)
		}
	}
}

func TestInterpretTrend_VolumeMultiplierNotAppliedWhenRoleOmitsIt(t *testing.T) {
	params := testInterpretationParams()
	role := TraitChannelRole{Lean: "chart", Mults: nil} // volume deliberately not listed

	bundle := PerceptionBundle{
		Chart:  &ChartReading{Direction: DirUp, Strength: 0.5, Cleanliness: 0, ReadOK: true},
		Volume: &VolumeReading{Confirming: true, ReadOK: true}, // would boost if the role listed volume
	}

	got := interpretTrend(bundle, role, params)
	if got == nil {
		t.Fatal("got nil, want a present verdict")
	}
	if math.Abs(*got-0.5) > 1e-9 {
		t.Errorf("volume multiplier applied despite role.Mults omitting it: got %v, want 0.5", *got)
	}
}

func TestInterpretMeanRev(t *testing.T) {
	params := testInterpretationParams()
	role := TraitChannelRoles["mean_rev"]

	cases := []struct {
		name    string
		rng     *RangeReading
		volume  *VolumeReading
		wantNil bool
		want    float64
	}{
		{
			name: "near top of range fades short",
			rng:  &RangeReading{PositionInRange: 0.8, Stretch: 0.5, ReadOK: true},
			want: -0.4, // lean=-0.8*0.5=-0.4; no volume reading → multiplier=1
		},
		{
			name:   "near bottom of range fades long, thin volume at an extreme boosts",
			rng:    &RangeReading{PositionInRange: -0.9, Stretch: 0.6, ReadOK: true},
			volume: &VolumeReading{Level: VolLow, ReadOK: true},
			want:   0.648, // lean=-(-0.9)*0.6=0.54; boost(Low&&stretch>0)=1.2 → 0.54*1.2=0.648
		},
		{
			name:   "thin volume without a stretched extreme does not boost",
			rng:    &RangeReading{PositionInRange: 0.5, Stretch: 0.3, ReadOK: true},
			volume: &VolumeReading{Level: VolNormal, ReadOK: true},
			want:   -0.15, // lean=-0.5*0.3=-0.15; Level is Normal, not Low → multiplier=1
		},
		{
			name:   "unreadable volume treated as neutral",
			rng:    &RangeReading{PositionInRange: 0.5, Stretch: 0.5, ReadOK: true},
			volume: &VolumeReading{Level: VolLow, ReadOK: false},
			want:   -0.25, // unreadable volume must not apply the extreme-exhaustion boost
		},
		{
			name:    "nil range is absent, not neutral",
			rng:     nil,
			wantNil: true,
		},
		{
			name:    "unreadable range is absent, not neutral",
			rng:     &RangeReading{PositionInRange: 0.5, Stretch: 0.5, ReadOK: false},
			wantNil: true,
		},
	}

	for _, testCase := range cases {
		bundle := PerceptionBundle{Range: testCase.rng, Volume: testCase.volume}
		got := interpretMeanRev(bundle, role, params)
		if testCase.wantNil {
			if got != nil {
				t.Errorf("%s: got %v, want nil", testCase.name, *got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%s: got nil, want %v", testCase.name, testCase.want)
			continue
		}
		if math.Abs(*got-testCase.want) > 1e-9 {
			t.Errorf("%s: got %v, want %v", testCase.name, *got, testCase.want)
		}
	}
}

// TestInterpret_TopLevelWiring confirms interpret() dispatches to the
// trend and mean-reversion helpers using the right bundle channels and
// the right TraitChannelRoles entry for each — a regression guard
// against a copy-paste mistake wiring mean_rev's role into the trend
// call or vice versa.
func TestInterpret_TopLevelWiring(t *testing.T) {
	params := testInterpretationParams()
	bundle := PerceptionBundle{
		Chart: &ChartReading{Direction: DirUp, Strength: 0.6, ReadOK: true},
		Range: &RangeReading{PositionInRange: 0.5, Stretch: 0.4, ReadOK: true},
	}

	verdicts := interpret(bundle, agent.TraitVector{}, TraitChannelRoles, params)

	if verdicts.trend == nil || math.Abs(*verdicts.trend-0.6) > 1e-9 {
		t.Errorf("trend verdict: got %v, want 0.6", verdictFloat(verdicts.trend))
	}
	wantMeanRev := -0.5 * 0.4
	if verdicts.meanRev == nil || math.Abs(*verdicts.meanRev-wantMeanRev) > 1e-9 {
		t.Errorf("meanRev verdict: got %v, want %v", verdictFloat(verdicts.meanRev), wantMeanRev)
	}
}

func TestDirectionSign(t *testing.T) {
	cases := []struct {
		direction Direction
		want      float64
	}{
		{DirUp, 1},
		{DirDown, -1},
		{DirFlat, 0},
	}
	for _, testCase := range cases {
		if got := directionSign(testCase.direction); got != testCase.want {
			t.Errorf("directionSign(%v): got %v, want %v", testCase.direction, got, testCase.want)
		}
	}
}

func TestSignedMagnitude_NeverFlipsSignAndAlwaysClamps(t *testing.T) {
	cases := []struct {
		lean, multiplier, want float64
	}{
		{0.5, 1, 0.5},
		{-0.5, 1, -0.5},
		{0.5, 3, 1},   // |lean|*multiplier=1.5 overshoots 1 → clamps to the sign's own bound of 1, never past it
		{-0.5, 3, -1}, // same, negative side
		{0, 10, 0},    // zero lean stays zero regardless of multiplier
	}
	for _, testCase := range cases {
		got := signedMagnitude(testCase.lean, testCase.multiplier)
		if math.Abs(got-testCase.want) > 1e-9 {
			t.Errorf("signedMagnitude(%v, %v): got %v, want %v", testCase.lean, testCase.multiplier, got, testCase.want)
		}
	}
}

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
