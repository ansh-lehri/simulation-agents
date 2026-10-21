package decision

import (
	"math"
	"testing"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// makeLiquidityAccessor builds an Accessor with a single instrument quote and
// the instrument marked present.
func makeLiquidityAccessor(inst string, q store.Quote, present bool) *Accessor {
	return NewAccessor(
		map[string]map[string][]store.Candle{
			inst: {"1m": {{Close: q.LastPrice, High: q.LastPrice, Low: q.LastPrice, Open: q.LastPrice, Volume: 100}}},
		},
		map[string]store.Quote{inst: q},
		map[string]bool{inst: present},
		TickContext{},
	)
}

func liquidityBlock(levels int) *agent.ChannelBlock {
	return &agent.ChannelBlock{
		ReadingParams: map[string]float64{"depth_levels": float64(levels)},
	}
}

func TestPerceiveLiquidity_Happy(t *testing.T) {
	q := store.Quote{
		BidPrice: 990, AskPrice: 1010, HasBid: true, HasAsk: true,
		LastPrice: 1000, HasLast: true,
		Depth: store.QuoteDepth{
			Bids: []store.QuoteLevel{{Price: 990, Qty: 100}},
			Asks: []store.QuoteLevel{{Price: 1010, Qty: 80}},
		},
	}
	acc := makeLiquidityAccessor("AAPL", q, true)
	r := perceiveLiquidity(liquidityBlock(1), acc, "AAPL", "1m")

	if !r.ReadOK {
		t.Fatal("ReadOK must be true when bid+ask present")
	}
	// spread = 20 ticks / 1000 price = 0.02
	wantSpread := 20.0 / 1000.0
	if math.Abs(r.Spread-wantSpread) > 1e-9 {
		t.Errorf("Spread: want %v, got %v", wantSpread, r.Spread)
	}
	// depth = 100 + 80 = 180
	if r.Depth != 180 {
		t.Errorf("Depth: want 180, got %v", r.Depth)
	}
	// imbalance = (100-80)/(100+80) = 20/180
	wantImb := 20.0 / 180.0
	if math.Abs(r.Imbalance-wantImb) > 1e-9 {
		t.Errorf("Imbalance: want %v, got %v", wantImb, r.Imbalance)
	}
}

func TestPerceiveLiquidity_NoQuote(t *testing.T) {
	acc := NewAccessor(
		map[string]map[string][]store.Candle{},
		map[string]store.Quote{},
		map[string]bool{"AAPL": true},
		TickContext{},
	)
	r := perceiveLiquidity(liquidityBlock(1), acc, "AAPL", "1m")
	if r.ReadOK {
		t.Fatal("ReadOK must be false when no quote available")
	}
}

func TestPerceiveLiquidity_Absent(t *testing.T) {
	acc := makeLiquidityAccessor("AAPL", store.Quote{HasBid: true, HasAsk: true, BidPrice: 90, AskPrice: 110, LastPrice: 100}, false)
	r := perceiveLiquidity(liquidityBlock(1), acc, "AAPL", "1m")
	if r.ReadOK {
		t.Fatal("ReadOK must be false when instrument absent")
	}
}

func TestPerceiveLiquidity_NoBid(t *testing.T) {
	q := store.Quote{AskPrice: 1010, HasAsk: true, LastPrice: 1000, HasLast: true}
	acc := makeLiquidityAccessor("AAPL", q, true)
	r := perceiveLiquidity(liquidityBlock(1), acc, "AAPL", "1m")
	if r.ReadOK {
		t.Fatal("ReadOK must be false when no bid")
	}
}

func TestPerceiveLiquidity_MultiLevel(t *testing.T) {
	q := store.Quote{
		BidPrice: 990, AskPrice: 1010, HasBid: true, HasAsk: true,
		LastPrice: 1000, HasLast: true,
		Depth: store.QuoteDepth{
			Bids: []store.QuoteLevel{{Price: 990, Qty: 50}, {Price: 980, Qty: 50}},
			Asks: []store.QuoteLevel{{Price: 1010, Qty: 40}, {Price: 1020, Qty: 40}},
		},
	}
	acc := makeLiquidityAccessor("AAPL", q, true)
	r := perceiveLiquidity(liquidityBlock(2), acc, "AAPL", "1m")
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	// 2 levels: 50+50 bid, 40+40 ask = 180 total
	if r.Depth != 180 {
		t.Errorf("Depth with 2 levels: want 180, got %v", r.Depth)
	}
}

func TestPerceiveLiquidity_ZeroImbalance(t *testing.T) {
	q := store.Quote{
		BidPrice: 99, AskPrice: 101, HasBid: true, HasAsk: true,
		LastPrice: 100, HasLast: true,
		Depth: store.QuoteDepth{
			Bids: []store.QuoteLevel{{Qty: 100}},
			Asks: []store.QuoteLevel{{Qty: 100}},
		},
	}
	acc := makeLiquidityAccessor("X", q, true)
	r := perceiveLiquidity(liquidityBlock(1), acc, "X", "1m")
	if !r.ReadOK {
		t.Fatal("ReadOK must be true")
	}
	if r.Imbalance != 0 {
		t.Errorf("equal sides: imbalance want 0, got %v", r.Imbalance)
	}
}
