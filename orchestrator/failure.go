package orchestrator

import (
	"simulation-agents/session"
	"simulation-agents/transport"
)

// FailBringUp is the single failure exit the bring-up tail and the
// per-exchange run goroutine both call when a step cannot proceed.
// This phase ships the surface only — a structured log line marking
// the exchange and the cause. The resilience layer later adds the
// abort-cleanup inside this method body: deregister the entry in
// sessions:active, release leased agents, drop any partial Redis
// snapshot and market-data cache, and close the live WebSocket.
// Making that layer land here (rather than in the Runner or the
// bring-up tail) is what the decide/write split prescribes — the
// Runner decides a failure, the orchestrator writes the consequence.
//
// The method is a no-return surface: callers log and go away. There
// is no error for a caller to interrogate because there is nothing
// the caller can do about the failure beyond stopping — which the
// Runner already does by returning from its goroutine after this
// call.
func (orchestrator *Orchestrator) FailBringUp(request transport.BringUpRequest, cause error) {
	orchestrator.logger.Error(
		"bring-up failed",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"country", request.Country,
		"cause", cause,
	)
	// Abort-cleanup (deregister sessions:active, release leased,
	// drop partial snapshot + market-data cache, close the live
	// WebSocket) is invoked from here when the resilience layer
	// lands; this phase is surface-only.
}

// ReportReady is the single ready-surface the per-exchange run
// goroutine calls at go-live, after the three pre-open ops have
// joined and the session's start wall-clock has been reached. This
// phase ships the surface only — a structured log line marking the
// exchange is live. The poll-status wiring the conductor consults
// is added here when the resilience layer lands.
func (orchestrator *Orchestrator) ReportReady(request transport.BringUpRequest) {
	orchestrator.logger.Info(
		"bring-up ready",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"country", request.Country,
	)
	// Poll-status surfacing lands here when the resilience layer
	// ships; this phase is surface-only.
}

// Compile-time witness: the orchestrator satisfies the Runner's
// view of the failure surface. Signature drift between the two
// packages would fail at build rather than at first bring-up spawn.
var _ session.FailureReporter = (*Orchestrator)(nil)
