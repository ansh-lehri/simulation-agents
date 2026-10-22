package decision

import (
	"testing"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/store"
)

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
	intent, ok := Manage(ManageInput{Position: core.Position{Qty: 0}})
	if ok {
		t.Fatalf("zero-qty position: want hold (false), got intent %+v", intent)
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
