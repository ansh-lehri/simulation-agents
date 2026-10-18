package store_test

import (
	"context"
	"strings"
	"testing"

	"simulation-agents/store"
)

// A first Apply creates the agents table with its expected columns and the
// partial reuse index on retired_at IS NULL, and records version 1 in
// schema_migrations.
func TestApply_CreatesAgentsTableAndPartialIndex(t *testing.T) {
	pool := openTestDB(t)
	ctx := context.Background()

	if err := store.Apply(ctx, pool); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	expectedColumns := []string{
		"agent_id", "population", "country", "capital_handle", "trading_capital",
		"traits", "traits_version", "traits_updated_at", "origin", "created_at", "retired_at",
	}
	var columnCount int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'agents' AND column_name = ANY($1)
	`, expectedColumns).Scan(&columnCount)
	if err != nil {
		t.Fatalf("query columns: %v", err)
	}
	if columnCount != len(expectedColumns) {
		t.Fatalf("agents table columns: got %d matching, expected %d", columnCount, len(expectedColumns))
	}

	var indexDef string
	err = pool.QueryRow(ctx, `
		SELECT pg_get_indexdef(pg_class.oid)
		FROM pg_class
		WHERE relname = 'idx_agents_reuse'
	`).Scan(&indexDef)
	if err != nil {
		t.Fatalf("query index def: %v", err)
	}
	if !strings.Contains(indexDef, "WHERE") || !strings.Contains(indexDef, "retired_at IS NULL") {
		t.Fatalf("idx_agents_reuse is not partial on retired_at IS NULL: %s", indexDef)
	}

	var version int
	if err := pool.QueryRow(ctx, `SELECT version FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if version != 1 {
		t.Fatalf("recorded migration version: got %d, expected 1", version)
	}
}

// Applying twice is a no-op — the second call must succeed without error
// and must not duplicate the version row.
func TestApply_IsIdempotent(t *testing.T) {
	pool := openTestDB(t)
	ctx := context.Background()

	if err := store.Apply(ctx, pool); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if err := store.Apply(ctx, pool); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	var rowCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&rowCount); err != nil {
		t.Fatalf("query schema_migrations count: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("schema_migrations rows after two Apply calls: got %d, expected 1", rowCount)
	}
}
