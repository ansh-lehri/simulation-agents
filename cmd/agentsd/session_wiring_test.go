package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"simulation-agents/core"
)

// sessionWiringTestClient builds a *core.Client pointed at the given
// test server — the same minimal, valid ClientConfig shape the core
// package's own tests use, since cmd/agentsd has no other reason to
// construct a *core.Client directly.
func sessionWiringTestClient(t *testing.T, server *httptest.Server) *core.Client {
	t.Helper()
	config := core.ClientConfig{
		BaseURL:          server.URL,
		WebSocketBaseURL: "ws://127.0.0.1:1",
		Timeouts: core.TimeoutConfig{
			PreOpenCall:       5 * time.Second,
			HotPathCall:       500 * time.Millisecond,
			FireAndForgetCall: 1 * time.Second,
		},
	}
	client, err := core.NewClient(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("core.NewClient: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

// GetAccountCash on the adapter must forward straight through to the
// wrapped *core.Client, the same byte-for-byte pass-through shape as
// the adapter's other methods (JoinCanonical, GetObservation,
// PunchOrder, ...).
func TestSessionCoreClientAdapter_GetAccountCashForwardsToInnerClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"trading_cash":87654,"bank_cash":321}`))
	}))
	t.Cleanup(server.Close)

	adapter := &sessionCoreClientAdapter{inner: sessionWiringTestClient(t, server)}

	cash, err := adapter.GetAccountCash(context.Background(), "acc-handle-1")
	if err != nil {
		t.Fatalf("GetAccountCash: %v", err)
	}
	if cash.TradingCash != 87654 {
		t.Errorf("TradingCash = %d, want 87654", cash.TradingCash)
	}
	if cash.BankCash != 321 {
		t.Errorf("BankCash = %d, want 321", cash.BankCash)
	}
}

// An error from the inner client (e.g. a non-2xx response) must
// surface through the adapter unchanged, not be swallowed.
func TestSessionCoreClientAdapter_GetAccountCashForwardsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	adapter := &sessionCoreClientAdapter{inner: sessionWiringTestClient(t, server)}

	if _, err := adapter.GetAccountCash(context.Background(), "acc-handle-1"); err == nil {
		t.Fatal("GetAccountCash: expected an error from a 500 response, got nil")
	}
}
