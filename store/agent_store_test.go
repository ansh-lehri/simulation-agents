package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// withTx opens a fresh transaction against the test pool and defers a
// rollback — tests get isolated writes without leaking rows into the
// shared database.
func withTx(t *testing.T, fn func(ctx context.Context, tx pgx.Tx)) {
	t.Helper()
	pool := openTestDB(t)
	applyFreshSchema(t, pool)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	fn(ctx, tx)
}

func makeEndogenousTraits() agent.TraitVector {
	rho, phi := 0.5, 0.1
	maxHold := 1200
	return agent.TraitVector{
		AgentKind:      agent.AgentKindEndogenous,
		Skill:          0.5,
		Aggressiveness: 0.5,
		CapitalSize:    100_000,
		PnlReactivity:  0.4,
		PAct:           0.3,
		Rho:            &rho,
		Phi:            &phi,
		MaxHold:        &maxHold,
	}
}

func makeHerdTraits() agent.TraitVector {
	actProb := 0.5
	return agent.TraitVector{
		AgentKind:      agent.AgentKindHerd,
		Skill:          0.3,
		Aggressiveness: 0.7,
		CapitalSize:    30_000,
		PnlReactivity:  0.3,
		PAct:           0.2,
		ActProb:        &actProb,
	}
}

// makeAgent builds a plausible Agent record for insertion. Timestamps are
// truncated to microsecond precision because Postgres timestamptz is
// microsecond-resolution and finer values would lose equality on reload.
func makeAgent(id, country, population string) agent.Agent {
	var traits agent.TraitVector
	tradingCapital := int64(50_000)
	if population == agent.PopulationCanonical {
		traits = makeEndogenousTraits()
	} else {
		traits = makeHerdTraits()
		// On-demand's TradingCapital is never persisted; leave non-zero
		// here to prove the store writes NULL regardless.
		tradingCapital = 999
	}
	return agent.Agent{
		ID:              agent.AgentID(id),
		Population:      population,
		Country:         country,
		Origin:          agent.OriginMint,
		CapitalHandle:   "econ-acct-" + id,
		TradingCapital:  tradingCapital,
		TraitsVersion:   1,
		TraitsUpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
		Traits:          traits,
	}
}

func agentIDSet(agents []agent.Agent) map[agent.AgentID]bool {
	ids := map[agent.AgentID]bool{}
	for _, agentRow := range agents {
		ids[agentRow.ID] = true
	}
	return ids
}

// Inserting the same agent id twice must leave exactly one row — the
// crash-recovery path depends on this being safe to retry.
func TestInsertAgent_IdempotentByPK(t *testing.T) {
	withTx(t, func(ctx context.Context, tx pgx.Tx) {
		agentStore := store.NewPostgresAgentStore(tx)
		rec := makeAgent("a-1", "US", agent.PopulationCanonical)
		if err := agentStore.InsertAgent(ctx, rec); err != nil {
			t.Fatalf("first insert: %v", err)
		}
		if err := agentStore.InsertAgent(ctx, rec); err != nil {
			t.Fatalf("second insert: %v", err)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM agents WHERE agent_id = $1`, "a-1").Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != 1 {
			t.Fatalf("agent rows after two inserts: got %d want 1", count)
		}
	})
}

// Canonical rows persist trading_capital — it survives insert + load.
func TestInsertAgent_CanonicalPersistsTradingCapital(t *testing.T) {
	withTx(t, func(ctx context.Context, tx pgx.Tx) {
		agentStore := store.NewPostgresAgentStore(tx)
		rec := makeAgent("a-canon", "US", agent.PopulationCanonical)
		rec.TradingCapital = 777_000
		if err := agentStore.InsertAgent(ctx, rec); err != nil {
			t.Fatalf("insert: %v", err)
		}

		loaded, err := agentStore.LoadMany(ctx, []agent.AgentID{"a-canon"})
		if err != nil {
			t.Fatalf("LoadMany: %v", err)
		}
		if len(loaded) != 1 {
			t.Fatalf("loaded count: got %d want 1", len(loaded))
		}
		if loaded[0].TradingCapital != 777_000 {
			t.Errorf("trading_capital: got %d want %d", loaded[0].TradingCapital, 777_000)
		}
	})
}

// On-demand rows must store NULL in trading_capital regardless of the
// Agent record's field — session money is Redis-only for on-demand.
func TestInsertAgent_OnDemandStoresNullTradingCapital(t *testing.T) {
	withTx(t, func(ctx context.Context, tx pgx.Tx) {
		agentStore := store.NewPostgresAgentStore(tx)
		rec := makeAgent("a-ond", "US", agent.PopulationOnDemand) // carries non-zero TradingCapital
		if err := agentStore.InsertAgent(ctx, rec); err != nil {
			t.Fatalf("insert: %v", err)
		}

		var isNull bool
		if err := tx.QueryRow(ctx, `SELECT trading_capital IS NULL FROM agents WHERE agent_id = $1`, "a-ond").Scan(&isNull); err != nil {
			t.Fatalf("query trading_capital null: %v", err)
		}
		if !isNull {
			t.Fatal("on-demand trading_capital must be NULL in Postgres")
		}

		loaded, err := agentStore.LoadMany(ctx, []agent.AgentID{"a-ond"})
		if err != nil {
			t.Fatalf("LoadMany: %v", err)
		}
		if len(loaded) != 1 {
			t.Fatalf("loaded count: got %d want 1", len(loaded))
		}
		if loaded[0].TradingCapital != 0 {
			t.Errorf("on-demand trading_capital after load: got %d want 0", loaded[0].TradingCapital)
		}
	})
}

// LiveMembers filters on country, population, and retired_at IS NULL.
func TestLiveMembers_FiltersCountryPopulationAndExcludesRetired(t *testing.T) {
	withTx(t, func(ctx context.Context, tx pgx.Tx) {
		agentStore := store.NewPostgresAgentStore(tx)

		liveUSCanon := makeAgent("live-us-canon", "US", agent.PopulationCanonical)
		liveUSOnd := makeAgent("live-us-ond", "US", agent.PopulationOnDemand)
		liveFRCanon := makeAgent("live-fr-canon", "FR", agent.PopulationCanonical)
		retiredUSCanon := makeAgent("retired-us-canon", "US", agent.PopulationCanonical)

		for _, rec := range []agent.Agent{liveUSCanon, liveUSOnd, liveFRCanon, retiredUSCanon} {
			if err := agentStore.InsertAgent(ctx, rec); err != nil {
				t.Fatalf("insert %s: %v", rec.ID, err)
			}
		}
		if err := agentStore.Retire(ctx, "retired-us-canon", time.Now().UTC()); err != nil {
			t.Fatalf("Retire: %v", err)
		}

		gotCanonicalUS, err := agentStore.LiveMembers(ctx, "US", agent.PopulationCanonical)
		if err != nil {
			t.Fatalf("LiveMembers canonical US: %v", err)
		}
		ids := agentIDSet(gotCanonicalUS)
		if len(ids) != 1 || !ids["live-us-canon"] {
			t.Errorf("canonical US: got %v want {live-us-canon}", ids)
		}

		gotOnDemandUS, err := agentStore.LiveMembers(ctx, "US", agent.PopulationOnDemand)
		if err != nil {
			t.Fatalf("LiveMembers ondemand US: %v", err)
		}
		ids = agentIDSet(gotOnDemandUS)
		if len(ids) != 1 || !ids["live-us-ond"] {
			t.Errorf("on-demand US: got %v want {live-us-ond}", ids)
		}

		gotCanonicalFR, err := agentStore.LiveMembers(ctx, "FR", agent.PopulationCanonical)
		if err != nil {
			t.Fatalf("LiveMembers canonical FR: %v", err)
		}
		ids = agentIDSet(gotCanonicalFR)
		if len(ids) != 1 || !ids["live-fr-canon"] {
			t.Errorf("canonical FR: got %v want {live-fr-canon}", ids)
		}
	})
}

// LoadMany returns exactly the requested subset.
func TestLoadMany_ReturnsRequestedSubsetOnly(t *testing.T) {
	withTx(t, func(ctx context.Context, tx pgx.Tx) {
		agentStore := store.NewPostgresAgentStore(tx)
		for _, id := range []string{"a-1", "a-2", "a-3"} {
			rec := makeAgent(id, "US", agent.PopulationOnDemand)
			if err := agentStore.InsertAgent(ctx, rec); err != nil {
				t.Fatalf("insert %s: %v", id, err)
			}
		}
		loaded, err := agentStore.LoadMany(ctx, []agent.AgentID{"a-1", "a-3"})
		if err != nil {
			t.Fatalf("LoadMany: %v", err)
		}
		ids := agentIDSet(loaded)
		if len(ids) != 2 || !ids["a-1"] || !ids["a-3"] {
			t.Errorf("loaded ids: got %v want {a-1, a-3}", ids)
		}
	})
}

// Retire stamps retired_at and the row drops out of LiveMembers.
func TestRetire_SetsRetiredAtAndRemovesFromLiveMembers(t *testing.T) {
	withTx(t, func(ctx context.Context, tx pgx.Tx) {
		agentStore := store.NewPostgresAgentStore(tx)
		rec := makeAgent("a-retire", "US", agent.PopulationCanonical)
		if err := agentStore.InsertAgent(ctx, rec); err != nil {
			t.Fatalf("insert: %v", err)
		}

		before, err := agentStore.LiveMembers(ctx, "US", agent.PopulationCanonical)
		if err != nil {
			t.Fatalf("LiveMembers before: %v", err)
		}
		if !agentIDSet(before)["a-retire"] {
			t.Fatal("agent should be a live member before retire")
		}

		if err := agentStore.Retire(ctx, "a-retire", time.Now().UTC()); err != nil {
			t.Fatalf("Retire: %v", err)
		}

		after, err := agentStore.LiveMembers(ctx, "US", agent.PopulationCanonical)
		if err != nil {
			t.Fatalf("LiveMembers after: %v", err)
		}
		if agentIDSet(after)["a-retire"] {
			t.Fatal("agent must not appear in live members after retire")
		}

		var stored time.Time
		if err := tx.QueryRow(ctx, `SELECT retired_at FROM agents WHERE agent_id = $1`, "a-retire").Scan(&stored); err != nil {
			t.Fatalf("read retired_at: %v", err)
		}
		if stored.IsZero() {
			t.Fatal("retired_at not persisted")
		}
	})
}

// Traits serialize and deserialize whole through the JSONB column, with
// the kind-gated blocks preserving their presence/absence pattern.
func TestInsertAgent_TraitsJSONBRoundTrip(t *testing.T) {
	withTx(t, func(ctx context.Context, tx pgx.Tx) {
		agentStore := store.NewPostgresAgentStore(tx)
		canonical := makeAgent("a-canon", "US", agent.PopulationCanonical)
		herd := makeAgent("a-herd", "US", agent.PopulationOnDemand)
		if err := agentStore.InsertAgent(ctx, canonical); err != nil {
			t.Fatalf("insert canonical: %v", err)
		}
		if err := agentStore.InsertAgent(ctx, herd); err != nil {
			t.Fatalf("insert herd: %v", err)
		}

		loaded, err := agentStore.LoadMany(ctx, []agent.AgentID{"a-canon", "a-herd"})
		if err != nil {
			t.Fatalf("LoadMany: %v", err)
		}
		byID := map[agent.AgentID]agent.Agent{}
		for _, agentRow := range loaded {
			byID[agentRow.ID] = agentRow
		}

		if got, want := byID["a-canon"].Traits.AgentKind, agent.AgentKindEndogenous; got != want {
			t.Errorf("canonical kind: got %q want %q", got, want)
		}
		if byID["a-canon"].Traits.Rho == nil {
			t.Error("canonical rho should round-trip non-nil")
		}
		if byID["a-canon"].Traits.ActProb != nil {
			t.Error("canonical act_prob should round-trip nil (herd-only)")
		}

		if got, want := byID["a-herd"].Traits.AgentKind, agent.AgentKindHerd; got != want {
			t.Errorf("herd kind: got %q want %q", got, want)
		}
		if byID["a-herd"].Traits.ActProb == nil {
			t.Error("herd act_prob should round-trip non-nil")
		}
		if byID["a-herd"].Traits.Rho != nil {
			t.Error("herd rho should round-trip nil (endogenous-only)")
		}
	})
}
