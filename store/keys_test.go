package store_test

import (
	"testing"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// Pin every Redis key format literally. These assertions are deliberately
// hard-coded strings: if someone renames a format string, the test fails
// before any Lua / consumer picks up the drift.
func TestKeyFormats(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"runtime", store.RuntimeKey("ex-1", agent.AgentID("a-9")), "runtime:ex-1:agent:a-9"},
		{"tipslot", store.TipSlotKey("ex-1"), "runtime:ex-1:tipslot"},
		{"candle_1m", store.CandleKey("ex-1", "ACME", "1m"), "marketdata:ex-1:ACME:candles:1m"},
		{"quote", store.QuoteKey("ex-1", "ACME"), "marketdata:ex-1:ACME:quote"},
		{"lease_available", store.LeaseAvailableKey("US"), "lease:US:available"},
		{"lease_leased", store.LeaseLeasedKey("US"), "lease:US:leased"},
		{"pending_mint", store.PendingMintKey(), "pending_mint"},
		{"sessions_active", store.SessionsActiveKey(), "sessions:active"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s key format: got %q want %q", tc.name, tc.got, tc.want)
		}
	}
}

// Keys must differ when any of their identity components differ — no two
// (exchange, agent) pairs may share a runtime key, and no two
// (exchange, instrument, tf) tuples may share a candle key.
func TestKeyUniqueness(t *testing.T) {
	if store.RuntimeKey("ex-1", "a") == store.RuntimeKey("ex-1", "b") {
		t.Error("runtime key collides across agent ids")
	}
	if store.RuntimeKey("ex-1", "a") == store.RuntimeKey("ex-2", "a") {
		t.Error("runtime key collides across exchanges")
	}
	if store.CandleKey("ex-1", "A", "1m") == store.CandleKey("ex-1", "A", "5m") {
		t.Error("candle key collides across timeframes")
	}
	if store.LeaseAvailableKey("US") == store.LeaseLeasedKey("US") {
		t.Error("lease available/leased keys collide for one country")
	}
}
