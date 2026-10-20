package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"simulation-agents/store"
)

func makeCandle(openTime int64, open, high, low, closePrice, volume int64) store.Candle {
	return store.Candle{
		OpenTime:  openTime,
		Open:      open,
		High:      high,
		Low:       low,
		Close:     closePrice,
		Volume:    volume,
		CloseTime: openTime + 60_000, // one-minute bar
	}
}

// WriteCandles must push every bar in order under the right key, and the
// JSON on the wire must match the Candle shape.
func TestWriteCandles_StoresInOrder(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	marketStore := store.NewRedisMarketDataStore(client)

	bars := []store.Candle{
		makeCandle(1_700_000_000_000, 100, 110, 95, 105, 1000),
		makeCandle(1_700_000_060_000, 105, 115, 100, 112, 1200),
		makeCandle(1_700_000_120_000, 112, 120, 108, 118, 1500),
	}
	if err := marketStore.WriteCandles(ctx, "ex-1", "ACME", "1m", bars); err != nil {
		t.Fatalf("WriteCandles: %v", err)
	}

	key := store.CandleKey("ex-1", "ACME", "1m")
	raw, err := client.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		t.Fatalf("LRange: %v", err)
	}
	if len(raw) != len(bars) {
		t.Fatalf("candle count: got %d want %d", len(raw), len(bars))
	}
	for index, encoded := range raw {
		var decoded store.Candle
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			t.Fatalf("unmarshal candle %d: %v", index, err)
		}
		if decoded != bars[index] {
			t.Errorf("candle %d mismatch: got %+v want %+v", index, decoded, bars[index])
		}
	}
}

// Writing a second batch to the same key must replace the first — the
// contract is "seed the window from this history pull," not "accumulate."
func TestWriteCandles_ReplacesPrevious(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	marketStore := store.NewRedisMarketDataStore(client)

	firstBatch := []store.Candle{
		makeCandle(1_700_000_000_000, 100, 110, 95, 105, 1000),
		makeCandle(1_700_000_060_000, 105, 115, 100, 112, 1200),
	}
	if err := marketStore.WriteCandles(ctx, "ex-1", "ACME", "1m", firstBatch); err != nil {
		t.Fatalf("first WriteCandles: %v", err)
	}

	secondBatch := []store.Candle{
		makeCandle(1_700_000_120_000, 112, 120, 108, 118, 1500),
	}
	if err := marketStore.WriteCandles(ctx, "ex-1", "ACME", "1m", secondBatch); err != nil {
		t.Fatalf("second WriteCandles: %v", err)
	}

	key := store.CandleKey("ex-1", "ACME", "1m")
	length, err := client.LLen(ctx, key).Result()
	if err != nil {
		t.Fatalf("LLen: %v", err)
	}
	if length != int64(len(secondBatch)) {
		t.Fatalf("after replace: got %d candles want %d", length, len(secondBatch))
	}
}

// AppendCandle extends the window with one bar at the tail.
func TestAppendCandle_ExtendsTail(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	marketStore := store.NewRedisMarketDataStore(client)

	seed := []store.Candle{
		makeCandle(1_700_000_000_000, 100, 110, 95, 105, 1000),
	}
	if err := marketStore.WriteCandles(ctx, "ex-1", "ACME", "1m", seed); err != nil {
		t.Fatalf("WriteCandles seed: %v", err)
	}

	tail := makeCandle(1_700_000_060_000, 105, 115, 100, 112, 1200)
	if err := marketStore.AppendCandle(ctx, "ex-1", "ACME", "1m", tail); err != nil {
		t.Fatalf("AppendCandle: %v", err)
	}

	key := store.CandleKey("ex-1", "ACME", "1m")
	raw, err := client.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		t.Fatalf("LRange: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("length after append: got %d want 2", len(raw))
	}
	var lastBar store.Candle
	if err := json.Unmarshal([]byte(raw[1]), &lastBar); err != nil {
		t.Fatalf("unmarshal tail: %v", err)
	}
	if lastBar != tail {
		t.Errorf("tail bar mismatch: got %+v want %+v", lastBar, tail)
	}
}

// WriteQuote serializes to the quote key as JSON; last writer wins.
func TestWriteQuote_LastWriterWins(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	marketStore := store.NewRedisMarketDataStore(client)

	first := store.Quote{BidPrice: 100, AskPrice: 101, BidQty: 50, AskQty: 60, Timestamp: 1_700_000_000_000}
	second := store.Quote{BidPrice: 102, AskPrice: 103, BidQty: 55, AskQty: 65, Timestamp: 1_700_000_001_000}

	if err := marketStore.WriteQuote(ctx, "ex-1", "ACME", first); err != nil {
		t.Fatalf("first WriteQuote: %v", err)
	}
	if err := marketStore.WriteQuote(ctx, "ex-1", "ACME", second); err != nil {
		t.Fatalf("second WriteQuote: %v", err)
	}

	raw, err := client.Get(ctx, store.QuoteKey("ex-1", "ACME")).Bytes()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var decoded store.Quote
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal quote: %v", err)
	}
	if !reflect.DeepEqual(decoded, second) {
		t.Errorf("quote after overwrite: got %+v want %+v", decoded, second)
	}
}

// ReadCandles with a limit smaller than the stored window returns only
// the trailing `limit` bars in chronological order.
func TestReadCandles_BoundedWindow(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	marketStore := store.NewRedisMarketDataStore(client)

	bars := []store.Candle{
		makeCandle(1_700_000_000_000, 100, 110, 95, 105, 1000),
		makeCandle(1_700_000_060_000, 105, 115, 100, 112, 1200),
		makeCandle(1_700_000_120_000, 112, 120, 108, 118, 1500),
		makeCandle(1_700_000_180_000, 118, 125, 115, 122, 1100),
		makeCandle(1_700_000_240_000, 122, 130, 120, 128, 900),
	}
	if err := marketStore.WriteCandles(ctx, "ex-1", "ACME", "1m", bars); err != nil {
		t.Fatalf("WriteCandles: %v", err)
	}

	got, err := marketStore.ReadCandles(ctx, "ex-1", "ACME", "1m", 3)
	if err != nil {
		t.Fatalf("ReadCandles: %v", err)
	}
	want := bars[2:]
	if len(got) != len(want) {
		t.Fatalf("candle count: got %d want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("candle %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

// ReadCandles with a limit larger than the stored window returns what
// exists — no error, no padding.
func TestReadCandles_ShortWindow(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	marketStore := store.NewRedisMarketDataStore(client)

	bars := []store.Candle{
		makeCandle(1_700_000_000_000, 100, 110, 95, 105, 1000),
		makeCandle(1_700_000_060_000, 105, 115, 100, 112, 1200),
	}
	if err := marketStore.WriteCandles(ctx, "ex-1", "ACME", "1m", bars); err != nil {
		t.Fatalf("WriteCandles: %v", err)
	}

	got, err := marketStore.ReadCandles(ctx, "ex-1", "ACME", "1m", 100)
	if err != nil {
		t.Fatalf("ReadCandles: %v", err)
	}
	if len(got) != len(bars) {
		t.Fatalf("candle count: got %d want %d", len(got), len(bars))
	}
}

// ReadCandles on a key that has never been written returns an empty
// slice — not an error.
func TestReadCandles_AbsentKey(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	marketStore := store.NewRedisMarketDataStore(client)

	got, err := marketStore.ReadCandles(ctx, "ex-missing", "ACME", "1m", 50)
	if err != nil {
		t.Fatalf("ReadCandles on absent key: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice for absent key, got %d candles", len(got))
	}
}

// ReadQuote returns the quote that was written and signals present=true.
func TestReadQuote_Present(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	marketStore := store.NewRedisMarketDataStore(client)

	want := store.Quote{
		BidPrice:  1000,
		AskPrice:  1002,
		BidQty:    50,
		AskQty:    40,
		Timestamp: 1_700_000_000_000,
		LastPrice: 1001,
		HasLast:   true,
		Phase:     "open",
		HasBid:    true,
		HasAsk:    true,
		Depth: store.QuoteDepth{
			Bids: []store.QuoteLevel{{Price: 1000, Qty: 50}, {Price: 999, Qty: 30}},
			Asks: []store.QuoteLevel{{Price: 1002, Qty: 40}},
		},
	}
	if err := marketStore.WriteQuote(ctx, "ex-1", "ACME", want); err != nil {
		t.Fatalf("WriteQuote: %v", err)
	}

	got, present, err := marketStore.ReadQuote(ctx, "ex-1", "ACME")
	if err != nil {
		t.Fatalf("ReadQuote: %v", err)
	}
	if !present {
		t.Fatal("ReadQuote: expected present=true after write")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadQuote: got %+v want %+v", got, want)
	}
}

// ReadQuote on a key that has never been written returns present=false
// with a zero Quote and no error.
func TestReadQuote_Absent(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	marketStore := store.NewRedisMarketDataStore(client)

	got, present, err := marketStore.ReadQuote(ctx, "ex-missing", "ACME")
	if err != nil {
		t.Fatalf("ReadQuote on absent key: %v", err)
	}
	if present {
		t.Errorf("ReadQuote: expected present=false for absent key, got quote %+v", got)
	}
}

// Candle and quote keys are namespaced by (exchange, instrument, …) —
// writes to different tuples must not collide.
func TestMarketDataStore_KeyNamespacing(t *testing.T) {
	client := openTestRedis(t)
	ctx := context.Background()
	marketStore := store.NewRedisMarketDataStore(client)

	first := makeCandle(1_700_000_000_000, 100, 110, 95, 105, 1000)
	second := makeCandle(1_700_000_000_000, 200, 210, 195, 205, 2000)

	if err := marketStore.AppendCandle(ctx, "ex-1", "ACME", "1m", first); err != nil {
		t.Fatalf("AppendCandle ex-1: %v", err)
	}
	if err := marketStore.AppendCandle(ctx, "ex-2", "ACME", "1m", second); err != nil {
		t.Fatalf("AppendCandle ex-2: %v", err)
	}

	firstKey := store.CandleKey("ex-1", "ACME", "1m")
	secondKey := store.CandleKey("ex-2", "ACME", "1m")

	firstRaw, err := client.LIndex(ctx, firstKey, 0).Result()
	if err != nil {
		t.Fatalf("LIndex ex-1: %v", err)
	}
	secondRaw, err := client.LIndex(ctx, secondKey, 0).Result()
	if err != nil {
		t.Fatalf("LIndex ex-2: %v", err)
	}

	var firstDecoded, secondDecoded store.Candle
	if err := json.Unmarshal([]byte(firstRaw), &firstDecoded); err != nil {
		t.Fatalf("unmarshal ex-1: %v", err)
	}
	if err := json.Unmarshal([]byte(secondRaw), &secondDecoded); err != nil {
		t.Fatalf("unmarshal ex-2: %v", err)
	}
	if firstDecoded != first || secondDecoded != second {
		t.Errorf("cross-exchange write bled: ex-1=%+v ex-2=%+v", firstDecoded, secondDecoded)
	}
}
