package main

import (
	"context"
	"time"

	"simulation-agents/core"
	"simulation-agents/orchestrator"
	"simulation-agents/session"
	"simulation-agents/transport"
)

// defaultSessionParams returns the composition-root-tuned
// calibration for the session Runner. Values are baseline
// placeholders — a tuning run moves the dials, this constructor
// does not pin them in code. The entry-curve defaults come from
// the session package's own baseline so the two stay in sync.
func defaultSessionParams() session.Params {
	return session.Params{
		EntryCurve:              session.DefaultEntryCurveParams(),
		HistoryLookback:         60 * time.Minute,
		LoadSessionsConcurrency: 16,
		OnDemandSessionDuration: 30 * time.Minute,
		AckReadTimeout:          5 * time.Second,
		HistoryTimeframes:       []string{core.TimeframeOneMinute},
		ConfiguredTimeframes:    []string{core.TimeframeOneMinute},
		MaxLookbackK:            120,
	}
}

// sessionCoreClientAdapter wraps the single *core.Client so the
// session.CoreClient interface's Subscribe return type lands as
// session.WSConnection (an interface) rather than the concrete
// *core.WebSocketConnection the real client hands back. The two
// pass-through methods are a byte-for-byte forward; the adapter
// exists only to adjust the one return type Go's type system does
// not synthesize covariantly.
type sessionCoreClientAdapter struct {
	inner *core.Client
}

func (adapter *sessionCoreClientAdapter) JoinCanonical(
	ctx context.Context,
	agentUserID, marketTag string,
) (core.CanonicalSessionJoin, error) {
	return adapter.inner.JoinCanonical(ctx, agentUserID, marketTag)
}

func (adapter *sessionCoreClientAdapter) GetCandles(
	ctx context.Context,
	request core.CandlesRequest,
) (core.CandlesPage, error) {
	return adapter.inner.GetCandles(ctx, request)
}

func (adapter *sessionCoreClientAdapter) Subscribe(
	ctx context.Context,
	sessionID string,
	subscriptions []core.MarketDataSubscription,
) (session.WSConnection, core.ConnectedFrame, error) {
	connection, connectedFrame, err := adapter.inner.Subscribe(ctx, sessionID, subscriptions)
	if err != nil {
		return nil, core.ConnectedFrame{}, err
	}
	// *core.WebSocketConnection has the ReadFrame and Close methods
	// session.WSConnection requires, so the return assignment is a
	// trivial interface conversion.
	return connection, connectedFrame, nil
}

func (adapter *sessionCoreClientAdapter) GetObservation(
	ctx context.Context,
	accountID string,
) (core.AccountObservation, error) {
	return adapter.inner.GetObservation(ctx, accountID)
}

func (adapter *sessionCoreClientAdapter) PunchOrder(
	ctx context.Context,
	request core.OrderPunchRequest,
) error {
	return adapter.inner.PunchOrder(ctx, request)
}

// orchestratorFailureReporterAdapter closes the dependency loop
// between the session runner and the orchestrator: the runner
// needs a FailureReporter at construction time, the orchestrator
// is that reporter, but the orchestrator cannot be constructed
// without the runner. The adapter holds a nil-initialised pointer
// to the orchestrator which the composition root patches in
// AFTER the orchestrator is built but BEFORE the server starts
// accepting bring-ups. No FailureReporter call can fire in that
// window.
type orchestratorFailureReporterAdapter struct {
	inner *orchestrator.Orchestrator
}

func (adapter *orchestratorFailureReporterAdapter) FailBringUp(
	request transport.BringUpRequest,
	cause error,
) {
	adapter.inner.FailBringUp(request, cause)
}

func (adapter *orchestratorFailureReporterAdapter) ReportReady(
	request transport.BringUpRequest,
) {
	adapter.inner.ReportReady(request)
}
