package decision

import "testing"

func testGateParams(threshold int) GateParams {
	return GateParams{WaitReassessThreshold: threshold}
}

func testSensitivities() map[string]float64 {
	return map[string]float64{
		"chart.direction":         0.1,
		"chart.strength":          0.1,
		"range.position_in_range": 0.1,
		"range.stretch":           0.1,
	}
}

func baselineReadings() map[string]float64 {
	return map[string]float64{
		"chart.direction":         0,
		"chart.strength":          0,
		"range.position_in_range": 0,
		"range.stretch":           0,
	}
}

// Flat-idle: each consumed field independently trips REDECIDE when its
// own delta exceeds its own threshold, with every other field held
// exactly at the last-decided baseline — proving no cross-field
// leakage (a chart.direction move must not be able to trip the gate
// via, say, range.stretch's threshold).
func TestGateDecide_FlatIdle_EachFieldIndependentlyTripsRedecide(t *testing.T) {
	for _, movedField := range []string{"chart.direction", "chart.strength", "range.position_in_range", "range.stretch"} {
		current := baselineReadings()
		current[movedField] = 0.2 // delta 0.2 > threshold 0.1

		got := gateDecide(nil, 0, current, baselineReadings(), testSensitivities(), testGateParams(5))
		if got != gateRedecide {
			t.Errorf("moving %s past its threshold: got %v, want gateRedecide", movedField, got)
		}
	}
}

func TestGateDecide_FlatIdle_AllFieldsUnderThreshold_Coasts(t *testing.T) {
	current := baselineReadings()
	for field := range current {
		current[field] = 0.05 // delta 0.05 < threshold 0.1, every field
	}

	got := gateDecide(nil, 0, current, baselineReadings(), testSensitivities(), testGateParams(5))
	if got != gateCoast {
		t.Errorf("all fields under threshold: got %v, want gateCoast", got)
	}
}

// First-ever decide for this instrument (no decided baseline exists
// yet) must always redecide, regardless of how small the readings
// are or whether any sensitivity would otherwise trip.
func TestGateDecide_FlatIdle_EmptyLastDecidedReadings_AlwaysRedecides(t *testing.T) {
	current := baselineReadings()
	got := gateDecide(nil, 0, current, nil, testSensitivities(), testGateParams(5))
	if got != gateRedecide {
		t.Errorf("empty lastDecidedReadings: got %v, want gateRedecide", got)
	}
}

func TestGateDecide_FlatIdle_NonPositiveThreshold_NeverTrips(t *testing.T) {
	current := baselineReadings()
	current["chart.direction"] = 100 // huge delta
	sensitivities := map[string]float64{"chart.direction": 0}

	got := gateDecide(nil, 0, current, baselineReadings(), sensitivities, testGateParams(5))
	if got != gateCoast {
		t.Errorf("zero threshold with huge delta: got %v, want gateCoast (zero threshold disables the field)", got)
	}
}

func TestGateDecide_FlatIdle_FieldAbsentThisTick_NotTreatedAsTripped(t *testing.T) {
	// chart.direction has a sensitivity entry and a last-decided value,
	// but no current reading this tick (its channel went unreadable).
	current := map[string]float64{"chart.strength": 0}
	lastDecided := map[string]float64{"chart.direction": 0, "chart.strength": 0}

	got := gateDecide(nil, 0, current, lastDecided, testSensitivities(), testGateParams(5))
	if got != gateCoast {
		t.Errorf("missing current reading for a sensitive field: got %v, want gateCoast", got)
	}
}

// Waiter: coasts (and the caller is expected to increment waitStreak)
// while waitStreak stays at or below the threshold, and is forced to
// redecide only once it strictly exceeds it — the quiet-confirm fix.
func TestGateDecide_Waiter_CoastsUntilPastThreshold(t *testing.T) {
	pendingWatch := &PendingWatch{Direction: 1, ConfirmsSeen: 1, SinceTick: 10}

	cases := []struct {
		waitStreak int
		want       gateOutcome
	}{
		{0, gateCoast},
		{4, gateCoast},
		{5, gateCoast},    // exactly at the threshold — must still coast, ">" is strict
		{6, gateRedecide}, // one past the threshold — forced reassess
		{50, gateRedecide},
	}
	for _, testCase := range cases {
		got := gateDecide(pendingWatch, testCase.waitStreak, nil, nil, nil, testGateParams(5))
		if got != testCase.want {
			t.Errorf("waitStreak=%d: got %v, want %v", testCase.waitStreak, got, testCase.want)
		}
	}
}

// A waiter's branch must never consult currentReadings, lastDecidedReadings,
// or sensitivities at all — its gate is purely waitStreak-vs-threshold.
// Passing readings that would obviously trip the flat-idle branch must
// have no effect while pendingWatch is set.
func TestGateDecide_Waiter_IgnoresReadingsEntirely(t *testing.T) {
	pendingWatch := &PendingWatch{Direction: -1, ConfirmsSeen: 2, SinceTick: 3}
	current := map[string]float64{"chart.direction": 999}
	lastDecided := map[string]float64{"chart.direction": 0}

	got := gateDecide(pendingWatch, 0, current, lastDecided, testSensitivities(), testGateParams(5))
	if got != gateCoast {
		t.Errorf("waiter with wildly moved readings: got %v, want gateCoast (readings must be ignored while waiting)", got)
	}
}
