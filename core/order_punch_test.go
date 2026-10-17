package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validLimitOrderRequest() OrderPunchRequest {
	return OrderPunchRequest{
		AccountID:  "acc-handle-123",
		Instrument: "INST-A",
		Side:       OrderSideBuy,
		Type:       OrderTypeLimit,
		TIF:        TimeInForceGoodTillCancel,
		Qty:        10,
		LimitPrice: 25500,
	}
}

func validMarketOrderRequest() OrderPunchRequest {
	return OrderPunchRequest{
		AccountID:  "acc-handle-123",
		Instrument: "INST-A",
		Side:       OrderSideSell,
		Type:       OrderTypeMarket,
		TIF:        TimeInForceImmediateOrCancel,
		Qty:        5,
		LimitPrice: 0,
	}
}

func TestPunchOrderSendsExpectedRequestForLimit(t *testing.T) {
	t.Parallel()

	var observedMethod atomic.Value
	var observedPath atomic.Value
	var observedBody atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		observedMethod.Store(request.Method)
		observedPath.Store(request.URL.Path)

		var decoded OrderPunchRequest
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			t.Errorf("server decode: %v", err)
			responseWriter.WriteHeader(http.StatusBadRequest)
			return
		}
		observedBody.Store(decoded)

		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusAccepted)
		_, _ = responseWriter.Write([]byte(`{"order_id":"acc-handle-123:42"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	if err := client.PunchOrder(context.Background(), validLimitOrderRequest()); err != nil {
		t.Fatalf("PunchOrder: %v", err)
	}
	if got := observedMethod.Load().(string); got != http.MethodPost {
		t.Fatalf("method = %q, want POST", got)
	}
	if got := observedPath.Load().(string); got != orderPunchEndpointPath {
		t.Fatalf("path = %q, want %q", got, orderPunchEndpointPath)
	}
	body := observedBody.Load().(OrderPunchRequest)
	if body.Type != OrderTypeLimit {
		t.Fatalf("body.type = %q", body.Type)
	}
	if body.TIF != TimeInForceGoodTillCancel {
		t.Fatalf("body.tif = %q, want %q for limit", body.TIF, TimeInForceGoodTillCancel)
	}
	if body.LimitPrice <= 0 {
		t.Fatalf("body.limit_price = %d, want > 0 for limit", body.LimitPrice)
	}
	if body.Qty != 10 {
		t.Fatalf("body.qty = %d", body.Qty)
	}
	if body.Side != OrderSideBuy {
		t.Fatalf("body.side = %q", body.Side)
	}
	if body.Instrument != "INST-A" {
		t.Fatalf("body.instrument = %q", body.Instrument)
	}
	if body.AccountID != "acc-handle-123" {
		t.Fatalf("body.account_id = %q", body.AccountID)
	}
}

func TestPunchOrderSendsExpectedRequestForMarket(t *testing.T) {
	t.Parallel()

	var observedBody atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		var decoded OrderPunchRequest
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			t.Errorf("server decode: %v", err)
			responseWriter.WriteHeader(http.StatusBadRequest)
			return
		}
		observedBody.Store(decoded)
		responseWriter.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	if err := client.PunchOrder(context.Background(), validMarketOrderRequest()); err != nil {
		t.Fatalf("PunchOrder: %v", err)
	}
	body := observedBody.Load().(OrderPunchRequest)
	if body.Type != OrderTypeMarket {
		t.Fatalf("body.type = %q", body.Type)
	}
	if body.TIF != TimeInForceImmediateOrCancel {
		t.Fatalf("body.tif = %q, want %q for market", body.TIF, TimeInForceImmediateOrCancel)
	}
	if body.LimitPrice != 0 {
		t.Fatalf("body.limit_price = %d, want 0 for market", body.LimitPrice)
	}
	if body.Side != OrderSideSell {
		t.Fatalf("body.side = %q", body.Side)
	}
}

// Verifies the response body is drained (the server sees the request
// cleanly, no partial-write errors) even though PunchOrder's return
// value does not expose the order_id. Fire-and-forget means the body
// is ignored at the API level, but the transport must still consume
// it so the connection returns to the pool.
func TestPunchOrderDrainsResponseBodyOnSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusAccepted)
		// Large-ish body so a missing drain would be visible as a
		// connection-not-reused warning in -race.
		_, _ = responseWriter.Write([]byte(strings.Repeat(`{"order_id":"acc:1"},`, 500)))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	for callNumber := 0; callNumber < 5; callNumber++ {
		if err := client.PunchOrder(context.Background(), validLimitOrderRequest()); err != nil {
			t.Fatalf("PunchOrder call %d: %v", callNumber, err)
		}
	}
}

func TestPunchOrderSurfacesCallErrorOnBusinessReject(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = responseWriter.Write([]byte(`{"error":"account cannot cover","reason":"insufficient_funds"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	err := client.PunchOrder(context.Background(), validLimitOrderRequest())
	var callError *CallError
	if !errors.As(err, &callError) {
		t.Fatalf("expected *CallError, got %v", err)
	}
	if callError.Reason != ReasonInsufficientFunds {
		t.Fatalf("Reason = %q, want %q", callError.Reason, ReasonInsufficientFunds)
	}
	if callError.HTTPStatus != http.StatusUnprocessableEntity {
		t.Fatalf("HTTPStatus = %d", callError.HTTPStatus)
	}
	if callError.RequestPath != orderPunchEndpointPath {
		t.Fatalf("RequestPath = %q", callError.RequestPath)
	}
}

// Writes must never retry — a slow 504 does not mean "nothing
// happened", so retrying could duplicate a fill. Even with a non-zero
// retry cap configured, the server must see exactly one hit.
func TestPunchOrderDoesNotRetryOn5xxEvenWithRetriesConfigured(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
		responseWriter.WriteHeader(http.StatusGatewayTimeout)
	}))
	t.Cleanup(server.Close)

	client := newTestClientWithRetryPolicy(t, server, 3, 1*time.Millisecond)
	err := client.PunchOrder(context.Background(), validLimitOrderRequest())
	var callError *CallError
	if !errors.As(err, &callError) {
		t.Fatalf("expected *CallError on 504, got %v", err)
	}
	if got := serverHits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1 (writes must not retry)", got)
	}
}

func TestPunchOrderHonorsFireAndForgetTimeout(t *testing.T) {
	t.Parallel()
	serverWait := make(chan struct{})
	t.Cleanup(func() { close(serverWait) })

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		select {
		case <-serverWait:
		case <-request.Context().Done():
		case <-time.After(2 * time.Second):
		}
		responseWriter.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	// Shrink only the fire-and-forget budget; pre-open stays generous
	// so the test proves the right class budget is being applied.
	config := ClientConfig{
		BaseURL:          server.URL,
		WebSocketBaseURL: "ws://127.0.0.1:1",
		Timeouts: TimeoutConfig{
			PreOpenCall:       5 * time.Second,
			HotPathCall:       5 * time.Second,
			FireAndForgetCall: 50 * time.Millisecond,
		},
	}
	client, err := NewClient(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)

	startedAt := time.Now()
	err = client.PunchOrder(context.Background(), validLimitOrderRequest())
	elapsed := time.Since(startedAt)

	if err == nil {
		t.Fatalf("expected timeout error, got nil (elapsed=%v)", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded in chain, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("PunchOrder took %v, expected it to abort before 500ms", elapsed)
	}
}

// Pre-flight tests cover every rejected shape. Each row mutates one
// field of a valid baseline so the row isolates a single rule; the
// server fixture is a trap — any pre-flight failure that reached the
// network would increment serverHits and fail the test.
func TestPunchOrderPreFlightRejectsMalformedRequests(t *testing.T) {
	t.Parallel()

	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	testCases := []struct {
		name              string
		mutate            func(*OrderPunchRequest)
		wantErrorContains string
	}{
		{
			name:              "empty account id",
			mutate:            func(request *OrderPunchRequest) { request.AccountID = "" },
			wantErrorContains: "non-empty account id",
		},
		{
			name:              "empty instrument",
			mutate:            func(request *OrderPunchRequest) { request.Instrument = "" },
			wantErrorContains: "non-empty instrument",
		},
		{
			name:              "zero qty",
			mutate:            func(request *OrderPunchRequest) { request.Qty = 0 },
			wantErrorContains: "qty must be > 0",
		},
		{
			name:              "negative qty",
			mutate:            func(request *OrderPunchRequest) { request.Qty = -1 },
			wantErrorContains: "qty must be > 0",
		},
		{
			name:              "unknown side",
			mutate:            func(request *OrderPunchRequest) { request.Side = "long" },
			wantErrorContains: "side must be",
		},
		{
			name:              "uppercase side rejected (lowercase enums only)",
			mutate:            func(request *OrderPunchRequest) { request.Side = "BUY" },
			wantErrorContains: "side must be",
		},
		{
			name:              "unknown type",
			mutate:            func(request *OrderPunchRequest) { request.Type = "stop" },
			wantErrorContains: "type must be",
		},
		{
			name: "limit with tif=fok rejected",
			mutate: func(request *OrderPunchRequest) {
				request.Type = OrderTypeLimit
				request.TIF = "fok"
				request.LimitPrice = 100
			},
			wantErrorContains: `tif="gtc"`,
		},
		{
			name: "limit with tif=ioc rejected",
			mutate: func(request *OrderPunchRequest) {
				request.Type = OrderTypeLimit
				request.TIF = TimeInForceImmediateOrCancel
				request.LimitPrice = 100
			},
			wantErrorContains: `tif="gtc"`,
		},
		{
			name: "limit with zero price rejected",
			mutate: func(request *OrderPunchRequest) {
				request.Type = OrderTypeLimit
				request.TIF = TimeInForceGoodTillCancel
				request.LimitPrice = 0
			},
			wantErrorContains: "limit_price > 0",
		},
		{
			name: "limit with negative price rejected",
			mutate: func(request *OrderPunchRequest) {
				request.Type = OrderTypeLimit
				request.TIF = TimeInForceGoodTillCancel
				request.LimitPrice = -1
			},
			wantErrorContains: "limit_price > 0",
		},
		{
			name: "market with tif=gtc rejected",
			mutate: func(request *OrderPunchRequest) {
				request.Type = OrderTypeMarket
				request.TIF = TimeInForceGoodTillCancel
				request.LimitPrice = 0
			},
			wantErrorContains: `tif="ioc"`,
		},
		{
			name: "market with tif=fok rejected",
			mutate: func(request *OrderPunchRequest) {
				request.Type = OrderTypeMarket
				request.TIF = "fok"
				request.LimitPrice = 0
			},
			wantErrorContains: `tif="ioc"`,
		},
		{
			name: "market with non-zero price rejected",
			mutate: func(request *OrderPunchRequest) {
				request.Type = OrderTypeMarket
				request.TIF = TimeInForceImmediateOrCancel
				request.LimitPrice = 500
			},
			wantErrorContains: "limit_price == 0",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			request := validLimitOrderRequest()
			testCase.mutate(&request)
			err := client.PunchOrder(context.Background(), request)
			if err == nil {
				t.Fatalf("expected pre-flight error, got nil")
			}
			if !strings.Contains(err.Error(), testCase.wantErrorContains) {
				t.Fatalf("error %q does not contain %q", err.Error(), testCase.wantErrorContains)
			}
			if _, isCallError := err.(*CallError); isCallError {
				t.Fatalf("pre-flight error must not be a *CallError (never reached the wire): %v", err)
			}
		})
	}

	if serverHits.Load() != 0 {
		t.Fatalf("server was hit %d time(s); pre-flight errors must never reach the network", serverHits.Load())
	}
}
