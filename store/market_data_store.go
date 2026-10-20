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

// QuoteLevel is one price-quantity pair in the order-book depth snapshot.
// Price is integer ticks; Qty is integer units.
type QuoteLevel struct {
	Price int64 `json:"price"`
	Qty   int64 `json:"qty"`
}

// QuoteDepth carries the top-of-book depth for one instrument. Both slices
// are in price-priority order as delivered by the live feed: bids descending,
// asks ascending.
type QuoteDepth struct {
	Bids []QuoteLevel `json:"bids"`
	Asks []QuoteLevel `json:"asks"`
}

// Quote is the latest market state for one instrument on one exchange.
// Prices and quantities are integer (ticks / units); Timestamp is unix
// milliseconds. The quote key is overwrite-only — this struct carries no
// history.
//
// HasLast gates whether LastPrice is meaningful: a freshly started market
// may not have printed a trade yet. HasBid and HasAsk gate the top-of-book
// quantities; a market with no resting orders sets them false. Phase carries
// the exchange's current trading phase (e.g. "open", "pre-open").
type Quote struct {
	BidPrice  int64      `json:"bid_price"`
	AskPrice  int64      `json:"ask_price"`
	BidQty    int64      `json:"bid_qty"`
	AskQty    int64      `json:"ask_qty"`
	Timestamp int64      `json:"timestamp"`
	LastPrice int64      `json:"last_price"`
	HasLast   bool       `json:"has_last"`
	Phase     string     `json:"phase"`
	HasBid    bool       `json:"has_bid"`
	HasAsk    bool       `json:"has_ask"`
	Depth     QuoteDepth `json:"depth"`
}

// MarketDataStore writes and reads the cached market-data window + latest
// quote. This system is a pure consumer / cache — no rollup, no derived
// timeframes. Which timeframes are populated is a caller-side decision:
// the history pull reads a configured list from the subset {1m, 5m, 30m,
// 60m} and writes each cleanly from Core. Serving a timeframe that was NOT
// written here is the broker's job.
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

	// ReadCandles returns the trailing window of at most limit bars for one
	// (exchange, instrument, timeframe) key. Uses a bounded tail read so a
	// late-session tick does not read the entire accumulated history. A short
	// or absent list is not an error — callers treat it as a partial or empty
	// window and degrade gracefully.
	ReadCandles(ctx context.Context, exchangeID, instrument, timeframe string, limit int) ([]Candle, error)

	// ReadQuote returns the latest cached quote for one (exchange, instrument)
	// pair. The bool is false when the key is absent — the ingest goroutine may
	// not have written it yet. A false present is not an error; callers treat
	// the instrument as flat for this tick.
	ReadQuote(ctx context.Context, exchangeID, instrument string) (Quote, bool, error)
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

func (marketStore *RedisMarketDataStore) ReadCandles(ctx context.Context, exchangeID, instrument, timeframe string, limit int) ([]Candle, error) {
	if limit <= 0 {
		return nil, nil
	}
	key := CandleKey(exchangeID, instrument, timeframe)
	// LRANGE -limit -1 returns the last `limit` elements. When the list is
	// shorter than limit, Redis returns what exists — no error, no padding.
	raw, err := marketStore.client.LRange(ctx, key, int64(-limit), -1).Result()
	if err != nil {
		return nil, fmt.Errorf("store: read candles for %s/%s/%s: %w", exchangeID, instrument, timeframe, err)
	}
	candles := make([]Candle, 0, len(raw))
	for barIndex, encoded := range raw {
		var bar Candle
		if err := json.Unmarshal([]byte(encoded), &bar); err != nil {
			return nil, fmt.Errorf("store: unmarshal candle %d for %s/%s/%s: %w", barIndex, exchangeID, instrument, timeframe, err)
		}
		candles = append(candles, bar)
	}
	return candles, nil
}

func (marketStore *RedisMarketDataStore) ReadQuote(ctx context.Context, exchangeID, instrument string) (Quote, bool, error) {
	key := QuoteKey(exchangeID, instrument)
	raw, err := marketStore.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return Quote{}, false, nil
	}
	if err != nil {
		return Quote{}, false, fmt.Errorf("store: read quote for %s/%s: %w", exchangeID, instrument, err)
	}
	var quote Quote
	if err := json.Unmarshal(raw, &quote); err != nil {
		return Quote{}, false, fmt.Errorf("store: unmarshal quote for %s/%s: %w", exchangeID, instrument, err)
	}
	return quote, true, nil
}
