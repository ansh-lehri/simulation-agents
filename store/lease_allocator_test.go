package store_test

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"testing"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// seedAvailable seeds the per-country available set with a fixed list
// of ids, so each test starts from a known pool shape.
func seedAvailable(t *testing.T, country string, ids ...string) {
	t.Helper()
	client := openTestRedis(t)
	ctx := context.Background()
	members := make([]any, len(ids))
	for index, id := range ids {
		members[index] = id
	}
	if err := client.SAdd(ctx, store.LeaseAvailableKey(country), members...).Err(); err != nil {
		t.Fatalf("seed available: %v", err)
	}
}

// A single SelectAvailable returns the requested count of ids, those
// ids are removed from the available set, and each is marked as leased
// to the given exchange.
func TestSelectAvailable_PopsAndMarks(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	leaseAllocator := store.NewRedisLeaseAllocator(client)

	seedAvailable(t, "US", "a-1", "a-2", "a-3", "a-4", "a-5")

	selected, err := leaseAllocator.SelectAvailable(ctx, "US", 3, "ex-1")
	if err != nil {
		t.Fatalf("SelectAvailable: %v", err)
	}
	if len(selected) != 3 {
		t.Fatalf("selected count: got %d want 3", len(selected))
	}

	remaining, err := client.SCard(ctx, store.LeaseAvailableKey("US")).Result()
	if err != nil {
		t.Fatalf("SCard: %v", err)
	}
	if remaining != 2 {
		t.Errorf("available set size after select: got %d want 2", remaining)
	}

	for _, agentID := range selected {
		got, err := client.HGet(ctx, store.LeaseLeasedKey("US"), string(agentID)).Result()
		if err != nil {
			t.Fatalf("HGet leased for %s: %v", agentID, err)
		}
		if got != "ex-1" {
			t.Errorf("leased exchange for %s: got %q want %q", agentID, got, "ex-1")
		}
	}
}

// Requesting more agents than are available returns at most the current
// available-set size (SPOP's native behavior); the remaining set is empty.
func TestSelectAvailable_RequestExceedsAvailable(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	leaseAllocator := store.NewRedisLeaseAllocator(client)

	seedAvailable(t, "US", "a-1", "a-2")

	selected, err := leaseAllocator.SelectAvailable(ctx, "US", 10, "ex-1")
	if err != nil {
		t.Fatalf("SelectAvailable: %v", err)
	}
	if len(selected) != 2 {
		t.Fatalf("selected count: got %d want 2", len(selected))
	}

	remaining, err := client.SCard(ctx, store.LeaseAvailableKey("US")).Result()
	if err != nil {
		t.Fatalf("SCard: %v", err)
	}
	if remaining != 0 {
		t.Errorf("available set should be empty after oversized request, got %d", remaining)
	}
}

// Concurrent selects on the same pool must never hand the same id to
// two callers — the sum of selections across callers cannot exceed the
// seeded size, and no id appears twice.
func TestSelectAvailable_AtomicUnderConcurrency(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	leaseAllocator := store.NewRedisLeaseAllocator(client)

	const seedSize = 10
	ids := make([]string, seedSize)
	for index := range ids {
		ids[index] = agentIDLabel(index)
	}
	seedAvailable(t, "US", ids...)

	const callers = 20
	var waitGroup sync.WaitGroup
	resultsChannel := make(chan []agent.AgentID, callers)
	errorsChannel := make(chan error, callers)

	for callerIndex := 0; callerIndex < callers; callerIndex++ {
		waitGroup.Add(1)
		exchangeID := exchangeLabel(callerIndex)
		go func() {
			defer waitGroup.Done()
			selected, err := leaseAllocator.SelectAvailable(ctx, "US", 1, exchangeID)
			if err != nil {
				errorsChannel <- err
				return
			}
			resultsChannel <- selected
		}()
	}
	waitGroup.Wait()
	close(resultsChannel)
	close(errorsChannel)

	for err := range errorsChannel {
		t.Fatalf("concurrent SelectAvailable error: %v", err)
	}

	seen := map[agent.AgentID]bool{}
	totalSelected := 0
	for selected := range resultsChannel {
		for _, agentID := range selected {
			if seen[agentID] {
				t.Errorf("agent %s selected twice across concurrent callers", agentID)
			}
			seen[agentID] = true
			totalSelected++
		}
	}
	if totalSelected > seedSize {
		t.Fatalf("concurrent selects handed out %d ids but seed size was %d", totalSelected, seedSize)
	}

	// The remainder in the available set + what was selected must equal the seed size.
	remaining, err := client.SCard(ctx, store.LeaseAvailableKey("US")).Result()
	if err != nil {
		t.Fatalf("SCard: %v", err)
	}
	if int(remaining)+totalSelected != seedSize {
		t.Errorf("accounting mismatch: selected=%d remaining=%d seed=%d", totalSelected, remaining, seedSize)
	}

	// Every selected id must appear in the leased hash, and no non-selected id should.
	leased, err := client.HKeys(ctx, store.LeaseLeasedKey("US")).Result()
	if err != nil {
		t.Fatalf("HKeys leased: %v", err)
	}
	sort.Strings(leased)
	if len(leased) != totalSelected {
		t.Errorf("leased hash size: got %d want %d", len(leased), totalSelected)
	}
}

// Enlist writes directly into the leased hash — covers the "born busy"
// path where a freshly minted agent never passes through the available set.
func TestEnlist_WritesLeasedEntry(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	leaseAllocator := store.NewRedisLeaseAllocator(client)

	if err := leaseAllocator.Enlist(ctx, "US", "a-new", "ex-1"); err != nil {
		t.Fatalf("Enlist: %v", err)
	}
	got, err := client.HGet(ctx, store.LeaseLeasedKey("US"), "a-new").Result()
	if err != nil {
		t.Fatalf("HGet: %v", err)
	}
	if got != "ex-1" {
		t.Errorf("enlisted exchange: got %q want %q", got, "ex-1")
	}
}

// Reconcile is declared on the interface for forward compatibility but
// its execution lives in the crash-recovery phase. Calling it must
// return the sentinel error so a caller cannot mistake a stub for a
// working impl.
func TestReconcile_ReturnsStubSentinel(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	leaseAllocator := store.NewRedisLeaseAllocator(client)

	reconcileErr := leaseAllocator.Reconcile(ctx, "US", map[string]bool{"ex-1": true}, []agent.AgentID{"a-1"})
	if !errors.Is(reconcileErr, store.ErrNotImplementedThisPhase) {
		t.Errorf("Reconcile err: got %v want ErrNotImplementedThisPhase", reconcileErr)
	}
}

// Release clears each agent's busy mark and returns it to the
// available set — the mirror image of SelectAvailable's pop+mark.
func TestRelease_ClearsLeasedAndRestoresAvailable(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	leaseAllocator := store.NewRedisLeaseAllocator(client)

	if err := leaseAllocator.Enlist(ctx, "US", "a-1", "ex-1"); err != nil {
		t.Fatalf("Enlist a-1: %v", err)
	}
	if err := leaseAllocator.Enlist(ctx, "US", "a-2", "ex-1"); err != nil {
		t.Fatalf("Enlist a-2: %v", err)
	}

	if err := leaseAllocator.Release(ctx, "US", []agent.AgentID{"a-1", "a-2"}); err != nil {
		t.Fatalf("Release: %v", err)
	}

	for _, agentID := range []string{"a-1", "a-2"} {
		exists, err := client.HExists(ctx, store.LeaseLeasedKey("US"), agentID).Result()
		if err != nil {
			t.Fatalf("HExists leased %s: %v", agentID, err)
		}
		if exists {
			t.Errorf("agent %s still present in leased hash after Release", agentID)
		}
		isMember, err := client.SIsMember(ctx, store.LeaseAvailableKey("US"), agentID).Result()
		if err != nil {
			t.Fatalf("SIsMember available %s: %v", agentID, err)
		}
		if !isMember {
			t.Errorf("agent %s not returned to available set after Release", agentID)
		}
	}
}

// Release processes every agent id in the batch, independent of each
// other — releasing a batch with one never-leased id (a no-op HDEL)
// alongside a genuinely leased one must still free the genuinely
// leased one, not abort the whole batch.
func TestRelease_ProcessesEveryAgentIDIndependently(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	leaseAllocator := store.NewRedisLeaseAllocator(client)

	if err := leaseAllocator.Enlist(ctx, "US", "a-leased", "ex-1"); err != nil {
		t.Fatalf("Enlist: %v", err)
	}

	// "a-never-leased" was never in the leased hash — its HDEL is a
	// harmless no-op, but it must still be returned to available.
	if err := leaseAllocator.Release(ctx, "US", []agent.AgentID{"a-never-leased", "a-leased"}); err != nil {
		t.Fatalf("Release: %v", err)
	}

	for _, agentID := range []string{"a-never-leased", "a-leased"} {
		isMember, err := client.SIsMember(ctx, store.LeaseAvailableKey("US"), agentID).Result()
		if err != nil {
			t.Fatalf("SIsMember available %s: %v", agentID, err)
		}
		if !isMember {
			t.Errorf("agent %s not returned to available set after Release", agentID)
		}
	}
	stillLeased, err := client.HExists(ctx, store.LeaseLeasedKey("US"), "a-leased").Result()
	if err != nil {
		t.Fatalf("HExists leased a-leased: %v", err)
	}
	if stillLeased {
		t.Error("a-leased still present in leased hash after Release")
	}
}

func agentIDLabel(index int) string {
	return "a-" + strconv.Itoa(index)
}

func exchangeLabel(index int) string {
	return "ex-" + strconv.Itoa(index)
}
