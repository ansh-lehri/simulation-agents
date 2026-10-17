package core

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetAccountCashSendsExpectedRequestAndDecodesFields(t *testing.T) {
	t.Parallel()

	var observedMethod atomic.Value // string
	var observedPath atomic.Value   // string
	var observedAccountIDQuery atomic.Value
	var observedAcceptHeader atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		observedMethod.Store(request.Method)
		observedPath.Store(request.URL.Path)
		observedAccountIDQuery.Store(request.URL.Query().Get("account_id"))
		observedAcceptHeader.Store(request.Header.Get("Accept"))

		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"trading_cash":12345678,"bank_cash":42}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	cash, err := client.GetAccountCash(context.Background(), "acc-handle-123")
	if err != nil {
		t.Fatalf("GetAccountCash: %v", err)
	}
	if cash.TradingCash != 12345678 {
		t.Fatalf("TradingCash = %d, want 12345678", cash.TradingCash)
	}
	if cash.BankCash != 42 {
		t.Fatalf("BankCash = %d, want 42", cash.BankCash)
	}
	if got := observedMethod.Load().(string); got != http.MethodGet {
		t.Fatalf("request method = %q, want GET", got)
	}
	if got := observedPath.Load().(string); got != accountCashEndpointPath {
		t.Fatalf("request path = %q, want %q", got, accountCashEndpointPath)
	}
	if got := observedAccountIDQuery.Load().(string); got != "acc-handle-123" {
		t.Fatalf("account_id query = %q", got)
	}
	if got := observedAcceptHeader.Load().(string); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
}

func TestGetAccountCashPreservesInt64PrecisionNearMaxInt(t *testing.T) {
	t.Parallel()
	// A float64 would lose precision at this magnitude; using json.Number
	// semantics via struct int64 tags must preserve the exact value.
	highValue := int64(math.MaxInt64 - 7)
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"trading_cash":9223372036854775800,"bank_cash":0}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	cash, err := client.GetAccountCash(context.Background(), "acc-handle-123")
	if err != nil {
		t.Fatalf("GetAccountCash: %v", err)
	}
	if cash.TradingCash != highValue {
		t.Fatalf("TradingCash = %d, want %d (precision lost?)", cash.TradingCash, highValue)
	}
}

func TestGetAccountCashRejectsEmptyAccountIDWithoutHittingServer(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.GetAccountCash(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error on empty account id")
	}
	if !strings.Contains(err.Error(), "non-empty account id") {
		t.Fatalf("error should mention account id requirement, got %v", err)
	}
	if serverHits.Load() != 0 {
		t.Fatalf("server was hit on empty account id")
	}
}

func TestGetAccountCashReturnsCallErrorOn404(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.GetAccountCash(context.Background(), "acc-handle-unknown")
	var callError *CallError
	if !errors.As(err, &callError) {
		t.Fatalf("expected *CallError, got %v", err)
	}
	if callError.HTTPStatus != http.StatusNotFound {
		t.Fatalf("HTTPStatus = %d", callError.HTTPStatus)
	}
}

func TestGetAccountCashRetriesTransient5xxThenSucceeds(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		attemptNumber := serverHits.Add(1)
		if attemptNumber == 1 {
			responseWriter.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"trading_cash":500,"bank_cash":0}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClientWithRetryPolicy(t, server, 2, 1*time.Millisecond)
	cash, err := client.GetAccountCash(context.Background(), "acc-handle-123")
	if err != nil {
		t.Fatalf("GetAccountCash: %v", err)
	}
	if cash.TradingCash != 500 {
		t.Fatalf("TradingCash = %d, want 500", cash.TradingCash)
	}
	if got := serverHits.Load(); got != 2 {
		t.Fatalf("server hits = %d, want 2", got)
	}
}
