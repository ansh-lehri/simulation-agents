package store_test

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"simulation-agents/connectivity/config"
	"simulation-agents/connectivity/db"
	"simulation-agents/store"
)

// Shared handles. Each is lazily opened by its openTest* function via
// sync.Once, so tests that skip when the env var is absent don't pay for
// a connection attempt. Pools and clients are closed on process exit by
// the runtime — no cleanup needed per test.

var (
	testDBOnce sync.Once
	testDBPool *pgxpool.Pool
	testDBSkip bool
)

// openTestDB returns a pgx pool bound to AGENTSD_TEST_DATABASE_URL. It
// refuses to proceed unless the database name ends in "_test" so a
// casual developer pointing this at their dev database gets a loud
// failure rather than silent destruction of real data. The pool is
// opened once per package run; schema state is NOT touched here — each
// test suite does its own setup via resetSchema or applyFreshSchema.
func openTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	testDBOnce.Do(initTestDB)
	if testDBSkip {
		t.Skip("AGENTSD_TEST_DATABASE_URL not set — requires a dedicated test postgres database")
	}
	if testDBPool == nil {
		t.Fatal("test postgres pool failed to initialize (see earlier log line)")
	}
	return testDBPool
}

func initTestDB() {
	dsn := os.Getenv("AGENTSD_TEST_DATABASE_URL")
	if dsn == "" {
		testDBSkip = true
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.NewPostgres(ctx, config.PostgresConfig{DSN: dsn, PingTimeout: 5 * time.Second})
	if err != nil {
		log.Printf("test postgres: open pool: %v", err)
		return
	}

	var dbName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		pool.Close()
		log.Printf("test postgres: read current_database: %v", err)
		return
	}
	if !strings.HasSuffix(dbName, "_test") {
		pool.Close()
		log.Printf("test postgres: refusing database %q (name must end in _test)", dbName)
		return
	}

	testDBPool = pool
}

// resetSchema drops every table this package manages. Used by tests that
// want to start from a schema-less database (migrations test).
func resetSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `DROP TABLE IF EXISTS agents; DROP TABLE IF EXISTS schema_migrations`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
}

// applyFreshSchema wipes any residual rows from the agents table and
// makes sure the migrations are applied. Used by store-op tests that
// then wrap their work in a transaction + rollback for isolation.
func applyFreshSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if err := store.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE TABLE agents`); err != nil {
		t.Fatalf("truncate agents: %v", err)
	}
}

var (
	testRedisOnce   sync.Once
	testRedisClient *redis.Client
	testRedisSkip   bool
)

// openTestRedis returns a go-redis client bound to AGENTSD_TEST_REDIS_URL.
// It refuses to proceed unless a non-zero database index is specified in
// the URL (same spirit as the postgres _test-suffix guard: Redis DB 0 is
// the default dev database on most machines, and a destructive FLUSHDB
// there would be a bad day). FLUSHDB runs on every call so each test
// starts from an empty keyspace.
func openTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	testRedisOnce.Do(initTestRedis)
	if testRedisSkip {
		t.Skip("AGENTSD_TEST_REDIS_URL not set — requires a dedicated test redis instance")
	}
	if testRedisClient == nil {
		t.Fatal("test redis client failed to initialize (see earlier log line)")
	}
	if err := testRedisClient.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flushdb: %v", err)
	}
	return testRedisClient
}

func initTestRedis() {
	rawURL := os.Getenv("AGENTSD_TEST_REDIS_URL")
	if rawURL == "" {
		testRedisSkip = true
		return
	}

	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		log.Printf("test redis: parse URL: %v", err)
		return
	}
	if opts.DB == 0 {
		log.Printf("test redis: refusing to run against DB 0; AGENTSD_TEST_REDIS_URL must select a non-zero database index")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := db.NewRedis(ctx, config.RedisConfig{URL: rawURL, PingTimeout: 5 * time.Second})
	if err != nil {
		log.Printf("test redis: open client: %v", err)
		return
	}
	testRedisClient = client
}
