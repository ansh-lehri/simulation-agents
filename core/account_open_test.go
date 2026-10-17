package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAccountSendsExpectedRequestAndDecodesAccountID(t *testing.T) {
	t.Parallel()

	var observedMethod atomic.Value // string
	var observedPath atomic.Value   // string
	var observedContentType atomic.Value
	var observedAccept atomic.Value
	var observedBody atomic.Value // openAccountRequestEnvelope

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		observedMethod.Store(request.Method)
		observedPath.Store(request.URL.Path)
		observedContentType.Store(request.Header.Get("Content-Type"))
		observedAccept.Store(request.Header.Get("Accept"))

		var decoded openAccountRequestEnvelope
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			t.Errorf("server-side decode: %v", err)
			responseWriter.WriteHeader(http.StatusBadRequest)
			return
		}
		observedBody.Store(decoded)

		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"account_id":"acc-handle-123"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)

	accountID, err := client.OpenAccount(context.Background(), "agent-7")
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if accountID != "acc-handle-123" {
		t.Fatalf("accountID = %q, want %q", accountID, "acc-handle-123")
	}

	if got := observedMethod.Load().(string); got != http.MethodPost {
		t.Fatalf("request method = %q, want POST", got)
	}
	if got := observedPath.Load().(string); got != openAccountEndpointPath {
		t.Fatalf("request path = %q, want %q", got, openAccountEndpointPath)
	}
	if got := observedContentType.Load().(string); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := observedAccept.Load().(string); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
	body := observedBody.Load().(openAccountRequestEnvelope)
	if body.User.UserID != "agent-7" {
		t.Fatalf("body user_id = %q, want %q", body.User.UserID, "agent-7")
	}
	if body.User.Email != "" {
		t.Fatalf("body email = %q, want empty string per contract", body.User.Email)
	}
}

func TestOpenAccountRejectsEmptyUserIDWithoutHittingServer(t *testing.T) {
	t.Parallel()
	var serverHit atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		serverHit.Add(1)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.OpenAccount(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error for empty user id")
	}
	if serverHit.Load() != 0 {
		t.Fatalf("server should not be hit when user id is empty")
	}
}

func TestOpenAccountSurfacesCallErrorOnBusinessReject(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusConflict)
		_, _ = responseWriter.Write([]byte(`{"error":"user id taken","reason":"something_specific"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.OpenAccount(context.Background(), "agent-7")
	if err == nil {
		t.Fatalf("expected error on 409")
	}
	var callError *CallError
	if !errors.As(err, &callError) {
		t.Fatalf("expected *CallError in chain, got %T: %v", err, err)
	}
	if callError.HTTPStatus != http.StatusConflict {
		t.Fatalf("HTTPStatus = %d, want %d", callError.HTTPStatus, http.StatusConflict)
	}
	if callError.Reason != "something_specific" {
		t.Fatalf("Reason = %q, want %q", callError.Reason, "something_specific")
	}
	if callError.RequestPath != openAccountEndpointPath {
		t.Fatalf("RequestPath = %q", callError.RequestPath)
	}
	if callError.HTTPMethod != http.MethodPost {
		t.Fatalf("HTTPMethod = %q", callError.HTTPMethod)
	}
}

func TestOpenAccountSurfacesCallErrorOn5xx(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.OpenAccount(context.Background(), "agent-7")
	var callError *CallError
	if !errors.As(err, &callError) {
		t.Fatalf("expected *CallError on 503, got %v", err)
	}
	if callError.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("HTTPStatus = %d", callError.HTTPStatus)
	}
}

func TestOpenAccountDoesNotRetryBusinessReject(t *testing.T) {
	t.Parallel()
	// A 4xx carries a stable business reject that will reproduce on
	// the next attempt verbatim, so retrying wastes a round trip.
	// Exactly one server hit is expected even when retries are
	// configured.
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusConflict)
		_, _ = responseWriter.Write([]byte(`{"reason":"something_specific"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClientWithRetryPolicy(t, server, 3, 1*time.Millisecond)
	_, err := client.OpenAccount(context.Background(), "agent-7")
	if err == nil {
		t.Fatalf("expected error on 409")
	}
	if serverHits.Load() != 1 {
		t.Fatalf("expected exactly 1 hit on 4xx (no retry), got %d", serverHits.Load())
	}
}

func TestOpenAccountReturnsErrorOnEmptyAccountIDFromServer(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusOK)
		_, _ = responseWriter.Write([]byte(`{"account_id":""}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.OpenAccount(context.Background(), "agent-7")
	if err == nil {
		t.Fatalf("expected error when server returns empty account_id")
	}
	if _, isCallError := err.(*CallError); isCallError {
		t.Fatalf("empty account_id is a server contract break, not a CallError, got %T", err)
	}
	if !strings.Contains(err.Error(), "empty account_id") {
		t.Fatalf("error should mention empty account_id, got %v", err)
	}
}

func TestOpenAccountHonorsPreOpenCallTimeoutWhenServerStalls(t *testing.T) {
	t.Parallel()
	// Server stalls past the configured pre-open budget so the
	// request's context expires and Do returns a transport error that
	// unwraps to context.DeadlineExceeded.
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

	client := newTestClientWithShortPreOpenCallTimeout(t, server, 50*time.Millisecond)
	startedAt := time.Now()
	_, err := client.OpenAccount(context.Background(), "agent-7")
	elapsed := time.Since(startedAt)

	if err == nil {
		t.Fatalf("expected timeout error, got nil (elapsed=%v)", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded in chain, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("OpenAccount took %v, expected it to abort well before 500ms", elapsed)
	}
}

