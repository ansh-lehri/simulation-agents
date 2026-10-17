package transport

import (
	"context"
	"time"
)

// AgentOrchestrator is the inbound seam the market conductor calls on this
// process to populate an agent crowd for a session (BringUp) and to tear it
// down (WindDown). The HTTP server in this package depends only on this
// interface, so swapping the current in-memory placeholder for the real
// orchestrator later is a composition-root change only — no callers change.
//
// BringUp is async in shape: the method validates inputs, performs the
// pre-spawn work that must finish before the caller is unblocked (identity
// assembly and broker account-open), and returns once those are complete.
// The remaining bring-up tail — durable persistence, runtime-state snapshots,
// the per-exchange run goroutine, pre-open subscriptions, and go-live — keeps
// running in the background after the method returns. The signature already
// reflects that split so the real implementation plugs in without changing
// what callers see.
type AgentOrchestrator interface {
	BringUp(ctx context.Context, request BringUpRequest) (BringUpAck, error)
	WindDown(ctx context.Context, exchangeID string, teardownMode string) error
}

// BringUpRequest is the input the market conductor sends when triggering a
// session population on this process. The fields are either consumed by the
// bring-up spine (SessionType dispatches the generation path; Country keys
// the trait distributions; ExchangeID tags the per-run namespace) or carried
// through to later phases (SessionStart/SessionEnd feed the scheduler clock;
// Instruments feeds the market-data subscription; BrokerID is reserved for
// multi-broker routing; Scenario is optional and only honored on on-demand
// sessions, where it shifts generation and lifecycle parameters).
type BringUpRequest struct {
	ExchangeID   string    `json:"exchange_id"`
	SessionType  string    `json:"session_type"`
	Country      string    `json:"country"`
	SessionStart time.Time `json:"session_start"`
	SessionEnd   time.Time `json:"session_end"`
	Instruments  []string  `json:"instruments"`
	BrokerID     string    `json:"broker_id"`
	Scenario     string    `json:"scenario,omitempty"`
}

// BringUpAck is returned once the synchronous prefix of BringUp has finished.
// Status names what has been achieved by the time the ack is returned — for
// the real orchestrator today that is broker-side account-open. The async
// tail of bring-up is still running inside this process when the ack is sent;
// its progress is surfaced via a separate poll-status path owned by the
// market conductor, not through this response.
type BringUpAck struct {
	ExchangeID string `json:"exchange_id"`
	Status     string `json:"status"`
}

// Status values returned on BringUpAck.
const (
	// StatusAccountsReady means the synchronous prefix of bring-up has run
	// through broker account-open and the async tail is in flight.
	StatusAccountsReady = "accounts_ready"
)
