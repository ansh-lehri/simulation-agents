package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"simulation-agents/core"
	"simulation-agents/transport"
)

// TestSubscribeLiveHappyPathBatchesAllSubsOnOneWebSocket: with
// two instruments and three configured timeframes, the op makes
// one Subscribe call to Core carrying one snapshot sub per
// instrument plus one candle sub per (instrument × timeframe) =
// 2 + 2×3 = 8 subs on the single WS, in deterministic order.
func TestSubscribeLiveHappyPathBatchesAllSubsOnOneWebSocket(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableSubscribeCoreClient()
	runner := subscribeLiveTestRunnerWithTimeframes(t, fakeCore, []string{
		core.TimeframeOneMinute,
		core.TimeframeFiveMinute,
		core.TimeframeSixtyMinute,
	})
	instruments := []string{"stock1", "stock2"}
	request := subscribeBringUpRequest(instruments)
	feedSessionID := "sess-feed-1"
	joinCtx, cancelJoin := context.WithCancel(context.Background())
	defer cancelJoin()
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	defer cancelSession()

	err := runner.subscribeLive(joinCtx, sessionCtx, request, feedSessionID)
	if err != nil {
		t.Fatalf("subscribeLive: %v", err)
	}

	if got := fakeCore.subscribeCount.Load(); got != 1 {
		t.Fatalf("Subscribe call count = %d, want 1 (single WS carries all subs)", got)
	}
	if fakeCore.lastSessionID != feedSessionID {
		t.Fatalf("Subscribe session id = %q, want %q", fakeCore.lastSessionID, feedSessionID)
	}
	wantSubs := []core.MarketDataSubscription{
		{Stream: core.MarketDataStreamSnapshot, Instrument: "stock1"},
		{Stream: core.MarketDataStreamSnapshot, Instrument: "stock2"},
		{Stream: core.MarketDataStreamCandle, Instrument: "stock1", Timeframe: core.TimeframeOneMinute},
		{Stream: core.MarketDataStreamCandle, Instrument: "stock2", Timeframe: core.TimeframeOneMinute},
		{Stream: core.MarketDataStreamCandle, Instrument: "stock1", Timeframe: core.TimeframeFiveMinute},
		{Stream: core.MarketDataStreamCandle, Instrument: "stock2", Timeframe: core.TimeframeFiveMinute},
		{Stream: core.MarketDataStreamCandle, Instrument: "stock1", Timeframe: core.TimeframeSixtyMinute},
		{Stream: core.MarketDataStreamCandle, Instrument: "stock2", Timeframe: core.TimeframeSixtyMinute},
	}
	assertSubscriptionsEqual(t, fakeCore.lastSubscriptions, wantSubs)

	// Ingest binds to sessionCtx, not joinCtx — joinCtx is the
	// errgroup-derived context that Run's preOpenGroup.Wait() cancels
	// the instant all three pre-open ops finish, seconds into the
	// session. Cancelling it alone must NOT close the ingest
	// connection; this is the exact regression this split guards
	// against (ingest used to die right at the pre-open join).
	cancelJoin()
	select {
	case <-fakeCore.connection.closedChannel:
		t.Fatalf("ingest closed the connection on joinCtx cancel — it must only react to sessionCtx")
	case <-time.After(100 * time.Millisecond):
	}

	// Cancelling sessionCtx (the long-lived context Run itself holds
	// for the session's whole life) must close the connection via
	// ingest's defer close.
	cancelSession()
	select {
	case <-fakeCore.connection.closedChannel:
	case <-time.After(time.Second):
		t.Fatalf("ingest did not close the connection within 1s after sessionCtx cancel")
	}
}

// TestSubscribeLiveFatalOnSubscribeError: the Subscribe call fails
// (dial / write / timeout). The op returns a non-nil error; ingest
// is not spawned.
func TestSubscribeLiveFatalOnSubscribeError(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableSubscribeCoreClient()
	fakeCore.subscribeError = errors.New("dial failed")
	runner := subscribeLiveTestRunnerWithTimeframes(t, fakeCore, []string{core.TimeframeOneMinute})

	err := runner.subscribeLive(context.Background(), context.Background(), subscribeBringUpRequest([]string{"stock1"}), "sess-feed-1")
	if err == nil {
		t.Fatalf("expected fatal error on subscribe failure; got nil")
	}
	if !strings.Contains(err.Error(), "dial failed") {
		t.Fatalf("error must wrap the subscribe cause; got %q", err.Error())
	}
}

// TestSubscribeLiveRejectsEmptyFeedSessionIDWithoutHittingCore:
// without a feed session id the WS cannot bind; the op refuses the
// call before dialing to turn a composition-root bug into a clear
// error rather than a cryptic Core 4xx.
func TestSubscribeLiveRejectsEmptyFeedSessionIDWithoutHittingCore(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableSubscribeCoreClient()
	runner := subscribeLiveTestRunnerWithTimeframes(t, fakeCore, []string{core.TimeframeOneMinute})
	err := runner.subscribeLive(context.Background(), context.Background(), subscribeBringUpRequest([]string{"stock1"}), "")
	if err == nil {
		t.Fatalf("expected error on empty feed session id; got nil")
	}
	if fakeCore.subscribeCount.Load() != 0 {
		t.Fatalf("Core must not be hit on empty feed session id")
	}
}

// TestSubscribeLiveRejectsEmptyInstrumentListWithoutHittingCore:
// zero instruments means nothing to subscribe to; refuse before
// dialing.
func TestSubscribeLiveRejectsEmptyInstrumentListWithoutHittingCore(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableSubscribeCoreClient()
	runner := subscribeLiveTestRunnerWithTimeframes(t, fakeCore, []string{core.TimeframeOneMinute})
	err := runner.subscribeLive(context.Background(), context.Background(), subscribeBringUpRequest(nil), "sess-feed-1")
	if err == nil {
		t.Fatalf("expected error on empty instruments; got nil")
	}
	if fakeCore.subscribeCount.Load() != 0 {
		t.Fatalf("Core must not be hit on empty instruments")
	}
}

// TestSubscribeLiveRejectsEmptyTimeframesWithoutHittingCore:
// no configured timeframes means we would subscribe only snapshot
// subs and never see live finalized bars for any timeframe —
// silent miscast. Refuse before dialing so the operator sees it.
func TestSubscribeLiveRejectsEmptyTimeframesWithoutHittingCore(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableSubscribeCoreClient()
	runner := subscribeLiveTestRunnerWithTimeframes(t, fakeCore, nil)
	err := runner.subscribeLive(context.Background(), context.Background(), subscribeBringUpRequest([]string{"stock1"}), "sess-feed-1")
	if err == nil {
		t.Fatalf("expected error on empty HistoryTimeframes; got nil")
	}
	if !strings.Contains(err.Error(), "HistoryTimeframes") {
		t.Fatalf("error must mention HistoryTimeframes; got %q", err.Error())
	}
	if fakeCore.subscribeCount.Load() != 0 {
		t.Fatalf("Core must not be hit on empty HistoryTimeframes")
	}
}

// TestSubscribeLiveRejectsUnknownTimeframeWithoutHittingCore: a
// configured timeframe outside the Core-accepted set is caught at
// the top of the op, before any dial.
func TestSubscribeLiveRejectsUnknownTimeframeWithoutHittingCore(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableSubscribeCoreClient()
	runner := subscribeLiveTestRunnerWithTimeframes(t, fakeCore, []string{core.TimeframeOneMinute, "15m"})
	err := runner.subscribeLive(context.Background(), context.Background(), subscribeBringUpRequest([]string{"stock1"}), "sess-feed-1")
	if err == nil {
		t.Fatalf("expected error on unknown timeframe; got nil")
	}
	if !strings.Contains(err.Error(), "15m") {
		t.Fatalf("error must name the offending timeframe; got %q", err.Error())
	}
	if fakeCore.subscribeCount.Load() != 0 {
		t.Fatalf("Core must not be hit on bad timeframe")
	}
}

// --- fixtures and fakes

func subscribeLiveTestRunnerWithTimeframes(t *testing.T, fakeCore *programmableSubscribeCoreClient, timeframes []string) *Runner {
	t.Helper()
	return testRunnerWithDependencies(t, Dependencies{
		Core:       fakeCore,
		Runtime:    stubRuntimeStore{},
		MarketData: newRecordingMarketDataStore(),
		Scheduler:  stubScheduler{},
		Failure:    stubFailureReporter{},
		Clock:      stubClock{},
		RNGSource:  stubRNGSource{},
		Params: Params{
			EntryCurve:              DefaultEntryCurveParams(),
			LoadSessionsConcurrency: 1,
			HistoryLookback:         60 * time.Minute,
			HistoryTimeframes:       timeframes,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func subscribeBringUpRequest(instruments []string) transport.BringUpRequest {
	return transport.BringUpRequest{
		ExchangeID:   "exch_live",
		SessionType:  "canonical",
		Country:      "IN",
		SessionStart: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC),
		SessionEnd:   time.Date(2026, 10, 20, 10, 30, 0, 0, time.UTC),
		Instruments:  instruments,
	}
}

func assertSubscriptionsEqual(t *testing.T, got, want []core.MarketDataSubscription) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("subscription count = %d, want %d; got=%+v want=%+v", len(got), len(want), got, want)
	}
	for subIndex, subscription := range got {
		if subscription != want[subIndex] {
			t.Fatalf("subscription %d = %+v, want %+v", subIndex, subscription, want[subIndex])
		}
	}
}

// programmableSubscribeCoreClient records the single Subscribe
// call and hands back a controllable fake WSConnection. The
// ingest goroutine consumes frames from that connection; the test
// can inject frames via connection.FeedFrame or close the
// connection to force ingest to exit.
type programmableSubscribeCoreClient struct {
	subscribeError    error
	subscribeCount    atomic.Int32
	lastSessionID     string
	lastSubscriptions []core.MarketDataSubscription
	connection        *fakeWSConnection
}

func newProgrammableSubscribeCoreClient() *programmableSubscribeCoreClient {
	return &programmableSubscribeCoreClient{
		connection: newFakeWSConnection(),
	}
}

func (fake *programmableSubscribeCoreClient) Subscribe(
	ctx context.Context,
	sessionID string,
	subscriptions []core.MarketDataSubscription,
) (WSConnection, core.ConnectedFrame, error) {
	_ = ctx
	fake.subscribeCount.Add(1)
	fake.lastSessionID = sessionID
	fake.lastSubscriptions = append([]core.MarketDataSubscription(nil), subscriptions...)
	if fake.subscribeError != nil {
		return nil, core.ConnectedFrame{}, fake.subscribeError
	}
	return fake.connection, core.ConnectedFrame{Market: "IN", Timezone: "UTC"}, nil
}

// The remaining CoreClient methods are present only to satisfy the
// interface. subscribeLive does not call them, so they return zero
// values.
func (fake *programmableSubscribeCoreClient) JoinCanonical(ctx context.Context, agentUserID, marketTag string) (core.CanonicalSessionJoin, error) {
	_ = ctx
	_ = agentUserID
	_ = marketTag
	return core.CanonicalSessionJoin{}, nil
}

func (fake *programmableSubscribeCoreClient) JoinOnDemand(ctx context.Context, agentUserID, exchangeID string) (core.OnDemandSessionJoin, error) {
	_ = ctx
	_ = agentUserID
	_ = exchangeID
	return core.OnDemandSessionJoin{}, nil
}

func (fake *programmableSubscribeCoreClient) JoinCanonicalAsOperator(ctx context.Context, operatorUserID, marketTag string) (core.CanonicalSessionJoin, error) {
	_ = ctx
	_ = operatorUserID
	_ = marketTag
	return core.CanonicalSessionJoin{}, nil
}

func (fake *programmableSubscribeCoreClient) JoinOnDemandAsOperator(ctx context.Context, operatorUserID, exchangeID string) (core.OnDemandSessionJoin, error) {
	_ = ctx
	_ = operatorUserID
	_ = exchangeID
	return core.OnDemandSessionJoin{}, nil
}

func (fake *programmableSubscribeCoreClient) OpenOperatorAccount(ctx context.Context, operatorUserID string) (string, error) {
	_ = ctx
	_ = operatorUserID
	return "", nil
}

func (fake *programmableSubscribeCoreClient) SeedInventory(ctx context.Context, request core.SeedInventoryRequest) (core.SeedInventoryResponse, error) {
	_ = ctx
	_ = request
	return core.SeedInventoryResponse{}, nil
}

func (fake *programmableSubscribeCoreClient) GetCandles(ctx context.Context, request core.CandlesRequest) (core.CandlesPage, error) {
	_ = ctx
	_ = request
	return core.CandlesPage{}, nil
}

func (fake *programmableSubscribeCoreClient) GetObservation(_ context.Context, _ string) (core.AccountObservation, error) {
	return core.AccountObservation{}, nil
}

func (fake *programmableSubscribeCoreClient) PunchOrder(_ context.Context, _ core.OrderPunchRequest) error {
	return nil
}

func (fake *programmableSubscribeCoreClient) GetAccountCash(_ context.Context, _ string) (core.AccountCash, error) {
	return core.AccountCash{}, nil
}

// fakeWSConnection is a controllable WSConnection for both
// subscribe_live and ingest tests. Tests push frames via
// FeedFrame; Close signals ingest to exit. ReadFrame returns the
// next frame or an error when the connection is closed or ctx is
// cancelled.
type fakeWSConnection struct {
	frames        chan core.IncomingFrame
	closedOnce    sync.Once
	closedChannel chan struct{}
}

func newFakeWSConnection() *fakeWSConnection {
	return &fakeWSConnection{
		frames:        make(chan core.IncomingFrame, 32),
		closedChannel: make(chan struct{}),
	}
}

func (connection *fakeWSConnection) FeedFrame(frame core.IncomingFrame) {
	connection.frames <- frame
}

func (connection *fakeWSConnection) ReadFrame(ctx context.Context) (core.IncomingFrame, error) {
	select {
	case frame, open := <-connection.frames:
		if !open {
			return core.IncomingFrame{}, errors.New("fakeWSConnection: frames channel closed")
		}
		return frame, nil
	case <-connection.closedChannel:
		return core.IncomingFrame{}, errors.New("fakeWSConnection: connection closed")
	case <-ctx.Done():
		return core.IncomingFrame{}, ctx.Err()
	}
}

func (connection *fakeWSConnection) WriteSubscribe(subscriptions []core.MarketDataSubscription) error {
	select {
	case <-connection.closedChannel:
		return errors.New("fakeWSConnection: WriteSubscribe on closed connection")
	default:
	}
	connection.frames <- core.IncomingFrame{
		Kind: core.IncomingFrameKindAck,
		Ack: &core.AckFrame{
			Results: func() []core.AckResult {
				results := make([]core.AckResult, len(subscriptions))
				for i, sub := range subscriptions {
					results[i] = core.AckResult{
						Stream:     sub.Stream,
						Instrument: sub.Instrument,
						Timeframe:  sub.Timeframe,
						Status:     core.SubscriptionAckStatusOK,
					}
				}
				return results
			}(),
		},
	}
	return nil
}

func (connection *fakeWSConnection) Close() error {
	connection.closedOnce.Do(func() { close(connection.closedChannel) })
	return nil
}
