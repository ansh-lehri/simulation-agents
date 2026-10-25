package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"

	"simulation-agents/agent"
)

// RuntimeSnapshot is the shape of the per-agent runtime state the Redis
// hash backs — but only its flat, per-agent fields. Every per-instrument
// field (perception gate refs, decision working memory: standing
// opinion/stance, wait streak, last-decided readings, pending watch,
// last commit tick, position context) is deliberately NOT represented
// here, the same way PerceptionRefs isn't: a single flat field can't
// hold one value per instrument an agent watches, and turning every one
// of them into a map would make this type track the per-instrument
// field-name helpers in keys.go redundantly. Whatever downstream stage
// builds the runtime snapshot reader reads those per-instrument blobs
// directly by field name instead.
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

// PerceptionRefs holds the last market readings an agent acted on. The
// decision logic carries these between ticks so it can compare current
// conditions against the previous observation and decide whether enough
// has changed to warrant re-evaluating a stance.
type PerceptionRefs struct {
	LastPrice    int64
	LastSpread   int64
	LastVolume   int64
	LastRangePos float64
	LastPhase    string
}

// PendingWatch mirrors the decision package's PendingWatch for Redis
// persistence: an agent's in-progress confirmation count toward a
// commit on one instrument. Kept as its own type here, independent of
// decision.PendingWatch, so store never depends on decision — the same
// separation every other working-memory type in this file (PositionContext,
// Tracking) already keeps.
type PendingWatch struct {
	Direction    float64
	ConfirmsSeen int
	SinceTick    int64
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

	// SetStandingOpinion writes the agent's current directional opinion
	// (a signed float in [-1, +1]) and its associated trading stance,
	// for one instrument. Written on a RE-DECIDE outcome, never on
	// COAST — the write-on-change discipline every setter in this file
	// follows.
	SetStandingOpinion(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, opinion float64, stance string) error

	// SetWaitStreak writes how many consecutive ticks the agent has
	// coasted in WAIT on one instrument, without re-deciding. Used by
	// the re-decide gate to detect a waiter that has coasted long
	// enough to force a fresh reassessment.
	SetWaitStreak(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, streak int) error

	// SetPendingWatch writes (or, with a nil watch, clears) the agent's
	// in-progress confirmation count toward a commit on one instrument.
	// A nil watch means the agent is not currently waiting on anything
	// — cleared on every COMMIT and every SKIP, written fresh or
	// updated on every WAIT.
	SetPendingWatch(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, pendingWatch *PendingWatch) error

	// SetLastCommitTick writes the tick of the agent's most recent
	// commit on one instrument. Written only on COMMIT — this is the
	// baseline the commit-gate's cooldown shave measures the next
	// commit attempt against.
	SetLastCommitTick(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, commitTick int64) error

	// SetLastDecidedReadings writes the reading snapshot the re-decide
	// gate compared against on its most recent RE-DECIDE for one
	// instrument. An empty/nil map signals this agent has never
	// decided on this instrument before — the gate's first-ever-decide
	// case.
	SetLastDecidedReadings(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, readings map[string]float64) error

	// SetPositionContext writes the entry conditions of the agent's
	// currently open position on one instrument. A nil positionContext
	// clears the field, signalling that the agent is flat on that
	// instrument. Stored as a per-instrument JSON blob, mirroring
	// SetPendingWatch/SetLastCommitTick, because an agent holding more
	// than one instrument carries one independent entry record per
	// instrument.
	SetPositionContext(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, positionContext *PositionContext) error

	// SetTipFollowState writes the full tip-following snapshot for a
	// herd agent: the tracking target, the last tip it acted on, and
	// its pre-computed exit and submission levels. All four are
	// co-written atomically because they represent one coherent state.
	SetTipFollowState(ctx context.Context, exchangeID string, agentID agent.AgentID, tracking *Tracking, lastActedTipID string, myExitLevel, submitAt int64) error

	// SetPerceptionRefs writes the last market readings the agent used when
	// it most recently ran perception for the given instrument. Stored as a
	// per-instrument JSON blob so agents with multiple instruments never
	// have their refs collide. The perception gate compares current readings
	// against these stored refs to decide whether enough has changed to
	// warrant re-running perception.
	SetPerceptionRefs(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, refs PerceptionRefs) error

	// DropSession wipes every runtime and market-data key this session
	// wrote: every per-agent runtime hash, the shared tip slot, and the
	// candle/quote cache, all scoped to exchangeID. Redis has no glob
	// delete, so the per-agent and market-data key sets are each walked
	// page-by-page via SCAN and UNLINKed as they're found, rather than
	// deleted in one call. Called once, at teardown, before the session
	// is struck off the active-sessions registry.
	DropSession(ctx context.Context, exchangeID string) error
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

// WriteInitialSnapshotBatch pipelines one HSet per agent in a single
// network roundtrip. Each agent gets its own key so there is no conflict
// between agents; pipelining batches the writes without changing any
// individual key's semantics.
func (redisStore *RedisRuntimeStore) WriteInitialSnapshotBatch(ctx context.Context, exchangeID string, agents []agent.Agent) error {
	if len(agents) == 0 {
		return nil
	}
	_, err := redisStore.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, agentRecord := range agents {
			key := RuntimeKey(exchangeID, agentRecord.ID)
			pipe.HSet(ctx, key,
				FieldPresence, PresenceDormant,
				FieldTradingCapital, strconv.FormatInt(agentRecord.TradingCapital, 10),
			)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: write initial snapshot batch (%d agents) on %s: %w", len(agents), exchangeID, err)
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

func (redisStore *RedisRuntimeStore) SetStandingOpinion(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, opinion float64, stance string) error {
	key := RuntimeKey(exchangeID, agentID)
	err := redisStore.client.HSet(ctx, key,
		StandingOpinionField(instrument), strconv.FormatFloat(opinion, 'f', -1, 64),
		StandingStanceField(instrument), stance,
	).Err()
	if err != nil {
		return fmt.Errorf("store: set standing opinion for %s/%s on %s: %w", agentID, instrument, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) SetWaitStreak(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, streak int) error {
	key := RuntimeKey(exchangeID, agentID)
	if err := redisStore.client.HSet(ctx, key, WaitStreakField(instrument), strconv.Itoa(streak)).Err(); err != nil {
		return fmt.Errorf("store: set wait streak for %s/%s on %s: %w", agentID, instrument, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) SetPendingWatch(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, pendingWatch *PendingWatch) error {
	key := RuntimeKey(exchangeID, agentID)
	var encoded string
	if pendingWatch != nil {
		raw, err := json.Marshal(pendingWatch)
		if err != nil {
			return fmt.Errorf("store: marshal pending watch for %s/%s on %s: %w", agentID, instrument, exchangeID, err)
		}
		encoded = string(raw)
	}
	if err := redisStore.client.HSet(ctx, key, PendingWatchField(instrument), encoded).Err(); err != nil {
		return fmt.Errorf("store: set pending watch for %s/%s on %s: %w", agentID, instrument, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) SetLastCommitTick(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, commitTick int64) error {
	key := RuntimeKey(exchangeID, agentID)
	if err := redisStore.client.HSet(ctx, key, LastCommitTickField(instrument), strconv.FormatInt(commitTick, 10)).Err(); err != nil {
		return fmt.Errorf("store: set last commit tick for %s/%s on %s: %w", agentID, instrument, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) SetLastDecidedReadings(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, readings map[string]float64) error {
	key := RuntimeKey(exchangeID, agentID)
	encoded, err := json.Marshal(readings)
	if err != nil {
		return fmt.Errorf("store: marshal last decided readings for %s/%s on %s: %w", agentID, instrument, exchangeID, err)
	}
	if err := redisStore.client.HSet(ctx, key, LastDecidedReadingsField(instrument), string(encoded)).Err(); err != nil {
		return fmt.Errorf("store: set last decided readings for %s/%s on %s: %w", agentID, instrument, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) SetPositionContext(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, positionContext *PositionContext) error {
	key := RuntimeKey(exchangeID, agentID)
	var encoded string
	if positionContext != nil {
		raw, err := json.Marshal(positionContext)
		if err != nil {
			return fmt.Errorf("store: marshal position context for %s/%s on %s: %w", agentID, instrument, exchangeID, err)
		}
		encoded = string(raw)
	}
	if err := redisStore.client.HSet(ctx, key, PositionContextField(instrument), encoded).Err(); err != nil {
		return fmt.Errorf("store: set position context for %s/%s on %s: %w", agentID, instrument, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) SetTipFollowState(ctx context.Context, exchangeID string, agentID agent.AgentID, tracking *Tracking, lastActedTipID string, myExitLevel, submitAt int64) error {
	key := RuntimeKey(exchangeID, agentID)
	var trackingEncoded string
	if tracking != nil {
		raw, err := json.Marshal(tracking)
		if err != nil {
			return fmt.Errorf("store: marshal tracking for %s on %s: %w", agentID, exchangeID, err)
		}
		trackingEncoded = string(raw)
	}
	err := redisStore.client.HSet(ctx, key,
		FieldTracking, trackingEncoded,
		FieldLastActedTipID, lastActedTipID,
		FieldMyExitLevel, strconv.FormatInt(myExitLevel, 10),
		FieldSubmitAt, strconv.FormatInt(submitAt, 10),
	).Err()
	if err != nil {
		return fmt.Errorf("store: set tip follow state for %s on %s: %w", agentID, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) SetPerceptionRefs(ctx context.Context, exchangeID string, agentID agent.AgentID, instrument string, refs PerceptionRefs) error {
	key := RuntimeKey(exchangeID, agentID)
	encoded, err := json.Marshal(refs)
	if err != nil {
		return fmt.Errorf("store: marshal perception refs for %s/%s on %s: %w", agentID, instrument, exchangeID, err)
	}
	if err := redisStore.client.HSet(ctx, key, GateRefsField(instrument), string(encoded)).Err(); err != nil {
		return fmt.Errorf("store: set perception refs for %s/%s on %s: %w", agentID, instrument, exchangeID, err)
	}
	return nil
}

func (redisStore *RedisRuntimeStore) DropSession(ctx context.Context, exchangeID string) error {
	agentKeyPattern := fmt.Sprintf("runtime:%s:agent:*", exchangeID)
	if err := scanAndUnlinkMatching(ctx, redisStore.client, agentKeyPattern); err != nil {
		return fmt.Errorf("store: drop session agent runtime keys for %s: %w", exchangeID, err)
	}
	if err := redisStore.client.Unlink(ctx, TipSlotKey(exchangeID)).Err(); err != nil {
		return fmt.Errorf("store: drop session tip slot for %s: %w", exchangeID, err)
	}
	marketDataKeyPattern := fmt.Sprintf("marketdata:%s:*", exchangeID)
	if err := scanAndUnlinkMatching(ctx, redisStore.client, marketDataKeyPattern); err != nil {
		return fmt.Errorf("store: drop session market data keys for %s: %w", exchangeID, err)
	}
	return nil
}

// scanKeyPageSize bounds how many keys SCAN returns per cursor page.
// Chosen to keep each round trip small without so many pages that
// wiping a large session's keyspace takes an excessive number of calls.
const scanKeyPageSize = 500

// scanAndUnlinkMatching walks the full keyspace matching pattern one
// SCAN page at a time and UNLINKs every key found on that page. A
// single matching key is never deleted directly by name — only this
// pattern-walk path is used, because the caller knows the glob, not
// the individual keys underneath it.
func scanAndUnlinkMatching(ctx context.Context, client *redis.Client, pattern string) error {
	var cursor uint64
	for {
		matchedKeys, nextCursor, err := client.Scan(ctx, cursor, pattern, scanKeyPageSize).Result()
		if err != nil {
			return fmt.Errorf("scan %s: %w", pattern, err)
		}
		if len(matchedKeys) > 0 {
			if err := client.Unlink(ctx, matchedKeys...).Err(); err != nil {
				return fmt.Errorf("unlink %d keys matching %s: %w", len(matchedKeys), pattern, err)
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			return nil
		}
	}
}

// MarkSessionLoadFailed stamps the pre-open session-install failure
// outcome for one agent so the tick-loop scheduler can skip the
// agent at activate time. The method is not on the RuntimeStore
// interface above because the orchestrator (its only interface
// consumer today) never calls it; the method sits on the concrete
// type so the session package can require it on its own narrow
// extension of this interface.
func (redisStore *RedisRuntimeStore) MarkSessionLoadFailed(
	ctx context.Context,
	exchangeID string,
	agentID agent.AgentID,
	reason string,
) error {
	key := RuntimeKey(exchangeID, agentID)
	if err := redisStore.client.HSet(ctx, key,
		FieldLoadStatus, LoadStatusFailed,
		FieldLoadError, reason,
	).Err(); err != nil {
		return fmt.Errorf("store: mark session load failed for %s on %s: %w", agentID, exchangeID, err)
	}
	return nil
}
