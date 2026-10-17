package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// sampleObservationResponseBody mirrors the exact wire shape Core
// emits — every field named by the broker's response shaper, with
// enum values spelled lowercase and timestamps in RFC3339Nano. Used
// across the happy-path and typed-decode tests.
const sampleObservationResponseBody = `{
	"balance": {
		"available": 500000,
		"reserved":   25000,
		"settled":   100000,
		"bank":     9999999
	},
	"positions": [
		{
			"instrument":"INST-A","qty":10,"reserved_qty":2,
			"weighted_avg_entry":25500,"mark_available":true,"ltp":25800,
			"gross_unrealized":3000
		},
		{
			"instrument":"INST-B","qty":-3,"reserved_qty":0,
			"weighted_avg_entry":17800,"mark_available":false,"ltp":0,
			"gross_unrealized":0
		}
	],
	"orders": [
		{
			"order_id":"acc-1:42","instrument":"INST-A","side":"buy",
			"is_market":false,"limit_price":25400,
			"orig_qty":10,"remaining_qty":6,"filled_qty":4,
			"reserved_cash":152400,"reserved_units":0,
			"status":"partially_filled","cancel_in_flight":false
		},
		{
			"order_id":"acc-1:43","instrument":"INST-B","side":"sell",
			"is_market":true,"limit_price":0,
			"orig_qty":5,"remaining_qty":5,"filled_qty":0,
			"reserved_cash":0,"reserved_units":5,
			"status":"working","cancel_in_flight":true
		}
	],
	"recent_trades": [
		{
			"trade_id":"t-1","instrument":"INST-A","direction":"long",
			"quantity":5,"entry_price":25400,"exit_price":25600,
			"entry_at":"2026-10-17T09:16:00Z","exit_at":"2026-10-17T09:45:00Z",
			"gross_pnl":1000,"total_costs":50,"net_pnl":950,"pnl_pct":0.78,
			"exit_reason":"take_profit"
		}
	],
	"trades_degraded": false
}`

func TestGetObservationSendsExpectedRequestAndDecodesBalance(t *testing.T) {
	t.Parallel()

	var observedMethod atomic.Value
	var observedPath atomic.Value
	var observedAccountIDQuery atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		observedMethod.Store(request.Method)
		observedPath.Store(request.URL.Path)
		observedAccountIDQuery.Store(request.URL.Query().Get("account_id"))

		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(sampleObservationResponseBody))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	observation, err := client.GetObservation(context.Background(), "acc-handle-123")
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if got := observedMethod.Load().(string); got != http.MethodGet {
		t.Fatalf("method = %q, want GET", got)
	}
	if got := observedPath.Load().(string); got != observationEndpointPath {
		t.Fatalf("path = %q, want %q", got, observationEndpointPath)
	}
	if got := observedAccountIDQuery.Load().(string); got != "acc-handle-123" {
		t.Fatalf("account_id = %q", got)
	}
	expectedBalance := AccountBalance{Available: 500000, Reserved: 25000, Settled: 100000, Bank: 9999999}
	if observation.Balance != expectedBalance {
		t.Fatalf("Balance = %+v, want %+v", observation.Balance, expectedBalance)
	}
	if observation.TradesDegraded {
		t.Fatalf("TradesDegraded = true, want false")
	}
}

func TestGetObservationDecodesPositionFields(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(sampleObservationResponseBody))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	observation, err := client.GetObservation(context.Background(), "acc-handle-123")
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if len(observation.Positions) != 2 {
		t.Fatalf("got %d positions", len(observation.Positions))
	}
	first := observation.Positions[0]
	if first.Instrument != "INST-A" || first.Qty != 10 || first.ReservedQty != 2 {
		t.Fatalf("Positions[0] = %+v", first)
	}
	if first.WeightedAvgEntry != 25500 || !first.MarkAvailable || first.LTP != 25800 || first.GrossUnrealized != 3000 {
		t.Fatalf("Positions[0] price-dependent fields = %+v", first)
	}
	// Second position tests the !MarkAvailable branch — LTP and
	// GrossUnrealized must stay zero when the mark is unavailable.
	second := observation.Positions[1]
	if second.MarkAvailable {
		t.Fatalf("Positions[1].MarkAvailable = true, want false")
	}
	if second.LTP != 0 || second.GrossUnrealized != 0 {
		t.Fatalf("Positions[1] price-dependent fields not zero on mark unavailable: %+v", second)
	}
	if second.Qty != -3 {
		t.Fatalf("Positions[1].Qty = %d, want -3 (short)", second.Qty)
	}
}

func TestGetObservationDecodesOpenOrderFields(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(sampleObservationResponseBody))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	observation, err := client.GetObservation(context.Background(), "acc-handle-123")
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if len(observation.Orders) != 2 {
		t.Fatalf("got %d orders", len(observation.Orders))
	}
	limitOrder := observation.Orders[0]
	if limitOrder.OrderID != "acc-1:42" || limitOrder.Instrument != "INST-A" {
		t.Fatalf("limitOrder identity = %+v", limitOrder)
	}
	if limitOrder.Side != OrderSideBuy {
		t.Fatalf("limitOrder.Side = %q, want %q", limitOrder.Side, OrderSideBuy)
	}
	if limitOrder.IsMarket {
		t.Fatalf("limitOrder.IsMarket = true, want false")
	}
	if limitOrder.LimitPrice != 25400 || limitOrder.OrigQty != 10 || limitOrder.RemainingQty != 6 || limitOrder.FilledQty != 4 {
		t.Fatalf("limitOrder qty/price = %+v", limitOrder)
	}
	if limitOrder.ReservedCash != 152400 || limitOrder.ReservedUnits != 0 {
		t.Fatalf("limitOrder reservation = %+v (buy must reserve cash, not units)", limitOrder)
	}
	if limitOrder.Status != OrderStatusPartiallyFilled {
		t.Fatalf("limitOrder.Status = %q, want %q", limitOrder.Status, OrderStatusPartiallyFilled)
	}

	marketOrder := observation.Orders[1]
	if !marketOrder.IsMarket {
		t.Fatalf("marketOrder.IsMarket = false, want true")
	}
	if marketOrder.LimitPrice != 0 {
		t.Fatalf("market order with non-zero LimitPrice: %d", marketOrder.LimitPrice)
	}
	if marketOrder.Side != OrderSideSell {
		t.Fatalf("marketOrder.Side = %q", marketOrder.Side)
	}
	if marketOrder.ReservedCash != 0 || marketOrder.ReservedUnits != 5 {
		t.Fatalf("marketOrder reservation = %+v (sell must reserve units, not cash)", marketOrder)
	}
	if marketOrder.Status != OrderStatusWorking {
		t.Fatalf("marketOrder.Status = %q, want %q", marketOrder.Status, OrderStatusWorking)
	}
	if !marketOrder.CancelInFlight {
		t.Fatalf("marketOrder.CancelInFlight = false, want true")
	}
}

func TestGetObservationDecodesRecentTradeFields(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(sampleObservationResponseBody))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	observation, err := client.GetObservation(context.Background(), "acc-handle-123")
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if len(observation.RecentTrades) != 1 {
		t.Fatalf("got %d trades", len(observation.RecentTrades))
	}
	trade := observation.RecentTrades[0]
	if trade.TradeID != "t-1" || trade.Instrument != "INST-A" {
		t.Fatalf("trade identity = %+v", trade)
	}
	if trade.Direction != TradeDirectionLong {
		t.Fatalf("trade.Direction = %q, want %q", trade.Direction, TradeDirectionLong)
	}
	if trade.Quantity != 5 || trade.EntryPrice != 25400 || trade.ExitPrice != 25600 {
		t.Fatalf("trade qty/prices = %+v", trade)
	}
	if trade.GrossPnl != 1000 || trade.TotalCosts != 50 || trade.NetPnl != 950 {
		t.Fatalf("trade pnl = %+v", trade)
	}
	if trade.PnlPct != 0.78 {
		t.Fatalf("trade.PnlPct = %v, want 0.78", trade.PnlPct)
	}
	if trade.ExitReason != "take_profit" {
		t.Fatalf("trade.ExitReason = %q", trade.ExitReason)
	}
	expectedEntryAt := time.Date(2026, 10, 17, 9, 16, 0, 0, time.UTC)
	expectedExitAt := time.Date(2026, 10, 17, 9, 45, 0, 0, time.UTC)
	if !trade.EntryAt.Equal(expectedEntryAt) {
		t.Fatalf("trade.EntryAt = %v, want %v", trade.EntryAt, expectedEntryAt)
	}
	if !trade.ExitAt.Equal(expectedExitAt) {
		t.Fatalf("trade.ExitAt = %v, want %v", trade.ExitAt, expectedExitAt)
	}
}

func TestGetObservationBalancePreservesInt64PrecisionNearMaxInt(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{
			"balance":{"available":9223372036854775800,"reserved":0,"settled":0,"bank":0},
			"positions":[],"orders":[],"recent_trades":[],"trades_degraded":false
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	observation, err := client.GetObservation(context.Background(), "acc-handle-123")
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	wantAvailable := int64(math.MaxInt64 - 7)
	if observation.Balance.Available != wantAvailable {
		t.Fatalf("Available = %d, want %d", observation.Balance.Available, wantAvailable)
	}
}

func TestGetObservationNormalizesNullCollectionsToEmptySlices(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{
			"balance":{"available":0,"reserved":0,"settled":0,"bank":0},
			"positions":null,"orders":null,"recent_trades":null,"trades_degraded":false
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	observation, err := client.GetObservation(context.Background(), "acc-handle-123")
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if observation.Positions == nil || len(observation.Positions) != 0 {
		t.Fatalf("Positions = %v, want empty slice", observation.Positions)
	}
	if observation.Orders == nil || len(observation.Orders) != 0 {
		t.Fatalf("Orders = %v, want empty slice", observation.Orders)
	}
	if observation.RecentTrades == nil || len(observation.RecentTrades) != 0 {
		t.Fatalf("RecentTrades = %v, want empty slice", observation.RecentTrades)
	}
}

func TestGetObservationRejectsEmptyAccountIDWithoutHittingServer(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.GetObservation(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error on empty account id")
	}
	if serverHits.Load() != 0 {
		t.Fatalf("server was hit on empty account id")
	}
}

func TestGetObservationReturnsCallErrorOnNoSession(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusConflict)
		_, _ = responseWriter.Write([]byte(`{"error":"account not installed","reason":"no_session"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.GetObservation(context.Background(), "acc-handle-123")
	var callError *CallError
	if !errors.As(err, &callError) {
		t.Fatalf("expected *CallError, got %v", err)
	}
	if callError.Reason != ReasonNoSession {
		t.Fatalf("Reason = %q, want %q", callError.Reason, ReasonNoSession)
	}
}

// GetObservation is a pure read and goes through the idempotent-retry
// wrapper. A transient 5xx must be retried; the test verifies the
// hot-path call eventually succeeds without the caller having to
// repeat.
func TestGetObservationRetriesTransient5xxThenSucceeds(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		attemptNumber := serverHits.Add(1)
		if attemptNumber == 1 {
			responseWriter.WriteHeader(http.StatusInternalServerError)
			return
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(sampleObservationResponseBody))
	}))
	t.Cleanup(server.Close)

	client := newTestClientWithRetryPolicy(t, server, 2, 1*time.Millisecond)
	observation, err := client.GetObservation(context.Background(), "acc-handle-123")
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if observation.Balance.Available != 500000 {
		t.Fatalf("Available = %d", observation.Balance.Available)
	}
	if got := serverHits.Load(); got != 2 {
		t.Fatalf("server hits = %d, want 2", got)
	}
}

// Hot-path budget must be enforced independently of pre-open: this
// test gives pre-open a generous budget and shrinks only hot-path, so
// a slow Core proves the correct class budget is being applied.
func TestGetObservationHonorsHotPathCallTimeout(t *testing.T) {
	t.Parallel()
	serverWait := make(chan struct{})
	t.Cleanup(func() { close(serverWait) })

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		select {
		case <-serverWait:
		case <-request.Context().Done():
		case <-time.After(2 * time.Second):
		}
		responseWriter.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	config := ClientConfig{
		BaseURL:          server.URL,
		WebSocketBaseURL: "ws://127.0.0.1:1",
		Timeouts: TimeoutConfig{
			PreOpenCall:       5 * time.Second,
			HotPathCall:       50 * time.Millisecond,
			FireAndForgetCall: 5 * time.Second,
		},
	}
	client, err := NewClient(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)

	startedAt := time.Now()
	_, err = client.GetObservation(context.Background(), "acc-handle-123")
	elapsed := time.Since(startedAt)

	if err == nil {
		t.Fatalf("expected timeout error, got nil (elapsed=%v)", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("GetObservation took %v; hot-path budget should abort well before 500ms", elapsed)
	}
}
