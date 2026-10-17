package core

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient builds a Client whose BaseURL points at the given test
// server. Timeouts are deliberately short so a mis-wired test fails
// fast rather than hanging. The WebSocket base URL is a placeholder
// (unused in Stage 2) that only has to pass Validate.
func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	config := ClientConfig{
		BaseURL:                server.URL,
		WebSocketBaseURL:       "ws://127.0.0.1:1",
		Timeouts:               validTestTimeouts(),
		MaxIdempotentRetries:   0,
		IdempotentRetryBackoff: 0,
	}
	client, err := NewClient(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newTestClient: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

// newTestClientWithShortPreOpenCallTimeout mirrors newTestClient but
// shrinks the pre-open class budget, used by the timeout test to
// trigger a context deadline without stalling the whole test run.
func newTestClientWithShortPreOpenCallTimeout(t *testing.T, server *httptest.Server, budget time.Duration) *Client {
	t.Helper()
	config := ClientConfig{
		BaseURL:          server.URL,
		WebSocketBaseURL: "ws://127.0.0.1:1",
		Timeouts: TimeoutConfig{
			PreOpenCall:       budget,
			HotPathCall:       budget,
			FireAndForgetCall: budget,
		},
	}
	client, err := NewClient(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newTestClientWithShortPreOpenCallTimeout: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

func TestCallClassStringCoversEveryClass(t *testing.T) {
	t.Parallel()
	cases := map[callClass]string{
		preOpenCall:       "pre_open",
		hotPathCall:       "hot_path",
		fireAndForgetCall: "fire_and_forget",
	}
	for class, want := range cases {
		if got := class.String(); got != want {
			t.Fatalf("callClass(%d).String() = %q, want %q", int(class), got, want)
		}
	}
}

func TestBuildRequestURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		baseURL         string
		requestPath     string
		wantRequestFull string
	}{
		{"base without slash + path with slash", "http://core.local", "/v1/account/open", "http://core.local/v1/account/open"},
		{"base with slash + path with slash", "http://core.local/", "/v1/account/open", "http://core.local/v1/account/open"},
		{"base with slash + path without slash", "http://core.local/", "v1/account/open", "http://core.local/v1/account/open"},
		{"base without slash + path without slash", "http://core.local", "v1/account/open", "http://core.local/v1/account/open"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			config := validTestConfig()
			config.BaseURL = testCase.baseURL
			client, err := NewClient(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			t.Cleanup(client.Close)
			got := client.buildRequestURL(testCase.requestPath)
			if got != testCase.wantRequestFull {
				t.Fatalf("buildRequestURL = %q, want %q", got, testCase.wantRequestFull)
			}
		})
	}
}

func TestApplyCallClassTimeoutImposesBudgetWhenCallerHasNone(t *testing.T) {
	t.Parallel()
	client, err := NewClient(validTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)
	boundedCtx, cancel := client.applyCallClassTimeout(context.Background(), preOpenCall)
	defer cancel()
	deadline, hasDeadline := boundedCtx.Deadline()
	if !hasDeadline {
		t.Fatalf("expected derived ctx to have a deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > client.config.Timeouts.PreOpenCall+10*time.Millisecond {
		t.Fatalf("derived deadline %v outside expected window", remaining)
	}
}

func TestApplyCallClassTimeoutRespectsShorterCallerDeadline(t *testing.T) {
	t.Parallel()
	client, err := NewClient(validTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)

	callerCtx, callerCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer callerCancel()
	boundedCtx, cancel := client.applyCallClassTimeout(callerCtx, preOpenCall)
	defer cancel()

	deadline, hasDeadline := boundedCtx.Deadline()
	if !hasDeadline {
		t.Fatalf("expected ctx to have a deadline")
	}
	// Shorter caller deadline must be honored, so the remaining budget
	// here should be bounded by the caller's 20ms, not by the much
	// larger pre-open class budget.
	if time.Until(deadline) > 50*time.Millisecond {
		t.Fatalf("expected caller's shorter deadline to be honored, got %v remaining", time.Until(deadline))
	}
}

func TestDecodeCallErrorExtractsReasonAndMessage(t *testing.T) {
	t.Parallel()
	response := responseWithBody(409, `{"error":"account not installed","reason":"no_session"}`)
	callError := decodeCallError(response, "POST", "/v1/orders")
	if callError.HTTPStatus != 409 {
		t.Fatalf("HTTPStatus = %d, want 409", callError.HTTPStatus)
	}
	if callError.Reason != "no_session" {
		t.Fatalf("Reason = %q, want %q", callError.Reason, "no_session")
	}
	if callError.Message != "account not installed" {
		t.Fatalf("Message = %q, want %q", callError.Message, "account not installed")
	}
}

func TestDecodeCallErrorHandlesReasonOnlyEnvelope(t *testing.T) {
	t.Parallel()
	response := responseWithBody(422, `{"reason":"insufficient_funds"}`)
	callError := decodeCallError(response, "POST", "/v1/orders")
	if callError.Reason != "insufficient_funds" {
		t.Fatalf("Reason = %q", callError.Reason)
	}
	if callError.Message != "" {
		t.Fatalf("Message should be empty when envelope has no error field, got %q", callError.Message)
	}
}

func TestDecodeCallErrorHandlesEmptyBody(t *testing.T) {
	t.Parallel()
	response := responseWithBody(503, "")
	callError := decodeCallError(response, "GET", "/v1/account/cash")
	if callError.HTTPStatus != 503 {
		t.Fatalf("HTTPStatus = %d", callError.HTTPStatus)
	}
	if callError.Reason != "" || callError.Message != "" {
		t.Fatalf("expected empty reason and message, got reason=%q message=%q", callError.Reason, callError.Message)
	}
}

func TestDecodeCallErrorKeepsNonJSONBodyAsMessage(t *testing.T) {
	t.Parallel()
	response := responseWithBody(502, "<html>bad gateway</html>")
	callError := decodeCallError(response, "GET", "/v1/account/cash")
	if callError.Message != "<html>bad gateway</html>" {
		t.Fatalf("Message = %q, want raw body preserved", callError.Message)
	}
	if callError.Reason != "" {
		t.Fatalf("Reason should be empty on non-JSON body, got %q", callError.Reason)
	}
}

func TestDecodeCallErrorTruncatesLongNonJSONBody(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("x", errorMessageMaxLength*4)
	response := responseWithBody(500, huge)
	callError := decodeCallError(response, "GET", "/v1/account/cash")
	if len(callError.Message) > errorMessageMaxLength+len("…") {
		t.Fatalf("Message length %d exceeded truncation cap %d", len(callError.Message), errorMessageMaxLength)
	}
	if !strings.HasSuffix(callError.Message, "…") {
		t.Fatalf("expected truncated message to carry ellipsis suffix, got %q", callError.Message[len(callError.Message)-10:])
	}
}

func TestExecuteJSONCallTransportErrorWraps(t *testing.T) {
	t.Parallel()
	// Point BaseURL at a closed port so Do returns a dial error.
	config := validTestConfig()
	config.BaseURL = "http://127.0.0.1:1"
	config.Timeouts.PreOpenCall = 50 * time.Millisecond
	client, err := NewClient(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)

	err = client.executeJSONCall(context.Background(), preOpenCall, http.MethodGet, "/anything", nil, nil)
	if err == nil {
		t.Fatalf("expected transport error, got nil")
	}
	if _, ok := err.(*CallError); ok {
		t.Fatalf("transport failure must not surface as *CallError, got %v", err)
	}
	if !strings.Contains(err.Error(), "transport") {
		t.Fatalf("transport error should mention transport, got %v", err)
	}
}

func responseWithBody(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}
