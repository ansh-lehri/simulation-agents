package store

import (
	"fmt"

	"simulation-agents/agent"
)

// Presence values stored in the per-agent runtime hash. The full lifecycle
// is DORMANT → ACTIVE → IDLE → DONE → RETIRED; this phase only writes
// DORMANT (initial snapshot) but names them all so the downstream
// lifecycle writer and any readers share a single alphabet.
const (
	PresenceDormant = "DORMANT"
	PresenceActive  = "ACTIVE"
	PresenceIdle    = "IDLE"
	PresenceDone    = "DONE"
	PresenceRetired = "RETIRED"
)

// Hash field names inside the per-agent runtime hash. Lifted to constants
// so key-scheme tests pin them literally and later writers (decision /
// position working memory) reuse the same names without string drift.
// Working-memory field names are intentionally not declared here — they
// come with their writer design in a downstream stage.
const (
	FieldTradingCapital = "trading_capital"
	FieldIsTipper       = "is_tipper"
	FieldPresence       = "presence"
	FieldEntryTime      = "entry_time"
	FieldNextCheckTime  = "next_check_time"
	FieldIdleSince      = "idle_since"
)

// IsTipperTrue is the stored value for the is_tipper flag. The runtime
// hash is a flat string-string hash in Redis, so booleans are serialized
// as "1" / "0". Only "1" is ever written explicitly; absence of the field
// means "not a tipper".
const IsTipperTrue = "1"

// RuntimeKey is the per-agent runtime hash: holds presence, timers,
// decision / position working memory, tip-following state, and session
// trading capital. Keyed by (exchange_id, agent_id) so a given agent's
// state in an on-demand instance never collides with its canonical state.
func RuntimeKey(exchangeID string, agentID agent.AgentID) string {
	return fmt.Sprintf("runtime:%s:agent:%s", exchangeID, agentID)
}

// TipSlotKey is the shared active-tip slot for one exchange. Writers put a
// {tip_id, direction, conviction, target, stop_loss} payload here; the
// per-herd-agent runtime hash tracks per-agent tip-following state.
func TipSlotKey(exchangeID string) string {
	return fmt.Sprintf("runtime:%s:tipslot", exchangeID)
}

// CandleKey is the cached OHLCV window for one instrument on one exchange
// at one timeframe. Only tf="1m" is populated by this system — serving
// any other timeframe is the broker's job.
func CandleKey(exchangeID, instrument, timeframe string) string {
	return fmt.Sprintf("marketdata:%s:%s:candles:%s", exchangeID, instrument, timeframe)
}

// QuoteKey is the latest quote / depth / price for one instrument on one
// exchange. Overwrite semantics (last writer wins) — the agent runtime
// loop reads the current value, not a time-series.
func QuoteKey(exchangeID, instrument string) string {
	return fmt.Sprintf("marketdata:%s:%s:quote", exchangeID, instrument)
}

// LeaseAvailableKey is the Redis SET of free on-demand agent ids for one
// country. The core operation is "take N free agents" (native SPOP), so
// SET is the natural shape. Membership of the pool is durable (Postgres);
// only free/busy availability is runtime (Redis).
func LeaseAvailableKey(country string) string {
	return fmt.Sprintf("lease:%s:available", country)
}

// LeaseLeasedKey is the Redis HASH mapping leased on-demand agent_id →
// exchange_id. Reverse-lookup for teardown ("session X is ending, which
// agents did it hold?") and crash-reclaim ("these exchanges are gone;
// return their agents to available").
func LeaseLeasedKey(country string) string {
	return fmt.Sprintf("lease:%s:leased", country)
}

// PendingMintKey is the single process-wide write-ahead-log hash. New
// on-demand agents are staged here before their economy account is
// opened; the entry is cleared only after the durable identity row is
// written, so a crash between account-open and persist leaves a
// recoverable record of the orphaned account.
func PendingMintKey() string {
	return "pending_mint"
}

// SessionsActiveKey is the HASH mapping exchange_id → session params so a
// restarted orchestrator knows which goroutines to respawn and with what
// parameters. Reader lives in a later stage; the name is pinned here so
// nobody re-invents it.
func SessionsActiveKey() string {
	return "sessions:active"
}
