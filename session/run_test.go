package session

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/store"
	"simulation-agents/transport"
)

// TestNewRunnerRejectsNilDependencies walks every field on
// Dependencies, blanks it in turn against an otherwise-valid struct,
// and asserts NewRunner reports exactly that field. A dependency
// slipping through unchecked would surface as a nil-pointer panic
// deep in a pre-open op during a live bring-up — the whole point of
// NewRunner's up-front validation is to turn that into a startup
// error with a clear name instead.
func TestNewRunnerRejectsNilDependencies(t *testing.T) {
	t.Parallel()

	validDependencies := func() Dependencies {
		return Dependencies{
			Core:       stubCoreClient{},
			Runtime:    stubRuntimeStore{},
			MarketData: stubMarketDataStore{},
			Scheduler:  stubScheduler{},
			Failure:    stubFailureReporter{},
			Clock:      stubClock{},
			RNGSource:  stubRNGSource{},
			Params:     Params{EntryCurve: DefaultEntryCurveParams()},
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
	}

	// Sanity: the valid struct itself must succeed, otherwise every
	// per-field zero-out below would succeed for the wrong reason.
	if runner, err := NewRunner(validDependencies()); err != nil || runner == nil {
		t.Fatalf("valid dependencies: want non-nil Runner, nil err; got runner=%v err=%v", runner, err)
	}

	testCases := []struct {
		fieldName      string
		mutate         func(*Dependencies)
		errorSubstring string
	}{
		{"Core", func(deps *Dependencies) { deps.Core = nil }, "Core"},
		{"Runtime", func(deps *Dependencies) { deps.Runtime = nil }, "Runtime"},
		{"MarketData", func(deps *Dependencies) { deps.MarketData = nil }, "MarketData"},
		{"Scheduler", func(deps *Dependencies) { deps.Scheduler = nil }, "Scheduler"},
		{"Failure", func(deps *Dependencies) { deps.Failure = nil }, "Failure"},
		{"Clock", func(deps *Dependencies) { deps.Clock = nil }, "Clock"},
		{"RNGSource", func(deps *Dependencies) { deps.RNGSource = nil }, "RNG"},
		{"Logger", func(deps *Dependencies) { deps.Logger = nil }, "Logger"},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.fieldName, func(t *testing.T) {
			t.Parallel()
			dependencies := validDependencies()
			testCase.mutate(&dependencies)
			runner, err := NewRunner(dependencies)
			if runner != nil {
				t.Fatalf("want nil Runner on missing %s; got %v", testCase.fieldName, runner)
			}
			if err == nil {
				t.Fatalf("want error on missing %s; got nil", testCase.fieldName)
			}
			if !strings.Contains(err.Error(), testCase.errorSubstring) {
				t.Fatalf("error on missing %s must mention %q; got %q",
					testCase.fieldName, testCase.errorSubstring, err.Error())
			}
		})
	}
}

// --- stubs: minimal implementations that let NewRunner's validation
// pass and let Run execute without touching any real collaborator.
// Each stub returns the zero value for every method it declares; a
// Stage-D test will replace these with recording spies.

type stubCoreClient struct{}

func (stubCoreClient) JoinCanonical(ctx context.Context, agentUserID, marketTag string) (core.CanonicalSessionJoin, error) {
	_ = ctx
	_ = agentUserID
	_ = marketTag
	return core.CanonicalSessionJoin{}, nil
}

func (stubCoreClient) JoinOnDemand(ctx context.Context, agentUserID, exchangeID string) (core.OnDemandSessionJoin, error) {
	_ = ctx
	_ = agentUserID
	_ = exchangeID
	return core.OnDemandSessionJoin{}, nil
}

func (stubCoreClient) GetCandles(ctx context.Context, request core.CandlesRequest) (core.CandlesPage, error) {
	_ = ctx
	_ = request
	return core.CandlesPage{}, nil
}

func (stubCoreClient) Subscribe(
	ctx context.Context,
	sessionID string,
	subscriptions []core.MarketDataSubscription,
) (WSConnection, core.ConnectedFrame, error) {
	_ = ctx
	_ = sessionID
	_ = subscriptions
	return nil, core.ConnectedFrame{}, nil
}

func (stubCoreClient) GetObservation(_ context.Context, _ string) (core.AccountObservation, error) {
	return core.AccountObservation{}, nil
}

func (stubCoreClient) PunchOrder(_ context.Context, _ core.OrderPunchRequest) error {
	return nil
}

type stubRuntimeStore struct{}

func (stubRuntimeStore) WriteInitialSnapshot(ctx context.Context, exchangeID string, agentRecord agent.Agent) error {
	_ = ctx
	_ = exchangeID
	_ = agentRecord
	return nil
}

func (stubRuntimeStore) SetPresence(ctx context.Context, exchangeID string, agentID agent.AgentID, presence string) error {
	_ = ctx
	_ = exchangeID
	_ = agentID
	_ = presence
	return nil
}

func (stubRuntimeStore) SetTimers(ctx context.Context, exchangeID string, agentID agent.AgentID, nextCheckTime, idleSince int64) error {
	_ = ctx
	_ = exchangeID
	_ = agentID
	_ = nextCheckTime
	_ = idleSince
	return nil
}

func (stubRuntimeStore) StampEntryTime(ctx context.Context, exchangeID string, agentID agent.AgentID, entryTime float64) error {
	_ = ctx
	_ = exchangeID
	_ = agentID
	_ = entryTime
	return nil
}

func (stubRuntimeStore) SetTipper(ctx context.Context, exchangeID string, agentID agent.AgentID) error {
	_ = ctx
	_ = exchangeID
	_ = agentID
	return nil
}

func (stubRuntimeStore) MarkSessionLoadFailed(ctx context.Context, exchangeID string, agentID agent.AgentID, reason string) error {
	_ = ctx
	_ = exchangeID
	_ = agentID
	_ = reason
	return nil
}

func (stubRuntimeStore) SetStandingOpinion(_ context.Context, _ string, _ agent.AgentID, _ string, _ float64, _ string) error {
	return nil
}
func (stubRuntimeStore) SetWaitStreak(_ context.Context, _ string, _ agent.AgentID, _ string, _ int) error {
	return nil
}
func (stubRuntimeStore) SetPendingWatch(_ context.Context, _ string, _ agent.AgentID, _ string, _ *store.PendingWatch) error {
	return nil
}
func (stubRuntimeStore) SetLastCommitTick(_ context.Context, _ string, _ agent.AgentID, _ string, _ int64) error {
	return nil
}
func (stubRuntimeStore) SetLastDecidedReadings(_ context.Context, _ string, _ agent.AgentID, _ string, _ map[string]float64) error {
	return nil
}
func (stubRuntimeStore) SetPositionContext(_ context.Context, _ string, _ agent.AgentID, _ string, _ *store.PositionContext) error {
	return nil
}
func (stubRuntimeStore) SetTipFollowState(_ context.Context, _ string, _ agent.AgentID, _ *store.Tracking, _ string, _, _ int64) error {
	return nil
}
func (stubRuntimeStore) SetPerceptionRefs(_ context.Context, _ string, _ agent.AgentID, _ string, _ store.PerceptionRefs) error {
	return nil
}
func (stubRuntimeStore) DropSession(_ context.Context, _ string) error {
	return nil
}

type stubMarketDataStore struct{}

func (stubMarketDataStore) WriteCandles(ctx context.Context, exchangeID, instrument, timeframe string, bars []store.Candle) error {
	_ = ctx
	_ = exchangeID
	_ = instrument
	_ = timeframe
	_ = bars
	return nil
}

func (stubMarketDataStore) AppendCandle(ctx context.Context, exchangeID, instrument, timeframe string, bar store.Candle) error {
	_ = ctx
	_ = exchangeID
	_ = instrument
	_ = timeframe
	_ = bar
	return nil
}

func (stubMarketDataStore) WriteQuote(ctx context.Context, exchangeID, instrument string, quote store.Quote) error {
	_ = ctx
	_ = exchangeID
	_ = instrument
	_ = quote
	return nil
}

func (stubMarketDataStore) ReadCandles(_ context.Context, _, _, _ string, _ int) ([]store.Candle, error) {
	return nil, nil
}

func (stubMarketDataStore) ReadQuote(_ context.Context, _, _ string) (store.Quote, bool, error) {
	return store.Quote{}, false, nil
}

type stubScheduler struct{}

func (stubScheduler) StartScheduler(
	ctx context.Context,
	request transport.BringUpRequest,
	population generator.Population,
	curveEffective *generator.CurveEffective,
	lifecycleChannel chan<- LifecycleDecision,
) {
	_, _, _, _, _ = ctx, request, population, curveEffective, lifecycleChannel
}

type stubFailureReporter struct{}

func (stubFailureReporter) FailBringUp(request transport.BringUpRequest, cause error) {
	_ = request
	_ = cause
}

func (stubFailureReporter) ReportReady(request transport.BringUpRequest) {
	_ = request
}

type stubClock struct{}

func (stubClock) WaitUntil(ctx context.Context, deadline time.Time) error {
	_ = ctx
	_ = deadline
	return nil
}

func (stubClock) Now() time.Time {
	return time.Time{}
}

type stubRNGSource struct{}

func (stubRNGSource) Fresh() *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}
