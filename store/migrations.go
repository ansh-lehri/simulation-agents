// Package store holds the Postgres durable store and the Redis runtime / market-data
// / lease / WAL stores. This file is the migration runner — it embeds the SQL files
// under migrations/ and applies pending ones in version order, one transaction per
// file. Applied versions are tracked in schema_migrations so re-running is a no-op.
package store

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.up.sql
var migrationsFS embed.FS

const migrationsDir = "migrations"

// schemaMigrationsDDL creates the applied-versions tracking table on first
// run. It uses IF NOT EXISTS so re-runs are no-ops without a dedicated
// migration file (bootstrapping the tracker with a tracked migration would
// be circular).
const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INT         PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// migrationFile is one parsed entry from the embedded migrations folder.
type migrationFile struct {
	version int
	name    string
	sql     string
}

// Apply runs all pending up-migrations in version order. Each file executes
// inside its own transaction together with the schema_migrations insert, so a
// mid-file failure leaves no half-applied state and no misleading version row.
// Already-applied versions are skipped — safe to call on every process start.
func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("store: ensure schema_migrations: %w", err)
	}

	files, err := loadMigrations()
	if err != nil {
		return err
	}

	applied, err := loadAppliedVersions(ctx, pool)
	if err != nil {
		return err
	}

	for _, file := range files {
		if applied[file.version] {
			continue
		}
		if err := applyMigration(ctx, pool, file); err != nil {
			return fmt.Errorf("store: migration %d (%s): %w", file.version, file.name, err)
		}
		slog.Info("store: migration applied", "version", file.version, "name", file.name)
	}
	return nil
}

// applyMigration executes one migration file + its tracking insert in a single
// transaction. defer-rollback is a no-op after a successful commit.
func applyMigration(ctx context.Context, pool *pgxpool.Pool, file migrationFile) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, file.sql); err != nil {
		return fmt.Errorf("exec migration body: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, file.version); err != nil {
		return fmt.Errorf("record version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// loadMigrations reads every *.up.sql file from the embedded folder, parses
// its version prefix, and returns the files sorted by version ascending. A
// malformed filename is a developer bug and surfaces as an error at startup
// rather than silently skipping.
func loadMigrations() ([]migrationFile, error) {
	entries, err := migrationsFS.ReadDir(migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("store: read embedded migrations dir: %w", err)
	}

	var files []migrationFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}

		versionPrefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("store: migration %q: filename must be NNNN_name.up.sql", name)
		}
		version, err := strconv.Atoi(versionPrefix)
		if err != nil {
			return nil, fmt.Errorf("store: migration %q: version prefix not numeric: %w", name, err)
		}

		body, err := migrationsFS.ReadFile(path.Join(migrationsDir, name))
		if err != nil {
			return nil, fmt.Errorf("store: read embedded migration %q: %w", name, err)
		}

		files = append(files, migrationFile{
			version: version,
			name:    name,
			sql:     string(body),
		})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	return files, nil
}

// loadAppliedVersions returns the set of versions already recorded in
// schema_migrations.
func loadAppliedVersions(ctx context.Context, pool *pgxpool.Pool) (map[int]bool, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: load applied versions: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("store: scan applied version: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate applied versions: %w", err)
	}
	return applied, nil
}
