package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"simulation-agents/connectivity/config"
)

// NewRedis parses the Redis URL, pings within redisCfg.PingTimeout, and returns
// the live client. The caller owns client.Close(). Fails at startup if the
// server is unreachable — never defers the error to the first real command.
// rediss:// scheme enables TLS automatically (system cert pool).
// PoolSize on redisCfg overrides the library default when non-zero.
func NewRedis(ctx context.Context, redisCfg config.RedisConfig) (*redis.Client, error) {
	slog.Debug("connectivity/db: opening redis client")

	opts, err := redis.ParseURL(redisCfg.URL)
	if err != nil {
		return nil, fmt.Errorf("connectivity/db: redis: parse URL: %w", err)
	}

	if redisCfg.PoolSize > 0 {
		opts.PoolSize = redisCfg.PoolSize
	}

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, redisCfg.PingTimeout)
	defer cancel()

	if pingErr := client.Ping(pingCtx).Err(); pingErr != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connectivity/db: redis: ping failed (addr=%s db=%d timeout=%s): %w",
			opts.Addr, opts.DB, redisCfg.PingTimeout, pingErr)
	}

	slog.Info("connectivity/db: redis client opened",
		"addr", opts.Addr,
		"db", opts.DB,
		"pool_size", opts.PoolSize,
	)

	return client, nil
}
