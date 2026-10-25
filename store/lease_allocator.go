package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"

	"simulation-agents/agent"
)

// LeaseAllocator is the on-demand pool's free/busy bookkeeping. The
// pool itself (membership) lives durably in Postgres; this type owns
// only the fast, transient availability state in Redis.
//
// SelectAvailable, Enlist, and Release are LIVE. Reconcile is declared
// on the interface so the composition root compiles against the full
// contract today, but its execution lives in the crash-recovery phase
// and currently returns the package-level ErrNotImplementedThisPhase.
type LeaseAllocator interface {
	// SelectAvailable atomically pops `count` free agent ids from the
	// per-country available-set and marks each as leased to the given
	// exchange. The whole op is one Lua script so two concurrent
	// callers cannot race onto the same id (which would hit the
	// broker's "session already live" backstop later).
	SelectAvailable(ctx context.Context, country string, count int, exchangeID string) ([]agent.AgentID, error)

	// Enlist marks a freshly minted on-demand agent as leased to the
	// given exchange. Called when the mint path produces an agent that
	// bypassed the available-set entirely (born busy).
	Enlist(ctx context.Context, country string, agentID agent.AgentID, exchangeID string) error

	// Release returns a session's agents to the available-set after the
	// broker has discarded the session. The ordering constraint
	// (session_discard → release, never the other way round) is the
	// caller's responsibility.
	//
	// Processes every agent id even if an earlier one's write fails —
	// a transient Redis error on one id must not strand the rest of
	// the batch in the leased set, since a later restart-time
	// reconcile pass is the only other path back to available.
	// Returns a combined error listing every id that failed either
	// half of its release; nil only if every id fully released.
	Release(ctx context.Context, country string, agentIDs []agent.AgentID) error

	// Reconcile rebuilds the per-country available-set from the durable
	// membership list minus the exchanges still legitimately holding
	// their agents — the restart / Redis-flush recovery path. Execution
	// deferred to the teardown phase.
	Reconcile(ctx context.Context, country string, liveExchanges map[string]bool, members []agent.AgentID) error
}

// selectAvailableScript atomically pops N free ids from the available
// set and records each in the leased hash under the given exchange.
// Running pop + mark as one Lua call closes the restart gap where a
// non-atomic sequence (SPOP then HSET) could leave an id popped from
// the set but not recorded as leased — a crash in that window would
// lose the agent to both views.
var selectAvailableScript = redis.NewScript(`
local availableKey = KEYS[1]
local leasedKey    = KEYS[2]
local requested    = tonumber(ARGV[1])
local exchange     = ARGV[2]

local popped = redis.call('SPOP', availableKey, requested)
for index = 1, #popped do
    redis.call('HSET', leasedKey, popped[index], exchange)
end
return popped
`)

// RedisLeaseAllocator is the Redis-backed LeaseAllocator.
type RedisLeaseAllocator struct {
	client *redis.Client
}

// NewRedisLeaseAllocator wraps a go-redis client. The caller retains
// ownership of the client's lifecycle.
func NewRedisLeaseAllocator(client *redis.Client) *RedisLeaseAllocator {
	return &RedisLeaseAllocator{client: client}
}

func (leaseAllocator *RedisLeaseAllocator) SelectAvailable(ctx context.Context, country string, count int, exchangeID string) ([]agent.AgentID, error) {
	if count <= 0 {
		return nil, nil
	}
	keys := []string{LeaseAvailableKey(country), LeaseLeasedKey(country)}
	raw, err := selectAvailableScript.Run(ctx, leaseAllocator.client, keys, count, exchangeID).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("store: select available (%s, %d, %s): %w", country, count, exchangeID, err)
	}
	selected := make([]agent.AgentID, len(raw))
	for index, id := range raw {
		selected[index] = agent.AgentID(id)
	}
	return selected, nil
}

func (leaseAllocator *RedisLeaseAllocator) Enlist(ctx context.Context, country string, agentID agent.AgentID, exchangeID string) error {
	if err := leaseAllocator.client.HSet(ctx, LeaseLeasedKey(country), string(agentID), exchangeID).Err(); err != nil {
		return fmt.Errorf("store: enlist %s in %s on %s: %w", agentID, country, exchangeID, err)
	}
	return nil
}

// EnlistBatch sets all agent-id → exchange mappings in a single HSet call
// (one network roundtrip). Semantically identical to calling Enlist N times.
func (leaseAllocator *RedisLeaseAllocator) EnlistBatch(ctx context.Context, country string, agentIDs []agent.AgentID, exchangeID string) error {
	if len(agentIDs) == 0 {
		return nil
	}
	leasedKey := LeaseLeasedKey(country)
	// HSet with alternating field/value pairs: agentID_1, exchangeID, agentID_2, exchangeID, ...
	args := make([]any, 0, len(agentIDs)*2)
	for _, agentID := range agentIDs {
		args = append(args, string(agentID), exchangeID)
	}
	if err := leaseAllocator.client.HSet(ctx, leasedKey, args...).Err(); err != nil {
		return fmt.Errorf("store: enlist batch %d agents in %s for %s: %w", len(agentIDs), country, exchangeID, err)
	}
	return nil
}

func (leaseAllocator *RedisLeaseAllocator) Release(ctx context.Context, country string, agentIDs []agent.AgentID) error {
	if len(agentIDs) == 0 {
		return nil
	}
	leasedKey := LeaseLeasedKey(country)
	availableKey := LeaseAvailableKey(country)

	// Pipeline all HDel+SAdd pairs so N agents cost one network roundtrip.
	// Discard the outer Pipelined error — it is just the first individual
	// command error, which the loop below already collects and attributes
	// per-agent. The nil return from the pipeline func ensures all commands
	// are sent regardless of intermediate results.
	cmds, _ := leaseAllocator.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, agentID := range agentIDs {
			pipe.HDel(ctx, leasedKey, string(agentID))
			pipe.SAdd(ctx, availableKey, string(agentID))
		}
		return nil
	})

	var releaseErrors []error
	for i, cmd := range cmds {
		if cmdErr := cmd.Err(); cmdErr != nil {
			agentIdx := i / 2
			if agentIdx < len(agentIDs) {
				if i%2 == 0 {
					releaseErrors = append(releaseErrors, fmt.Errorf("HDEL leased for %s: %w", agentIDs[agentIdx], cmdErr))
				} else {
					releaseErrors = append(releaseErrors, fmt.Errorf("SADD available for %s: %w", agentIDs[agentIdx], cmdErr))
				}
			}
		}
	}
	if len(releaseErrors) > 0 {
		return fmt.Errorf("store: release %d error(s) across %d agent(s) in %s: %w",
			len(releaseErrors), len(agentIDs), country, errors.Join(releaseErrors...))
	}
	return nil
}

// Reconcile stub — execution lives in the teardown / restart phase.
func (leaseAllocator *RedisLeaseAllocator) Reconcile(ctx context.Context, country string, liveExchanges map[string]bool, members []agent.AgentID) error {
	_, _, _, _ = ctx, country, liveExchanges, members
	return ErrNotImplementedThisPhase
}
