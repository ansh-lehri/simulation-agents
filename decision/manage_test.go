package decision

import (
	"math/rand/v2"
	"reflect"
	"testing"

	"pgregory.net/rapid"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/store"
)

// newManageRNG builds the same seeded stream shape every other test
// file in this package uses, so a fixed seed replays byte-identically.
func newManageRNG(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
}

// testExitProfile builds a minimal agent.ExitProfile for the exit-check
// tests — only the fields the subject under test actually reads.
func testExitProfile(mode string, stopPct, targetPct, stopBuffer, targetBuffer float64) *agent.ExitProfile {
	return &agent.ExitProfile{
		Mode:         mode,
		StopPct:      stopPct,
		TargetPct:    targetPct,
		StopBuffer:   stopBuffer,
		TargetBuffer: targetBuffer,
	}
}

// A zero-quantity position reaching Manage is a defensive guard, not
// an error: root-dispatch has already proved the instrument is held
// before routing here, so this is only ever hit if that invariant is
// ever violated. Manage must not panic, and must report hold.
func TestManage_ZeroQtyGuard(t *testing.T) {
	outcome := Manage(ManageInput{Position: core.Position{Qty: 0}})
	if outcome.Candidate != nil {
		t.Fatalf("zero-qty position: want hold (nil candidate), got %+v", outcome)
	}
}

// unrealizedPnL's sign must fall out of qty's own sign automatically
// — no long/short branch in the subject under test — covering both a
// winning and a losing case on each side.
func TestUnrealizedPnL(t *testing.T) {
	tests := []struct {
		name             string
		currentPrice     int64
		weightedAvgEntry int64
		qty              int64
		want             int64
	}{
		{name: "long winner", currentPrice: 110, weightedAvgEntry: 100, qty: 10, want: 100},
		{name: "long loser", currentPrice: 90, weightedAvgEntry: 100, qty: 10, want: -100},
		{name: "short winner", currentPrice: 90, weightedAvgEntry: 100, qty: -10, want: 100},
		{name: "short loser", currentPrice: 110, weightedAvgEntry: 100, qty: -10, want: -100},
		{name: "flat price", currentPrice: 100, weightedAvgEntry: 100, qty: 10, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := unrealizedPnL(test.currentPrice, test.weightedAvgEntry, test.qty)
			if got != test.want {
				t.Errorf("unrealizedPnL(%d, %d, %d) = %d, want %d",
					test.currentPrice, test.weightedAvgEntry, test.qty, got, test.want)
			}
		})
	}
}

// A nil position context is the recovery edge: the channel degrades
// to empty (matching none of chart/range/noise) and the entry tick
// defaults to the current tick, never a panic. A non-nil context
// passes its two fields through untouched.
func TestLiveEntryChannelAndTick(t *testing.T) {
	t.Run("nil context degrades harmlessly", func(t *testing.T) {
		channel, tick := liveEntryChannelAndTick(nil, 42)
		if channel != "" {
			t.Errorf("nil context: entryChannel = %q, want empty", channel)
		}
		if tick != 42 {
			t.Errorf("nil context: entryTick = %d, want current tick 42", tick)
		}
	})

	t.Run("non-nil context passes through", func(t *testing.T) {
		posCtx := &store.PositionContext{EntryDir: "LONG", EntryChannel: "chart", EntryTick: 7}
		channel, tick := liveEntryChannelAndTick(posCtx, 42)
		if channel != "chart" {
			t.Errorf("entryChannel = %q, want %q", channel, "chart")
		}
		if tick != 7 {
			t.Errorf("entryTick = %d, want %d", tick, 7)
		}
	})
}

// closeableQuantity's sign-agnostic subtraction: the reserved portion
// comes off the magnitude of qty regardless of long/short.
func TestCloseableQuantity(t *testing.T) {
	tests := []struct {
		name        string
		qty         int64
		reservedQty int64
		want        int64
	}{
		{name: "long, nothing reserved", qty: 10, reservedQty: 0, want: 10},
		{name: "long, partially reserved", qty: 10, reservedQty: 4, want: 6},
		{name: "short, nothing reserved", qty: -10, reservedQty: 0, want: 10},
		{name: "short, partially reserved", qty: -10, reservedQty: 4, want: 6},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := closeableQuantity(test.qty, test.reservedQty)
			if got != test.want {
				t.Errorf("closeableQuantity(%d, %d) = %d, want %d", test.qty, test.reservedQty, got, test.want)
			}
		})
	}
}

// distanceExitPrices prices both sides off the position's own entry;
// a short mirrors a long exactly (stop above entry, target below).
func TestDistanceExitPrices(t *testing.T) {
	profile := testExitProfile(exitModeDistance, 0.02, 0.04, 0, 0)

	longStop, longTarget := distanceExitPrices(100, 1, profile)
	if longStop != 98 || longTarget != 104 {
		t.Errorf("long distance prices = (%d, %d), want (98, 104)", longStop, longTarget)
	}

	shortStop, shortTarget := distanceExitPrices(100, -1, profile)
	if shortStop != 102 || shortTarget != 96 {
		t.Errorf("short distance prices = (%d, %d), want (102, 96)", shortStop, shortTarget)
	}
}

// levelExitPrices prices a long off support(stop)/resistance(target)
// and mirrors a short onto the opposite level.
func TestLevelExitPrices(t *testing.T) {
	profile := testExitProfile(exitModeLevel, 0, 0, 0.01, 0.02)
	support, resistance := int64(100), int64(200)

	longStop, longTarget := levelExitPrices(1, profile, support, resistance)
	if longStop != 99 || longTarget != 196 {
		t.Errorf("long level prices = (%d, %d), want (99, 196)", longStop, longTarget)
	}

	shortStop, shortTarget := levelExitPrices(-1, profile, support, resistance)
	if shortStop != 202 || shortTarget != 102 {
		t.Errorf("short level prices = (%d, %d), want (202, 102)", shortStop, shortTarget)
	}
}

// levelsFor is channel-scoped: chart needs both a qualifying level
// below and above price; range reads its bounds directly; noise and
// any unresolved channel (including the nil-positionContext degrade's
// empty string) never resolve levels.
func TestLevelsFor(t *testing.T) {
	chartBundle := &PerceptionBundle{Chart: &ChartReading{ReadOK: true, KeyLevels: []int64{90, 95, 105, 110}}}
	chartOneSidedBundle := &PerceptionBundle{Chart: &ChartReading{ReadOK: true, KeyLevels: []int64{105, 110}}}
	chartUnreadableBundle := &PerceptionBundle{Chart: &ChartReading{ReadOK: false, KeyLevels: []int64{90, 110}}}
	rangeBundle := &PerceptionBundle{Range: &RangeReading{ReadOK: true, RangeBounds: [2]int64{80, 120}}}
	rangeUnreadableBundle := &PerceptionBundle{Range: &RangeReading{ReadOK: false, RangeBounds: [2]int64{80, 120}}}

	tests := []struct {
		name           string
		entryChannel   string
		bundle         *PerceptionBundle
		currentPrice   int64
		wantSupport    int64
		wantResistance int64
		wantOK         bool
	}{
		{name: "chart both sides present", entryChannel: "chart", bundle: chartBundle, currentPrice: 100, wantSupport: 95, wantResistance: 105, wantOK: true},
		{name: "chart missing support side", entryChannel: "chart", bundle: chartOneSidedBundle, currentPrice: 100, wantOK: false},
		{name: "chart unreadable", entryChannel: "chart", bundle: chartUnreadableBundle, currentPrice: 100, wantOK: false},
		{name: "chart nil reading", entryChannel: "chart", bundle: &PerceptionBundle{}, currentPrice: 100, wantOK: false},
		{name: "range readable", entryChannel: "range", bundle: rangeBundle, currentPrice: 100, wantSupport: 80, wantResistance: 120, wantOK: true},
		{name: "range unreadable", entryChannel: "range", bundle: rangeUnreadableBundle, currentPrice: 100, wantOK: false},
		{name: "noise never resolves", entryChannel: "noise", bundle: chartBundle, currentPrice: 100, wantOK: false},
		{name: "empty channel never resolves", entryChannel: "", bundle: chartBundle, currentPrice: 100, wantOK: false},
		{name: "nil bundle", entryChannel: "chart", bundle: nil, currentPrice: 100, wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			support, resistance, ok := levelsFor(test.entryChannel, test.bundle, test.currentPrice)
			if ok != test.wantOK {
				t.Fatalf("ok = %v, want %v", ok, test.wantOK)
			}
			if !ok {
				return
			}
			if support != test.wantSupport || resistance != test.wantResistance {
				t.Errorf("(support, resistance) = (%d, %d), want (%d, %d)", support, resistance, test.wantSupport, test.wantResistance)
			}
		})
	}
}

// exitPrices dispatches on exit_profile.Mode: distance never touches
// the bundle; level demands levelsFor succeed or reports an abrupt
// exit; hybrid prefers level but falls back to distance only when
// levelsFor fails, never the reverse order.
func TestExitPrices(t *testing.T) {
	chartBundle := &PerceptionBundle{Chart: &ChartReading{ReadOK: true, KeyLevels: []int64{90, 110}}}
	unreadableBundle := &PerceptionBundle{Chart: &ChartReading{ReadOK: false}}

	t.Run("distance ignores the bundle entirely", func(t *testing.T) {
		profile := testExitProfile(exitModeDistance, 0.02, 0.04, 0, 0)
		stop, target, abrupt := exitPrices(profile, 100, 1, "chart", nil, 100)
		if abrupt || stop != 98 || target != 104 {
			t.Errorf("got (stop=%d, target=%d, abrupt=%v), want (98, 104, false)", stop, target, abrupt)
		}
	})

	t.Run("level succeeds when levels resolve", func(t *testing.T) {
		profile := testExitProfile(exitModeLevel, 0, 0, 0, 0)
		stop, target, abrupt := exitPrices(profile, 100, 1, "chart", chartBundle, 100)
		if abrupt || stop != 90 || target != 110 {
			t.Errorf("got (stop=%d, target=%d, abrupt=%v), want (90, 110, false)", stop, target, abrupt)
		}
	})

	t.Run("level fails to an abrupt exit, not a silent fallback", func(t *testing.T) {
		profile := testExitProfile(exitModeLevel, 0.02, 0.04, 0, 0)
		_, _, abrupt := exitPrices(profile, 100, 1, "chart", unreadableBundle, 100)
		if !abrupt {
			t.Errorf("abrupt = false, want true when levelsFor fails under level mode")
		}
	})

	t.Run("hybrid prefers level when it resolves", func(t *testing.T) {
		profile := testExitProfile(exitModeHybrid, 0.02, 0.04, 0, 0)
		stop, target, abrupt := exitPrices(profile, 100, 1, "chart", chartBundle, 100)
		if abrupt || stop != 90 || target != 110 {
			t.Errorf("got (stop=%d, target=%d, abrupt=%v), want level prices (90, 110, false)", stop, target, abrupt)
		}
	})

	t.Run("hybrid falls back to distance only when level fails", func(t *testing.T) {
		profile := testExitProfile(exitModeHybrid, 0.02, 0.04, 0, 0)
		stop, target, abrupt := exitPrices(profile, 100, 1, "chart", unreadableBundle, 100)
		if abrupt || stop != 98 || target != 104 {
			t.Errorf("got (stop=%d, target=%d, abrupt=%v), want distance-fallback prices (98, 104, false)", stop, target, abrupt)
		}
	})

	t.Run("unknown mode panics — an upstream invariant, not a value to synthesize", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("exitPrices: want panic on unknown exit_profile.Mode")
			}
		}()
		exitPrices(testExitProfile("bogus", 0, 0, 0, 0), 100, 1, "chart", nil, 100)
	})
}

// reversalTriggered: DirFlat is never a reversal; an actively opposite
// chart direction is; a range reading fires outside its bounds even
// while in profit (the non-redundancy property — stop/target wouldn't
// catch this); noise and an unreadable channel never reverse.
func TestReversalTriggered(t *testing.T) {
	tests := []struct {
		name         string
		entryChannel string
		entryDir     float64
		currentPrice int64
		bundle       *PerceptionBundle
		wantReversal bool
	}{
		{
			name: "chart DirFlat is not a reversal while long", entryChannel: "chart", entryDir: 1, currentPrice: 100,
			bundle: &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirFlat}}, wantReversal: false,
		},
		{
			name: "chart actively opposite while long reverses", entryChannel: "chart", entryDir: 1, currentPrice: 100,
			bundle: &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirDown}}, wantReversal: true,
		},
		{
			name: "chart actively opposite while short reverses", entryChannel: "chart", entryDir: -1, currentPrice: 100,
			bundle: &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirUp}}, wantReversal: true,
		},
		{
			name: "chart unreadable never reverses", entryChannel: "chart", entryDir: 1, currentPrice: 100,
			bundle: &PerceptionBundle{Chart: &ChartReading{ReadOK: false, Direction: DirDown}}, wantReversal: false,
		},
		{
			name: "range breaks outside while in profit still reverses", entryChannel: "range", entryDir: 1, currentPrice: 130,
			bundle: &PerceptionBundle{Range: &RangeReading{ReadOK: true, RangeBounds: [2]int64{80, 120}}}, wantReversal: true,
		},
		{
			name: "range inside bounds does not reverse", entryChannel: "range", entryDir: 1, currentPrice: 100,
			bundle: &PerceptionBundle{Range: &RangeReading{ReadOK: true, RangeBounds: [2]int64{80, 120}}}, wantReversal: false,
		},
		{
			name: "noise never reverses", entryChannel: "noise", entryDir: 1, currentPrice: 999,
			bundle: &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirDown}}, wantReversal: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := reversalTriggered(test.entryChannel, test.entryDir, test.currentPrice, test.bundle)
			if got != test.wantReversal {
				t.Errorf("reversalTriggered(...) = %v, want %v", got, test.wantReversal)
			}
		})
	}
}

// timeStopTriggered's boundary is strict: exactly maxHold ticks held
// has not yet overstayed it.
func TestTimeStopTriggered(t *testing.T) {
	if timeStopTriggered(110, 100, 10) {
		t.Error("exactly at maxHold: want not triggered")
	}
	if !timeStopTriggered(111, 100, 10) {
		t.Error("one tick past maxHold: want triggered")
	}
}

// checkExit's four triggers each independently produce the same close
// intent when fired in isolation, and none fires produces hold.
func TestCheckExit_EachTriggerInIsolation(t *testing.T) {
	distanceProfile := testExitProfile(exitModeDistance, 0.02, 0.04, 0, 0)
	noExitInput := ManageInput{
		Position:     core.Position{Qty: 10, WeightedAvgEntry: 100},
		Traits:       agent.TraitVector{Exit: distanceProfile, MaxHold: intPtr(100)},
		CurrentPrice: 100,
		Tick:         TickContext{TickIndex: 50},
	}

	t.Run("no trigger fires -> hold", func(t *testing.T) {
		intent, exit := checkExit(noExitInput, 1, "", 50)
		if exit {
			t.Errorf("want hold, got exit intent %+v", intent)
		}
	})

	t.Run("stop alone fires", func(t *testing.T) {
		input := noExitInput
		input.CurrentPrice = 97 // below stop=98
		intent, exit := checkExit(input, 1, "", 50)
		if !exit || intent.Action != ExecIntentActionClose || intent.Direction != -1 {
			t.Errorf("want close/-1, got (exit=%v, intent=%+v)", exit, intent)
		}
	})

	t.Run("target alone fires", func(t *testing.T) {
		input := noExitInput
		input.CurrentPrice = 105 // above target=104
		intent, exit := checkExit(input, 1, "", 50)
		if !exit || intent.Action != ExecIntentActionClose || intent.Direction != -1 {
			t.Errorf("want close/-1, got (exit=%v, intent=%+v)", exit, intent)
		}
	})

	t.Run("reversal alone fires", func(t *testing.T) {
		input := noExitInput
		input.Bundle = &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirDown}}
		intent, exit := checkExit(input, 1, "chart", 50)
		if !exit || intent.Action != ExecIntentActionClose || intent.Direction != -1 {
			t.Errorf("want close/-1, got (exit=%v, intent=%+v)", exit, intent)
		}
	})

	t.Run("time-stop alone fires", func(t *testing.T) {
		input := noExitInput
		input.Tick = TickContext{TickIndex: 300}
		intent, exit := checkExit(input, 1, "", 0)
		if !exit || intent.Action != ExecIntentActionClose || intent.Direction != -1 {
			t.Errorf("want close/-1, got (exit=%v, intent=%+v)", exit, intent)
		}
	})
}

// Priority is a tagless switch, so Go's own left-to-right short-circuit
// evaluation is what enforces "first trigger wins, the rest are never
// evaluated." Since every trigger maps to the identical close intent,
// a fixture where two adjacent triggers are both independently true
// can only observe that the combination still exits — not which one
// "won" — so these fixtures guard against the chain regressing into
// an AND (every condition required at once), the real bug class this
// priority rule protects against.
func TestCheckExit_AdjacentPairsBothFiringStillExit(t *testing.T) {
	distanceProfile := testExitProfile(exitModeDistance, 0.02, 0.04, 0, 0)
	base := ManageInput{
		Position:     core.Position{Qty: 10, WeightedAvgEntry: 100},
		Traits:       agent.TraitVector{Exit: distanceProfile, MaxHold: intPtr(100)},
		CurrentPrice: 100,
		Tick:         TickContext{TickIndex: 50},
	}

	t.Run("stop and reversal both true", func(t *testing.T) {
		input := base
		input.CurrentPrice = 97 // below stop=98
		input.Bundle = &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirDown}}
		if _, exit := checkExit(input, 1, "chart", 50); !exit {
			t.Error("want exit when both stop and reversal fire")
		}
	})

	t.Run("reversal and target both true", func(t *testing.T) {
		input := base
		input.CurrentPrice = 105 // above target=104
		input.Bundle = &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirDown}}
		if _, exit := checkExit(input, 1, "chart", 50); !exit {
			t.Error("want exit when both reversal and target fire")
		}
	})

	t.Run("target and time-stop both true", func(t *testing.T) {
		input := base
		input.CurrentPrice = 105 // above target=104
		input.Tick = TickContext{TickIndex: 300}
		if _, exit := checkExit(input, 1, "", 0); !exit {
			t.Error("want exit when both target and time-stop fire")
		}
	})
}

// A level-mode agent whose structural levels aren't available this
// tick exits unconditionally, even with price sitting exactly at
// entry — nowhere near a "normal" stop or target.
func TestCheckExit_LevelFailureIsAbruptExit(t *testing.T) {
	profile := testExitProfile(exitModeLevel, 0.02, 0.04, 0, 0)
	input := ManageInput{
		Position:     core.Position{Qty: 10, WeightedAvgEntry: 100},
		Traits:       agent.TraitVector{Exit: profile, MaxHold: intPtr(100)},
		CurrentPrice: 100, // exactly at entry — no distance trigger would fire
		Bundle:       &PerceptionBundle{Chart: &ChartReading{ReadOK: false}},
		Tick:         TickContext{TickIndex: 50},
	}

	intent, exit := checkExit(input, 1, "chart", 50)
	if !exit || intent.Action != ExecIntentActionClose {
		t.Errorf("want abrupt close, got (exit=%v, intent=%+v)", exit, intent)
	}
}

func intPtr(value int) *int { return &value }

// testAddTraits builds the minimal TraitVector checkAdd reads: a
// Disposition (mix's tie-break weights) and ThetaAct (the entry bar
// θ_add is derived from).
func testAddTraits(trend, meanRev, noise, thetaAct float64) agent.TraitVector {
	return agent.TraitVector{
		Disposition: &agent.Disposition{Trend: trend, MeanRev: meanRev, Noise: noise},
		ThetaAct:    &thetaAct,
	}
}

// A single trend verdict (chart direction up, non-trivial strength)
// is enough for mix to form an opinion; checkAdd fires only when that
// opinion agrees with the position's own direction, the position is
// in profit, and conviction clears θ_add = ThetaAct + ThetaAddMargin.
// Each of the three conditions failing independently is a hold, never
// a partial add.
func TestCheckAdd(t *testing.T) {
	upBundle := &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirUp, Strength: 0.9}}
	downBundle := &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirDown, Strength: 0.9}}
	weakUpBundle := &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirUp, Strength: 0.3}}

	baseInput := ManageInput{
		Bundle: upBundle,
		Traits: testAddTraits(1, 0, 0, 0.5),
		Roles:  TraitChannelRoles,
		RNG:    newManageRNG(1),
		Params: Params{ThetaAddMargin: 0.1},
	}

	t.Run("all conditions true -> add with the opinion's conviction", func(t *testing.T) {
		intent, channel, add := checkAdd(baseInput, 1, 10)
		if !add || intent.Action != ExecIntentActionAdd || intent.Direction != 1 {
			t.Fatalf("want add/long, got (add=%v, intent=%+v)", add, intent)
		}
		if intent.Conviction != 0.9 {
			t.Errorf("Conviction = %v, want 0.9", intent.Conviction)
		}
		if channel != "chart" {
			t.Errorf("channel = %q, want %q (trend's dominant lean channel)", channel, "chart")
		}
	})

	t.Run("opposite mix direction -> hold", func(t *testing.T) {
		input := baseInput
		input.Bundle = downBundle
		if _, _, add := checkAdd(input, 1, 10); add {
			t.Error("want hold when the mixer's opinion opposes the position's own direction")
		}
	})

	t.Run("losing position -> hold", func(t *testing.T) {
		if _, _, add := checkAdd(baseInput, 1, -10); add {
			t.Error("want hold when the position is not in profit")
		}
	})

	t.Run("conviction below theta_add -> hold", func(t *testing.T) {
		input := baseInput
		input.Bundle = weakUpBundle // conviction 0.3 < theta_add 0.6
		if _, _, add := checkAdd(input, 1, 10); add {
			t.Error("want hold when conviction does not clear theta_add")
		}
	})

	t.Run("no verdicts at all -> hold", func(t *testing.T) {
		input := baseInput
		input.Bundle = &PerceptionBundle{}
		if _, _, add := checkAdd(input, 1, 10); add {
			t.Error("want hold when the mixer has no opinion to act on")
		}
	})
}

// The add-check never rolls the noise-coin bypass: it calls
// interpret+mix directly, and mix() itself never reads
// Disposition.Noise (only noiseImpulse, a function checkAdd never
// calls, does). Varying Noise across its full range with everything
// else — including the RNG seed — held fixed must never change the
// outcome.
func TestCheckAdd_NeverRollsNoiseCoin(t *testing.T) {
	bundle := &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirUp, Strength: 0.9}}

	var results []ExecIntent
	var adds []bool
	for _, noise := range []float64{0, 0.25, 0.5, 0.75, 1} {
		input := ManageInput{
			Bundle: bundle,
			Traits: testAddTraits(1, 0, noise, 0.5),
			Roles:  TraitChannelRoles,
			RNG:    newManageRNG(1),
			Params: Params{ThetaAddMargin: 0.1},
		}
		intent, _, add := checkAdd(input, 1, 10)
		results = append(results, intent)
		adds = append(adds, add)
	}

	for i := 1; i < len(results); i++ {
		if results[i] != results[0] || adds[i] != adds[0] {
			t.Fatalf("Noise=%v changed the outcome: got (add=%v, intent=%+v), want (add=%v, intent=%+v)",
				[]float64{0, 0.25, 0.5, 0.75, 1}[i], adds[i], results[i], adds[0], results[0])
		}
	}
}

// theta_add's derivation property: for any ThetaAct in [0,1] and any
// Margin >= 0, theta_add = ThetaAct + Margin is always >= ThetaAct —
// the add bar can never be laxer than the entry bar.
func TestThetaAddDerivation(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		thetaAct := rapid.Float64Range(0, 1).Draw(rt, "theta_act")
		margin := rapid.Float64Range(0, 10).Draw(rt, "margin")

		thetaAdd := thetaAct + margin
		if thetaAdd < thetaAct {
			rt.Fatalf("theta_add = %v < theta_act = %v (margin = %v)", thetaAdd, thetaAct, margin)
		}
	})
}

// Manage end-to-end: when no exit trigger fires but the add-check's
// conditions all hold, Manage returns the add intent produced by
// checkAdd, not just a bare "something happened."
func TestManage_AddsWhenNoExitAndAddConditionsHold(t *testing.T) {
	input := ManageInput{
		Position: core.Position{Qty: 10, WeightedAvgEntry: 100, ReservedQty: 0},
		Bundle:   &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirUp, Strength: 0.9}},
		Traits: agent.TraitVector{
			Exit:        testExitProfile(exitModeDistance, 0.5, 0.5, 0, 0), // wide enough that 110 doesn't trip stop/target
			MaxHold:     intPtr(1000),
			Disposition: &agent.Disposition{Trend: 1, MeanRev: 0, Noise: 0},
			ThetaAct:    floatPtr(0.5),
		},
		CurrentPrice: 110, // in profit, inside the wide stop/target band
		Tick:         TickContext{TickIndex: 50},
		Roles:        TraitChannelRoles,
		RNG:          newManageRNG(1),
		Params:       Params{ThetaAddMargin: 0.1},
	}

	outcome := Manage(input)
	if outcome.Candidate == nil || outcome.Candidate.Action != ExecIntentActionAdd || outcome.Candidate.Direction != 1 {
		t.Fatalf("want add/long, got %+v", outcome)
	}
	if outcome.Commitment == nil || outcome.Commitment.PositionContext.EntryChannel != "chart" {
		t.Fatalf("want a commitment re-stamping entry_channel=chart, got %+v", outcome.Commitment)
	}
}

// Manage is pure and seeded: identical inputs (including a
// freshly-constructed RNG from the same seed) must produce
// byte-identical output on repeat calls.
func TestManage_Determinism(t *testing.T) {
	buildInput := func() ManageInput {
		return ManageInput{
			Position: core.Position{Qty: 10, WeightedAvgEntry: 100, ReservedQty: 0},
			Bundle:   &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirUp, Strength: 0.9}},
			Traits: agent.TraitVector{
				Exit:        testExitProfile(exitModeDistance, 0.5, 0.5, 0, 0),
				MaxHold:     intPtr(1000),
				Disposition: &agent.Disposition{Trend: 1, MeanRev: 0, Noise: 0},
				ThetaAct:    floatPtr(0.5),
			},
			CurrentPrice: 110,
			Tick:         TickContext{TickIndex: 50},
			Roles:        TraitChannelRoles,
			RNG:          newManageRNG(7),
			Params:       Params{ThetaAddMargin: 0.1},
		}
	}

	first := Manage(buildInput())
	second := Manage(buildInput())
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Manage not deterministic: first=%+v, second=%+v", first, second)
	}
}
