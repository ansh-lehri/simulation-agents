package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"simulation-agents/core"
)

// Environment variable names that drive the Core client's configuration.
// Kept as named constants rather than scattered literals so the
// test file can reference the same keys and so missing-env error
// messages name the exact variable an operator should set.
const (
	envCoreBaseURL                = "AGENTSD_CORE_BASE_URL"
	envCoreWebSocketBaseURL       = "AGENTSD_CORE_WEBSOCKET_BASE_URL"
	envCoreTimeoutPreOpenCall     = "AGENTSD_CORE_TIMEOUT_PREOPEN_CALL"
	envCoreTimeoutHotPathCall     = "AGENTSD_CORE_TIMEOUT_HOTPATH_CALL"
	envCoreTimeoutFireForgetCall  = "AGENTSD_CORE_TIMEOUT_FIREANDFORGET_CALL"
	envCoreMaxIdempotentRetries   = "AGENTSD_CORE_MAX_IDEMPOTENT_RETRIES"
	envCoreIdempotentRetryBackoff = "AGENTSD_CORE_IDEMPOTENT_RETRY_BACKOFF"
)

// Default values applied when an optional env var is empty or unset.
// BaseURL and WebSocketBaseURL have no default on purpose — leaving
// them unset in deployment is a configuration bug the process should
// refuse to start on, not silently fall back to localhost.
const (
	defaultCoreTimeoutPreOpenCall     = 10 * time.Second
	defaultCoreTimeoutHotPathCall     = 500 * time.Millisecond
	defaultCoreTimeoutFireForgetCall  = 1 * time.Second
	defaultCoreMaxIdempotentRetries   = 2
	defaultCoreIdempotentRetryBackoff = 100 * time.Millisecond
)

// loadCoreClientConfig reads the Core client configuration from the
// environment, applying defaults for every optional variable and
// returning the first problem it finds. The resulting ClientConfig has
// already passed its own Validate so core.NewClient will accept it
// without further checks.
//
// This function is the ONLY place the executable touches os.Getenv
// for Core-side configuration. The core package itself reads no
// environment variables.
func loadCoreClientConfig() (core.ClientConfig, error) {
	baseURL := os.Getenv(envCoreBaseURL)
	if baseURL == "" {
		return core.ClientConfig{}, fmt.Errorf(
			"agentsd: %s is required (no sensible default)", envCoreBaseURL,
		)
	}
	webSocketBaseURL := os.Getenv(envCoreWebSocketBaseURL)
	if webSocketBaseURL == "" {
		return core.ClientConfig{}, fmt.Errorf(
			"agentsd: %s is required (schemes differ from HTTP so cannot be inferred)",
			envCoreWebSocketBaseURL,
		)
	}

	preOpenCallTimeout, err := durationFromEnvWithDefault(envCoreTimeoutPreOpenCall, defaultCoreTimeoutPreOpenCall)
	if err != nil {
		return core.ClientConfig{}, err
	}
	hotPathCallTimeout, err := durationFromEnvWithDefault(envCoreTimeoutHotPathCall, defaultCoreTimeoutHotPathCall)
	if err != nil {
		return core.ClientConfig{}, err
	}
	fireAndForgetCallTimeout, err := durationFromEnvWithDefault(envCoreTimeoutFireForgetCall, defaultCoreTimeoutFireForgetCall)
	if err != nil {
		return core.ClientConfig{}, err
	}
	maxIdempotentRetries, err := intFromEnvWithDefault(envCoreMaxIdempotentRetries, defaultCoreMaxIdempotentRetries)
	if err != nil {
		return core.ClientConfig{}, err
	}
	idempotentRetryBackoff, err := durationFromEnvWithDefault(envCoreIdempotentRetryBackoff, defaultCoreIdempotentRetryBackoff)
	if err != nil {
		return core.ClientConfig{}, err
	}

	clientConfig := core.ClientConfig{
		BaseURL:          baseURL,
		WebSocketBaseURL: webSocketBaseURL,
		Timeouts: core.TimeoutConfig{
			PreOpenCall:       preOpenCallTimeout,
			HotPathCall:       hotPathCallTimeout,
			FireAndForgetCall: fireAndForgetCallTimeout,
		},
		MaxIdempotentRetries:   maxIdempotentRetries,
		IdempotentRetryBackoff: idempotentRetryBackoff,
	}
	if err := clientConfig.Validate(); err != nil {
		return core.ClientConfig{}, fmt.Errorf("agentsd: %w", err)
	}
	return clientConfig, nil
}

func durationFromEnvWithDefault(envVar string, defaultValue time.Duration) (time.Duration, error) {
	raw := os.Getenv(envVar)
	if raw == "" {
		return defaultValue, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("agentsd: %s=%q is not a valid duration: %w", envVar, raw, err)
	}
	return parsed, nil
}

func intFromEnvWithDefault(envVar string, defaultValue int) (int, error) {
	raw := os.Getenv(envVar)
	if raw == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("agentsd: %s=%q is not a valid integer: %w", envVar, raw, err)
	}
	return parsed, nil
}
