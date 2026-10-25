package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"

	"simulation-agents/agent"
)

// StageRecord is one write-ahead-log entry. It carries just enough to
// re-drive the mint sequence after a crash — the frozen traits that
// were drawn, the population/country for routing, and the exchange the
// agent is bound to. TradingCapital is deliberately NOT staged: on
// recovery it is redrawn from the broker's funding ceiling, so freezing
// the pre-crash draw here would introduce a stale number the recovery
// path has to work around.
type StageRecord struct {
	Traits     agent.TraitVector `json:"traits"`
	Population string            `json:"population"`
	Country    string            `json:"country"`
	ExchangeID string            `json:"exchange_id"`
}

// PendingMintWAL is the crash-recovery write-ahead log for the mint →
// account-open → persist chain. The mint path stages the record BEFORE
// opening the economy account, so a crash in the window where the
// account exists but the durable row does not can later be detected
// (Drain) and reconciled.
//
// Stage and Clear are LIVE in this phase; Drain is declared on the
// interface so the composition root compiles against the full contract
// today, but its execution — the "on startup, re-drive the chain for
// every orphan" step — lives in the crash-recovery phase.
type PendingMintWAL interface {
	// Stage records that a mint is in progress. Must be called before
	// the economy account-open so recovery can tell the account is
	// orphaned if the chain crashes mid-way.
	Stage(ctx context.Context, agentID agent.AgentID, record StageRecord) error

	// Clear removes the staged entry once the durable row is written.
	// Calling Clear for an id that was never staged (or was already
	// cleared) is a no-op and must not error — recovery can call Clear
	// defensively.
	Clear(ctx context.Context, agentID agent.AgentID) error

	// Drain returns every currently staged record. Called at startup
	// before new mint requests are served. Execution deferred to the
	// recovery phase.
	Drain(ctx context.Context) ([]StageRecord, error)
}

// RedisPendingMintWAL is the Redis-backed PendingMintWAL. Entries live
// under a single process-wide hash (PendingMintKey), with each agent
// id as a hash field — one HSET per stage, one HDEL per clear.
type RedisPendingMintWAL struct {
	client *redis.Client
}

// NewRedisPendingMintWAL wraps a go-redis client. The caller retains
// ownership of the client's lifecycle.
func NewRedisPendingMintWAL(client *redis.Client) *RedisPendingMintWAL {
	return &RedisPendingMintWAL{client: client}
}

func (walStore *RedisPendingMintWAL) Stage(ctx context.Context, agentID agent.AgentID, record StageRecord) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("store: marshal stage record for %s: %w", agentID, err)
	}
	if err := walStore.client.HSet(ctx, PendingMintKey(), string(agentID), payload).Err(); err != nil {
		return fmt.Errorf("store: stage pending mint for %s: %w", agentID, err)
	}
	return nil
}

func (walStore *RedisPendingMintWAL) Clear(ctx context.Context, agentID agent.AgentID) error {
	// HDEL on a missing field returns 0 affected and no error — matches
	// the "defensive clear is a no-op" contract.
	if err := walStore.client.HDel(ctx, PendingMintKey(), string(agentID)).Err(); err != nil {
		return fmt.Errorf("store: clear pending mint for %s: %w", agentID, err)
	}
	return nil
}

// ClearBatch removes all staged entries for the given ids in a single
// HDel call (one network roundtrip). Clearing a field that was never
// staged or already cleared is a no-op per the Redis HDEL contract.
func (walStore *RedisPendingMintWAL) ClearBatch(ctx context.Context, agentIDs []agent.AgentID) error {
	if len(agentIDs) == 0 {
		return nil
	}
	fields := make([]string, len(agentIDs))
	for i, agentID := range agentIDs {
		fields[i] = string(agentID)
	}
	if err := walStore.client.HDel(ctx, PendingMintKey(), fields...).Err(); err != nil {
		return fmt.Errorf("store: clear batch pending mints (%d agents): %w", len(agentIDs), err)
	}
	return nil
}

// Drain stub — execution lives in the crash-recovery phase.
func (walStore *RedisPendingMintWAL) Drain(ctx context.Context) ([]StageRecord, error) {
	_ = ctx
	return nil, ErrNotImplementedThisPhase
}
