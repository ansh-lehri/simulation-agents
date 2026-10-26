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
//
// brokerDefaultFundedCash is threaded in by the caller rather than
// pinned here so there is exactly one source for this number in the
// composition root — defaultOrchestratorConfig's CapitalParams.
// DefaultFundedCash — instead of two independently-maintained copies
// of the same constant.
func defaultSessionParams(brokerDefaultFundedCash int64) session.Params {
	return session.Params{
		EntryCurve:     session.DefaultEntryCurveParams(),
		LifecycleCurve: session.DefaultLifecycleCurveParams(),
		// HistoryLookback must be wide enough to catch the canonical
		// market's most recent candle even across multi-week gaps
		// (testing breaks, holidays, etc.). 20 days covers any
		// realistic pause. SubscribeMaxRetries:36 (180s window)
		// covers the ~58-page pullHistory round-trip so WS retries
		// don't exhaust before the seeder places its first orders.
		HistoryLookback:         20 * 24 * time.Hour,
		LoadSessionsConcurrency: 50,
		OnDemandSessionDuration: 30 * time.Minute,
		AckReadTimeout:          5 * time.Second,
		// SubscribeMaxRetries raised from the default (12 = 60s window)
		// to give the liquidity seeder enough runway to tape-learn the
		// instrument before retries exhaust for large populations. 36
		// retries × 5s interval = 180s window, comfortably larger than
		// any realistic preOpenGroup.Wait() duration.
		SubscribeMaxRetries:     36,
		HistoryTimeframes:       []string{core.TimeframeOneMinute},
		ConfiguredTimeframes:    []string{core.TimeframeOneMinute},
		MaxLookbackK:            120,
		Perception:              session.DefaultPerceptionParams(),
		Decision:                defaultDecisionParams(),
		// TAction/TPresence/PunchBuffer are calibration placeholders
		// like every other dial in this function, picked to match
		// L2_TIER3_RUNTIME_LOOP_DESIGN.md §4's stated bands rather
		// than invented from scratch: TAction sub-second (the doc's
		// own worked figure is a multi-hour session running ~22,500
		// ticks, which lands here around 1s/tick), TPresence "tens of
		// seconds" and far coarser than TAction. PunchBuffer sized to
		// comfortably absorb one action tick's worth of punches for
		// the default PopulationSize (100) without the act-coin gate
		// needing every agent to fire for the channel to fill.
		TAction:           1 * time.Second,
		TPresence:         30 * time.Second,
		PunchBuffer:       2000,
		DispatcherWorkers: 50,
		// LiquiditySeeder — see docs/agents/LIQUIDITY_SEEDER.md.
		// Calibration placeholders like every other dial here: 3
		// sellers + 3 buyers, orders sized 5-20 units, 3 pivot levels
		// per side plus 4 scattered random levels within 0.5% of the
		// anchor, continuous for the first third of the session then
		// sparse random top-ups every 30-90s.
		LiquiditySeeder: session.LiquiditySeederParams{
			SellerCount:          10,
			BuyerCount:           5,
			OrderSizeMin:         5,
			OrderSizeMax:         500,
			PivotLevelCount:      5,
			RandomLevelCount:     3,
			RandomLevelSpreadBps: 100,
			ContinuousFraction:   0.25,
			ReentryCadenceMin:    5 * time.Second,
			ReentryCadenceMax:    15 * time.Second,
			GenesisOffsetBps:     100,
		},
		// ExpectedQuitsPerSession is the canonical quit-for-good
		// baseline from the lifecycle design (~one quitter every
		// 3-4 sessions) — a calibration placeholder like every
		// other number in this file, not a fitted value.
		ExpectedQuitsPerSession: 0.3,
		BrokerDefaultFundedCash: brokerDefaultFundedCash,
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

func (adapter *sessionCoreClientAdapter) JoinOnDemand(
	ctx context.Context,
	agentUserID, exchangeID string,
) (core.OnDemandSessionJoin, error) {
	return adapter.inner.JoinOnDemand(ctx, agentUserID, exchangeID)
}

func (adapter *sessionCoreClientAdapter) JoinCanonicalAsOperator(
	ctx context.Context,
	operatorUserID, marketTag string,
) (core.CanonicalSessionJoin, error) {
	return adapter.inner.JoinCanonicalAsOperator(ctx, operatorUserID, marketTag)
}

func (adapter *sessionCoreClientAdapter) JoinOnDemandAsOperator(
	ctx context.Context,
	operatorUserID, exchangeID string,
) (core.OnDemandSessionJoin, error) {
	return adapter.inner.JoinOnDemandAsOperator(ctx, operatorUserID, exchangeID)
}

func (adapter *sessionCoreClientAdapter) OpenOperatorAccount(
	ctx context.Context,
	operatorUserID string,
) (string, error) {
	return adapter.inner.OpenOperatorAccount(ctx, operatorUserID)
}

func (adapter *sessionCoreClientAdapter) SeedInventory(
	ctx context.Context,
	request core.SeedInventoryRequest,
) (core.SeedInventoryResponse, error) {
	return adapter.inner.SeedInventory(ctx, request)
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

func (adapter *sessionCoreClientAdapter) CancelOrder(
	ctx context.Context,
	accountID, orderID string,
) error {
	return adapter.inner.CancelOrder(ctx, accountID, orderID)
}

func (adapter *sessionCoreClientAdapter) PunchOrderTracked(
	ctx context.Context,
	request core.OrderPunchRequest,
) (string, error) {
	return adapter.inner.PunchOrderTracked(ctx, request)
}

func (adapter *sessionCoreClientAdapter) GetAccountCash(
	ctx context.Context,
	accountID string,
) (core.AccountCash, error) {
	return adapter.inner.GetAccountCash(ctx, accountID)
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
