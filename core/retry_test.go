package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIdempotentRetrySucceedsAfterTransient5xx(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		attemptNumber := serverHits.Add(1)
		if attemptNumber < 3 {
			responseWriter.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"account_id":"acc-after-retry"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClientWithRetryPolicy(t, server, 2, 1*time.Millisecond)
	accountID, err := client.OpenAccount(context.Background(), "agent-7")
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if accountID != "acc-after-retry" {
		t.Fatalf("accountID = %q", accountID)
	}
	if got := serverHits.Load(); got != 3 {
		t.Fatalf("server hits = %d, want 3 (initial + 2 retries)", got)
	}
}

func TestIdempotentRetryGivesUpAfterConfiguredCap(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	client := newTestClientWithRetryPolicy(t, server, 2, 1*time.Millisecond)
	_, err := client.OpenAccount(context.Background(), "agent-7")
	if err == nil {
		t.Fatalf("expected error after retries exhausted")
	}
	var callError *CallError
	if !errors.As(err, &callError) || callError.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("expected *CallError with 503 status, got %v", err)
	}
	if got := serverHits.Load(); got != 3 {
		t.Fatalf("server hits = %d, want 3 (initial + 2 retries)", got)
	}
}

func TestIdempotentRetryDoesNotRetryBusinessReject(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
		responseWriter.Header().Set("Content-Type", "application/json")
		responseWriter.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = responseWriter.Write([]byte(`{"reason":"something_specific"}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClientWithRetryPolicy(t, server, 3, 1*time.Millisecond)
	_, err := client.OpenAccount(context.Background(), "agent-7")
	var callError *CallError
	if !errors.As(err, &callError) {
		t.Fatalf("expected *CallError, got %v", err)
	}
	if got := serverHits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1 (4xx must not retry)", got)
	}
}

func TestIdempotentRetryStopsWhenOuterContextCancelled(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	// Backoff is long on purpose so the context cancels during the
	// wait, not between attempts.
	client := newTestClientWithRetryPolicy(t, server, 5, 500*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	startedAt := time.Now()
	_, err := client.OpenAccount(ctx, "agent-7")
	elapsed := time.Since(startedAt)

	if err == nil {
		t.Fatalf("expected error on cancelled context")
	}
	// The configured 5-retry policy would run far longer than the 50ms
	// context deadline if the wrapper did not honor cancellation.
	if elapsed > 300*time.Millisecond {
		t.Fatalf("retry wrapper did not honor outer ctx cancellation, elapsed=%v", elapsed)
	}
	if got := serverHits.Load(); got > 2 {
		t.Fatalf("too many server hits after cancellation: %d", got)
	}
}

func TestIdempotentRetryHonorsBackoffBetweenAttempts(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	const backoff = 40 * time.Millisecond
	client := newTestClientWithRetryPolicy(t, server, 2, backoff)

	startedAt := time.Now()
	_, _ = client.OpenAccount(context.Background(), "agent-7")
	elapsed := time.Since(startedAt)

	// Two retries → two backoff waits. Expect at least ~2 * backoff
	// elapsed (allow for scheduling noise). Attempts themselves are
	// essentially instant against the local test server.
	if elapsed < 2*backoff {
		t.Fatalf("expected at least %v elapsed for two backoff waits, got %v", 2*backoff, elapsed)
	}
	if got := serverHits.Load(); got != 3 {
		t.Fatalf("server hits = %d, want 3", got)
	}
}

func TestIsIdempotentRetryable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"CallError 500 is retryable", &CallError{HTTPStatus: 500}, true},
		{"CallError 503 is retryable", &CallError{HTTPStatus: 503}, true},
		{"CallError 400 is not retryable", &CallError{HTTPStatus: 400}, false},
		{"CallError 409 with reason is not retryable", &CallError{HTTPStatus: 409, Reason: "no_session"}, false},
		{"CallError 422 is not retryable", &CallError{HTTPStatus: 422}, false},
		{"transport-level error is retryable", errors.New("dial tcp: connection refused"), true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := isIdempotentRetryable(testCase.err)
			if got != testCase.want {
				t.Fatalf("isIdempotentRetryable(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}

func TestWaitBeforeRetryReturnsNilOnCompletion(t *testing.T) {
	t.Parallel()
	start := time.Now()
	err := waitBeforeRetry(context.Background(), 20*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("waitBeforeRetry returned too early: %v", elapsed)
	}
}

func TestWaitBeforeRetryReturnsCtxErrOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := waitBeforeRetry(ctx, 1*time.Second)
	if err == nil {
		t.Fatalf("expected context error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestWaitBeforeRetryWithZeroBackoffIsNoOp(t *testing.T) {
	t.Parallel()
	start := time.Now()
	if err := waitBeforeRetry(context.Background(), 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Millisecond {
		t.Fatalf("zero backoff should return immediately, elapsed=%v", elapsed)
	}
}

// Ensure the error message from a retry-wrapped 5xx preserves the
// stable CallError fields so downstream log boundary lines can still
// render method and path from the final failure.
func TestRetryExhaustionPreservesCallErrorContext(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	client := newTestClientWithRetryPolicy(t, server, 1, 1*time.Millisecond)
	_, err := client.OpenAccount(context.Background(), "agent-7")
	var callError *CallError
	if !errors.As(err, &callError) {
		t.Fatalf("expected *CallError after retry exhaustion, got %v", err)
	}
	if callError.HTTPMethod != http.MethodPost {
		t.Fatalf("HTTPMethod = %q", callError.HTTPMethod)
	}
	if callError.RequestPath != openAccountEndpointPath {
		t.Fatalf("RequestPath = %q", callError.RequestPath)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 in rendered error, got %v", err)
	}
}
