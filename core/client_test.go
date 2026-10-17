package core

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewClientRejectsNilLogger(t *testing.T) {
	t.Parallel()
	if _, err := NewClient(validTestConfig(), nil); err == nil {
		t.Fatalf("expected error when logger is nil, got nil")
	} else if !strings.Contains(err.Error(), "non-nil logger") {
		t.Fatalf("error message does not mention logger: %v", err)
	}
}

func TestNewClientRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	bad := validTestConfig()
	bad.BaseURL = ""
	if _, err := NewClient(bad, discardLogger()); err == nil {
		t.Fatalf("expected error for empty BaseURL, got nil")
	}
}

func TestNewClientSucceedsAndLogsReadyLineWithoutLeakingBaseURL(t *testing.T) {
	t.Parallel()
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	client, err := NewClient(validTestConfig(), logger)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client == nil {
		t.Fatalf("NewClient returned nil client with nil error")
	}
	logged := logBuffer.String()
	if !strings.Contains(logged, "core client ready") {
		t.Fatalf("missing ready log line: %q", logged)
	}
	if !strings.Contains(logged, "core.local:8080") {
		t.Fatalf("ready log missing host: %q", logged)
	}
	// The full URL (scheme + host) must never appear verbatim in logs —
	// only the host:port — because in some deployments the base URL
	// carries credentials in its userinfo component.
	if strings.Contains(logged, "http://core.local:8080") {
		t.Fatalf("ready log leaked full BaseURL: %q", logged)
	}
	if strings.Contains(logged, "ws://core.local:8080") {
		t.Fatalf("ready log leaked full WebSocketBaseURL: %q", logged)
	}
}

func TestNewClientUsesInjectedHTTPClient(t *testing.T) {
	t.Parallel()
	injected := &http.Client{Timeout: 1234 * time.Millisecond}
	config := validTestConfig()
	config.HTTPClient = injected
	client, err := NewClient(config, discardLogger())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.httpClient != injected {
		t.Fatalf("expected the injected HTTPClient to be used; got a different pointer")
	}
}

func TestNewClientBuildsPooledTransportWhenNoHTTPClientInjected(t *testing.T) {
	t.Parallel()
	client, err := NewClient(validTestConfig(), discardLogger())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected default *http.Transport, got %T", client.httpClient.Transport)
	}
	if transport.MaxIdleConns == 0 {
		t.Fatalf("expected pooled transport to configure MaxIdleConns, got zero")
	}
	if transport.IdleConnTimeout == 0 {
		t.Fatalf("expected pooled transport to configure IdleConnTimeout, got zero")
	}
}

func TestClientCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	client, err := NewClient(validTestConfig(), discardLogger())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.Close()
	client.Close()
	client.Close()
}

func TestHostForLog(t *testing.T) {
	testCases := []struct {
		name     string
		rawURL   string
		wantHost string
	}{
		{"empty string returns empty", "", ""},
		{"http with explicit port", "http://core.local:8080/path", "core.local:8080"},
		{"https without port", "https://core.local", "core.local"},
		{"ws with explicit port", "ws://127.0.0.1:9000", "127.0.0.1:9000"},
		{"wss without port", "wss://core.local", "core.local"},
		{"relative path has no host", "relative/path/only", "unparseable"},
		{"url with unparseable brackets", "http://[::invalid", "unparseable"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := hostForLog(testCase.rawURL)
			if got != testCase.wantHost {
				t.Fatalf("hostForLog(%q) = %q, want %q", testCase.rawURL, got, testCase.wantHost)
			}
		})
	}
}
