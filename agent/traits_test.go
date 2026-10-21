package agent_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"simulation-agents/agent"
)

// A herd row carries only the universal fields plus ActProb; every
// endogenous and canonical-only block must be literally absent on the wire,
// not JSON null — asserted at byte level.
func TestTraitVector_HerdWireIsLean(t *testing.T) {
	actProb := 0.42
	herd := agent.TraitVector{
		AgentKind:      agent.AgentKindHerd,
		Skill:          0.3,
		Aggressiveness: 0.7,
		CapitalSize:    50_000,
		PnlReactivity:  0.2,
		PAct:           0.1,
		ActProb:        &actProb,
	}

	raw, err := json.Marshal(herd)
	if err != nil {
		t.Fatalf("marshal herd: %v", err)
	}

	mustOmit := []string{
		"disposition", "diet", "rho", "phi",
		"perception_profile", "exit_profile", "max_hold",
		"theta_act", "needs_confirm", "confirms_needed",
		"is_tipper",
	}
	for _, key := range mustOmit {
		needle := []byte(`"` + key + `"`)
		if bytes.Contains(raw, needle) {
			t.Errorf("herd row carries %q; expected it absent: %s", key, raw)
		}
	}
	if !bytes.Contains(raw, []byte(`"act_prob"`)) {
		t.Errorf("herd row missing act_prob: %s", raw)
	}

	var decoded agent.TraitVector
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal herd: %v", err)
	}
	if !reflect.DeepEqual(herd, decoded) {
		t.Errorf("herd round-trip mismatch:\n got  %+v\n want %+v", decoded, herd)
	}
}

// A fully populated endogenous (tipper) row must round-trip exactly, and
// must not carry the herd-only act_prob field. Includes all three commit-gate
// trait fields so the JSONB path for those fields is exercised end-to-end.
func TestTraitVector_EndogenousTipperRoundTrip(t *testing.T) {
	rho := 0.55
	phi := -0.2
	maxHold := 1500
	thetaAct := 0.45
	needsConfirm := true
	confirmsNeeded := 2

	original := agent.TraitVector{
		AgentKind:      agent.AgentKindEndogenous,
		Skill:          0.6,
		Aggressiveness: 0.4,
		CapitalSize:    1_000_000,
		PnlReactivity:  0.5,
		PAct:           0.3,
		Disposition: &agent.Disposition{
			Trend:   0.5,
			MeanRev: 0.3,
			Noise:   0.2,
		},
		Diet: &agent.Diet{
			Chart:         true,
			Volume:        true,
			TimeOfSession: true,
		},
		Rho: &rho,
		Phi: &phi,
		Perception: &agent.PerceptionProfile{
			Chart: &agent.ChannelBlock{
				Method:         "moving_average",
				ReadingParams:  map[string]float64{"short_window": 10, "long_window": 50},
				ReadingCutoffs: map[string]float64{"breakout": 0.02},
			},
			Volume: &agent.ChannelBlock{
				ReadingParams: map[string]float64{"window": 20},
			},
			Bias:              map[string]float64{"direction": 0.1},
			ChannelWeights:    map[string]map[string]float64{"trend": {"chart": 0.8, "volume": 0.2}},
			RereadSensitivity: map[string]float64{"chart": 0.05},
			DecideSensitivity: map[string]float64{"breakout": 0.1},
		},
		Exit: &agent.ExitProfile{
			Mode:         "distance",
			StopPct:      0.02,
			TargetPct:    0.04,
			StopBuffer:   0.005,
			TargetBuffer: 0.005,
		},
		MaxHold:        &maxHold,
		ThetaAct:       &thetaAct,
		NeedsConfirm:   &needsConfirm,
		ConfirmsNeeded: &confirmsNeeded,
		IsTipper:       true,
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal endogenous: %v", err)
	}

	if bytes.Contains(raw, []byte(`"act_prob"`)) {
		t.Errorf("endogenous row carries act_prob: %s", raw)
	}
	for _, key := range []string{
		"disposition", "diet", "rho", "phi",
		"perception_profile", "exit_profile", "max_hold",
		"theta_act", "needs_confirm", "confirms_needed",
		"is_tipper",
	} {
		if !bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Errorf("endogenous row missing %q: %s", key, raw)
		}
	}

	var decoded agent.TraitVector
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal endogenous: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Errorf("endogenous round-trip mismatch:\n got  %+v\n want %+v", decoded, original)
	}
}

// A non-tipper endogenous row must omit is_tipper on the wire (omitempty
// on a bool drops the key when false).
func TestTraitVector_EndogenousNonTipper_OmitsIsTipper(t *testing.T) {
	nonTipper := agent.TraitVector{
		AgentKind:      agent.AgentKindEndogenous,
		Skill:          0.5,
		Aggressiveness: 0.5,
		CapitalSize:    100_000,
		PnlReactivity:  0.5,
		PAct:           0.5,
	}
	raw, err := json.Marshal(nonTipper)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(raw, []byte(`"is_tipper"`)) {
		t.Errorf("non-tipper carries is_tipper: %s", raw)
	}
}

// Commit-gate fields on an endogenous row must survive a JSONB round-trip
// with their exact types preserved: theta_act as float64, needs_confirm as
// bool, confirms_needed as int. This exercises the pointer-unwrap path and
// catches any accidental type coercion during JSON decode.
func TestTraitVector_CommitGateFieldsRoundTrip(t *testing.T) {
	thetaAct := 0.62
	needsConfirmFalse := false
	confirmsNeeded := 1

	endogenous := agent.TraitVector{
		AgentKind:      agent.AgentKindEndogenous,
		Skill:          0.5,
		Aggressiveness: 0.7,
		CapitalSize:    200_000,
		PnlReactivity:  0.4,
		PAct:           0.2,
		ThetaAct:       &thetaAct,
		NeedsConfirm:   &needsConfirmFalse,
		ConfirmsNeeded: &confirmsNeeded,
	}

	raw, err := json.Marshal(endogenous)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, key := range []string{"theta_act", "needs_confirm", "confirms_needed"} {
		if !bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Errorf("commit-gate field %q absent from wire: %s", key, raw)
		}
	}

	var decoded agent.TraitVector
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(endogenous, decoded) {
		t.Errorf("commit-gate fields round-trip mismatch:\n got  %+v\n want %+v", decoded, endogenous)
	}
	if decoded.ThetaAct == nil || *decoded.ThetaAct != thetaAct {
		t.Errorf("ThetaAct wrong after round-trip: got %v, want %v", decoded.ThetaAct, thetaAct)
	}
	if decoded.NeedsConfirm == nil || *decoded.NeedsConfirm != needsConfirmFalse {
		t.Errorf("NeedsConfirm wrong after round-trip: got %v, want %v", decoded.NeedsConfirm, needsConfirmFalse)
	}
	if decoded.ConfirmsNeeded == nil || *decoded.ConfirmsNeeded != confirmsNeeded {
		t.Errorf("ConfirmsNeeded wrong after round-trip: got %v, want %v", decoded.ConfirmsNeeded, confirmsNeeded)
	}
}

// Herd rows must carry nil for all three commit-gate fields — they follow
// tips and never go through the commit-gate decision path.
func TestTraitVector_CommitGateFieldsAbsentOnHerd(t *testing.T) {
	actProb := 0.5
	herd := agent.TraitVector{
		AgentKind:      agent.AgentKindHerd,
		Skill:          0.3,
		Aggressiveness: 0.6,
		CapitalSize:    50_000,
		PnlReactivity:  0.3,
		PAct:           0.1,
		ActProb:        &actProb,
	}

	raw, err := json.Marshal(herd)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, key := range []string{"theta_act", "needs_confirm", "confirms_needed"} {
		if bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Errorf("herd row carries commit-gate field %q; must be absent: %s", key, raw)
		}
	}

	var decoded agent.TraitVector
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ThetaAct != nil {
		t.Errorf("herd ThetaAct must be nil, got %v", *decoded.ThetaAct)
	}
	if decoded.NeedsConfirm != nil {
		t.Errorf("herd NeedsConfirm must be nil, got %v", *decoded.NeedsConfirm)
	}
	if decoded.ConfirmsNeeded != nil {
		t.Errorf("herd ConfirmsNeeded must be nil, got %v", *decoded.ConfirmsNeeded)
	}
}

// A perception block with only Chart on must omit all other channel
// blocks on the wire — off channels are absent, not null.
func TestPerceptionProfile_OffChannelsAbsent(t *testing.T) {
	profile := agent.PerceptionProfile{
		Chart: &agent.ChannelBlock{
			Method:        "moving_average",
			ReadingParams: map[string]float64{"short_window": 5},
		},
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"range", "liquidity", "volume", "time_of_session"} {
		if bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Errorf("perception carries off channel %q: %s", key, raw)
		}
	}
	if !bytes.Contains(raw, []byte(`"chart"`)) {
		t.Errorf("perception missing on channel chart: %s", raw)
	}
}
