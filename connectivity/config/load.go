package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

const defaultPingTimeout = 5 * time.Second

// Load reads env vars once, validates them, applies defaults, and returns a
// fully-populated Config. It is the only place in the codebase that reads env.
// Fails fast on the first missing required var or malformed value — never
// returns a partial Config alongside a non-nil error.
func Load() (Config, error) {
	return load(os.Getenv)
}

// load is the testable core; Load delegates here with os.Getenv.
func load(getenv func(string) string) (Config, error) {
	dbDSN, err := requireEnv(getenv, "DATABASE_URL")
	if err != nil {
		return Config{}, err
	}

	redisURL, err := requireEnv(getenv, "REDIS_URL")
	if err != nil {
		return Config{}, err
	}

	if err := validateAbsoluteURL("DATABASE_URL", dbDSN); err != nil {
		return Config{}, err
	}
	if err := validateAbsoluteURL("REDIS_URL", redisURL); err != nil {
		return Config{}, err
	}

	pingTimeout := defaultPingTimeout
	if raw := getenv("DB_PING_TIMEOUT"); raw != "" {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil {
			return Config{}, fmt.Errorf("%w: DB_PING_TIMEOUT=%q: %s", ErrBadDuration, raw, parseErr)
		}
		pingTimeout = parsed
	}

	maxConns, err := parseOptionalInt32(getenv, "PG_MAX_CONNS")
	if err != nil {
		return Config{}, err
	}
	minConns, err := parseOptionalInt32(getenv, "PG_MIN_CONNS")
	if err != nil {
		return Config{}, err
	}
	poolSize, err := parseOptionalInt(getenv, "REDIS_POOL_SIZE")
	if err != nil {
		return Config{}, err
	}

	return Config{
		Postgres: PostgresConfig{
			DSN:         dbDSN,
			MaxConns:    maxConns,
			MinConns:    minConns,
			PingTimeout: pingTimeout,
		},
		Redis: RedisConfig{
			URL:         redisURL,
			PoolSize:    poolSize,
			PingTimeout: pingTimeout,
		},
	}, nil
}

// requireEnv returns the env value or ErrMissingEnvVar if it is empty/unset.
func requireEnv(getenv func(string) string, varName string) (string, error) {
	val := getenv(varName)
	if val == "" {
		return "", ErrMissingEnvVar{VarName: varName}
	}
	return val, nil
}

// validateAbsoluteURL checks the value has a non-empty URL scheme, which is the
// minimum bar for a usable DSN. Full driver-level validation happens in the
// constructors; this catches obviously wrong values early.
func validateAbsoluteURL(varName, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("%w: %s=%q: must be an absolute URL with a scheme (e.g. postgres:// or redis://)", ErrBadURL, varName, rawURL)
	}
	return nil
}

func parseOptionalInt32(getenv func(string) string, varName string) (int32, error) {
	raw := getenv(varName)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: %s=%q: %s", ErrBadInt, varName, raw, err)
	}
	return int32(v), nil
}

func parseOptionalInt(getenv func(string) string, varName string) (int, error) {
	raw := getenv(varName)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: %s=%q: %s", ErrBadInt, varName, raw, err)
	}
	return v, nil
}
