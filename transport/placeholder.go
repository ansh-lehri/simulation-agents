package transport

import (
	"context"
	"log/slog"
)

// PlaceholderOrchestrator stands in for the real orchestrator while the
// surrounding process scaffolding is being built. It logs every call at the
// inbound boundary and returns a stub ack. It owns no state, performs no
// generation, opens no accounts on the broker, spawns no goroutines, and
// makes no outbound calls. Swapping this out for the real orchestrator is a
// composition-root change — callers in this package depend only on the
// AgentOrchestrator interface.
type PlaceholderOrchestrator struct {
	logger *slog.Logger
}

// NewPlaceholderOrchestrator returns a placeholder that logs through the
// given logger. The logger must be non-nil: a missing logger would silently
// drop the inbound boundary trail that is the placeholder's only observable
// behavior, so it is treated as a composition-root invariant violation and
// panics here instead of being tolerated.
func NewPlaceholderOrchestrator(logger *slog.Logger) *PlaceholderOrchestrator {
	if logger == nil {
		panic("transport: PlaceholderOrchestrator requires a non-nil logger")
	}
	return &PlaceholderOrchestrator{logger: logger}
}

// BringUp logs the inbound bring-up call at the boundary and returns a stub
// ack indicating accounts are ready. The context is accepted to match the
// seam shape the real orchestrator needs; the placeholder does not consult
// cancellation or deadlines.
func (placeholder *PlaceholderOrchestrator) BringUp(ctx context.Context, request BringUpRequest) (BringUpAck, error) {
	_ = ctx
	placeholder.logger.Info(
		"inbound bring-up received by placeholder orchestrator",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"country", request.Country,
		"broker_id", request.BrokerID,
		"scenario", request.Scenario,
		"session_start", request.SessionStart,
		"session_end", request.SessionEnd,
		"instrument_count", len(request.Instruments),
	)
	return BringUpAck{ExchangeID: request.ExchangeID, Status: StatusAccountsReady}, nil
}

// WindDown logs the inbound wind-down call at the boundary and returns nil.
// The context is accepted to match the seam shape; the placeholder does not
// consult cancellation or deadlines.
func (placeholder *PlaceholderOrchestrator) WindDown(ctx context.Context, exchangeID string, teardownMode string) error {
	_ = ctx
	placeholder.logger.Info(
		"inbound wind-down received by placeholder orchestrator",
		"exchange_id", exchangeID,
		"teardown_mode", teardownMode,
	)
	return nil
}
