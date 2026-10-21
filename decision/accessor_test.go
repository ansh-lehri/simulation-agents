package decision

import (
	"testing"
	"time"

	"simulation-agents/store"
)

// --- fixtures

func makeCandles(closes ...int64) []store.Candle {
	bars := make([]store.Candle, len(closes))
	for idx, close := range closes {
		bars[idx] = store.Candle{
			Close:  close,
			High:   close + 10,
			Low:    close - 10,
			Volume: 1000 + int64(idx)*100,
		}
	}
	return bars
}

func singleInstAccessor(inst, tf string, candles []store.Candle, quote store.Quote, present bool) *Accessor {
	candleMap := map[string]map[string][]store.Candle{
		inst: {tf: candles},
	}
	quotes := map[string]store.Quote{inst: quote}
	presentMap := map[string]bool{inst: present}
	return NewAccessor(candleMap, quotes, presentMap, TickContext{})
}

// --- Closes

func TestAccessor_Closes_LastNBars(t *testing.T) {
	candles := makeCandles(100, 110, 120, 130, 140)
	acc := singleInstAccessor("AAPL", "1m", candles, store.Quote{}, false)

	got := acc.Closes("AAPL", "1m", 3)
	want := []float64{120, 130, 140}
	if len(got) != len(want) {
		t.Fatalf("Closes len: got %d want %d", len(got), len(want))
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("Closes[%d]: got %v want %v", i, got[i], v)
		}
	}
}

func TestAccessor_Closes_FewerThanNReturnsAll(t *testing.T) {
	candles := makeCandles(100, 110)
	acc := singleInstAccessor("AAPL", "1m", candles, store.Quote{}, false)

	got := acc.Closes("AAPL", "1m", 10)
	if len(got) != 2 {
		t.Fatalf("Closes with fewer bars: got %d want 2", len(got))
	}
}

func TestAccessor_Closes_AbsentInstrumentReturnsNil(t *testing.T) {
	acc := NewAccessor(nil, nil, nil, TickContext{})
	if got := acc.Closes("ABSENT", "1m", 5); got != nil {
		t.Fatalf("Closes absent instrument: want nil, got %v", got)
	}
}

func TestAccessor_Closes_AbsentTimeframeReturnsNil(t *testing.T) {
	candles := makeCandles(100, 110)
	acc := singleInstAccessor("AAPL", "1m", candles, store.Quote{}, false)

	if got := acc.Closes("AAPL", "5m", 2); got != nil {
		t.Fatalf("Closes absent timeframe: want nil, got %v", got)
	}
}

// --- Highs / Lows / Volumes

func TestAccessor_Highs_ReturnsHighColumn(t *testing.T) {
	candles := makeCandles(100, 200)
	acc := singleInstAccessor("AAPL", "1m", candles, store.Quote{}, false)

	got := acc.Highs("AAPL", "1m", 2)
	if len(got) != 2 || got[0] != 110 || got[1] != 210 {
		t.Fatalf("Highs: got %v", got)
	}
}

func TestAccessor_Lows_ReturnsLowColumn(t *testing.T) {
	candles := makeCandles(100, 200)
	acc := singleInstAccessor("AAPL", "1m", candles, store.Quote{}, false)

	got := acc.Lows("AAPL", "1m", 2)
	if len(got) != 2 || got[0] != 90 || got[1] != 190 {
		t.Fatalf("Lows: got %v", got)
	}
}

func TestAccessor_Volumes_ReturnsVolumeColumn(t *testing.T) {
	candles := makeCandles(100, 200) // volumes are 1000, 1100 from fixture
	acc := singleInstAccessor("AAPL", "1m", candles, store.Quote{}, false)

	got := acc.Volumes("AAPL", "1m", 2)
	if len(got) != 2 || got[0] != 1000 || got[1] != 1100 {
		t.Fatalf("Volumes: got %v", got)
	}
}

// --- BarCount

func TestAccessor_BarCount_KnownInstrument(t *testing.T) {
	candles := makeCandles(100, 110, 120)
	acc := singleInstAccessor("AAPL", "1m", candles, store.Quote{}, false)

	if got := acc.BarCount("AAPL", "1m"); got != 3 {
		t.Fatalf("BarCount: got %d want 3", got)
	}
}

func TestAccessor_BarCount_AbsentInstrumentReturnsZero(t *testing.T) {
	acc := NewAccessor(nil, nil, nil, TickContext{})
	if got := acc.BarCount("ABSENT", "1m"); got != 0 {
		t.Fatalf("BarCount absent: got %d want 0", got)
	}
}

// --- CurrentPrice

func TestAccessor_CurrentPrice_UsesLastPriceWhenHasLastTrue(t *testing.T) {
	candles := makeCandles(100)
	quote := store.Quote{HasLast: true, LastPrice: 999}
	acc := singleInstAccessor("AAPL", "1m", candles, quote, true)

	price, ok := acc.CurrentPrice("AAPL", "1m")
	if !ok {
		t.Fatal("CurrentPrice: ok=false, want true")
	}
	if price != 999 {
		t.Fatalf("CurrentPrice: got %d want 999 (LastPrice)", price)
	}
}

func TestAccessor_CurrentPrice_FallsBackToCandleClose(t *testing.T) {
	candles := makeCandles(100, 200, 300)
	quote := store.Quote{HasLast: false}
	acc := singleInstAccessor("AAPL", "1m", candles, quote, true)

	price, ok := acc.CurrentPrice("AAPL", "1m")
	if !ok {
		t.Fatal("CurrentPrice fallback: ok=false, want true")
	}
	if price != 300 {
		t.Fatalf("CurrentPrice fallback: got %d want 300 (last bar close)", price)
	}
}

func TestAccessor_CurrentPrice_AbsentInstrumentReturnsFalse(t *testing.T) {
	acc := NewAccessor(nil, nil, nil, TickContext{})
	_, ok := acc.CurrentPrice("ABSENT", "1m")
	if ok {
		t.Fatal("CurrentPrice absent: want ok=false")
	}
}

func TestAccessor_CurrentPrice_NoBarsNoQuoteReturnsFalse(t *testing.T) {
	candleMap := map[string]map[string][]store.Candle{"AAPL": {"1m": nil}}
	quotes := map[string]store.Quote{"AAPL": {HasLast: false}}
	present := map[string]bool{"AAPL": true}
	acc := NewAccessor(candleMap, quotes, present, TickContext{})

	_, ok := acc.CurrentPrice("AAPL", "1m")
	if ok {
		t.Fatal("CurrentPrice no bars no quote: want ok=false")
	}
}

// --- Spread

func TestAccessor_Spread_ReturnsAskMinusBid(t *testing.T) {
	quote := store.Quote{HasBid: true, BidPrice: 100, HasAsk: true, AskPrice: 105}
	acc := singleInstAccessor("AAPL", "1m", nil, quote, true)

	spread, ok := acc.Spread("AAPL")
	if !ok {
		t.Fatal("Spread: ok=false, want true")
	}
	if spread != 5 {
		t.Fatalf("Spread: got %d want 5", spread)
	}
}

func TestAccessor_Spread_AbsentQuoteReturnsFalse(t *testing.T) {
	acc := NewAccessor(nil, nil, nil, TickContext{})
	_, ok := acc.Spread("ABSENT")
	if ok {
		t.Fatal("Spread absent: want ok=false")
	}
}

func TestAccessor_Spread_PartialQuoteMissingAskReturnsFalse(t *testing.T) {
	quote := store.Quote{HasBid: true, BidPrice: 100, HasAsk: false}
	acc := singleInstAccessor("AAPL", "1m", nil, quote, true)

	_, ok := acc.Spread("AAPL")
	if ok {
		t.Fatal("Spread missing ask: want ok=false")
	}
}

// --- DepthQty

func makeQuoteWithDepth() store.Quote {
	return store.Quote{
		HasBid: true,
		HasAsk: true,
		Depth: store.QuoteDepth{
			Bids: []store.QuoteLevel{{Price: 100, Qty: 300}, {Price: 99, Qty: 200}},
			Asks: []store.QuoteLevel{{Price: 101, Qty: 150}, {Price: 102, Qty: 250}},
		},
	}
}

func TestAccessor_DepthQty_BidSumTopLevels(t *testing.T) {
	acc := singleInstAccessor("AAPL", "1m", nil, makeQuoteWithDepth(), true)
	if got := acc.DepthQty("AAPL", 2, sideBid); got != 500 {
		t.Fatalf("DepthQty bid: got %d want 500", got)
	}
}

func TestAccessor_DepthQty_AskSumTopLevels(t *testing.T) {
	acc := singleInstAccessor("AAPL", "1m", nil, makeQuoteWithDepth(), true)
	if got := acc.DepthQty("AAPL", 2, sideAsk); got != 400 {
		t.Fatalf("DepthQty ask: got %d want 400", got)
	}
}

func TestAccessor_DepthQty_OneLevelOnly(t *testing.T) {
	acc := singleInstAccessor("AAPL", "1m", nil, makeQuoteWithDepth(), true)
	if got := acc.DepthQty("AAPL", 1, sideBid); got != 300 {
		t.Fatalf("DepthQty bid 1 level: got %d want 300", got)
	}
}

func TestAccessor_DepthQty_AbsentReturnsZero(t *testing.T) {
	acc := NewAccessor(nil, nil, nil, TickContext{})
	if got := acc.DepthQty("ABSENT", 2, sideBid); got != 0 {
		t.Fatalf("DepthQty absent: got %d want 0", got)
	}
}

// --- Imbalance

func TestAccessor_Imbalance_HappyPath(t *testing.T) {
	// bid total 600, ask total 400 → (600-400)/(600+400) = 0.2
	quote := store.Quote{
		HasBid: true,
		HasAsk: true,
		Depth: store.QuoteDepth{
			Bids: []store.QuoteLevel{{Price: 100, Qty: 600}},
			Asks: []store.QuoteLevel{{Price: 101, Qty: 400}},
		},
	}
	acc := singleInstAccessor("AAPL", "1m", nil, quote, true)

	imbalance, ok := acc.Imbalance("AAPL", 2)
	if !ok {
		t.Fatal("Imbalance: ok=false, want true")
	}
	const want = 0.2
	if imbalance < want-1e-9 || imbalance > want+1e-9 {
		t.Fatalf("Imbalance: got %v want %v", imbalance, want)
	}
}

func TestAccessor_Imbalance_ZeroTotalReturnsFalse(t *testing.T) {
	quote := store.Quote{
		HasBid: true,
		HasAsk: true,
		Depth:  store.QuoteDepth{},
	}
	acc := singleInstAccessor("AAPL", "1m", nil, quote, true)

	_, ok := acc.Imbalance("AAPL", 2)
	if ok {
		t.Fatal("Imbalance zero total: want ok=false")
	}
}

func TestAccessor_Imbalance_AbsentReturnsFalse(t *testing.T) {
	acc := NewAccessor(nil, nil, nil, TickContext{})
	_, ok := acc.Imbalance("ABSENT", 2)
	if ok {
		t.Fatal("Imbalance absent: want ok=false")
	}
}

// --- Phase

func TestAccessor_Phase_ReturnsQuotePhase(t *testing.T) {
	quote := store.Quote{Phase: "continuous"}
	acc := singleInstAccessor("AAPL", "1m", nil, quote, true)

	if got := acc.Phase("AAPL"); got != "continuous" {
		t.Fatalf("Phase: got %q want %q", got, "continuous")
	}
}

func TestAccessor_Phase_AbsentReturnsEmpty(t *testing.T) {
	acc := NewAccessor(nil, nil, nil, TickContext{})
	if got := acc.Phase("ABSENT"); got != "" {
		t.Fatalf("Phase absent: got %q want empty", got)
	}
}

// --- SessionProgress

func TestAccessor_SessionProgress_AtStart(t *testing.T) {
	start := time.Date(2024, 1, 2, 9, 30, 0, 0, time.UTC)
	end := start.Add(6 * time.Hour)
	tick := TickContext{Now: start, SessionStart: start, SessionEnd: end}
	acc := NewAccessor(nil, nil, nil, tick)

	if got := acc.SessionProgress(); got != 0.0 {
		t.Fatalf("SessionProgress at start: got %v want 0.0", got)
	}
}

func TestAccessor_SessionProgress_AtEnd(t *testing.T) {
	start := time.Date(2024, 1, 2, 9, 30, 0, 0, time.UTC)
	end := start.Add(6 * time.Hour)
	tick := TickContext{Now: end, SessionStart: start, SessionEnd: end}
	acc := NewAccessor(nil, nil, nil, tick)

	if got := acc.SessionProgress(); got != 1.0 {
		t.Fatalf("SessionProgress at end: got %v want 1.0", got)
	}
}

func TestAccessor_SessionProgress_Midpoint(t *testing.T) {
	start := time.Date(2024, 1, 2, 9, 30, 0, 0, time.UTC)
	end := start.Add(6 * time.Hour)
	mid := start.Add(3 * time.Hour)
	tick := TickContext{Now: mid, SessionStart: start, SessionEnd: end}
	acc := NewAccessor(nil, nil, nil, tick)

	const want = 0.5
	got := acc.SessionProgress()
	if got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("SessionProgress midpoint: got %v want %v", got, want)
	}
}

// --- Present

func TestAccessor_Present_TrueWhenPresent(t *testing.T) {
	present := map[string]bool{"AAPL": true, "GOOG": false}
	acc := NewAccessor(nil, nil, present, TickContext{})

	if !acc.Present("AAPL") {
		t.Fatal("Present AAPL: want true")
	}
	if acc.Present("GOOG") {
		t.Fatal("Present GOOG: want false")
	}
}

func TestAccessor_Present_AbsentKeyReturnsFalse(t *testing.T) {
	acc := NewAccessor(nil, nil, nil, TickContext{})
	if acc.Present("ABSENT") {
		t.Fatal("Present absent key: want false")
	}
}

// --- Candles

func TestAccessor_Candles_ReturnsFullSlice(t *testing.T) {
	candles := makeCandles(100, 200, 300)
	acc := singleInstAccessor("AAPL", "1m", candles, store.Quote{}, false)

	got := acc.Candles("AAPL", "1m")
	if len(got) != 3 {
		t.Fatalf("Candles: got %d bars want 3", len(got))
	}
}

func TestAccessor_Candles_AbsentReturnsNil(t *testing.T) {
	acc := NewAccessor(nil, nil, nil, TickContext{})
	if got := acc.Candles("ABSENT", "1m"); got != nil {
		t.Fatalf("Candles absent: want nil, got %v", got)
	}
}

// --- candleWindow bounds

func TestAccessor_CandleWindow_ExactN(t *testing.T) {
	candles := makeCandles(1, 2, 3, 4, 5)
	acc := singleInstAccessor("AAPL", "1m", candles, store.Quote{}, false)

	got := acc.Closes("AAPL", "1m", 5)
	if len(got) != 5 {
		t.Fatalf("Closes exact N: got %d want 5", len(got))
	}
}

func TestAccessor_CandleWindow_ZeroNReturnsNil(t *testing.T) {
	candles := makeCandles(1, 2, 3)
	acc := singleInstAccessor("AAPL", "1m", candles, store.Quote{}, false)

	if got := acc.Closes("AAPL", "1m", 0); got != nil {
		t.Fatalf("Closes n=0: want nil, got %v", got)
	}
}
