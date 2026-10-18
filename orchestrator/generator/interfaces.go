package generator

import (
	"context"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// This file holds the narrow interfaces the generator depends on. Each
// one carves out just the methods the generator calls, not the whole
// store contract, so tests can inject small fakes and so a reader can
// tell at a glance what side-effects a Generator can produce. The real
// store types in the store package satisfy every interface here.

// AgentStoreReader is the read-side slice of the durable-agent store the
// generator needs: find the live members of a canonical country/
// population for the revive-first path, and load specific rows for the
// on-demand lease path. Writes (insert, retire, etc.) are not the
// generator's business — the orchestrator's persistence step owns them.
type AgentStoreReader interface {
	LiveMembers(ctx context.Context, country, population string) ([]agent.Agent, error)
	LoadMany(ctx context.Context, agentIDs []agent.AgentID) ([]agent.Agent, error)
}

// LeaseSelector is the one method the on-demand lease path needs: pop
// up to count free agent ids from the per-country pool and mark them
// leased to the given exchange, atomically. Enlist / Release /
// Reconcile are owned by other actors (orchestrator persistence,
// teardown); the generator is read-only against the lease.
type LeaseSelector interface {
	SelectAvailable(ctx context.Context, country string, count int, exchangeID string) ([]agent.AgentID, error)
}

// WALStager is the write-ahead-log entry point the generator uses to
// record a mint-in-progress before the economy account is opened. The
// Clear and Drain methods on the full WAL interface are not called
// here — Clear runs after the durable persist (orchestrator step),
// Drain runs at startup recovery (a later phase).
type WALStager interface {
	Stage(ctx context.Context, agentID agent.AgentID, record store.StageRecord) error
}
