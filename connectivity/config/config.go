package config

import "time"

// Config holds all runtime infrastructure connection configuration.
type Config struct {
	Postgres PostgresConfig
	Redis    RedisConfig
}

// PostgresConfig carries Postgres connection parameters.
// DSN is the single authority for host, port, credentials, sslmode, and pool params
// (via query args such as pool_max_conns). MaxConns/MinConns override DSN pool args
// when non-zero; zero means defer to DSN or library default.
type PostgresConfig struct {
	DSN         string        // full pgx DSN, e.g. postgres://user:pass@host:5432/db?sslmode=disable
	MaxConns    int32         // 0 = defer to pool_max_conns query param or library default
	MinConns    int32         // 0 = defer to pool_min_conns query param or library default
	PingTimeout time.Duration // bounded startup ping deadline; sourced from DB_PING_TIMEOUT
}

// RedisConfig carries Redis connection parameters.
// URL is the single authority; redis:// for plain, rediss:// for TLS.
// PoolSize overrides the library default when non-zero.
type RedisConfig struct {
	URL         string        // full go-redis URL, e.g. redis://:pass@host:6379/0
	PoolSize    int           // 0 = defer to library default
	PingTimeout time.Duration // bounded startup ping deadline; sourced from DB_PING_TIMEOUT
}
