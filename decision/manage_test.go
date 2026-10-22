package decision

import (
	"testing"

	"simulation-agents/core"
	"simulation-agents/store"
)

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
