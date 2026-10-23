package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestJoinCanonicalSendsExpectedRequestAndDecodesResponse(t *testing.T) {
	t.Parallel()

	var observedMethod atomic.Value
	var observedPath atomic.Value
	var observedContentType atomic.Value
	var observedBody atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		observedMethod.Store(request.Method)
		observedPath.Store(request.URL.Path)
		observedContentType.Store(request.Header.Get("Content-Type"))

		var decoded canonicalSessionJoinRequestEnvelope
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			t.Errorf("server decode: %v", err)
			responseWriter.WriteHeader(http.StatusBadRequest)
			return
		}
		observedBody.Store(decoded)

		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{
			"session_id":"sess-abc",
			"participant_id":"part-xyz",
			"status":"active"
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	join, err := client.JoinCanonical(context.Background(), "agent-7", "IN")
	if err != nil {
		t.Fatalf("JoinCanonical: %v", err)
	}

	if join.SessionID != "sess-abc" {
		t.Fatalf("SessionID = %q", join.SessionID)
	}
	if join.ParticipantID != "part-xyz" {
		t.Fatalf("ParticipantID = %q", join.ParticipantID)
	}
	if join.Status != "active" {
		t.Fatalf("Status = %q", join.Status)
	}

	if got := observedMethod.Load().(string); got != http.MethodPost {
		t.Fatalf("method = %q, want POST", got)
	}
	if got := observedPath.Load().(string); got != canonicalSessionJoinEndpointPath {
		t.Fatalf("path = %q, want %q", got, canonicalSessionJoinEndpointPath)
	}
	if got := observedContentType.Load().(string); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	body := observedBody.Load().(canonicalSessionJoinRequestEnvelope)
	if body.User.UserID != "agent-7" {
		t.Fatalf("body user_id = %q", body.User.UserID)
	}
	if body.Market != "IN" {
		t.Fatalf("body market = %q", body.Market)
	}
	if body.User.OwnerType != "agent" {
		t.Fatalf("body owner_type = %q, want %q (this client represents only agents)", body.User.OwnerType, "agent")
	}
}

func TestJoinOnDemandSendsExpectedRequestAndDecodesResponse(t *testing.T) {
	t.Parallel()

	var observedPath atomic.Value
	var observedBody atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		observedPath.Store(request.URL.Path)

		var decoded onDemandSessionJoinRequestEnvelope
		if err := json.NewDecoder(request.Body).Decode(&decoded); err != nil {
			t.Errorf("server decode: %v", err)
			responseWriter.WriteHeader(http.StatusBadRequest)
			return
		}
		observedBody.Store(decoded)

		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{
			"session_id":"sess-od-abc",
			"participant_id":"part-od-xyz",
			"status":"active"
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	join, err := client.JoinOnDemand(context.Background(), "agent-7", "od:exchange-1")
	if err != nil {
		t.Fatalf("JoinOnDemand: %v", err)
	}

	if join.SessionID != "sess-od-abc" || join.ParticipantID != "part-od-xyz" || join.Status != "active" {
		t.Fatalf("join = %+v", join)
	}
	if got := observedPath.Load().(string); got != onDemandSessionJoinEndpointPath {
		t.Fatalf("path = %q, want %q", got, onDemandSessionJoinEndpointPath)
	}
	body := observedBody.Load().(onDemandSessionJoinRequestEnvelope)
	if body.User.UserID != "agent-7" {
		t.Fatalf("body user_id = %q", body.User.UserID)
	}
	if body.ExchangeID != "od:exchange-1" {
		t.Fatalf("body exchange_id = %q", body.ExchangeID)
	}
}

func TestJoinOnDemandRejectsEmptyIDsWithoutHittingServer(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		t.Error("server should not be hit on empty-id rejection")
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	if _, err := client.JoinOnDemand(context.Background(), "", "od:exchange-1"); err == nil {
		t.Fatal("JoinOnDemand with empty agentUserID: want error, got nil")
	}
	if _, err := client.JoinOnDemand(context.Background(), "agent-7", ""); err == nil {
		t.Fatal("JoinOnDemand with empty exchangeID: want error, got nil")
	}
}

func TestJoinCanonicalRejectsEmptyUserIDWithoutHittingServer(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.JoinCanonical(context.Background(), "", "IN")
	if err == nil {
		t.Fatalf("expected error on empty user id")
	}
	if serverHits.Load() != 0 {
		t.Fatalf("server was hit on empty user id")
	}
}

func TestJoinCanonicalRejectsEmptyMarketTagWithoutHittingServer(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.JoinCanonical(context.Background(), "agent-7", "")
	if err == nil {
		t.Fatalf("expected error on empty market tag")
	}
	if serverHits.Load() != 0 {
		t.Fatalf("server was hit on empty market tag")
	}
}

func TestJoinCanonicalSurfacesCallErrorOnBusinessReject(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusConflict)
		_, _ = responseWriter.Write([]byte(`{"error":"already installed","reason":"some_business_reason"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.JoinCanonical(context.Background(), "agent-7", "IN")
	var callError *CallError
	if !errors.As(err, &callError) {
		t.Fatalf("expected *CallError, got %v", err)
	}
	if callError.HTTPStatus != http.StatusConflict {
		t.Fatalf("HTTPStatus = %d", callError.HTTPStatus)
	}
	if callError.Reason != "some_business_reason" {
		t.Fatalf("Reason = %q", callError.Reason)
	}
	if callError.RequestPath != canonicalSessionJoinEndpointPath {
		t.Fatalf("RequestPath = %q", callError.RequestPath)
	}
	if callError.HTTPMethod != http.MethodPost {
		t.Fatalf("HTTPMethod = %q", callError.HTTPMethod)
	}
}

// JoinCanonical is a write — a 5xx must surface after exactly one
// attempt even when the client is configured with a non-zero retry
// cap. Retrying an install risks a duplicate server-side mutation;
// the caller reconciles via a fresh read instead.
func TestJoinCanonicalDoesNotRetryOn5xxEvenWithRetriesConfigured(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	client := newTestClientWithRetryPolicy(t, server, 3, 1*time.Millisecond)
	_, err := client.JoinCanonical(context.Background(), "agent-7", "IN")
	var callError *CallError
	if !errors.As(err, &callError) {
		t.Fatalf("expected *CallError on 503, got %v", err)
	}
	if got := serverHits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1 (writes must not retry)", got)
	}
}

func TestJoinCanonicalHonorsPreOpenCallTimeout(t *testing.T) {
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

	client := newTestClientWithShortPreOpenCallTimeout(t, server, 50*time.Millisecond)
	startedAt := time.Now()
	_, err := client.JoinCanonical(context.Background(), "agent-7", "IN")
	elapsed := time.Since(startedAt)

	if err == nil {
		t.Fatalf("expected timeout error, got nil (elapsed=%v)", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded in chain, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("JoinCanonical took %v, expected it to abort well before 500ms", elapsed)
	}
}
