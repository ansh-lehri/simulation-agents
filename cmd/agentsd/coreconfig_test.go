package main

import (
	"strings"
	"testing"
	"time"
)

// baseEnvForCoreConfig returns the env-var map a happy-path test
// starts from. Rows mutate exactly one key to isolate each rule.
func baseEnvForCoreConfig() map[string]string {
	return map[string]string{
		envCoreBaseURL:                "http://core.local:8080",
		envCoreWebSocketBaseURL:       "ws://core.local:8080",
		envCoreTimeoutPreOpenCall:     "10s",
		envCoreTimeoutHotPathCall:     "500ms",
		envCoreTimeoutFireForgetCall:  "1s",
		envCoreMaxIdempotentRetries:   "2",
		envCoreIdempotentRetryBackoff: "100ms",
	}
}

func applyEnvForCoreConfig(t *testing.T, overrides map[string]string) {
	t.Helper()
	env := baseEnvForCoreConfig()
	for key, value := range overrides {
		env[key] = value
	}
	// Unset entries that are explicitly "" so we can distinguish
	// "value is empty string" from "variable is absent."
	for key, value := range env {
		if value == "<unset>" {
			t.Setenv(key, "")
			// t.Setenv does not provide an Unsetenv; empty-string
			// vs missing is treated identically by os.Getenv, which
			// is what loadCoreClientConfig consumes.
			continue
		}
		t.Setenv(key, value)
	}
}

func TestLoadCoreClientConfigHappyPath(t *testing.T) {
	applyEnvForCoreConfig(t, nil)
	config, err := loadCoreClientConfig()
	if err != nil {
		t.Fatalf("loadCoreClientConfig: %v", err)
	}
	if config.BaseURL != "http://core.local:8080" {
		t.Fatalf("BaseURL = %q", config.BaseURL)
	}
	if config.WebSocketBaseURL != "ws://core.local:8080" {
		t.Fatalf("WebSocketBaseURL = %q", config.WebSocketBaseURL)
	}
	if config.Timeouts.PreOpenCall != 10*time.Second {
		t.Fatalf("PreOpenCall = %v", config.Timeouts.PreOpenCall)
	}
	if config.Timeouts.HotPathCall != 500*time.Millisecond {
		t.Fatalf("HotPathCall = %v", config.Timeouts.HotPathCall)
	}
	if config.Timeouts.FireAndForgetCall != 1*time.Second {
		t.Fatalf("FireAndForgetCall = %v", config.Timeouts.FireAndForgetCall)
	}
	if config.MaxIdempotentRetries != 2 {
		t.Fatalf("MaxIdempotentRetries = %d", config.MaxIdempotentRetries)
	}
	if config.IdempotentRetryBackoff != 100*time.Millisecond {
		t.Fatalf("IdempotentRetryBackoff = %v", config.IdempotentRetryBackoff)
	}
}

func TestLoadCoreClientConfigAppliesDefaultsForOptionalEnvVars(t *testing.T) {
	applyEnvForCoreConfig(t, map[string]string{
		envCoreTimeoutPreOpenCall:     "",
		envCoreTimeoutHotPathCall:     "",
		envCoreTimeoutFireForgetCall:  "",
		envCoreMaxIdempotentRetries:   "",
		envCoreIdempotentRetryBackoff: "",
	})
	config, err := loadCoreClientConfig()
	if err != nil {
		t.Fatalf("loadCoreClientConfig: %v", err)
	}
	if config.Timeouts.PreOpenCall != defaultCoreTimeoutPreOpenCall {
		t.Fatalf("PreOpenCall = %v, want default %v", config.Timeouts.PreOpenCall, defaultCoreTimeoutPreOpenCall)
	}
	if config.Timeouts.HotPathCall != defaultCoreTimeoutHotPathCall {
		t.Fatalf("HotPathCall = %v, want default %v", config.Timeouts.HotPathCall, defaultCoreTimeoutHotPathCall)
	}
	if config.Timeouts.FireAndForgetCall != defaultCoreTimeoutFireForgetCall {
		t.Fatalf("FireAndForgetCall = %v, want default %v", config.Timeouts.FireAndForgetCall, defaultCoreTimeoutFireForgetCall)
	}
	if config.MaxIdempotentRetries != defaultCoreMaxIdempotentRetries {
		t.Fatalf("MaxIdempotentRetries = %d, want default %d", config.MaxIdempotentRetries, defaultCoreMaxIdempotentRetries)
	}
	if config.IdempotentRetryBackoff != defaultCoreIdempotentRetryBackoff {
		t.Fatalf("IdempotentRetryBackoff = %v, want default %v", config.IdempotentRetryBackoff, defaultCoreIdempotentRetryBackoff)
	}
}

func TestLoadCoreClientConfigRejectsMissingBaseURL(t *testing.T) {
	applyEnvForCoreConfig(t, map[string]string{envCoreBaseURL: ""})
	_, err := loadCoreClientConfig()
	if err == nil {
		t.Fatalf("expected error on missing BaseURL")
	}
	if !strings.Contains(err.Error(), envCoreBaseURL) {
		t.Fatalf("error should name the env var: %v", err)
	}
}

func TestLoadCoreClientConfigRejectsMissingWebSocketBaseURL(t *testing.T) {
	applyEnvForCoreConfig(t, map[string]string{envCoreWebSocketBaseURL: ""})
	_, err := loadCoreClientConfig()
	if err == nil {
		t.Fatalf("expected error on missing WebSocketBaseURL")
	}
	if !strings.Contains(err.Error(), envCoreWebSocketBaseURL) {
		t.Fatalf("error should name the env var: %v", err)
	}
}

func TestLoadCoreClientConfigRejectsMalformedDuration(t *testing.T) {
	applyEnvForCoreConfig(t, map[string]string{envCoreTimeoutHotPathCall: "not-a-duration"})
	_, err := loadCoreClientConfig()
	if err == nil {
		t.Fatalf("expected error on malformed duration")
	}
	if !strings.Contains(err.Error(), envCoreTimeoutHotPathCall) {
		t.Fatalf("error should name the env var: %v", err)
	}
	if !strings.Contains(err.Error(), "not a valid duration") {
		t.Fatalf("error should mention duration parse: %v", err)
	}
}

func TestLoadCoreClientConfigRejectsMalformedInteger(t *testing.T) {
	applyEnvForCoreConfig(t, map[string]string{envCoreMaxIdempotentRetries: "two"})
	_, err := loadCoreClientConfig()
	if err == nil {
		t.Fatalf("expected error on malformed integer")
	}
	if !strings.Contains(err.Error(), envCoreMaxIdempotentRetries) {
		t.Fatalf("error should name the env var: %v", err)
	}
	if !strings.Contains(err.Error(), "not a valid integer") {
		t.Fatalf("error should mention integer parse: %v", err)
	}
}

func TestLoadCoreClientConfigRejectsInvalidURLScheme(t *testing.T) {
	applyEnvForCoreConfig(t, map[string]string{envCoreBaseURL: "ftp://core.local"})
	_, err := loadCoreClientConfig()
	if err == nil {
		t.Fatalf("expected error on invalid BaseURL scheme")
	}
	// Error should surface core.ClientConfig.Validate's complaint
	// through our wrapper.
	if !strings.Contains(err.Error(), "BaseURL scheme") {
		t.Fatalf("expected scheme-validation error, got %v", err)
	}
}

func TestLoadCoreClientConfigRejectsNegativeRetries(t *testing.T) {
	applyEnvForCoreConfig(t, map[string]string{envCoreMaxIdempotentRetries: "-1"})
	_, err := loadCoreClientConfig()
	if err == nil {
		t.Fatalf("expected error on negative retries")
	}
	if !strings.Contains(err.Error(), "MaxIdempotentRetries") {
		t.Fatalf("expected validation error on retries, got %v", err)
	}
}

func TestLoadCoreClientConfigAllowsZeroRetries(t *testing.T) {
	applyEnvForCoreConfig(t, map[string]string{
		envCoreMaxIdempotentRetries:   "0",
		envCoreIdempotentRetryBackoff: "0s",
	})
	config, err := loadCoreClientConfig()
	if err != nil {
		t.Fatalf("loadCoreClientConfig: %v", err)
	}
	if config.MaxIdempotentRetries != 0 {
		t.Fatalf("MaxIdempotentRetries = %d, want 0", config.MaxIdempotentRetries)
	}
	if config.IdempotentRetryBackoff != 0 {
		t.Fatalf("IdempotentRetryBackoff = %v, want 0", config.IdempotentRetryBackoff)
	}
}
