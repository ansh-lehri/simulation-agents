package main

import (
	"strings"
	"testing"
)

func TestLoadListenAddr(t *testing.T) {
	testCases := []struct {
		name                   string
		envValue               string
		expectedAddr           string
		expectedErrorSubstring string
	}{
		{
			name:         "empty env falls back to loopback default",
			envValue:     "",
			expectedAddr: defaultListenAddr,
		},
		{
			name:         "explicit loopback bind is returned verbatim",
			envValue:     "127.0.0.1:9000",
			expectedAddr: "127.0.0.1:9000",
		},
		{
			name:         "explicit wildcard bind is accepted",
			envValue:     "0.0.0.0:7777",
			expectedAddr: "0.0.0.0:7777",
		},
		{
			name:                   "value missing a port is rejected",
			envValue:               "127.0.0.1",
			expectedErrorSubstring: "is not a valid host:port",
		},
		{
			name:                   "garbage value is rejected",
			envValue:               "not-a-valid-address",
			expectedErrorSubstring: "is not a valid host:port",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(envListenAddr, testCase.envValue)

			listenAddr, err := loadListenAddr()

			if testCase.expectedErrorSubstring != "" {
				if err == nil {
					t.Fatalf("loadListenAddr returned nil error, want error containing %q", testCase.expectedErrorSubstring)
				}
				if !strings.Contains(err.Error(), testCase.expectedErrorSubstring) {
					t.Errorf("loadListenAddr error = %q, want substring %q", err.Error(), testCase.expectedErrorSubstring)
				}
				return
			}

			if err != nil {
				t.Fatalf("loadListenAddr returned unexpected error: %v", err)
			}
			if listenAddr != testCase.expectedAddr {
				t.Errorf("loadListenAddr = %q, want %q", listenAddr, testCase.expectedAddr)
			}
		})
	}
}
