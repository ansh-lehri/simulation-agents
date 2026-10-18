package store_test

import (
	"context"
	"sort"
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
