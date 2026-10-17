package db_test

import (
	"context"
	"os"
	"testing"
	"time"

	"simulation-agents/connectivity/config"
	"simulation-agents/connectivity/db"
)

func TestNewRedis_Success(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set — requires a running local Redis instance")
	}

	redisCfg := config.RedisConfig{
		URL:         redisURL,
		PingTimeout: 5 * time.Second,
	}

	client, err := db.NewRedis(context.Background(), redisCfg)
	if err != nil {
		t.Fatalf("NewRedis() error = %v", err)
	}
	defer client.Close()

	if pingErr := client.Ping(context.Background()).Err(); pingErr != nil {
		t.Fatalf("client.Ping() after NewRedis() error = %v", pingErr)
	}
}

func TestNewRedis_BadURL_NoHang(t *testing.T) {
	// Use a black-hole IP to force a connection timeout, confirming PingTimeout bounds it.
	redisCfg := config.RedisConfig{
		URL:         "redis://10.255.255.1:6379/0",
		PingTimeout: 500 * time.Millisecond,
	}

	start := time.Now()
	_, err := db.NewRedis(context.Background(), redisCfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("NewRedis() to unreachable host: expected error, got nil")
	}
	maxAllowed := 3 * redisCfg.PingTimeout
	if elapsed > maxAllowed {
		t.Errorf("NewRedis() took %v, want < %v (ping timeout=%v)",
			elapsed, maxAllowed, redisCfg.PingTimeout)
	}
}

func TestNewRedis_TwoCallsIndependent(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set — requires a running local Redis instance")
	}

	redisCfg := config.RedisConfig{
		URL:         redisURL,
		PingTimeout: 5 * time.Second,
	}

	clientA, err := db.NewRedis(context.Background(), redisCfg)
	if err != nil {
		t.Fatalf("first NewRedis() error = %v", err)
	}
	defer clientA.Close()

	clientB, err := db.NewRedis(context.Background(), redisCfg)
	if err != nil {
		t.Fatalf("second NewRedis() error = %v", err)
	}
	defer clientB.Close()

	if clientA == clientB {
		t.Error("NewRedis() returned the same client pointer twice — singleton detected")
	}
}
