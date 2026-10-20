package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ActiveSessionStore writes the sessions:active HASH, which maps
// each live exchange_id to the session parameters the orchestrator
// used to spawn its runner. A restart of the agent process reads
// this hash to decide which per-exchange goroutines to respawn and
// with what params — the record is the one place that durable
// session shape (country, session_type, instruments, time window,
// broker_id, scenario curve) is kept.
//
// Register is LIVE this phase; Deregister (teardown at session end
// or on abort-cleanup) lives in the resilience phase and is not
// declared on this interface yet to keep the surface small.
type ActiveSessionStore interface {
	// Register writes the per-exchange record under the
	// sessions:active HASH. Overwrite semantics: a subsequent
	// Register for the same exchange_id replaces the prior entry
	// (the orchestrator must only Register for an exchange that
	// is coming up fresh).
	Register(ctx context.Context, exchangeID string, record ActiveSessionRecord) error
}

// ActiveSessionRecord is the shape serialized under one hash field
// of sessions:active. The orchestrator fills it from the bring-up
// request at the end of the tail (just before spawning the per-
// exchange runner). The scenario curve rides here so a restarted
// orchestrator rebuilds scenario-shaped lifecycle curves rather
// than baseline ones.
type ActiveSessionRecord struct {
	Country     string    `json:"country"`
	SessionType string    `json:"session_type"`
	Instruments []string  `json:"instruments"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	BrokerID    string    `json:"broker_id"`

	// CurveEff is the scenario-shifted lifecycle-curve parameter
	// set carried into the session's goroutine. nil when the
	// bring-up carried no scenario — the goroutine then builds
	// baseline curves. Defined locally (rather than imported from
	// generator) so this package stays dependency-free of the
	// generator; the orchestrator copies values across at write
	// time.
	CurveEff *CurveEffectiveSnapshot `json:"curve_eff,omitempty"`
}

// CurveEffectiveSnapshot is the shape stored under
// ActiveSessionRecord.CurveEff. Four strictly-positive rates — the
// Log-ruler-inverted values the scenario engine produces for a
// scenario-attached on-demand bring-up. The orchestrator copies
// them from the generator's identically-shaped output at register
// time.
type CurveEffectiveSnapshot struct {
	EntryRate   float64 `json:"entry_rate"`
	StepOutRate float64 `json:"step_out_rate"`
	DoneRate    float64 `json:"done_rate"`
	ReentryRate float64 `json:"reentry_rate"`
}

// RedisActiveSessionStore is the Redis-backed ActiveSessionStore.
type RedisActiveSessionStore struct {
	client *redis.Client
}

// NewRedisActiveSessionStore wraps a go-redis client. The caller
// retains ownership of the client's lifecycle.
func NewRedisActiveSessionStore(client *redis.Client) *RedisActiveSessionStore {
	return &RedisActiveSessionStore{client: client}
}

// Register serializes the record to JSON and HSET-s it onto
// sessions:active under exchange_id. HSET is idempotent at the
// value level — a replay with identical bytes lands the hash in
// the same state.
func (sessionStore *RedisActiveSessionStore) Register(
	ctx context.Context,
	exchangeID string,
	record ActiveSessionRecord,
) error {
	if exchangeID == "" {
		return fmt.Errorf("store: Register active session requires a non-empty exchange id")
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("store: marshal active session record for %s: %w", exchangeID, err)
	}
	if setErr := sessionStore.client.HSet(ctx, SessionsActiveKey(), exchangeID, payload).Err(); setErr != nil {
		return fmt.Errorf("store: HSET sessions:active for %s: %w", exchangeID, setErr)
	}
	return nil
}
