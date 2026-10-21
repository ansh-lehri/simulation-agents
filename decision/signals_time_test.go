package decision

import (
	"testing"
	"time"

	"simulation-agents/store"
)

func TestPerceiveTime_Present(t *testing.T) {
	now := time.Now()
	start := now.Add(-30 * time.Minute)
	end := now.Add(30 * time.Minute)

	acc := NewAccessor(
		map[string]map[string][]store.Candle{},
		map[string]store.Quote{
			"AAPL": {Phase: "open", HasLast: false},
		},
		map[string]bool{"AAPL": true},
		TickContext{Now: now, SessionStart: start, SessionEnd: end},
	)

	got := perceiveTime(acc, "AAPL")
	if !got.ReadOK {
		t.Fatal("ReadOK must be true when instrument is present")
	}
	if got.Phase != "open" {
		t.Errorf("Phase: want %q, got %q", "open", got.Phase)
	}
	// Progress should be ~0.5 (half-way through session).
	if got.SessionProgress < 0.45 || got.SessionProgress > 0.55 {
		t.Errorf("SessionProgress expected near 0.5, got %v", got.SessionProgress)
	}
}

func TestPerceiveTime_Absent(t *testing.T) {
	acc := NewAccessor(
		map[string]map[string][]store.Candle{},
		map[string]store.Quote{},
		map[string]bool{"AAPL": false},
		TickContext{},
	)
	got := perceiveTime(acc, "AAPL")
	if got.ReadOK {
		t.Fatal("ReadOK must be false when instrument is absent")
	}
}

func TestPerceiveTime_SessionStartAndEnd(t *testing.T) {
	start := time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 1, 17, 0, 0, 0, time.UTC)

	checkProgress := func(name string, now time.Time, wantApprox float64) {
		t.Helper()
		acc := NewAccessor(
			map[string]map[string][]store.Candle{},
			map[string]store.Quote{"X": {}},
			map[string]bool{"X": true},
			TickContext{Now: now, SessionStart: start, SessionEnd: end},
		)
		got := perceiveTime(acc, "X").SessionProgress
		if got < wantApprox-0.01 || got > wantApprox+0.01 {
			t.Errorf("%s: want ~%.2f, got %.4f", name, wantApprox, got)
		}
	}

	checkProgress("at start", start, 0.0)
	checkProgress("at end", end, 1.0)
	checkProgress("past end", end.Add(time.Hour), 1.0)
	checkProgress("before start", start.Add(-time.Minute), 0.0)
}
