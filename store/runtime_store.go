package store

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"

	"simulation-agents/agent"
)

// RuntimeSnapshot is the shape of the per-agent runtime state the Redis
// hash backs. This file writes only the identity + presence fields
// (initial snapshot) + the four field-level setters below; the decision
// / position / tip-following working-memory fields are declared here so
// the type is complete, but their writers live in a downstream stage.
//
// Never carries frozen traits (those live in Postgres) and never carries
// positions / cash / P&L (those are broker-owned, re-read fresh each use).
type RuntimeSnapshot struct {
	TradingCapital int64   // session money A; sole home for on-demand, convenience copy for canonical
	IsTipper       bool    // on-demand per-session tipper flag; canonical's tipper flag lives in Postgres traits
	Presence       string  // one of PresenceDormant / Active / Idle / Done / Retired
	EntryTime      float64 // normalized position in [0,1] over [session_start, session_end]; one-shot DORMANT→ACTIVE trigger
	NextCheckTime  int64   // staggered lifecycle re-check timestamp
	IdleSince      int64   // start of the current IDLE run, for the re-entry roll

	// Decision working memory (endogenous agents). Written by a downstream stage.
	StandingOpinion     float64
	StandingStance      string
	WaitStreak          int
	LastDecidedReadings map[string]float64

	// Position / exit working memory (while an endogenous agent is holding).
	PositionContext *PositionContext

	// Tip-following state (herd agents).
	LastActedTipID string
	Tracking       *Tracking
	MyExitLevel    int64
	SubmitAt       int64
}

// PositionContext records the entry conditions of an open position so the
// exit-management logic can reference them later. Written by whoever
// opens or adds to the position; read by the exit logic.
type PositionContext struct {
	EntryDir     string
	EntryChannel string
	EntryTick    int64
}

// Tracking captures the one tip currently being "noticed" by a herd
// agent — the tip id, the price target it carries, and the tick the
// agent will act on if its noticing delay elapses.
type Tracking struct {
	TrackingTipID string
	Target        int64
	NoticeAt      int64
}

// RuntimeStore writes the per-session Redis state. All methods are
// write-on-change: a caller only invokes them when its decision changed
// the stored value. The reader side (one-shot "load snapshot and
// reconcile" after a process restart) lives in a downstream stage.
type RuntimeStore interface {
	// WriteInitialSnapshot writes the two fields a freshly provisioned
	// agent needs to exist as a runtime entity: its presence (DORMANT, so
	// the lifecycle writer picks it up on its next tick) and its session
	// trading capital. Nothing else is written here — entry_time, timers,
	// and all working-memory fields are stamped later by the goroutine.
	WriteInitialSnapshot(ctx context.Context, exchangeID string, agentRecord agent.Agent) error

	// SetPresence updates the presence field. The lifecycle writer calls
	// this on every presence transition.
	SetPresence(ctx context.Context, exchangeID string, agentID agent.AgentID, presence string) error

	// SetTimers writes the next-check and idle-since timestamps together
	// since they are the paired output of a single lifecycle decision.
	SetTimers(ctx context.Context, exchangeID string, agentID agent.AgentID, nextCheckTime, idleSince int64) error

	// StampEntryTime writes the normalized DORMANT→ACTIVE trigger point.
	// Called exactly once per agent per session, when the goroutine
	// finishes its pre-open ramp-up and goes live.
	StampEntryTime(ctx context.Context, exchangeID string, agentID agent.AgentID, entryTime float64) error

	// SetTipper flags the agent as this session's tipper. Used only for
	// on-demand (the canonical tipper is a static Postgres trait).
	SetTipper(ctx context.Context, exchangeID string, agentID agent.AgentID) error
}

// RedisRuntimeStore is the Redis-backed RuntimeStore.
type RedisRuntimeStore struct {
	client *redis.Client
}

// NewRedisRuntimeStore wraps a go-redis client. The caller retains
// ownership of the client's lifecycle.
func NewRedisRuntimeStore(client *redis.Client) *RedisRuntimeStore {
	return &RedisRuntimeStore{client: client}
}

func (redisStore *RedisRuntimeStore) WriteInitialSnapshot(ctx context.Context, exchangeID string, agentRecord agent.Agent) error {
	key := RuntimeKey(exchangeID, agentRecord.ID)
	err := redisStore.client.HSet(ctx, key,
		FieldPresence, PresenceDormant,
		FieldTradingCapital, strconv.FormatInt(agentRecord.TradingCapital, 10),
	).Err()
	if err != nil {
		return fmt.Errorf("store: write initial snapshot for %s on %s: %w", agentRecord.ID, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) SetPresence(ctx context.Context, exchangeID string, agentID agent.AgentID, presence string) error {
	key := RuntimeKey(exchangeID, agentID)
	if err := redisStore.client.HSet(ctx, key, FieldPresence, presence).Err(); err != nil {
		return fmt.Errorf("store: set presence for %s on %s: %w", agentID, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) SetTimers(ctx context.Context, exchangeID string, agentID agent.AgentID, nextCheckTime, idleSince int64) error {
	key := RuntimeKey(exchangeID, agentID)
	err := redisStore.client.HSet(ctx, key,
		FieldNextCheckTime, strconv.FormatInt(nextCheckTime, 10),
		FieldIdleSince, strconv.FormatInt(idleSince, 10),
	).Err()
	if err != nil {
		return fmt.Errorf("store: set timers for %s on %s: %w", agentID, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) StampEntryTime(ctx context.Context, exchangeID string, agentID agent.AgentID, entryTime float64) error {
	key := RuntimeKey(exchangeID, agentID)
	// -1 precision lets strconv pick the shortest round-trippable form; a
	// fixed precision would either lose information or add trailing noise.
	formatted := strconv.FormatFloat(entryTime, 'f', -1, 64)
	if err := redisStore.client.HSet(ctx, key, FieldEntryTime, formatted).Err(); err != nil {
		return fmt.Errorf("store: stamp entry_time for %s on %s: %w", agentID, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) SetTipper(ctx context.Context, exchangeID string, agentID agent.AgentID) error {
	key := RuntimeKey(exchangeID, agentID)
	if err := redisStore.client.HSet(ctx, key, FieldIsTipper, IsTipperTrue).Err(); err != nil {
		return fmt.Errorf("store: set is_tipper for %s on %s: %w", agentID, exchangeID, err)
	}
	return nil
}
