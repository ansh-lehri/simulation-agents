package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Candle is one OHLCV bar as cached in Redis. Prices are integer ticks
// and volume is an integer unit count — never floats. Open and close
// times are unix milliseconds so a canonical seed and a live-stream bar
// land in the same wire shape.
type Candle struct {
	OpenTime  int64 `json:"open_time"`
	Open      int64 `json:"open"`
	High      int64 `json:"high"`
	Low       int64 `json:"low"`
	Close     int64 `json:"close"`
	Volume    int64 `json:"volume"`
	CloseTime int64 `json:"close_time"`
}

// Quote is the latest bid/ask snapshot for one instrument on one
// exchange. Prices in ticks, quantities in units, timestamp in unix
// milliseconds. The quote key is overwrite-only — this struct carries
// no history.
type Quote struct {
	BidPrice  int64 `json:"bid_price"`
	AskPrice  int64 `json:"ask_price"`
	BidQty    int64 `json:"bid_qty"`
	AskQty    int64 `json:"ask_qty"`
	Timestamp int64 `json:"timestamp"`
}

// MarketDataStore writes the cached market-data window + latest quote.
// This system is a pure consumer / cache — no rollup, no derived
// timeframes. Serving timeframes other than the one written here is the
// broker's job.
type MarketDataStore interface {
	// WriteCandles seeds the OHLCV window for one (exchange, instrument,
	// timeframe) key. Replace semantics: the current list is dropped
	// atomically before the new bars are pushed, so a repeated call lands
	// the key in the same state regardless of what was there before.
	WriteCandles(ctx context.Context, exchangeID, instrument, timeframe string, bars []Candle) error

	// AppendCandle extends the OHLCV window with one live bar. Callers
	// are expected to feed bars in chronological order — the store does
	// not re-sort.
	AppendCandle(ctx context.Context, exchangeID, instrument, timeframe string, bar Candle) error

	// WriteQuote overwrites the latest-quote key. Last writer wins.
	WriteQuote(ctx context.Context, exchangeID, instrument string, quote Quote) error
}

// RedisMarketDataStore is the Redis-backed MarketDataStore.
type RedisMarketDataStore struct {
	client *redis.Client
}

// NewRedisMarketDataStore wraps a go-redis client. The caller retains
// ownership of the client's lifecycle.
func NewRedisMarketDataStore(client *redis.Client) *RedisMarketDataStore {
	return &RedisMarketDataStore{client: client}
}

func (marketStore *RedisMarketDataStore) WriteCandles(ctx context.Context, exchangeID, instrument, timeframe string, bars []Candle) error {
	key := CandleKey(exchangeID, instrument, timeframe)

	// Marshal first so a bad serialization never leaves the key in a
	// half-replaced state (DEL executed but RPUSH aborted).
	payloads := make([]any, 0, len(bars))
	for index, bar := range bars {
		encoded, err := json.Marshal(bar)
		if err != nil {
			return fmt.Errorf("store: marshal candle %d for %s/%s/%s: %w", index, exchangeID, instrument, timeframe, err)
		}
		payloads = append(payloads, encoded)
	}

	// Wrap DEL + RPUSH in a MULTI/EXEC transaction so a reader never
	// observes the key in a cleared-but-not-yet-filled state.
	pipeline := marketStore.client.TxPipeline()
	pipeline.Del(ctx, key)
	if len(payloads) > 0 {
		pipeline.RPush(ctx, key, payloads...)
	}
	if _, err := pipeline.Exec(ctx); err != nil {
		return fmt.Errorf("store: write candles for %s/%s/%s: %w", exchangeID, instrument, timeframe, err)
	}
	return nil
}

func (marketStore *RedisMarketDataStore) AppendCandle(ctx context.Context, exchangeID, instrument, timeframe string, bar Candle) error {
	encoded, err := json.Marshal(bar)
	if err != nil {
		return fmt.Errorf("store: marshal candle for %s/%s/%s: %w", exchangeID, instrument, timeframe, err)
	}
	key := CandleKey(exchangeID, instrument, timeframe)
	if err := marketStore.client.RPush(ctx, key, encoded).Err(); err != nil {
		return fmt.Errorf("store: append candle for %s/%s/%s: %w", exchangeID, instrument, timeframe, err)
	}
	return nil
}

func (marketStore *RedisMarketDataStore) WriteQuote(ctx context.Context, exchangeID, instrument string, quote Quote) error {
	encoded, err := json.Marshal(quote)
	if err != nil {
		return fmt.Errorf("store: marshal quote for %s/%s: %w", exchangeID, instrument, err)
	}
	key := QuoteKey(exchangeID, instrument)
	// Zero TTL means no expiry — the quote lives until the next writer
	// overwrites it or the key is explicitly cleared by teardown.
	if err := marketStore.client.Set(ctx, key, encoded, 0).Err(); err != nil {
		return fmt.Errorf("store: write quote for %s/%s: %w", exchangeID, instrument, err)
	}
	return nil
}
