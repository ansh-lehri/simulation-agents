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
		// HistoryLookback is wide (20 days, not the original 1 hour)
		// so the pre-open history pull reaches back far enough to
		// catch the canonical market's actual most recent candle even
		// when canonical isn't trading at this exact wall-clock
		// moment — CurrentPrice's candle fallback already takes
		// whatever bar is chronologically last in the returned
		// window, so a wider window just makes it more likely that
		// last bar is real data instead of an empty result. Costs
		// ~58 paged Core round-trips per instrument/timeframe at 1m
		// bars (vs. 1 at the old 1h window) — accepted for now,
		// revisit if bring-up latency becomes a problem.
		HistoryLookback:         20 * 24 * time.Hour,
		LoadSessionsConcurrency: 16,
		OnDemandSessionDuration: 30 * time.Minute,
		AckReadTimeout:          5 * time.Second,
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
		TAction:     1 * time.Second,
		TPresence:   30 * time.Second,
		PunchBuffer: 32,
		// LiquiditySeeder — see docs/agents/LIQUIDITY_SEEDER.md.
		// Calibration placeholders like every other dial here: 3
		// sellers + 3 buyers, orders sized 5-20 units, 3 pivot levels
		// per side plus 4 scattered random levels within 0.5% of the
		// anchor, continuous for the first third of the session then
		// sparse random top-ups every 30-90s.
		LiquiditySeeder: session.LiquiditySeederParams{
			SellerCount:          3,
			BuyerCount:           3,
			OrderSizeMin:         5,
			OrderSizeMax:         20,
			PivotLevelCount:      3,
			RandomLevelCount:     4,
			RandomLevelSpreadBps: 50,
			ContinuousFraction:   1.0 / 3.0,
			ReentryCadenceMin:    30 * time.Second,
			ReentryCadenceMax:    90 * time.Second,
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
