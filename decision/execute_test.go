package decision

import (
	"math"
	"math/rand/v2"
	"testing"

	"pgregory.net/rapid"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/store"
)

// --- fixtures

func testExecParams() Params {
	return Params{
		MaxFrac:     0.4,
		Gamma:       0.5,
		ThetaMarket: 0.7,
		KPlace:      0.5,
		SigmaExec:   0, // no noise unless a test sets it explicitly
	}
}

func testExecTraits(aggressiveness, skill float64) agent.TraitVector {
	return agent.TraitVector{Aggressiveness: aggressiveness, Skill: skill}
}

func quoteAccessor(inst string, quote store.Quote) *Accessor {
	return singleInstAccessor(inst, "1m", nil, quote, true)
}

func twoSidedQuote(bestBid, bestAsk, lastPrice int64) store.Quote {
	return store.Quote{
		HasLast: true, LastPrice: lastPrice,
		HasBid: true, BidPrice: bestBid,
		HasAsk: true, AskPrice: bestAsk,
	}
}

func newExecRNG(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
}

// --- position-case dispatch

func TestExecute_Flat_RunsOpenMath(t *testing.T) {
	ambient := AmbientInputs{
		AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m",
		TradingCapital: 10_000,
	}
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	intent := ExecIntent{Action: ExecIntentActionOpen, Direction: 1, Conviction: 0.5}

	req, ok := Execute(intent, ambient, acc, testExecTraits(0.9, 1), newExecRNG(1), testExecParams())
	if !ok {
		t.Fatal("Execute: got ok=false for a flat agent with capital and a valid price, want ok=true")
	}
	// baseNotional = 10000*0.4*0.5 = 2000; qty = floor(2000/(10000*0.01)) = floor(2000/100) = 20.
	if req.Qty != 20 {
		t.Errorf("Qty: got %d, want 20", req.Qty)
	}
	if req.Side != core.OrderSideBuy {
		t.Errorf("Side: got %q, want buy", req.Side)
	}
}

func TestExecute_HoldingSameDirection_RunsAddMathWithSharedFormula(t *testing.T) {
	ambient := AmbientInputs{
		AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m",
		TradingCapital: 10_000,
		Positions:      []core.Position{{Instrument: "AAPL", Qty: 10, WeightedAvgEntry: 10000}},
	}
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	intent := ExecIntent{Action: ExecIntentActionOpen, Direction: 1, Conviction: 0.5}

	req, ok := Execute(intent, ambient, acc, testExecTraits(0.9, 1), newExecRNG(1), testExecParams())
	if !ok {
		t.Fatal("Execute: got ok=false for a same-direction add, want ok=true")
	}
	// committed = int64(10000*0.01*10) = 1000; available = 9000; baseNotional = 9000*0.4*0.5 = 1800; qty = floor(1800/(10000*0.01)) = floor(1800/100) = 18.
	if req.Qty != 18 {
		t.Errorf("Qty: got %d, want 18 (the existing same-instrument position must shrink the shared pool)", req.Qty)
	}
}

func TestExecute_HoldingOppositeDirection_Ignored(t *testing.T) {
	ambient := AmbientInputs{
		AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m",
		TradingCapital: 10_000,
		Positions:      []core.Position{{Instrument: "AAPL", Qty: 10, WeightedAvgEntry: 10000}},
	}
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	intent := ExecIntent{Action: ExecIntentActionOpen, Direction: -1, Conviction: 0.9}

	_, ok := Execute(intent, ambient, acc, testExecTraits(0.9, 1), newExecRNG(1), testExecParams())
	if ok {
		t.Fatal("Execute: got ok=true for an opposite-direction hold, want ok=false (reduce/flip is not this executor's job)")
	}
}

func TestExecute_ActionClose_RoutesToCloseRegardlessOfDirectionVsPositionSign(t *testing.T) {
	ambient := AmbientInputs{
		AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m",
		TradingCapital: 1_000_000, // deliberately huge so a bug that fell through to OPEN/ADD math would size very differently
		Positions:      []core.Position{{Instrument: "AAPL", Qty: 10, WeightedAvgEntry: 10000}},
	}
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	// Direction matches the position's own sign (long, +1) — if the dispatch
	// were (wrongly) deriving close from a sign comparison rather than
	// reading Action directly, this would read as "holding same direction"
	// and run OPEN/ADD math instead.
	intent := ExecIntent{Action: ExecIntentActionClose, Direction: 1, Conviction: 0.9}

	req, ok := Execute(intent, ambient, acc, testExecTraits(0.9, 1), newExecRNG(1), testExecParams())
	if !ok {
		t.Fatal("Execute: got ok=false for a close with a closeable position, want ok=true")
	}
	if req.Side != core.OrderSideSell {
		t.Errorf("Side: got %q, want sell (closing a long position) — OPEN/ADD math would have produced buy", req.Side)
	}
	if req.Qty != 10 {
		t.Errorf("Qty: got %d, want 10 (closeable qty), not a capital-sized quantity", req.Qty)
	}
}

// --- qty, formulation Y (open/add)

func TestBuildOpenOrAddOrder_SharedCapitalPool_ExistingPositionElsewhereReducesAvailable(t *testing.T) {
	acc := quoteAccessor("MSFT", twoSidedQuote(9900, 10100, 10000))
	intent := ExecIntent{Action: ExecIntentActionOpen, Direction: 1, Conviction: 1}
	cfg := testExecParams()
	cfg.MaxFrac = 1 // isolate the shared-pool effect from max_frac shaping

	withoutOtherPosition := AmbientInputs{
		AccountID: "acct-1", Instrument: "MSFT", Timeframe: "1m", TradingCapital: 10_000,
	}
	reqA, okA := Execute(intent, withoutOtherPosition, acc, testExecTraits(0.9, 1), newExecRNG(1), cfg)
	if !okA {
		t.Fatal("baseline (no other position): got ok=false, want ok=true")
	}

	withOtherPosition := AmbientInputs{
		AccountID: "acct-1", Instrument: "MSFT", Timeframe: "1m", TradingCapital: 10_000,
		Positions: []core.Position{{Instrument: "AAPL", Qty: 50, WeightedAvgEntry: 10000}}, // committed = int64(10000*0.01*50) = 5000; consumes 5000 of the pool
	}
	reqB, okB := Execute(intent, withOtherPosition, acc, testExecTraits(0.9, 1), newExecRNG(1), cfg)
	if !okB {
		t.Fatal("with an AAPL position: got ok=false, want ok=true")
	}

	if reqB.Qty >= reqA.Qty {
		t.Errorf("Qty with a competing AAPL position: got %d, want strictly less than the no-position baseline %d", reqB.Qty, reqA.Qty)
	}
	if reqA.Qty != 100 || reqB.Qty != 50 {
		t.Errorf("Qty: got (baseline=%d, withOther=%d), want (100, 50)", reqA.Qty, reqB.Qty)
	}
}

func TestBuildOpenOrAddOrder_Edges(t *testing.T) {
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	cfg := testExecParams()

	t.Run("capital already fully committed elsewhere", func(t *testing.T) {
		ambient := AmbientInputs{
			AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m", TradingCapital: 1_000,
			Positions: []core.Position{{Instrument: "OTHER", Qty: 10, WeightedAvgEntry: 10000}}, // consumes all 1000
		}
		intent := ExecIntent{Action: ExecIntentActionOpen, Direction: 1, Conviction: 1}
		_, ok := Execute(intent, ambient, acc, testExecTraits(0.9, 1), newExecRNG(1), cfg)
		if ok {
			t.Error("got ok=true with available capital <= 0, want ok=false")
		}
	})

	t.Run("qty floors to zero", func(t *testing.T) {
		ambient := AmbientInputs{
			AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m", TradingCapital: 100,
		}
		intent := ExecIntent{Action: ExecIntentActionOpen, Direction: 1, Conviction: 0.01}
		_, ok := Execute(intent, ambient, acc, testExecTraits(0.9, 1), newExecRNG(1), cfg)
		if ok {
			t.Error("got ok=true when sized qty floors to 0, want ok=false")
		}
	})

	t.Run("no price available", func(t *testing.T) {
		noPriceAcc := singleInstAccessor("AAPL", "1m", nil, store.Quote{}, true)
		ambient := AmbientInputs{
			AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m", TradingCapital: 10_000,
		}
		intent := ExecIntent{Action: ExecIntentActionOpen, Direction: 1, Conviction: 1}
		_, ok := Execute(intent, ambient, noPriceAcc, testExecTraits(0.9, 1), newExecRNG(1), cfg)
		if ok {
			t.Error("got ok=true with no readable current price, want ok=false")
		}
	})
}

// --- type: market vs limit, effective_aggr

func TestWarpedAggressiveness_StaysInUnitIntervalAndIsNonDecreasingInConviction(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		baseAggressiveness := rapid.Float64Range(0, 1).Draw(rt, "aggressiveness")
		gamma := rapid.Float64Range(0, 0.999).Draw(rt, "gamma")
		lowConviction := rapid.Float64Range(0, 1).Draw(rt, "low_conviction")
		highConviction := rapid.Float64Range(lowConviction, 1).Draw(rt, "high_conviction")

		low := warpedAggressiveness(baseAggressiveness, lowConviction, gamma)
		high := warpedAggressiveness(baseAggressiveness, highConviction, gamma)

		if low < 0 || low > 1 || high < 0 || high > 1 {
			t.Fatalf("warpedAggressiveness out of [0,1]: low=%v high=%v (aggressiveness=%v gamma=%v)", low, high, baseAggressiveness, gamma)
		}
		if high < low-1e-12 {
			t.Fatalf("warpedAggressiveness not non-decreasing in conviction: low=%v (conviction=%v) > high=%v (conviction=%v)", low, lowConviction, high, highConviction)
		}
	})
}

func TestWarpedAggressiveness_ZeroConvictionMatchesBaseAggressivenessExactly(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		baseAggressiveness := rapid.Float64Range(0, 1).Draw(rt, "aggressiveness")
		gamma := rapid.Float64Range(0, 1).Draw(rt, "gamma")
		got := warpedAggressiveness(baseAggressiveness, 0, gamma)
		if math.Abs(got-baseAggressiveness) > 1e-9 {
			t.Fatalf("warpedAggressiveness(aggr, 0, gamma): got %v, want %v", got, baseAggressiveness)
		}
	})
}

// --- limit_price: tick-rounding direction, two-sided-book edge

func TestResolveTypeAndPrice_BuyRoundsDownSellRoundsUp(t *testing.T) {
	acc := quoteAccessor("AAPL", twoSidedQuote(100, 110, 105)) // spread = 10
	cfg := testExecParams()
	cfg.ThetaMarket = 1 // force the limit branch regardless of effectiveAggressiveness
	cfg.KPlace = 1

	// effectiveAggressiveness*KPlace = 0.25 -> depthIntoSpread*spread = 2.5, a fractional tick.
	orderType, buyPrice, _, ok := resolveTypeAndPrice(core.OrderSideBuy, 0.25, "AAPL", acc, cfg)
	if !ok || orderType != core.OrderTypeLimit {
		t.Fatalf("buy: got (type=%v ok=%v), want (limit, true)", orderType, ok)
	}
	if buyPrice != 102 {
		t.Errorf("buy limit price: got %d, want 102 (100 + floor(2.5))", buyPrice)
	}

	_, sellPrice, _, ok := resolveTypeAndPrice(core.OrderSideSell, 0.25, "AAPL", acc, cfg)
	if !ok {
		t.Fatal("sell: got ok=false, want true")
	}
	if sellPrice != 108 {
		t.Errorf("sell limit price: got %d, want 108 (110 - ceil(2.5), rounding up toward less aggressive)", sellPrice)
	}
}

func TestResolveTypeAndPrice_NoTwoSidedBook_NoLimitOrder(t *testing.T) {
	oneSidedQuote := store.Quote{HasLast: true, LastPrice: 100, HasBid: true, BidPrice: 99, HasAsk: false}
	acc := quoteAccessor("AAPL", oneSidedQuote)
	cfg := testExecParams()
	cfg.ThetaMarket = 1 // force the limit branch

	_, _, _, ok := resolveTypeAndPrice(core.OrderSideBuy, 0.5, "AAPL", acc, cfg)
	if ok {
		t.Error("got ok=true with no two-sided book, want ok=false")
	}
}

func TestResolveTypeAndPrice_MarketUnaffectedByMissingBook(t *testing.T) {
	acc := quoteAccessor("AAPL", store.Quote{HasLast: true, LastPrice: 100}) // no bid/ask at all
	cfg := testExecParams()
	cfg.ThetaMarket = 0 // force the market branch

	orderType, limitPrice, _, ok := resolveTypeAndPrice(core.OrderSideBuy, 0.9, "AAPL", acc, cfg)
	if !ok || orderType != core.OrderTypeMarket || limitPrice != 0 {
		t.Errorf("got (type=%v price=%v ok=%v), want (market, 0, true)", orderType, limitPrice, ok)
	}
}

// --- skill execution-noise

func TestExecute_FixedSeed_IsDeterministic(t *testing.T) {
	ambient := AmbientInputs{
		AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m", TradingCapital: 10_000,
	}
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	cfg := testExecParams()
	cfg.SigmaExec = 0.2
	intent := ExecIntent{Action: ExecIntentActionOpen, Direction: 1, Conviction: 0.6}
	traits := testExecTraits(0.5, 0.3)

	reqA, okA := Execute(intent, ambient, acc, traits, newExecRNG(42), cfg)
	reqB, okB := Execute(intent, ambient, acc, traits, newExecRNG(42), cfg)

	if okA != okB || reqA != reqB {
		t.Errorf("Execute on identical inputs with the same seed: got (%+v,%v) and (%+v,%v), want identical", reqA, okA, reqB, okB)
	}
}

func TestExecute_Noise_NeverChangesTypeOrSide(t *testing.T) {
	ambient := AmbientInputs{
		AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m", TradingCapital: 10_000,
	}
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	cfg := testExecParams()
	cfg.SigmaExec = 0.3
	cfg.ThetaMarket = 1 // keep the whole sweep on the limit branch so a flip would be obvious
	intent := ExecIntent{Action: ExecIntentActionOpen, Direction: 1, Conviction: 0.6}
	traits := testExecTraits(0.5, 0) // skill=0 maximizes noise width

	var wantType, wantSide string
	for seed := uint64(0); seed < 200; seed++ {
		req, ok := Execute(intent, ambient, acc, traits, newExecRNG(seed), cfg)
		if !ok {
			continue
		}
		if wantType == "" {
			wantType, wantSide = req.Type, req.Side
			continue
		}
		if req.Type != wantType || req.Side != wantSide {
			t.Fatalf("seed %d: Type/Side changed under noise: got (%q,%q), want (%q,%q)", seed, req.Type, req.Side, wantType, wantSide)
		}
	}
}

func TestExecute_PostNoiseClamp_OpenAdd_RespectsCapitalCeiling(t *testing.T) {
	ambient := AmbientInputs{
		AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m", TradingCapital: 10_000,
	}
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	cfg := testExecParams()
	cfg.SigmaExec = 5 // deliberately extreme to push noise past both clamp bounds
	intent := ExecIntent{Action: ExecIntentActionOpen, Direction: 1, Conviction: 1}
	traits := testExecTraits(0.5, 0)

	ceiling := int64(10_000 / 100) // available / (price_ticks * 0.01) = 10000 / (10000 * 0.01) = 100
	for seed := uint64(0); seed < 200; seed++ {
		req, ok := Execute(intent, ambient, acc, traits, newExecRNG(seed), cfg)
		if !ok {
			continue
		}
		if req.Qty < 1 || req.Qty > ceiling {
			t.Fatalf("seed %d: Qty=%d outside post-noise clamp [1,%d]", seed, req.Qty, ceiling)
		}
	}
}

// --- close path

func TestExecute_Close_ConvictionHasNoEffect(t *testing.T) {
	ambient := AmbientInputs{
		AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m", TradingCapital: 10_000,
		Positions: []core.Position{{Instrument: "AAPL", Qty: 10, ReservedQty: 2, WeightedAvgEntry: 10000}},
	}
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	cfg := testExecParams()
	traits := testExecTraits(0.5, 1)

	low := ExecIntent{Action: ExecIntentActionClose, Direction: 1, Conviction: 0}
	high := ExecIntent{Action: ExecIntentActionClose, Direction: 1, Conviction: 0.999}

	reqLow, okLow := Execute(low, ambient, acc, traits, newExecRNG(7), cfg)
	reqHigh, okHigh := Execute(high, ambient, acc, traits, newExecRNG(7), cfg)

	if !okLow || !okHigh || reqLow != reqHigh {
		t.Errorf("close with Conviction=0 vs 0.999: got (%+v,%v) and (%+v,%v), want identical", reqLow, okLow, reqHigh, okHigh)
	}
}

func TestExecute_Close_UsesRawAggressivenessNotWarped(t *testing.T) {
	ambient := AmbientInputs{
		AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m", TradingCapital: 10_000,
		Positions: []core.Position{{Instrument: "AAPL", Qty: 10, WeightedAvgEntry: 10000}},
	}
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	cfg := testExecParams()
	cfg.ThetaMarket = 0.6 // between the raw aggressiveness below and what a high-conviction warp would push it to
	traits := testExecTraits(0.5, 1)

	// A high conviction would warp 0.5 well above 0.6 on the open/add path
	// (gamma=0.5 => effective = 0.5^(1-0.5*0.99) ≈ 0.70), but close must
	// ignore the warp entirely and stay on raw aggressiveness (0.5 < 0.6
	// => limit).
	intent := ExecIntent{Action: ExecIntentActionClose, Direction: 1, Conviction: 0.99}
	req, ok := Execute(intent, ambient, acc, traits, newExecRNG(1), cfg)
	if !ok {
		t.Fatal("got ok=false, want true")
	}
	if req.Type != core.OrderTypeLimit {
		t.Errorf("Type: got %q, want limit (raw aggressiveness 0.5 is below theta_market 0.6; a conviction warp would have wrongly pushed this to market)", req.Type)
	}
}

func TestExecute_Close_PostNoiseCeilingIsCloseableQtyNotCapital(t *testing.T) {
	ambient := AmbientInputs{
		AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m",
		TradingCapital: 1_000_000_000, // capital-based ceiling would be enormous
		Positions:      []core.Position{{Instrument: "AAPL", Qty: 5, WeightedAvgEntry: 10000}},
	}
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	cfg := testExecParams()
	cfg.SigmaExec = 5
	traits := testExecTraits(0.5, 0)
	intent := ExecIntent{Action: ExecIntentActionClose, Direction: 1, Conviction: 1}

	for seed := uint64(0); seed < 200; seed++ {
		req, ok := Execute(intent, ambient, acc, traits, newExecRNG(seed), cfg)
		if !ok {
			continue
		}
		if req.Qty < 1 || req.Qty > 5 {
			t.Fatalf("seed %d: Qty=%d outside the closeable-qty clamp [1,5] — the capital ceiling must not apply on close", seed, req.Qty)
		}
	}
}

func TestExecute_Close_NoCloseableQty_NoOrder(t *testing.T) {
	ambient := AmbientInputs{
		AccountID: "acct-1", Instrument: "AAPL", Timeframe: "1m", TradingCapital: 10_000,
		Positions: []core.Position{{Instrument: "AAPL", Qty: 5, ReservedQty: 5}},
	}
	acc := quoteAccessor("AAPL", twoSidedQuote(9900, 10100, 10000))
	intent := ExecIntent{Action: ExecIntentActionClose, Direction: 1, Conviction: 1}

	_, ok := Execute(intent, ambient, acc, testExecTraits(0.5, 1), newExecRNG(1), testExecParams())
	if ok {
		t.Error("got ok=true with closeableQty<=0 (fully reserved), want ok=false")
	}
}

// --- valid-by-construction

// assertValidOrderPunchRequest replicates core's PunchOrder pre-flight
// pairing rules exactly, so this package's output can be checked
// against the write contract without an exported hook into the core
// package's unexported validator.
func assertValidOrderPunchRequest(t *rapid.T, req core.OrderPunchRequest) {
	if req.AccountID == "" {
		t.Fatal("AccountID is empty")
	}
	if req.Instrument == "" {
		t.Fatal("Instrument is empty")
	}
	if req.Qty <= 0 {
		t.Fatalf("Qty=%d, want > 0", req.Qty)
	}
	if req.Side != core.OrderSideBuy && req.Side != core.OrderSideSell {
		t.Fatalf("Side=%q, want buy or sell", req.Side)
	}
	switch req.Type {
	case core.OrderTypeLimit:
		if req.TIF != core.TimeInForceGoodTillCancel {
			t.Fatalf("limit order TIF=%q, want gtc", req.TIF)
		}
		if req.LimitPrice <= 0 {
			t.Fatalf("limit order LimitPrice=%d, want > 0", req.LimitPrice)
		}
	case core.OrderTypeMarket:
		if req.TIF != core.TimeInForceImmediateOrCancel {
			t.Fatalf("market order TIF=%q, want ioc", req.TIF)
		}
		if req.LimitPrice != 0 {
			t.Fatalf("market order LimitPrice=%d, want 0", req.LimitPrice)
		}
	default:
		t.Fatalf("Type=%q, want limit or market", req.Type)
	}
}

func TestExecute_OpenAdd_ValidByConstruction(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		instrument := "AAPL"
		bestBid := rapid.Int64Range(1, 10_000).Draw(rt, "best_bid")
		spreadTicks := rapid.Int64Range(0, 1_000).Draw(rt, "spread_ticks")
		bestAsk := bestBid + spreadTicks
		hasBid := rapid.Bool().Draw(rt, "has_bid")
		hasAsk := rapid.Bool().Draw(rt, "has_ask")
		lastPrice := rapid.Int64Range(1, 10_000).Draw(rt, "last_price")

		quote := store.Quote{HasLast: true, LastPrice: lastPrice, HasBid: hasBid, BidPrice: bestBid, HasAsk: hasAsk, AskPrice: bestAsk}
		acc := quoteAccessor(instrument, quote)

		tradingCapital := rapid.Int64Range(0, 1_000_000).Draw(rt, "trading_capital")
		otherQty := rapid.Int64Range(-100, 100).Draw(rt, "other_qty")
		otherEntry := rapid.Int64Range(1, 1_000).Draw(rt, "other_entry")
		ambient := AmbientInputs{
			AccountID: "acct-1", Instrument: instrument, Timeframe: "1m",
			TradingCapital: tradingCapital,
			Positions:      []core.Position{{Instrument: "OTHER", Qty: otherQty, WeightedAvgEntry: otherEntry}},
		}

		intent := ExecIntent{
			Action:     ExecIntentActionOpen,
			Direction:  rapid.SampledFrom([]float64{1, -1}).Draw(rt, "direction"),
			Conviction: rapid.Float64Range(0, 1).Draw(rt, "conviction"),
		}
		traits := testExecTraits(rapid.Float64Range(0, 1).Draw(rt, "aggressiveness"), rapid.Float64Range(0, 1).Draw(rt, "skill"))
		cfg := Params{
			MaxFrac:     rapid.Float64Range(0.01, 1).Draw(rt, "max_frac"),
			Gamma:       rapid.Float64Range(0, 0.99).Draw(rt, "gamma"),
			ThetaMarket: rapid.Float64Range(0, 1).Draw(rt, "theta_market"),
			KPlace:      rapid.Float64Range(0, 1).Draw(rt, "k_place"),
			SigmaExec:   rapid.Float64Range(0, 1).Draw(rt, "sigma_exec"),
		}
		seed := rapid.Uint64().Draw(rt, "seed")

		req, ok := Execute(intent, ambient, acc, traits, newExecRNG(seed), cfg)
		if ok {
			assertValidOrderPunchRequest(rt, req)
		}
	})
}

func TestExecute_Close_ValidByConstruction(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		instrument := "AAPL"
		bestBid := rapid.Int64Range(1, 10_000).Draw(rt, "best_bid")
		spreadTicks := rapid.Int64Range(0, 1_000).Draw(rt, "spread_ticks")
		bestAsk := bestBid + spreadTicks
		hasBid := rapid.Bool().Draw(rt, "has_bid")
		hasAsk := rapid.Bool().Draw(rt, "has_ask")
		lastPrice := rapid.Int64Range(1, 10_000).Draw(rt, "last_price")

		quote := store.Quote{HasLast: true, LastPrice: lastPrice, HasBid: hasBid, BidPrice: bestBid, HasAsk: hasAsk, AskPrice: bestAsk}
		acc := quoteAccessor(instrument, quote)

		positionQty := rapid.Int64Range(-1_000, 1_000).Draw(rt, "position_qty")
		reservedQty := rapid.Int64Range(0, 1_000).Draw(rt, "reserved_qty")
		ambient := AmbientInputs{
			AccountID: "acct-1", Instrument: instrument, Timeframe: "1m",
			TradingCapital: rapid.Int64Range(0, 1_000_000).Draw(rt, "trading_capital"),
			Positions:      []core.Position{{Instrument: instrument, Qty: positionQty, ReservedQty: reservedQty, WeightedAvgEntry: rapid.Int64Range(1, 1_000).Draw(rt, "entry")}},
		}

		intent := ExecIntent{
			Action:     ExecIntentActionClose,
			Direction:  rapid.SampledFrom([]float64{1, -1}).Draw(rt, "direction"),
			Conviction: rapid.Float64Range(0, 1).Draw(rt, "conviction"),
		}
		traits := testExecTraits(rapid.Float64Range(0, 1).Draw(rt, "aggressiveness"), rapid.Float64Range(0, 1).Draw(rt, "skill"))
		cfg := Params{
			MaxFrac:     rapid.Float64Range(0.01, 1).Draw(rt, "max_frac"),
			Gamma:       rapid.Float64Range(0, 0.99).Draw(rt, "gamma"),
			ThetaMarket: rapid.Float64Range(0, 1).Draw(rt, "theta_market"),
			KPlace:      rapid.Float64Range(0, 1).Draw(rt, "k_place"),
			SigmaExec:   rapid.Float64Range(0, 1).Draw(rt, "sigma_exec"),
		}
		seed := rapid.Uint64().Draw(rt, "seed")

		req, ok := Execute(intent, ambient, acc, traits, newExecRNG(seed), cfg)
		if ok {
			assertValidOrderPunchRequest(rt, req)
		}
	})
}
