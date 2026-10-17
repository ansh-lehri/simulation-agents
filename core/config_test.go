package core

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// validTestConfig returns a baseline ClientConfig that passes Validate.
// Tests that want to exercise a specific failure mode mutate exactly one
// field and assert the error contains the expected substring, so each
// row isolates a single validation rule.
func validTestConfig() ClientConfig {
	return ClientConfig{
		BaseURL:                "http://core.local:8080",
		WebSocketBaseURL:       "ws://core.local:8080",
		Timeouts:               validTestTimeouts(),
		MaxIdempotentRetries:   2,
		IdempotentRetryBackoff: 50 * time.Millisecond,
	}
}

func validTestTimeouts() TimeoutConfig {
	return TimeoutConfig{
		PreOpenCall:       5 * time.Second,
		HotPathCall:       500 * time.Millisecond,
		FireAndForgetCall: 1 * time.Second,
	}
}

func TestClientConfigValidate(t *testing.T) {
	testCases := []struct {
		name              string
		mutate            func(*ClientConfig)
		wantErrorContains string
	}{
		{
			name:   "valid baseline passes",
			mutate: func(_ *ClientConfig) {},
		},
		{
			name: "valid with https and wss schemes",
			mutate: func(config *ClientConfig) {
				config.BaseURL = "https://core.local"
				config.WebSocketBaseURL = "wss://core.local"
			},
		},
		{
			name: "valid with retries disabled regardless of backoff",
			mutate: func(config *ClientConfig) {
				config.MaxIdempotentRetries = 0
				config.IdempotentRetryBackoff = 0
			},
		},
		{
			name:              "empty BaseURL is rejected",
			mutate:            func(config *ClientConfig) { config.BaseURL = "" },
			wantErrorContains: "BaseURL is empty",
		},
		{
			name:              "BaseURL with non-http scheme is rejected",
			mutate:            func(config *ClientConfig) { config.BaseURL = "ftp://core.local" },
			wantErrorContains: "BaseURL scheme",
		},
		{
			name:              "BaseURL without host is rejected",
			mutate:            func(config *ClientConfig) { config.BaseURL = "http:///path" },
			wantErrorContains: "BaseURL host is empty",
		},
		{
			name:              "empty WebSocketBaseURL is rejected",
			mutate:            func(config *ClientConfig) { config.WebSocketBaseURL = "" },
			wantErrorContains: "WebSocketBaseURL is empty",
		},
		{
			name:              "WebSocketBaseURL with http scheme is rejected",
			mutate:            func(config *ClientConfig) { config.WebSocketBaseURL = "http://core.local" },
			wantErrorContains: "WebSocketBaseURL scheme",
		},
		{
			name:              "WebSocketBaseURL without host is rejected",
			mutate:            func(config *ClientConfig) { config.WebSocketBaseURL = "ws:///path" },
			wantErrorContains: "WebSocketBaseURL host is empty",
		},
		{
			name:              "zero PreOpenCall timeout is rejected",
			mutate:            func(config *ClientConfig) { config.Timeouts.PreOpenCall = 0 },
			wantErrorContains: "PreOpenCall must be > 0",
		},
		{
			name:              "negative HotPathCall timeout is rejected",
			mutate:            func(config *ClientConfig) { config.Timeouts.HotPathCall = -time.Second },
			wantErrorContains: "HotPathCall must be > 0",
		},
		{
			name:              "zero FireAndForgetCall timeout is rejected",
			mutate:            func(config *ClientConfig) { config.Timeouts.FireAndForgetCall = 0 },
			wantErrorContains: "FireAndForgetCall must be > 0",
		},
		{
			name:              "negative MaxIdempotentRetries is rejected",
			mutate:            func(config *ClientConfig) { config.MaxIdempotentRetries = -1 },
			wantErrorContains: "MaxIdempotentRetries must be >= 0",
		},
		{
			name: "retries enabled with zero backoff is rejected",
			mutate: func(config *ClientConfig) {
				config.MaxIdempotentRetries = 3
				config.IdempotentRetryBackoff = 0
			},
			wantErrorContains: "IdempotentRetryBackoff must be > 0 when retries are enabled",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			config := validTestConfig()
			testCase.mutate(&config)
			gotErr := config.Validate()
			if testCase.wantErrorContains == "" {
				if gotErr != nil {
					t.Fatalf("expected nil error, got %v", gotErr)
				}
				return
			}
			if gotErr == nil {
				t.Fatalf("expected error containing %q, got nil", testCase.wantErrorContains)
			}
			if !strings.Contains(gotErr.Error(), testCase.wantErrorContains) {
				t.Fatalf("error %q does not contain %q", gotErr.Error(), testCase.wantErrorContains)
			}
		})
	}
}

func TestClientConfigValidateAcceptsInjectedHTTPClient(t *testing.T) {
	t.Parallel()
	config := validTestConfig()
	config.HTTPClient = &http.Client{}
	if err := config.Validate(); err != nil {
		t.Fatalf("validate with injected HTTPClient: %v", err)
	}
}
