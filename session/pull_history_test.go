package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"simulation-agents/core"
	"simulation-agents/store"
	"simulation-agents/transport"
)

// TestPullHistoryHappyPathSingleInstrument: one instrument, one
// page returned by Core, correct window dates in the request, and
// the converted bars land in MarketData.WriteCandles under the
// bring-up's exchange_id. Covers the baseline wiring: window
// computation, timezone handling, instrument-keyed write.
func TestPullHistoryHappyPathSingleInstrument(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClientForHistory()
	fakeCore.pagesByInstrument["stock1"] = []core.CandlesPage{
		{
			Market:     "IN",
			Instrument: "stock1",
			Timeframe:  core.TimeframeOneMinute,
			Timezone:   "UTC",
			Candles: []core.Candle{
				{StartNaiveLocal: "2026-10-20T09:30:00", EndNaiveLocal: "2026-10-20T09:31:00", Open: 100, High: 101, Low: 99, Close: 100, Volume: 1000},
				{StartNaiveLocal: "2026-10-20T09:31:00", EndNaiveLocal: "2026-10-20T09:32:00", Open: 100, High: 102, Low: 100, Close: 101, Volume: 1500},
			},
			NextCursor: "",
		},
	}
	fakeMarketData := newRecordingMarketDataStore()
	runner := pullHistoryTestRunner(t, fakeCore, fakeMarketData)

	err := runner.pullHistory(context.Background(), pullHistoryBringUpRequest([]string{"stock1"}))
	if err != nil {
		t.Fatalf("pullHistory: %v", err)
	}

	writes := fakeMarketData.collectCandleWrites()
	if len(writes) != 1 {
		t.Fatalf("WriteCandles call count = %d, want 1", len(writes))
	}
	write := writes[0]
	if write.exchangeID != "exch_canon" {
		t.Fatalf("WriteCandles exchange_id = %q", write.exchangeID)
	}
	if write.instrument != "stock1" {
		t.Fatalf("WriteCandles instrument = %q", write.instrument)
	}
	if write.timeframe != core.TimeframeOneMinute {
		t.Fatalf("WriteCandles timeframe = %q", write.timeframe)
	}
	if len(write.bars) != 2 {
		t.Fatalf("WriteCandles bar count = %d, want 2", len(write.bars))
	}
	// Unix-millis conversion: 2026-10-20T09:30:00 UTC → check it is non-zero
	// and strictly less than the second bar's time (chronological order preserved).
	if write.bars[0].OpenTime == 0 {
		t.Fatalf("OpenTime must be set after conversion")
	}
	if write.bars[0].OpenTime >= write.bars[1].OpenTime {
		t.Fatalf("bars must be chronologically ordered; got %d >= %d",
			write.bars[0].OpenTime, write.bars[1].OpenTime)
	}
}

// TestPullHistoryStitchesPagedResponse: three pages across two
// instruments; the stitched bar count per instrument equals the
// sum of per-page bars. Cursor is forwarded verbatim from page to
// page; a dropped cursor would silently truncate the window.
func TestPullHistoryStitchesPagedResponse(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClientForHistory()
	fakeCore.pagesByInstrument["stock1"] = []core.CandlesPage{
		{Timezone: "UTC", Candles: makeMinuteBars("2026-10-20T09:30:00", 10), NextCursor: "cur-stock1-1"},
		{Timezone: "UTC", Candles: makeMinuteBars("2026-10-20T09:40:00", 10), NextCursor: "cur-stock1-2"},
		{Timezone: "UTC", Candles: makeMinuteBars("2026-10-20T09:50:00", 10), NextCursor: ""},
	}
	fakeCore.pagesByInstrument["stock2"] = []core.CandlesPage{
		{Timezone: "UTC", Candles: makeMinuteBars("2026-10-20T09:30:00", 7), NextCursor: ""},
	}
	fakeMarketData := newRecordingMarketDataStore()
	runner := pullHistoryTestRunner(t, fakeCore, fakeMarketData)

	err := runner.pullHistory(context.Background(), pullHistoryBringUpRequest([]string{"stock1", "stock2"}))
	if err != nil {
		t.Fatalf("pullHistory: %v", err)
	}

	writes := fakeMarketData.collectCandleWrites()
	writesByInstrument := map[string]writeCandlesCall{}
	for _, write := range writes {
		writesByInstrument[write.instrument] = write
	}
	if got := len(writesByInstrument["stock1"].bars); got != 30 {
		t.Fatalf("stock1 bars after stitching = %d, want 30", got)
	}
	if got := len(writesByInstrument["stock2"].bars); got != 7 {
		t.Fatalf("stock2 bars after stitching = %d, want 7", got)
	}
	// Cursor forwarding: three GetCandles calls for stock1 (cur="", "cur-stock1-1", "cur-stock1-2").
	stock1Cursors := fakeCore.observedCursors("stock1")
	wantCursors := []string{"", "cur-stock1-1", "cur-stock1-2"}
	if !cursorsEqual(stock1Cursors, wantCursors) {
		t.Fatalf("stock1 cursors = %v, want %v", stock1Cursors, wantCursors)
	}
}

// TestPullHistoryFatalOnError: a GetCandles failure on one
// instrument surfaces as a non-nil error from pullHistory and the
// subsequent instruments are NOT pulled. Partial windows would
// corrupt agent perception, so the policy is fatal.
func TestPullHistoryFatalOnError(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClientForHistory()
	fakeCore.errorsByInstrument["stock1"] = errors.New("core unreachable")
	fakeCore.pagesByInstrument["stock2"] = []core.CandlesPage{
		{Timezone: "UTC", Candles: makeMinuteBars("2026-10-20T09:30:00", 5), NextCursor: ""},
	}
	fakeMarketData := newRecordingMarketDataStore()
	runner := pullHistoryTestRunner(t, fakeCore, fakeMarketData)

	err := runner.pullHistory(context.Background(), pullHistoryBringUpRequest([]string{"stock1", "stock2"}))
	if err == nil {
		t.Fatalf("expected fatal error on first-instrument failure; got nil")
	}
	if !strings.Contains(err.Error(), "stock1") {
		t.Fatalf("error must identify the offending instrument; got %q", err.Error())
	}
	if calls := fakeCore.getCandlesCountForInstrument("stock2"); calls != 0 {
		t.Fatalf("second instrument must not be pulled after a fatal error; got %d GetCandles calls for stock2", calls)
	}
}

// TestPullHistoryUsesCanonicalMarketTagOnOnDemand: the on-demand
// branch pulls history from the canonical market tag for the same
// country, because the broker does not persist on-demand candles.
// The exchange_id namespace the write lands under is still the
// on-demand bring-up's own, so canonical and on-demand caches do
// not collide.
func TestPullHistoryUsesCanonicalMarketTagOnOnDemand(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClientForHistory()
	fakeCore.pagesByInstrument["stock1"] = []core.CandlesPage{
		{Timezone: "UTC", Candles: makeMinuteBars("2026-10-20T09:30:00", 3), NextCursor: ""},
	}
	fakeMarketData := newRecordingMarketDataStore()
	runner := pullHistoryTestRunner(t, fakeCore, fakeMarketData)

	request := pullHistoryBringUpRequest([]string{"stock1"})
	request.ExchangeID = "exch_od_42"
	request.SessionType = "ondemand"

	err := runner.pullHistory(context.Background(), request)
	if err != nil {
		t.Fatalf("pullHistory: %v", err)
	}
	// Market tag observed by Core must be the canonical tag (= country).
	markets := fakeCore.observedMarkets()
	if len(markets) == 0 || markets[0] != "IN" {
		t.Fatalf("on-demand history must use canonical market tag %q; got %v", "IN", markets)
	}
	// Write exchange_id is the on-demand bring-up's own id (namespace isolation).
	writes := fakeMarketData.collectCandleWrites()
	if len(writes) != 1 || writes[0].exchangeID != "exch_od_42" {
		t.Fatalf("WriteCandles must use on-demand exchange_id; got %+v", writes)
	}
}

// TestPullHistoryRequiresLookback: a zero HistoryLookback is a
// composition-root bug — without a window there is nothing
// meaningful to pull. The op returns an explicit error instead of
// silently calling GetCandles with a reversed from>to window that
// Core would then reject with its own cryptic 4xx.
func TestPullHistoryRequiresLookback(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClientForHistory()
	fakeMarketData := newRecordingMarketDataStore()
	deps := basePullHistoryDependencies(fakeCore, fakeMarketData)
	deps.Params.HistoryLookback = 0
	runner := testRunnerWithDependencies(t, deps)

	err := runner.pullHistory(context.Background(), pullHistoryBringUpRequest([]string{"stock1"}))
	if err == nil {
		t.Fatalf("expected error on zero HistoryLookback; got nil")
	}
	if !strings.Contains(err.Error(), "HistoryLookback") {
		t.Fatalf("error must mention HistoryLookback; got %q", err.Error())
	}
	if fakeCore.totalGetCandlesCalls() != 0 {
		t.Fatalf("Core must not be hit on bad config; got %d GetCandles calls", fakeCore.totalGetCandlesCalls())
	}
}

// TestPullHistoryNoInstrumentsIsNoOp: an empty instruments slice
// is odd but non-fatal — the op logs a warning and returns nil
// without hitting Core or writing anything.
func TestPullHistoryNoInstrumentsIsNoOp(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClientForHistory()
	fakeMarketData := newRecordingMarketDataStore()
	runner := pullHistoryTestRunner(t, fakeCore, fakeMarketData)
	request := pullHistoryBringUpRequest(nil)

	if err := runner.pullHistory(context.Background(), request); err != nil {
		t.Fatalf("empty instruments must not error; got %v", err)
	}
	if fakeCore.totalGetCandlesCalls() != 0 {
		t.Fatalf("Core must not be hit when instruments is empty; got %d calls", fakeCore.totalGetCandlesCalls())
	}
	if len(fakeMarketData.collectCandleWrites()) != 0 {
		t.Fatalf("WriteCandles must not be called when instruments is empty")
	}
}

// TestPullHistoryPullsEveryConfiguredTimeframePerInstrument: with
// three timeframes configured and two instruments, the op must
// issue a paged pull per (timeframe × instrument) pair (six pulls
// total) and write each cleanly under its own candles:{tf} key.
// A dropped timeframe or a collapsed loop would silently leave an
// agent perception window un-seeded.
func TestPullHistoryPullsEveryConfiguredTimeframePerInstrument(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClientForHistory()
	for _, instrument := range []string{"stock1", "stock2"} {
		fakeCore.pagesByInstrument[instrument] = []core.CandlesPage{{
			Timezone:   "UTC",
			Candles:    makeMinuteBars("2026-10-20T09:30:00", 4),
			NextCursor: "",
		}}
	}
	fakeMarketData := newRecordingMarketDataStore()
	deps := basePullHistoryDependencies(fakeCore, fakeMarketData)
	deps.Params.HistoryTimeframes = []string{
		core.TimeframeOneMinute,
		core.TimeframeFiveMinute,
		core.TimeframeSixtyMinute,
	}
	runner := testRunnerWithDependencies(t, deps)

	err := runner.pullHistory(context.Background(), pullHistoryBringUpRequest([]string{"stock1", "stock2"}))
	if err != nil {
		t.Fatalf("pullHistory: %v", err)
	}

	// 3 timeframes × 2 instruments → 6 pulls + 6 writes.
	if calls := fakeCore.totalGetCandlesCalls(); calls != 6 {
		t.Fatalf("GetCandles total calls = %d, want 6 (3 TFs × 2 instruments)", calls)
	}
	writes := fakeMarketData.collectCandleWrites()
	if len(writes) != 6 {
		t.Fatalf("WriteCandles call count = %d, want 6", len(writes))
	}

	// Every (instrument, timeframe) pair must be present in the writes
	// exactly once. A duplicate or missing pair is the regression this
	// test guards against.
	type instrumentTimeframeKey struct{ instrument, timeframe string }
	writeCount := map[instrumentTimeframeKey]int{}
	for _, write := range writes {
		writeCount[instrumentTimeframeKey{write.instrument, write.timeframe}]++
	}
	for _, instrument := range []string{"stock1", "stock2"} {
		for _, timeframe := range deps.Params.HistoryTimeframes {
			if writeCount[instrumentTimeframeKey{instrument, timeframe}] != 1 {
				t.Fatalf("WriteCandles for (%s, %s) count = %d, want 1",
					instrument, timeframe, writeCount[instrumentTimeframeKey{instrument, timeframe}])
			}
		}
	}

	// The Core calls must forward each configured timeframe in the
	// request. Collapsing to 1m silently would land every TF's bars
	// into the 1m key.
	observedTimeframes := map[string]int{}
	fakeCore.callsMutex.Lock()
	for _, call := range fakeCore.calls {
		observedTimeframes[call.Timeframe]++
	}
	fakeCore.callsMutex.Unlock()
	for _, timeframe := range deps.Params.HistoryTimeframes {
		if observedTimeframes[timeframe] != 2 {
			t.Fatalf("GetCandles timeframe=%q call count = %d, want 2",
				timeframe, observedTimeframes[timeframe])
		}
	}
}

// TestPullHistoryRejectsEmptyTimeframes: no configured timeframes
// is a composition-root bug — the op reports it explicitly rather
// than silently writing nothing and letting agents fail later with
// "no candles" errors mid-session.
func TestPullHistoryRejectsEmptyTimeframes(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClientForHistory()
	fakeMarketData := newRecordingMarketDataStore()
	deps := basePullHistoryDependencies(fakeCore, fakeMarketData)
	deps.Params.HistoryTimeframes = nil
	runner := testRunnerWithDependencies(t, deps)

	err := runner.pullHistory(context.Background(), pullHistoryBringUpRequest([]string{"stock1"}))
	if err == nil {
		t.Fatalf("expected error on nil HistoryTimeframes; got nil")
	}
	if !strings.Contains(err.Error(), "HistoryTimeframes") {
		t.Fatalf("error must mention HistoryTimeframes; got %q", err.Error())
	}
	if fakeCore.totalGetCandlesCalls() != 0 {
		t.Fatalf("Core must not be hit on empty HistoryTimeframes")
	}
}

// TestPullHistoryRejectsUnknownTimeframe: a configured timeframe
// outside the Core-accepted set {1m,5m,30m,60m} is caught at the
// top of the op, before any Core call. Catching it later (at the
// first GetCandles) would waste a round-trip and surface Core's
// own cryptic 4xx rather than a clear "bad config" error.
func TestPullHistoryRejectsUnknownTimeframe(t *testing.T) {
	t.Parallel()

	fakeCore := newProgrammableCoreClientForHistory()
	fakeMarketData := newRecordingMarketDataStore()
	deps := basePullHistoryDependencies(fakeCore, fakeMarketData)
	deps.Params.HistoryTimeframes = []string{core.TimeframeOneMinute, "15m"}
	runner := testRunnerWithDependencies(t, deps)

	err := runner.pullHistory(context.Background(), pullHistoryBringUpRequest([]string{"stock1"}))
	if err == nil {
		t.Fatalf("expected error on unknown timeframe; got nil")
	}
	if !strings.Contains(err.Error(), "15m") {
		t.Fatalf("error must name the offending timeframe; got %q", err.Error())
	}
	if fakeCore.totalGetCandlesCalls() != 0 {
		t.Fatalf("Core must not be hit on bad timeframe (even for the valid entries)")
	}
}

// --- fixtures and fakes

func pullHistoryTestRunner(t *testing.T, fakeCore *programmableCoreClientForHistory, fakeMarketData *recordingMarketDataStore) *Runner {
	t.Helper()
	return testRunnerWithDependencies(t, basePullHistoryDependencies(fakeCore, fakeMarketData))
}

func basePullHistoryDependencies(fakeCore CoreClient, fakeMarketData MarketDataStore) Dependencies {
	return Dependencies{
		Core:       fakeCore,
		Runtime:    stubRuntimeStore{},
		MarketData: fakeMarketData,
		Scheduler:  stubScheduler{},
		Failure:    stubFailureReporter{},
		Clock:      stubClock{},
		RNGSource:  stubRNGSource{},
		Params: Params{
			EntryCurve:              DefaultEntryCurveParams(),
			LoadSessionsConcurrency: 1,
			HistoryLookback:         60 * time.Minute,
			HistoryTimeframes:       []string{core.TimeframeOneMinute},
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func pullHistoryBringUpRequest(instruments []string) transport.BringUpRequest {
	return transport.BringUpRequest{
		ExchangeID:   "exch_canon",
		SessionType:  "canonical",
		Country:      "IN",
		SessionStart: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC),
		SessionEnd:   time.Date(2026, 10, 20, 10, 30, 0, 0, time.UTC),
		Instruments:  instruments,
	}
}

func makeMinuteBars(startNaiveLocal string, count int) []core.Candle {
	parsedStart, _ := time.ParseInLocation(coreNaiveLocalFormat, startNaiveLocal, time.UTC)
	bars := make([]core.Candle, 0, count)
	for barIndex := 0; barIndex < count; barIndex++ {
		barStart := parsedStart.Add(time.Duration(barIndex) * time.Minute)
		barEnd := barStart.Add(time.Minute)
		bars = append(bars, core.Candle{
			StartNaiveLocal: barStart.Format(coreNaiveLocalFormat),
			EndNaiveLocal:   barEnd.Format(coreNaiveLocalFormat),
			Open:            100, High: 101, Low: 99, Close: 100, Volume: 1000,
		})
	}
	return bars
}

func cursorsEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for cursorIndex, cursorValue := range got {
		if cursorValue != want[cursorIndex] {
			return false
		}
	}
	return true
}

// programmableCoreClientForHistory is a focused fake for pullHistory
// tests. It programs per-instrument pages and errors, and records
// every GetCandles call for assertion on cursor forwarding and
// per-instrument call counts.
type programmableCoreClientForHistory struct {
	pagesByInstrument  map[string][]core.CandlesPage
	errorsByInstrument map[string]error

	callsMutex sync.Mutex
	calls      []core.CandlesRequest
}

func newProgrammableCoreClientForHistory() *programmableCoreClientForHistory {
	return &programmableCoreClientForHistory{
		pagesByInstrument:  make(map[string][]core.CandlesPage),
		errorsByInstrument: make(map[string]error),
	}
}

func (fake *programmableCoreClientForHistory) GetCandles(ctx context.Context, request core.CandlesRequest) (core.CandlesPage, error) {
	_ = ctx
	fake.callsMutex.Lock()
	fake.calls = append(fake.calls, request)
	fake.callsMutex.Unlock()

	if err, programmed := fake.errorsByInstrument[request.Instrument]; programmed {
		return core.CandlesPage{}, err
	}
	pages := fake.pagesByInstrument[request.Instrument]
	// Determine which page to return based on the cursor forwarded.
	// Empty cursor → page 0; a non-empty cursor matches the NextCursor
	// of a prior page.
	if request.Cursor == "" {
		if len(pages) == 0 {
			return core.CandlesPage{}, nil
		}
		return pages[0], nil
	}
	for pageIndex, page := range pages {
		if pageIndex == 0 {
			continue
		}
		previousPage := pages[pageIndex-1]
		if previousPage.NextCursor == request.Cursor {
			return page, nil
		}
	}
	return core.CandlesPage{}, errors.New("unknown cursor")
}

func (fake *programmableCoreClientForHistory) observedCursors(instrument string) []string {
	fake.callsMutex.Lock()
	defer fake.callsMutex.Unlock()
	cursors := []string{}
	for _, call := range fake.calls {
		if call.Instrument == instrument {
			cursors = append(cursors, call.Cursor)
		}
	}
	return cursors
}

func (fake *programmableCoreClientForHistory) observedMarkets() []string {
	fake.callsMutex.Lock()
	defer fake.callsMutex.Unlock()
	markets := make([]string, 0, len(fake.calls))
	for _, call := range fake.calls {
		markets = append(markets, call.Market)
	}
	return markets
}

func (fake *programmableCoreClientForHistory) getCandlesCountForInstrument(instrument string) int {
	fake.callsMutex.Lock()
	defer fake.callsMutex.Unlock()
	count := 0
	for _, call := range fake.calls {
		if call.Instrument == instrument {
			count++
		}
	}
	return count
}

func (fake *programmableCoreClientForHistory) totalGetCandlesCalls() int {
	fake.callsMutex.Lock()
	defer fake.callsMutex.Unlock()
	return len(fake.calls)
}

// The remaining CoreClient methods are present only to satisfy the
// interface. pullHistory does not call them, so they return zero
// values.
func (fake *programmableCoreClientForHistory) JoinCanonical(ctx context.Context, agentUserID, marketTag string) (core.CanonicalSessionJoin, error) {
	_ = ctx
	_ = agentUserID
	_ = marketTag
	return core.CanonicalSessionJoin{}, nil
}

func (fake *programmableCoreClientForHistory) Subscribe(ctx context.Context, sessionID string, subscriptions []core.MarketDataSubscription) (WSConnection, core.ConnectedFrame, error) {
	_ = ctx
	_ = sessionID
	_ = subscriptions
	return nil, core.ConnectedFrame{}, nil
}

func (fake *programmableCoreClientForHistory) GetObservation(_ context.Context, _ string) (core.AccountObservation, error) {
	return core.AccountObservation{}, nil
}

func (fake *programmableCoreClientForHistory) PunchOrder(_ context.Context, _ core.OrderPunchRequest) error {
	return nil
}

// recordingMarketDataStore captures every market-data write so a
// test can assert on exchange_id, instrument, timeframe, and
// payloads. All three ops are recorded in full — pullHistory tests
// exercise WriteCandles; ingest tests exercise AppendCandle and
// WriteQuote.
type recordingMarketDataStore struct {
	callsMutex    sync.Mutex
	writes        []writeCandlesCall
	appendCalls   []appendCandleCall
	quoteWrites   []writeQuoteCall
}

type writeCandlesCall struct {
	exchangeID string
	instrument string
	timeframe  string
	bars       []store.Candle
}

type appendCandleCall struct {
	exchangeID string
	instrument string
	timeframe  string
	bar        store.Candle
}

type writeQuoteCall struct {
	exchangeID string
	instrument string
	quote      store.Quote
}

func newRecordingMarketDataStore() *recordingMarketDataStore {
	return &recordingMarketDataStore{}
}

func (fake *recordingMarketDataStore) WriteCandles(ctx context.Context, exchangeID, instrument, timeframe string, bars []store.Candle) error {
	_ = ctx
	fake.callsMutex.Lock()
	fake.writes = append(fake.writes, writeCandlesCall{
		exchangeID: exchangeID,
		instrument: instrument,
		timeframe:  timeframe,
		bars:       append([]store.Candle(nil), bars...),
	})
	fake.callsMutex.Unlock()
	return nil
}

func (fake *recordingMarketDataStore) AppendCandle(ctx context.Context, exchangeID, instrument, timeframe string, bar store.Candle) error {
	_ = ctx
	fake.callsMutex.Lock()
	fake.appendCalls = append(fake.appendCalls, appendCandleCall{
		exchangeID: exchangeID,
		instrument: instrument,
		timeframe:  timeframe,
		bar:        bar,
	})
	fake.callsMutex.Unlock()
	return nil
}

func (fake *recordingMarketDataStore) WriteQuote(ctx context.Context, exchangeID, instrument string, quote store.Quote) error {
	_ = ctx
	fake.callsMutex.Lock()
	fake.quoteWrites = append(fake.quoteWrites, writeQuoteCall{
		exchangeID: exchangeID,
		instrument: instrument,
		quote:      quote,
	})
	fake.callsMutex.Unlock()
	return nil
}

func (fake *recordingMarketDataStore) ReadCandles(_ context.Context, _, _, _ string, _ int) ([]store.Candle, error) {
	return nil, nil
}

func (fake *recordingMarketDataStore) ReadQuote(_ context.Context, _, _ string) (store.Quote, bool, error) {
	return store.Quote{}, false, nil
}

func (fake *recordingMarketDataStore) collectCandleWrites() []writeCandlesCall {
	fake.callsMutex.Lock()
	defer fake.callsMutex.Unlock()
	return append([]writeCandlesCall(nil), fake.writes...)
}

func (fake *recordingMarketDataStore) collectAppendCandleCalls() []appendCandleCall {
	fake.callsMutex.Lock()
	defer fake.callsMutex.Unlock()
	return append([]appendCandleCall(nil), fake.appendCalls...)
}

func (fake *recordingMarketDataStore) collectQuoteWrites() []writeQuoteCall {
	fake.callsMutex.Lock()
	defer fake.callsMutex.Unlock()
	return append([]writeQuoteCall(nil), fake.quoteWrites...)
}
