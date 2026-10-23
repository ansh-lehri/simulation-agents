package store_test

import (
	"context"
	"testing"
	"time"

	"simulation-agents/store"
)

func sampleActiveSessionRecord() store.ActiveSessionRecord {
	return store.ActiveSessionRecord{
		Country:     "US",
		SessionType: "canonical",
		Instruments: []string{"stock1"},
		Start:       time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC),
		End:         time.Date(2026, 10, 20, 17, 0, 0, 0, time.UTC),
		BrokerID:    "broker-A",
	}
}

// Register writes the record under sessions:active, keyed by exchange id.
func TestRegister_WritesUnderSessionsActive(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	activeSessionStore := store.NewRedisActiveSessionStore(client)

	if err := activeSessionStore.Register(ctx, "ex-1", sampleActiveSessionRecord()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	exists, err := client.HExists(ctx, store.SessionsActiveKey(), "ex-1").Result()
	if err != nil {
		t.Fatalf("HExists: %v", err)
	}
	if !exists {
		t.Fatal("expected sessions:active to carry an entry for ex-1 after Register")
	}
}

// Deregister removes the exchange's entry; other exchanges' entries
// are untouched.
func TestDeregister_RemovesOnlyTheGivenExchangeEntry(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	activeSessionStore := store.NewRedisActiveSessionStore(client)

	record := sampleActiveSessionRecord()
	if err := activeSessionStore.Register(ctx, "ex-1", record); err != nil {
		t.Fatalf("Register ex-1: %v", err)
	}
	if err := activeSessionStore.Register(ctx, "ex-2", record); err != nil {
		t.Fatalf("Register ex-2: %v", err)
	}

	if err := activeSessionStore.Deregister(ctx, "ex-1"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	ex1Exists, err := client.HExists(ctx, store.SessionsActiveKey(), "ex-1").Result()
	if err != nil {
		t.Fatalf("HExists ex-1: %v", err)
	}
	if ex1Exists {
		t.Error("ex-1 entry still present in sessions:active after Deregister")
	}
	ex2Exists, err := client.HExists(ctx, store.SessionsActiveKey(), "ex-2").Result()
	if err != nil {
		t.Fatalf("HExists ex-2: %v", err)
	}
	if !ex2Exists {
		t.Error("Deregister(ex-1) must not remove ex-2's entry")
	}
}

// Deregistering an exchange with no entry is a silent no-op.
func TestDeregister_NoMatchingEntryIsSilentNoOp(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	activeSessionStore := store.NewRedisActiveSessionStore(client)

	if err := activeSessionStore.Deregister(ctx, "ex-never-registered"); err != nil {
		t.Fatalf("Deregister on missing entry: %v", err)
	}
}
