package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

const (
	validDatabaseURL = "postgres://user:pass@localhost:5432/simdb?sslmode=disable"
	validRedisURL    = "redis://:pass@localhost:6379/0"
)

// envFrom builds an env getter backed by a static map.
// Missing keys return "" — identical behaviour to os.Getenv for unset vars.
func envFrom(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// baseEnv returns a minimal valid env with both required vars set.
func baseEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": validDatabaseURL,
		"REDIS_URL":    validRedisURL,
	}
}

// --- Required-var tests ---

func TestLoad_ValidAllDefaults(t *testing.T) {
	cfg, err := load(envFrom(baseEnv()))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Postgres.DSN != validDatabaseURL {
		t.Errorf("Postgres.DSN = %q, want %q", cfg.Postgres.DSN, validDatabaseURL)
	}
	if cfg.Redis.URL != validRedisURL {
		t.Errorf("Redis.URL = %q, want %q", cfg.Redis.URL, validRedisURL)
	}
	if cfg.Postgres.PingTimeout != defaultPingTimeout {
		t.Errorf("Postgres.PingTimeout = %v, want %v", cfg.Postgres.PingTimeout, defaultPingTimeout)
	}
	if cfg.Redis.PingTimeout != defaultPingTimeout {
		t.Errorf("Redis.PingTimeout = %v, want %v", cfg.Redis.PingTimeout, defaultPingTimeout)
	}
	if cfg.Postgres.MaxConns != 0 || cfg.Postgres.MinConns != 0 || cfg.Redis.PoolSize != 0 {
		t.Errorf("expected all optional overrides to be zero: MaxConns=%d MinConns=%d PoolSize=%d",
			cfg.Postgres.MaxConns, cfg.Postgres.MinConns, cfg.Redis.PoolSize)
	}
}

func TestLoad_MissingDatabaseURL(t *testing.T) {
	env := map[string]string{"REDIS_URL": validRedisURL}
	_, err := load(envFrom(env))
	if err == nil {
		t.Fatal("expected error for missing DATABASE_URL, got nil")
	}
	if !errors.Is(err, ErrMissingVar) {
		t.Errorf("errors.Is(err, ErrMissingVar) = false; err = %v", err)
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error message %q does not mention DATABASE_URL", err.Error())
	}
}

func TestLoad_MissingRedisURL(t *testing.T) {
	env := map[string]string{"DATABASE_URL": validDatabaseURL}
	_, err := load(envFrom(env))
	if err == nil {
		t.Fatal("expected error for missing REDIS_URL, got nil")
	}
	if !errors.Is(err, ErrMissingVar) {
		t.Errorf("errors.Is(err, ErrMissingVar) = false; err = %v", err)
	}
	if !strings.Contains(err.Error(), "REDIS_URL") {
		t.Errorf("error message %q does not mention REDIS_URL", err.Error())
	}
}

// --- URL validation tests ---

func TestLoad_MalformedDatabaseURL(t *testing.T) {
	cases := []string{"not-a-url", "://bad", "/relative/path", ""}
	for _, badURL := range cases {
		env := map[string]string{
			"DATABASE_URL": badURL,
			"REDIS_URL":    validRedisURL,
		}
		// empty string is caught by requireEnv, not validateURL — both are ErrConfigLoad subtypes
		_, err := load(envFrom(env))
		if err == nil {
			t.Errorf("expected error for DATABASE_URL=%q, got nil", badURL)
			continue
		}
		if !errors.Is(err, ErrConfigLoad) {
			t.Errorf("DATABASE_URL=%q: errors.Is(err, ErrConfigLoad) = false; err = %v", badURL, err)
		}
	}
}

func TestLoad_MalformedRedisURL(t *testing.T) {
	cases := []string{"not-a-url", "://bad", "/relative/path"}
	for _, badURL := range cases {
		env := map[string]string{
			"DATABASE_URL": validDatabaseURL,
			"REDIS_URL":    badURL,
		}
		_, err := load(envFrom(env))
		if err == nil {
			t.Errorf("expected error for REDIS_URL=%q, got nil", badURL)
			continue
		}
		if !errors.Is(err, ErrBadURL) {
			t.Errorf("REDIS_URL=%q: errors.Is(err, ErrBadURL) = false; err = %v", badURL, err)
		}
	}
}

// --- Ping timeout tests ---

func TestLoad_DefaultPingTimeout(t *testing.T) {
	cfg, err := load(envFrom(baseEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Postgres.PingTimeout != 5*time.Second {
		t.Errorf("Postgres.PingTimeout = %v, want 5s", cfg.Postgres.PingTimeout)
	}
	if cfg.Redis.PingTimeout != 5*time.Second {
		t.Errorf("Redis.PingTimeout = %v, want 5s", cfg.Redis.PingTimeout)
	}
}

func TestLoad_CustomPingTimeout(t *testing.T) {
	env := baseEnv()
	env["DB_PING_TIMEOUT"] = "10s"
	cfg, err := load(envFrom(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Postgres.PingTimeout != 10*time.Second {
		t.Errorf("Postgres.PingTimeout = %v, want 10s", cfg.Postgres.PingTimeout)
	}
	if cfg.Redis.PingTimeout != 10*time.Second {
		t.Errorf("Redis.PingTimeout = %v, want 10s", cfg.Redis.PingTimeout)
	}
}

func TestLoad_MalformedPingTimeout(t *testing.T) {
	env := baseEnv()
	env["DB_PING_TIMEOUT"] = "notaduration"
	_, err := load(envFrom(env))
	if err == nil {
		t.Fatal("expected error for malformed DB_PING_TIMEOUT, got nil")
	}
	if !errors.Is(err, ErrBadDuration) {
		t.Errorf("errors.Is(err, ErrBadDuration) = false; err = %v", err)
	}
}

// --- Optional pool override tests ---

func TestLoad_PoolOverridesSet(t *testing.T) {
	env := baseEnv()
	env["PG_MAX_CONNS"] = "10"
	env["PG_MIN_CONNS"] = "2"
	env["REDIS_POOL_SIZE"] = "5"
	cfg, err := load(envFrom(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Postgres.MaxConns != 10 {
		t.Errorf("Postgres.MaxConns = %d, want 10", cfg.Postgres.MaxConns)
	}
	if cfg.Postgres.MinConns != 2 {
		t.Errorf("Postgres.MinConns = %d, want 2", cfg.Postgres.MinConns)
	}
	if cfg.Redis.PoolSize != 5 {
		t.Errorf("Redis.PoolSize = %d, want 5", cfg.Redis.PoolSize)
	}
}

func TestLoad_PoolOverrideMalformed(t *testing.T) {
	cases := []struct {
		varName string
		value   string
	}{
		{"PG_MAX_CONNS", "notanint"},
		{"PG_MIN_CONNS", "1.5"},
		{"REDIS_POOL_SIZE", "abc"},
	}
	for _, tc := range cases {
		env := baseEnv()
		env[tc.varName] = tc.value
		_, err := load(envFrom(env))
		if err == nil {
			t.Errorf("%s=%q: expected error, got nil", tc.varName, tc.value)
			continue
		}
		if !errors.Is(err, ErrBadInt) {
			t.Errorf("%s=%q: errors.Is(err, ErrBadInt) = false; err = %v", tc.varName, tc.value, err)
		}
	}
}

// --- Determinism ---

func TestLoad_Determinism(t *testing.T) {
	env := baseEnv()
	env["DB_PING_TIMEOUT"] = "7s"
	env["PG_MAX_CONNS"] = "4"
	getter := envFrom(env)

	first, err1 := load(getter)
	second, err2 := load(getter)
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v / %v", err1, err2)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("load() is not deterministic:\n  first:  %+v\n  second: %+v", first, second)
	}
}

// --- Property: never panics, never returns partial Config ---

// TestLoad_NeverPanicsOrPartialReturn checks that for any combination of env values,
// load() either returns a fully-populated Config with nil error, or a nil Config
// with a typed error that wraps ErrConfigLoad. It never panics or returns both.
func TestLoad_NeverPanicsOrPartialReturn(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dbURL := rapid.String().Draw(rt, "DATABASE_URL")
		redisURL := rapid.String().Draw(rt, "REDIS_URL")
		pingTimeout := rapid.String().Draw(rt, "DB_PING_TIMEOUT")
		pgMaxConns := rapid.String().Draw(rt, "PG_MAX_CONNS")
		pgMinConns := rapid.String().Draw(rt, "PG_MIN_CONNS")
		redisPoolSize := rapid.String().Draw(rt, "REDIS_POOL_SIZE")

		env := map[string]string{
			"DATABASE_URL":    dbURL,
			"REDIS_URL":       redisURL,
			"DB_PING_TIMEOUT": pingTimeout,
			"PG_MAX_CONNS":    pgMaxConns,
			"PG_MIN_CONNS":    pgMinConns,
			"REDIS_POOL_SIZE": redisPoolSize,
		}

		cfg, err := load(envFrom(env))

		if err != nil {
			// Every error from load() must wrap ErrConfigLoad.
			if !errors.Is(err, ErrConfigLoad) {
				rt.Fatalf("load() returned error not wrapping ErrConfigLoad: %v", err)
			}
			// Config must be zero on error — no partial state.
			if cfg != (Config{}) {
				rt.Fatalf("load() returned non-zero Config alongside error: cfg=%+v err=%v", cfg, err)
			}
			return
		}

		// On success, Config must be fully populated.
		if cfg.Postgres.DSN == "" {
			rt.Fatalf("load() returned nil error but empty Postgres.DSN")
		}
		if cfg.Redis.URL == "" {
			rt.Fatalf("load() returned nil error but empty Redis.URL")
		}
		if cfg.Postgres.PingTimeout <= 0 {
			rt.Fatalf("load() returned nil error but non-positive Postgres.PingTimeout: %v", cfg.Postgres.PingTimeout)
		}
		if cfg.Redis.PingTimeout <= 0 {
			rt.Fatalf("load() returned nil error but non-positive Redis.PingTimeout: %v", cfg.Redis.PingTimeout)
		}
	})
}
