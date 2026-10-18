package store_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"simulation-agents/connectivity/config"
	"simulation-agents/connectivity/db"
)

// openTestDB opens a pgx pool against AGENTSD_TEST_DATABASE_URL and refuses
// to proceed unless the database name ends in "_test" — so a careless
// developer who points this at their dev database gets a loud test
// failure, not silent destruction of real data.
//
// Each caller gets a freshly reset schema: all tables we touch are dropped
// before the test runs, so tests can assume an empty database. Tests in
// this package therefore must not use t.Parallel against this pool.
func openTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("AGENTSD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AGENTSD_TEST_DATABASE_URL not set — requires a dedicated test postgres database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.NewPostgres(ctx, config.PostgresConfig{
		DSN:         dsn,
		PingTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}

	var dbName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		pool.Close()
		t.Fatalf("query current_database: %v", err)
	}
	if !strings.HasSuffix(dbName, "_test") {
		pool.Close()
		t.Fatalf("refusing to run tests against %q: database name must end in _test", dbName)
	}

	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS agents; DROP TABLE IF EXISTS schema_migrations`); err != nil {
		pool.Close()
		t.Fatalf("reset test schema: %v", err)
	}

	t.Cleanup(func() { pool.Close() })
	return pool
}
