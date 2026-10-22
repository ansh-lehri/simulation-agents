package decision

import (
	"math"
	"testing"

	"pgregory.net/rapid"

	"simulation-agents/agent"
)

func testCommitParams() Params {
	return Params{
		TimeShave: TimeShaveParams{
			OpenDamp: 0.3, CloseDamp: 0.4, OpenRampExponent: 2, CloseRampExponent: 2, Floor: 0.3,
		},
		SpreadShave: SpreadShaveParams{
			SpreadSensitivity: 1.0, NormalSpread: 0.001, Floor: 0.3,
		},
		CooldownShave: CooldownShaveParams{
			Damp: 0.5, Tau: 10, Floor: 0.4,
		},
	}
}

// thetaAct/needsConfirm/confirmsNeeded helpers build a minimal valid
// TraitVector for commit's tests — only the fields commit actually
// reads.
func testTraits(thetaAct float64, needsConfirm bool, confirmsNeeded int) agent.TraitVector {
	return agent.TraitVector{
		ThetaAct:       &thetaAct,
		NeedsConfirm:   &needsConfirm,
		ConfirmsNeeded: &confirmsNeeded,
		Diet:           &agent.Diet{OrderBook: false},
	}
}

// A mid-session tick (sessionProgress=0.5) with OrderBook off and no
// prior commit leaves every shave at its neutral 1 — isolating the
// floor-check and state-machine tests below from the shave math.
const midSessionNoShaveTick = 100

func TestCommit_MissingGeneratedTraitsPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic when theta_act/needs_confirm/confirms_needed are missing")
		}
	}()
	traits := agent.TraitVector{} // all three nil
	commit(mixOpinion{Direction: 1, Conviction: 0.9}, traits, midSessionNoShaveTick, 0.5, nil, NoPriorCommit, nil, testCommitParams())
}

func TestCommit_BelowThetaAct_SkipsAndClearsPendingWatch(t *testing.T) {
	traits := testTraits(0.8, true, 2)
	existingWatch := &PendingWatch{Direction: 1, ConfirmsSeen: 1, SinceTick: 50}

	result := commit(mixOpinion{Direction: 1, Conviction: 0.5}, traits, midSessionNoShaveTick, 0.5, nil, NoPriorCommit, existingWatch, testCommitParams())
	if result.Outcome != commitSkip {
		t.Errorf("Outcome: got %v, want commitSkip", result.Outcome)
	}
	if result.PendingWatch != nil {
		t.Errorf("PendingWatch: got %+v, want nil (SKIP always clears the watch)", result.PendingWatch)
	}
}

func TestCommit_ImmediateActor_CommitsWithEffectiveConvictionNotRaw(t *testing.T) {
	traits := testTraits(0.2, false, 1) // needs_confirm=false
	traits.Diet = &agent.Diet{OrderBook: true}
	liquidity := &LiquidityReading{Spread: 0.002, ReadOK: true} // wide enough to actually shave

	result := commit(mixOpinion{Direction: -1, Conviction: 0.9}, traits, midSessionNoShaveTick, 0.5, liquidity, NoPriorCommit, nil, testCommitParams())
	if result.Outcome != commitCommit {
		t.Fatalf("Outcome: got %v, want commitCommit", result.Outcome)
	}
	if result.Direction != -1 {
		t.Errorf("Direction: got %v, want -1", result.Direction)
	}
	if result.EffectiveConviction >= 0.9 {
		t.Errorf("EffectiveConviction: got %v, want < 0.9 (the spread shave must have applied)", result.EffectiveConviction)
	}
	if result.PendingWatch != nil {
		t.Errorf("PendingWatch: got %+v, want nil on COMMIT", result.PendingWatch)
	}
}

func TestCommit_WaiterStateMachine(t *testing.T) {
	traits := testTraits(0.1, true, 3) // needs_confirm=true, 3 confirmations required
	params := testCommitParams()

	// First WAIT: no prior watch creates one with ConfirmsSeen=1.
	first := commit(mixOpinion{Direction: 1, Conviction: 0.9}, traits, 100, 0.5, nil, NoPriorCommit, nil, params)
	if first.Outcome != commitWait {
		t.Fatalf("first: Outcome=%v, want commitWait", first.Outcome)
	}
	if first.PendingWatch == nil || first.PendingWatch.ConfirmsSeen != 1 || first.PendingWatch.Direction != 1 || first.PendingWatch.SinceTick != 100 {
		t.Fatalf("first: PendingWatch=%+v, want {Direction:1 ConfirmsSeen:1 SinceTick:100}", first.PendingWatch)
	}

	// Second WAIT, same direction: increments without resetting SinceTick.
	second := commit(mixOpinion{Direction: 1, Conviction: 0.9}, traits, 110, 0.5, nil, NoPriorCommit, first.PendingWatch, params)
	if second.Outcome != commitWait {
		t.Fatalf("second: Outcome=%v, want commitWait", second.Outcome)
	}
	if second.PendingWatch == nil || second.PendingWatch.ConfirmsSeen != 2 || second.PendingWatch.SinceTick != 100 {
		t.Fatalf("second: PendingWatch=%+v, want {ConfirmsSeen:2 SinceTick:100 (unchanged)}", second.PendingWatch)
	}

	// Third same-direction confirmation reaches confirms_needed=3: COMMIT, watch cleared.
	third := commit(mixOpinion{Direction: 1, Conviction: 0.9}, traits, 120, 0.5, nil, NoPriorCommit, second.PendingWatch, params)
	if third.Outcome != commitCommit {
		t.Fatalf("third: Outcome=%v, want commitCommit", third.Outcome)
	}
	if third.PendingWatch != nil {
		t.Errorf("third: PendingWatch=%+v, want nil on COMMIT", third.PendingWatch)
	}
	if third.Direction != 1 {
		t.Errorf("third: Direction=%v, want 1", third.Direction)
	}
}

func TestCommit_WaiterDirectionFlip_RestartsConfirmCount(t *testing.T) {
	traits := testTraits(0.1, true, 3)
	params := testCommitParams()
	existingWatch := &PendingWatch{Direction: 1, ConfirmsSeen: 2, SinceTick: 50}

	result := commit(mixOpinion{Direction: -1, Conviction: 0.9}, traits, 100, 0.5, nil, NoPriorCommit, existingWatch, params)
	if result.Outcome != commitWait {
		t.Fatalf("Outcome: got %v, want commitWait", result.Outcome)
	}
	if result.PendingWatch == nil {
		t.Fatal("PendingWatch is nil, want a restarted watch")
	}
	if result.PendingWatch.Direction != -1 || result.PendingWatch.ConfirmsSeen != 1 || result.PendingWatch.SinceTick != 100 {
		t.Errorf("PendingWatch: got %+v, want {Direction:-1 ConfirmsSeen:1 SinceTick:100} (restarted, not incremented)", result.PendingWatch)
	}
}

func TestSpreadShaveMultiplier_OffDietOrUnreadable_IsAlwaysNeutral(t *testing.T) {
	params := SpreadShaveParams{SpreadSensitivity: 10, NormalSpread: 0.001, Floor: 0.1}
	wideLiquidity := &LiquidityReading{Spread: 10, ReadOK: true} // would shave hard if consulted

	cases := []struct {
		name    string
		diet    *agent.Diet
		reading *LiquidityReading
	}{
		{"diet nil", nil, wideLiquidity},
		{"OrderBook off", &agent.Diet{OrderBook: false}, wideLiquidity},
		{"liquidity nil", &agent.Diet{OrderBook: true}, nil},
		{"liquidity unreadable", &agent.Diet{OrderBook: true}, &LiquidityReading{Spread: 10, ReadOK: false}},
	}
	for _, testCase := range cases {
		got := spreadShaveMultiplier(testCase.diet, testCase.reading, params)
		if got != 1 {
			t.Errorf("%s: got %v, want 1 (neutral)", testCase.name, got)
		}
	}
}

func TestCooldownShaveMultiplier_NoPriorCommit_IsAlwaysNeutral(t *testing.T) {
	params := CooldownShaveParams{Damp: 0.9, Tau: 1, Floor: 0.1} // would shave hard if consulted
	got := cooldownShaveMultiplier(1_000_000, NoPriorCommit, params)
	if got != 1 {
		t.Errorf("got %v, want 1 (an agent that never committed has nothing to cool down from)", got)
	}
}

// Property test: each shave multiplier, run across a wide sweep of
// inputs, never falls below its own configured floor — a genuinely
// strong view must survive a rough environment, never get
// force-zeroed by one bad condition alone.
func TestShaveMultipliers_NeverBelowTheirOwnFloor(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		floor := rapid.Float64Range(0.01, 0.9).Draw(rt, "floor")

		timeParams := TimeShaveParams{
			OpenDamp:          rapid.Float64Range(0, 2).Draw(rt, "open_damp"),
			CloseDamp:         rapid.Float64Range(0, 2).Draw(rt, "close_damp"),
			OpenRampExponent:  rapid.Float64Range(0.1, 5).Draw(rt, "open_exp"),
			CloseRampExponent: rapid.Float64Range(0.1, 5).Draw(rt, "close_exp"),
			Floor:             floor,
		}
		sessionProgress := rapid.Float64Range(0, 1).Draw(rt, "session_progress")
		if got := timeShaveMultiplier(sessionProgress, timeParams); got < floor {
			rt.Fatalf("timeShaveMultiplier=%v below floor=%v (progress=%v, params=%+v)", got, floor, sessionProgress, timeParams)
		}

		spreadParams := SpreadShaveParams{
			SpreadSensitivity: rapid.Float64Range(0, 100).Draw(rt, "spread_sensitivity"),
			NormalSpread:      rapid.Float64Range(0.0001, 1).Draw(rt, "normal_spread"),
			Floor:             floor,
		}
		liquidity := &LiquidityReading{Spread: rapid.Float64Range(0, 1000).Draw(rt, "spread"), ReadOK: true}
		if got := spreadShaveMultiplier(&agent.Diet{OrderBook: true}, liquidity, spreadParams); got < floor {
			rt.Fatalf("spreadShaveMultiplier=%v below floor=%v (liquidity=%+v, params=%+v)", got, floor, liquidity, spreadParams)
		}

		cooldownParams := CooldownShaveParams{
			Damp:  rapid.Float64Range(0, 2).Draw(rt, "cooldown_damp"),
			Tau:   rapid.Float64Range(0.01, 100).Draw(rt, "cooldown_tau"),
			Floor: floor,
		}
		gap := rapid.Int64Range(0, 10_000).Draw(rt, "gap")
		if got := cooldownShaveMultiplier(gap, 0, cooldownParams); got < floor {
			rt.Fatalf("cooldownShaveMultiplier=%v below floor=%v (gap=%v, params=%+v)", got, floor, gap, cooldownParams)
		}
	})
}

func TestClampFloor(t *testing.T) {
	cases := []struct {
		value, floor, want float64
	}{
		{0.5, 0.3, 0.5},
		{0.1, 0.3, 0.3}, // below floor clamps up to it
		{2.0, 0.3, 1.0}, // above 1 clamps down to it
	}
	for _, testCase := range cases {
		got := clampFloor(testCase.value, testCase.floor)
		if math.Abs(got-testCase.want) > 1e-9 {
			t.Errorf("clampFloor(%v, %v): got %v, want %v", testCase.value, testCase.floor, got, testCase.want)
		}
	}
}
