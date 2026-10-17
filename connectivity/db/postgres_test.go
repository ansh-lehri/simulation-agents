package db_test

import (
	"context"
	"os"
	"testing"
	"time"

	"simulation-agents/connectivity/config"
	"simulation-agents/connectivity/db"
)

func TestNewPostgres_Success(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	//fmt.print(dsn)
	if dsn == "" {
		t.Skip("DATABASE_URL not set — requires a running local Postgres instance")
	}

	pgCfg := config.PostgresConfig{
		DSN:         dsn,
		PingTimeout: 5 * time.Second,
	}

	pool, err := db.NewPostgres(context.Background(), pgCfg)
	if err != nil {
		t.Fatalf("NewPostgres() error = %v", err)
	}
	defer pool.Close()

	if pingErr := pool.Ping(context.Background()); pingErr != nil {
		t.Fatalf("pool.Ping() after NewPostgres() error = %v", pingErr)
	}
}

func TestNewPostgres_BadDSN_NoHang(t *testing.T) {
	// Use a black-hole IP to force a connection timeout rather than an
	// immediate TCP reset, confirming that PingTimeout actually bounds the call.
	pgCfg := config.PostgresConfig{
		DSN:         "postgres://user:pass@10.255.255.1:5432/db?sslmode=disable",
		PingTimeout: 500 * time.Millisecond,
	}

	start := time.Now()
	_, err := db.NewPostgres(context.Background(), pgCfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("NewPostgres() to unreachable host: expected error, got nil")
	}
	// Allow 3× the timeout to account for OS overhead; the point is it does not hang.
	maxAllowed := 3 * pgCfg.PingTimeout
	if elapsed > maxAllowed {
		t.Errorf("NewPostgres() took %v, want < %v (ping timeout=%v)",
			elapsed, maxAllowed, pgCfg.PingTimeout)
	}
}

func TestNewPostgres_TwoCallsIndependent(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — requires a running local Postgres instance")
	}

	pgCfg := config.PostgresConfig{
		DSN:         dsn,
		PingTimeout: 5 * time.Second,
	}

	poolA, err := db.NewPostgres(context.Background(), pgCfg)
	if err != nil {
		t.Fatalf("first NewPostgres() error = %v", err)
	}
	defer poolA.Close()

	poolB, err := db.NewPostgres(context.Background(), pgCfg)
	if err != nil {
		t.Fatalf("second NewPostgres() error = %v", err)
	}
	defer poolB.Close()

	if poolA == poolB {
		t.Error("NewPostgres() returned the same pool pointer twice — singleton detected")
	}
}
