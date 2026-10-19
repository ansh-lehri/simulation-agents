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

func TestValidateScenarioNameAcceptsEmpty(t *testing.T) {
	// An empty scenario name means "baseline on-demand" and must
	// always validate regardless of the registered accept-set.
	if err := validateScenarioName("", map[string]struct{}{}); err != nil {
		t.Fatalf("empty scenario name rejected: %v", err)
	}
	if err := validateScenarioName("", map[string]struct{}{"thin": {}}); err != nil {
		t.Fatalf("empty scenario name rejected with non-empty set: %v", err)
	}
}

func TestValidateScenarioNameAcceptsRegisteredName(t *testing.T) {
	validSet := map[string]struct{}{"thin": {}, "calm": {}}
	if err := validateScenarioName("thin", validSet); err != nil {
		t.Fatalf("registered name rejected: %v", err)
	}
}

func TestValidateScenarioNameRejectsUnregisteredName(t *testing.T) {
	validSet := map[string]struct{}{"thin": {}, "calm": {}}
	err := validateScenarioName("thinnn", validSet)
	if err == nil {
		t.Fatal("expected rejection for unregistered name")
	}
	if !strings.Contains(err.Error(), "thinnn") {
		t.Errorf("error should name the bad input: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown scenario") {
		t.Errorf("error should name the kind of failure: %v", err)
	}
}

func TestValidateScenarioNameRejectsAnyNameAgainstEmptySet(t *testing.T) {
	// A process with no scenario engine wired has an empty accept-set.
	// Any non-empty Scenario field must be rejected — otherwise an
	// unconfigured process would silently drop the shift.
	err := validateScenarioName("thin", map[string]struct{}{})
	if err == nil {
		t.Fatal("expected rejection against empty accept-set")
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
