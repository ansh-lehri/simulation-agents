package transport_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"simulation-agents/transport"
)

// newCapturingLogger returns an slog.Logger whose output is written into the
// returned buffer in text format, so tests can assert the exact key/value
// pairs that appear on a boundary log line.
func newCapturingLogger() (*slog.Logger, *bytes.Buffer) {
	var captured bytes.Buffer
	handler := slog.NewTextHandler(&captured, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(handler), &captured
}

// sampleBringUpRequest returns a representative BringUpRequest built from
// non-secret identifiers so the log-content assertions have deterministic
// substrings to match on.
func sampleBringUpRequest() transport.BringUpRequest {
	return transport.BringUpRequest{
		ExchangeID:   "exchange-us-equities-001",
		SessionType:  "canonical",
		Country:      "US",
		SessionStart: time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC),
		SessionEnd:   time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC),
		Instruments:  []string{"ACME", "FICT"},
		BrokerID:     "broker-primary",
		Scenario:     "",
	}
}

func TestPlaceholderOrchestrator_BringUp_ReturnsAckEchoingRequest(t *testing.T) {
	logger, _ := newCapturingLogger()
	orchestrator := transport.NewPlaceholderOrchestrator(logger)

	request := sampleBringUpRequest()
	ack, err := orchestrator.BringUp(context.Background(), request)
	if err != nil {
		t.Fatalf("BringUp returned error, want nil: %v", err)
	}
	if ack.ExchangeID != request.ExchangeID {
		t.Errorf("ack.ExchangeID = %q, want %q", ack.ExchangeID, request.ExchangeID)
	}
	if ack.Status != transport.StatusAccountsReady {
		t.Errorf("ack.Status = %q, want %q", ack.Status, transport.StatusAccountsReady)
	}
}

func TestPlaceholderOrchestrator_BringUp_LogsRequiredBoundaryFields(t *testing.T) {
	logger, captured := newCapturingLogger()
	orchestrator := transport.NewPlaceholderOrchestrator(logger)

	request := sampleBringUpRequest()
	if _, err := orchestrator.BringUp(context.Background(), request); err != nil {
		t.Fatalf("BringUp returned error, want nil: %v", err)
	}

	logText := captured.String()
	requiredSubstrings := []string{
		"exchange_id=" + request.ExchangeID,
		"session_type=" + request.SessionType,
		"country=" + request.Country,
		"broker_id=" + request.BrokerID,
	}
	for _, needle := range requiredSubstrings {
		if !strings.Contains(logText, needle) {
			t.Errorf("boundary log missing %q; got:\n%s", needle, logText)
		}
	}
}

func TestPlaceholderOrchestrator_WindDown_ReturnsNoError(t *testing.T) {
	logger, _ := newCapturingLogger()
	orchestrator := transport.NewPlaceholderOrchestrator(logger)
	if err := orchestrator.WindDown(context.Background(), "exchange-us-equities-001", "graceful"); err != nil {
		t.Fatalf("WindDown returned error, want nil: %v", err)
	}
}

func TestPlaceholderOrchestrator_WindDown_LogsRequiredBoundaryFields(t *testing.T) {
	logger, captured := newCapturingLogger()
	orchestrator := transport.NewPlaceholderOrchestrator(logger)
	if err := orchestrator.WindDown(context.Background(), "exchange-us-equities-001", "graceful"); err != nil {
		t.Fatalf("WindDown returned error, want nil: %v", err)
	}

	logText := captured.String()
	for _, needle := range []string{
		"exchange_id=exchange-us-equities-001",
		"teardown_mode=graceful",
	} {
		if !strings.Contains(logText, needle) {
			t.Errorf("boundary log missing %q; got:\n%s", needle, logText)
		}
	}
}

func TestPlaceholderOrchestrator_BoundaryLogsOmitSecretShapedTokens(t *testing.T) {
	logger, captured := newCapturingLogger()
	orchestrator := transport.NewPlaceholderOrchestrator(logger)

	if _, err := orchestrator.BringUp(context.Background(), sampleBringUpRequest()); err != nil {
		t.Fatalf("BringUp returned error, want nil: %v", err)
	}
	if err := orchestrator.WindDown(context.Background(), "exchange-us-equities-001", "graceful"); err != nil {
		t.Fatalf("WindDown returned error, want nil: %v", err)
	}

	logText := strings.ToLower(captured.String())
	forbiddenTokens := []string{"postgres://", "redis://", "rediss://", "password=", "dsn="}
	for _, token := range forbiddenTokens {
		if strings.Contains(logText, token) {
			t.Errorf("boundary log unexpectedly contains secret-shaped token %q; got:\n%s", token, logText)
		}
	}
}

func TestNewPlaceholderOrchestrator_PanicsOnNilLogger(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewPlaceholderOrchestrator(nil) did not panic")
		}
	}()
	_ = transport.NewPlaceholderOrchestrator(nil)
}
