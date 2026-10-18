package orchestrator

import (
	"strings"
	"testing"
	"time"

	"simulation-agents/transport"
)

// completeBringUpRequest returns a request that passes validation. Each
// table row below mutates exactly one field so the row's assertion
// isolates one rule.
func completeBringUpRequest() transport.BringUpRequest {
	return transport.BringUpRequest{
		ExchangeID:   "exch-IN-A",
		SessionType:  SessionTypeCanonical,
		Country:      "IN",
		SessionStart: time.Date(2026, 10, 18, 9, 15, 0, 0, time.UTC),
		SessionEnd:   time.Date(2026, 10, 18, 15, 30, 0, 0, time.UTC),
		Instruments:  []string{"ACME", "BRIK"},
		BrokerID:     "primary",
	}
}

func TestValidateTriggerAcceptsCanonical(t *testing.T) {
	request := completeBringUpRequest()
	if err := validateTrigger(request); err != nil {
		t.Fatalf("expected nil error on complete canonical request, got %v", err)
	}
}

func TestValidateTriggerAcceptsOnDemand(t *testing.T) {
	request := completeBringUpRequest()
	request.SessionType = SessionTypeOnDemand
	request.Scenario = "polarized_optimism"
	if err := validateTrigger(request); err != nil {
		t.Fatalf("expected nil error on complete on-demand request, got %v", err)
	}
}

func TestValidateTriggerCarriesEveryOtherFieldWithoutRejecting(t *testing.T) {
	// Zero out every non-validated field and confirm validation still
	// passes. The orchestrator's contract is "we validate what we
	// dispatch on; everything else is carried through for later phases".
	request := transport.BringUpRequest{
		ExchangeID:  "exch-A",
		SessionType: SessionTypeCanonical,
		Country:     "IN",
		// SessionStart, SessionEnd, Instruments, BrokerID, Scenario all zero/empty.
	}
	if err := validateTrigger(request); err != nil {
		t.Fatalf("expected minimal request to validate, got %v", err)
	}
}

func TestValidateTriggerRejectsStructuralViolations(t *testing.T) {
	cases := []struct {
		name          string
		mutate        func(*transport.BringUpRequest)
		errorContains string
	}{
		{
			name:          "session type missing",
			mutate:        func(request *transport.BringUpRequest) { request.SessionType = "" },
			errorContains: "SessionType is required",
		},
		{
			name:          "session type unknown",
			mutate:        func(request *transport.BringUpRequest) { request.SessionType = "canonicl" },
			errorContains: "canonicl",
		},
		{
			name:          "country missing",
			mutate:        func(request *transport.BringUpRequest) { request.Country = "" },
			errorContains: "Country is required",
		},
		{
			name:          "exchange id missing",
			mutate:        func(request *transport.BringUpRequest) { request.ExchangeID = "" },
			errorContains: "ExchangeID is required",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			request := completeBringUpRequest()
			testCase.mutate(&request)
			err := validateTrigger(request)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", testCase.errorContains)
			}
			if !strings.Contains(err.Error(), testCase.errorContains) {
				t.Fatalf("expected error containing %q, got %q", testCase.errorContains, err.Error())
			}
		})
	}
}
