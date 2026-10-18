package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"simulation-agents/agent"
	"simulation-agents/store"
)

func makeStageRecord(country, exchangeID string) store.StageRecord {
	actProb := 0.4
	return store.StageRecord{
		Traits: agent.TraitVector{
			AgentKind:      agent.AgentKindHerd,
			Skill:          0.3,
			Aggressiveness: 0.6,
			CapitalSize:    40_000,
			PnlReactivity:  0.3,
			PAct:           0.2,
			ActProb:        &actProb,
		},
		Population: agent.PopulationOnDemand,
		Country:    country,
		ExchangeID: exchangeID,
	}
}

// Stage must write the record under the agent's hash field — recovery
// finds orphaned entries by iterating the hash's fields, so each entry
// is one HSET field, not a serialized blob of the whole log.
func TestStage_WritesUnderAgentField(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	walStore := store.NewRedisPendingMintWAL(client)

	record := makeStageRecord("US", "ex-1")
	if err := walStore.Stage(ctx, "a-new", record); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	payload, err := client.HGet(ctx, store.PendingMintKey(), "a-new").Bytes()
	if err != nil {
		t.Fatalf("HGet: %v", err)
	}
	var decoded store.StageRecord
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Country != record.Country || decoded.ExchangeID != record.ExchangeID {
		t.Errorf("staged envelope mismatch: got %+v want %+v", decoded, record)
	}
	if decoded.Traits.AgentKind != agent.AgentKindHerd {
		t.Errorf("staged traits kind: got %q want %q", decoded.Traits.AgentKind, agent.AgentKindHerd)
	}
	if decoded.Traits.ActProb == nil || *decoded.Traits.ActProb != 0.4 {
		t.Errorf("staged act_prob should round-trip 0.4: got %v", decoded.Traits.ActProb)
	}
}

// Multiple concurrent mints share one process-wide WAL key but live
// under distinct hash fields — one agent's clear must not touch another.
func TestStage_MultipleEntriesCoexist(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	walStore := store.NewRedisPendingMintWAL(client)

	first := makeStageRecord("US", "ex-1")
	second := makeStageRecord("FR", "ex-2")
	if err := walStore.Stage(ctx, "a-1", first); err != nil {
		t.Fatalf("Stage first: %v", err)
	}
	if err := walStore.Stage(ctx, "a-2", second); err != nil {
		t.Fatalf("Stage second: %v", err)
	}

	count, err := client.HLen(ctx, store.PendingMintKey()).Result()
	if err != nil {
		t.Fatalf("HLen: %v", err)
	}
	if count != 2 {
		t.Fatalf("hash length: got %d want 2", count)
	}
}

// Clear removes the field so recovery no longer sees the entry.
func TestClear_RemovesStagedEntry(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	walStore := store.NewRedisPendingMintWAL(client)

	if err := walStore.Stage(ctx, "a-done", makeStageRecord("US", "ex-1")); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := walStore.Clear(ctx, "a-done"); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	exists, err := client.HExists(ctx, store.PendingMintKey(), "a-done").Result()
	if err != nil {
		t.Fatalf("HExists: %v", err)
	}
	if exists {
		t.Errorf("field still present after Clear")
	}
}

// Clearing an id that was never staged (or was already cleared) is a
// defensive no-op — recovery and retry paths must be safe to call Clear
// without first checking.
func TestClear_UnstagedIsNoOp(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	walStore := store.NewRedisPendingMintWAL(client)

	if err := walStore.Clear(ctx, "a-nobody"); err != nil {
		t.Errorf("Clear on missing field returned error: %v", err)
	}
}

// Clear on one entry must leave the others intact.
func TestClear_IsPerAgent(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	walStore := store.NewRedisPendingMintWAL(client)

	if err := walStore.Stage(ctx, "a-1", makeStageRecord("US", "ex-1")); err != nil {
		t.Fatalf("Stage a-1: %v", err)
	}
	if err := walStore.Stage(ctx, "a-2", makeStageRecord("US", "ex-1")); err != nil {
		t.Fatalf("Stage a-2: %v", err)
	}
	if err := walStore.Clear(ctx, "a-1"); err != nil {
		t.Fatalf("Clear a-1: %v", err)
	}

	stillStaged, err := client.HExists(ctx, store.PendingMintKey(), "a-2").Result()
	if err != nil {
		t.Fatalf("HExists a-2: %v", err)
	}
	if !stillStaged {
		t.Error("Clear leaked across hash fields — a-2 lost")
	}
}

// Drain is declared on the interface but its execution lives in the
// crash-recovery phase. Calling it must return the sentinel so a caller
// cannot mistake a stub for a working implementation.
func TestDrain_ReturnsStubSentinel(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	walStore := store.NewRedisPendingMintWAL(client)

	records, err := walStore.Drain(ctx)
	if records != nil {
		t.Errorf("Drain records: got %+v want nil", records)
	}
	if !errors.Is(err, store.ErrNotImplementedThisPhase) {
		t.Errorf("Drain err: got %v want ErrNotImplementedThisPhase", err)
	}
}
