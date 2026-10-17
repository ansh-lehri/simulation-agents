package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCallErrorErrorIncludesStableFields(t *testing.T) {
	testCases := []struct {
		name              string
		callError         CallError
		wantSubstrings    []string
		disallowedSubstrs []string
	}{
		{
			name: "with reason tag renders reason and omits free text",
			callError: CallError{
				HTTPStatus:  409,
				Reason:      "no_session",
				Message:     "account not installed on shard",
				HTTPMethod:  "POST",
				RequestPath: "/v1/orders",
			},
			wantSubstrings:    []string{"POST", "/v1/orders", "409", "no_session"},
			disallowedSubstrs: []string{"account not installed on shard"},
		},
		{
			name: "with message only renders message in parens",
			callError: CallError{
				HTTPStatus:  500,
				Message:     "upstream shard offline",
				HTTPMethod:  "GET",
				RequestPath: "/v1/account/cash",
			},
			wantSubstrings: []string{"GET", "/v1/account/cash", "500", "upstream shard offline"},
		},
		{
			name: "with status only renders status alone",
			callError: CallError{
				HTTPStatus:  503,
				HTTPMethod:  "GET",
				RequestPath: "/v1/candles",
			},
			wantSubstrings: []string{"GET", "/v1/candles", "503"},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			rendered := testCase.callError.Error()
			for _, wanted := range testCase.wantSubstrings {
				if !strings.Contains(rendered, wanted) {
					t.Fatalf("rendered %q missing expected substring %q", rendered, wanted)
				}
			}
			for _, forbidden := range testCase.disallowedSubstrs {
				if strings.Contains(rendered, forbidden) {
					t.Fatalf("rendered %q unexpectedly contains forbidden substring %q", rendered, forbidden)
				}
			}
		})
	}
}

func TestCallErrorUnwrappedByErrorsAs(t *testing.T) {
	t.Parallel()
	original := &CallError{
		HTTPStatus:  409,
		Reason:      "no_session",
		HTTPMethod:  "POST",
		RequestPath: "/v1/orders",
	}
	wrapped := fmt.Errorf("placing order for agent %q: %w", "agent-7", original)

	var extracted *CallError
	if !errors.As(wrapped, &extracted) {
		t.Fatalf("errors.As failed to extract *CallError from wrapped error")
	}
	if extracted != original {
		t.Fatalf("extracted error is not the original: got %p want %p", extracted, original)
	}
	if extracted.HTTPStatus != 409 || extracted.Reason != "no_session" {
		t.Fatalf("extracted fields incorrect: %+v", extracted)
	}
}

func TestReasonConstantsAreLowerSnakeCase(t *testing.T) {
	t.Parallel()
	reasons := map[string]string{
		"ReasonNoSession":         ReasonNoSession,
		"ReasonInsufficientFunds": ReasonInsufficientFunds,
		"ReasonBadQty":            ReasonBadQty,
		"ReasonNoMarketPrice":     ReasonNoMarketPrice,
	}
	for name, value := range reasons {
		if value == "" {
			t.Fatalf("reason %s is empty string", name)
		}
		if strings.ToLower(value) != value {
			t.Fatalf("reason %s = %q is not all-lowercase", name, value)
		}
		if strings.ContainsAny(value, " -") {
			t.Fatalf("reason %s = %q contains illegal character (space or dash)", name, value)
		}
	}
}
