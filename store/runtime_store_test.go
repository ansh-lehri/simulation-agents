package store_test

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"testing"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// WriteInitialSnapshot must leave the per-agent hash with exactly two
// fields: presence (DORMANT) and trading_capital. Any other field here
// would mean the thin-by-design seam between the orchestrator and the
// goroutine has been punctured.
func TestWriteInitialSnapshot_WritesExactlyPresenceAndCapital(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	agentRecord := agent.Agent{
		ID:             "a-1",
		Population:     agent.PopulationOnDemand,
		Country:        "US",
		TradingCapital: 12345,
	}
	if err := runtimeStore.WriteInitialSnapshot(ctx, "ex-1", agentRecord); err != nil {
		t.Fatalf("WriteInitialSnapshot: %v", err)
	}

	key := store.RuntimeKey("ex-1", agentRecord.ID)
	gotKeys, err := client.HKeys(ctx, key).Result()
	if err != nil {
		t.Fatalf("HKeys: %v", err)
	}
	sort.Strings(gotKeys)
	wantKeys := []string{store.FieldPresence, store.FieldTradingCapital}
	sort.Strings(wantKeys)
	if !equalStringSlices(gotKeys, wantKeys) {
		t.Fatalf("initial snapshot fields: got %v want %v", gotKeys, wantKeys)
	}

	presence, err := client.HGet(ctx, key, store.FieldPresence).Result()
	if err != nil {
		t.Fatalf("HGet presence: %v", err)
	}
	if presence != store.PresenceDormant {
		t.Errorf("initial presence: got %q want %q", presence, store.PresenceDormant)
	}

	tradingCapital, err := client.HGet(ctx, key, store.FieldTradingCapital).Result()
	if err != nil {
		t.Fatalf("HGet trading_capital: %v", err)
	}
	if tradingCapital != "12345" {
		t.Errorf("initial trading_capital: got %q want %q", tradingCapital, "12345")
	}
}

func TestSetPresence_RoundTrip(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	if err := runtimeStore.SetPresence(ctx, "ex-1", "a-1", store.PresenceActive); err != nil {
		t.Fatalf("SetPresence: %v", err)
	}
	got, err := client.HGet(ctx, store.RuntimeKey("ex-1", "a-1"), store.FieldPresence).Result()
	if err != nil {
		t.Fatalf("HGet: %v", err)
	}
	if got != store.PresenceActive {
		t.Errorf("presence after SetPresence: got %q want %q", got, store.PresenceActive)
	}
}

func TestSetTimers_WritesBothFieldsAtomically(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	const (
		nextCheck = int64(1_700_000_000)
		idleSince = int64(1_699_990_000)
	)
	if err := runtimeStore.SetTimers(ctx, "ex-1", "a-1", nextCheck, idleSince); err != nil {
		t.Fatalf("SetTimers: %v", err)
	}

	key := store.RuntimeKey("ex-1", "a-1")
	gotNext, err := client.HGet(ctx, key, store.FieldNextCheckTime).Result()
	if err != nil {
		t.Fatalf("HGet next_check_time: %v", err)
	}
	if gotNext != "1700000000" {
		t.Errorf("next_check_time: got %q want %q", gotNext, "1700000000")
	}
	gotIdle, err := client.HGet(ctx, key, store.FieldIdleSince).Result()
	if err != nil {
		t.Fatalf("HGet idle_since: %v", err)
	}
	if gotIdle != "1699990000" {
		t.Errorf("idle_since: got %q want %q", gotIdle, "1699990000")
	}
}

func TestStampEntryTime_RoundTrip(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	if err := runtimeStore.StampEntryTime(ctx, "ex-1", "a-1", 0.4275); err != nil {
		t.Fatalf("StampEntryTime: %v", err)
	}
	got, err := client.HGet(ctx, store.RuntimeKey("ex-1", "a-1"), store.FieldEntryTime).Result()
	if err != nil {
		t.Fatalf("HGet: %v", err)
	}
	if got != "0.4275" {
		t.Errorf("entry_time: got %q want %q", got, "0.4275")
	}
}

func TestSetTipper_WritesTrueFlag(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	if err := runtimeStore.SetTipper(ctx, "ex-1", "a-1"); err != nil {
		t.Fatalf("SetTipper: %v", err)
	}
	got, err := client.HGet(ctx, store.RuntimeKey("ex-1", "a-1"), store.FieldIsTipper).Result()
	if err != nil {
		t.Fatalf("HGet: %v", err)
	}
	if got != store.IsTipperTrue {
		t.Errorf("is_tipper: got %q want %q", got, store.IsTipperTrue)
	}
}

// Field-level setters on different (exchange, agent) pairs must not
// collide — their keys are independent namespaces.
func TestRuntimeStore_KeysAreNamespaced(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	if err := runtimeStore.SetPresence(ctx, "ex-1", "a-1", store.PresenceActive); err != nil {
		t.Fatalf("SetPresence ex-1: %v", err)
	}
	if err := runtimeStore.SetPresence(ctx, "ex-2", "a-1", store.PresenceIdle); err != nil {
		t.Fatalf("SetPresence ex-2: %v", err)
	}

	first, err := client.HGet(ctx, store.RuntimeKey("ex-1", "a-1"), store.FieldPresence).Result()
	if err != nil {
		t.Fatalf("HGet ex-1: %v", err)
	}
	second, err := client.HGet(ctx, store.RuntimeKey("ex-2", "a-1"), store.FieldPresence).Result()
	if err != nil {
		t.Fatalf("HGet ex-2: %v", err)
	}
	if first != store.PresenceActive {
		t.Errorf("ex-1 presence: got %q want %q", first, store.PresenceActive)
	}
	if second != store.PresenceIdle {
		t.Errorf("ex-2 presence: got %q want %q", second, store.PresenceIdle)
	}
}

// SetStandingOpinion writes both the opinion (as a shortest-round-trip
// float string) and the stance to their dedicated hash fields.
func TestSetStandingOpinion_RoundTrip(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	if err := runtimeStore.SetStandingOpinion(ctx, "ex-1", "a-1", 0.75, "TREND_LONG"); err != nil {
		t.Fatalf("SetStandingOpinion: %v", err)
	}

	key := store.RuntimeKey("ex-1", "a-1")
	gotOpinion, err := client.HGet(ctx, key, store.FieldStandingOpinion).Result()
	if err != nil {
		t.Fatalf("HGet standing_opinion: %v", err)
	}
	// FormatFloat with 'f', -1 uses the shortest round-trippable form.
	if gotOpinion != strconv.FormatFloat(0.75, 'f', -1, 64) {
		t.Errorf("standing_opinion: got %q want %q", gotOpinion, strconv.FormatFloat(0.75, 'f', -1, 64))
	}
	gotStance, err := client.HGet(ctx, key, store.FieldStandingStance).Result()
	if err != nil {
		t.Fatalf("HGet standing_stance: %v", err)
	}
	if gotStance != "TREND_LONG" {
		t.Errorf("standing_stance: got %q want %q", gotStance, "TREND_LONG")
	}
}

// SetWaitStreak writes the streak count as a decimal string.
func TestSetWaitStreak_RoundTrip(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	if err := runtimeStore.SetWaitStreak(ctx, "ex-1", "a-1", 7); err != nil {
		t.Fatalf("SetWaitStreak: %v", err)
	}

	got, err := client.HGet(ctx, store.RuntimeKey("ex-1", "a-1"), store.FieldWaitStreak).Result()
	if err != nil {
		t.Fatalf("HGet wait_streak: %v", err)
	}
	if got != "7" {
		t.Errorf("wait_streak: got %q want %q", got, "7")
	}
}

// SetPositionContext with a non-nil context stores JSON-encoded entry
// conditions and the decoded struct matches what was written.
func TestSetPositionContext_NonNilRoundTrip(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	posCtx := &store.PositionContext{
		EntryDir:     "LONG",
		EntryChannel: "technical",
		EntryTick:    1_700_000_100,
	}
	if err := runtimeStore.SetPositionContext(ctx, "ex-1", "a-1", posCtx); err != nil {
		t.Fatalf("SetPositionContext: %v", err)
	}

	raw, err := client.HGet(ctx, store.RuntimeKey("ex-1", "a-1"), store.FieldPositionContext).Result()
	if err != nil {
		t.Fatalf("HGet position_context: %v", err)
	}
	var decoded store.PositionContext
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal position_context: %v", err)
	}
	if decoded != *posCtx {
		t.Errorf("position_context round-trip: got %+v want %+v", decoded, *posCtx)
	}
}

// SetPositionContext with nil writes an empty string — signals the
// agent is currently flat with no open position.
func TestSetPositionContext_NilClearsField(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	if err := runtimeStore.SetPositionContext(ctx, "ex-1", "a-1", nil); err != nil {
		t.Fatalf("SetPositionContext nil: %v", err)
	}

	got, err := client.HGet(ctx, store.RuntimeKey("ex-1", "a-1"), store.FieldPositionContext).Result()
	if err != nil {
		t.Fatalf("HGet position_context: %v", err)
	}
	if got != "" {
		t.Errorf("nil position_context: expected empty string, got %q", got)
	}
}

// SetTipFollowState writes all four tip-following fields atomically;
// the tracking struct round-trips through JSON correctly.
func TestSetTipFollowState_RoundTrip(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	tracking := &store.Tracking{
		TrackingTipID: "tip-42",
		Target:        10500,
		NoticeAt:      1_700_000_050,
	}
	if err := runtimeStore.SetTipFollowState(ctx, "ex-1", "a-1", tracking, "tip-41", 10300, 1_700_000_100); err != nil {
		t.Fatalf("SetTipFollowState: %v", err)
	}

	key := store.RuntimeKey("ex-1", "a-1")

	rawTracking, err := client.HGet(ctx, key, store.FieldTracking).Result()
	if err != nil {
		t.Fatalf("HGet tracking: %v", err)
	}
	var decodedTracking store.Tracking
	if err := json.Unmarshal([]byte(rawTracking), &decodedTracking); err != nil {
		t.Fatalf("unmarshal tracking: %v", err)
	}
	if decodedTracking != *tracking {
		t.Errorf("tracking round-trip: got %+v want %+v", decodedTracking, *tracking)
	}

	gotTipID, err := client.HGet(ctx, key, store.FieldLastActedTipID).Result()
	if err != nil {
		t.Fatalf("HGet last_acted_tip_id: %v", err)
	}
	if gotTipID != "tip-41" {
		t.Errorf("last_acted_tip_id: got %q want %q", gotTipID, "tip-41")
	}

	gotExit, err := client.HGet(ctx, key, store.FieldMyExitLevel).Result()
	if err != nil {
		t.Fatalf("HGet my_exit_level: %v", err)
	}
	if gotExit != "10300" {
		t.Errorf("my_exit_level: got %q want %q", gotExit, "10300")
	}

	gotSubmit, err := client.HGet(ctx, key, store.FieldSubmitAt).Result()
	if err != nil {
		t.Fatalf("HGet submit_at: %v", err)
	}
	if gotSubmit != "1700000100" {
		t.Errorf("submit_at: got %q want %q", gotSubmit, "1700000100")
	}
}

// SetTipFollowState with a nil tracking writes an empty string for the
// tracking field while still writing the remaining fields.
func TestSetTipFollowState_NilTrackingClearsTracking(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	if err := runtimeStore.SetTipFollowState(ctx, "ex-1", "a-1", nil, "tip-41", 10300, 1_700_000_100); err != nil {
		t.Fatalf("SetTipFollowState nil tracking: %v", err)
	}

	got, err := client.HGet(ctx, store.RuntimeKey("ex-1", "a-1"), store.FieldTracking).Result()
	if err != nil {
		t.Fatalf("HGet tracking: %v", err)
	}
	if got != "" {
		t.Errorf("nil tracking: expected empty string, got %q", got)
	}
}

// SetPerceptionRefs writes all five gate-ref fields with the correct
// field names and correctly encoded values.
func TestSetPerceptionRefs_RoundTrip(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	runtimeStore := store.NewRedisRuntimeStore(client)

	refs := store.PerceptionRefs{
		LastPrice:    10250,
		LastSpread:   5,
		LastVolume:   3400,
		LastRangePos: 0.6125,
		LastPhase:    "open",
	}
	if err := runtimeStore.SetPerceptionRefs(ctx, "ex-1", "a-1", refs); err != nil {
		t.Fatalf("SetPerceptionRefs: %v", err)
	}

	key := store.RuntimeKey("ex-1", "a-1")
	cases := []struct {
		field string
		want  string
	}{
		{store.FieldGateRefPrice, "10250"},
		{store.FieldGateRefSpread, "5"},
		{store.FieldGateRefVolume, "3400"},
		{store.FieldGateRefRangePos, strconv.FormatFloat(0.6125, 'f', -1, 64)},
		{store.FieldGateRefPhase, "open"},
	}
	for _, tc := range cases {
		got, err := client.HGet(ctx, key, tc.field).Result()
		if err != nil {
			t.Fatalf("HGet %s: %v", tc.field, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.field, got, tc.want)
		}
	}
}

func equalStringSlices(firstSlice, secondSlice []string) bool {
	if len(firstSlice) != len(secondSlice) {
		return false
	}
	for i := range firstSlice {
		if firstSlice[i] != secondSlice[i] {
			return false
		}
	}
	return true
}
