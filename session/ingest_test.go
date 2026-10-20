package session

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"simulation-agents/core"
)

// TestIngestRoutesSnapshotFrameToWriteQuote: a snapshot frame
// results in a WriteQuote call with the two-sided price/qty taken
// from the SnapshotFrameData.Quote block, honoring the HasBid /
// HasAsk gates.
func TestIngestRoutesSnapshotFrameToWriteQuote(t *testing.T) {
	t.Parallel()

	fakeMarketData := newRecordingMarketDataStore()
	runner := ingestTestRunner(t, fakeMarketData)
	connection := newFakeWSConnection()

	ingestDone := make(chan struct{})
	go func() {
		runner.runIngest(context.Background(), "exch_live", "UTC", connection)
		close(ingestDone)
	}()

	connection.FeedFrame(core.IncomingFrame{
		Kind: core.IncomingFrameKindSnapshot,
		Snapshot: &core.SnapshotFrame{
			Instrument: "stock1",
			Data: core.SnapshotFrameData{
				Phase:     "open",
				LastPrice: 10050,
				HasLast:   true,
				Quote: core.MarketDataQuote{
					BestBid: 10040, BidQty: 7, HasBid: true,
					BestAsk: 10060, AskQty: 9, HasAsk: true,
				},
			},
		},
	})
	connection.FeedFrame(core.IncomingFrame{
		Kind: core.IncomingFrameKindSnapshot,
		Snapshot: &core.SnapshotFrame{
			Instrument: "stock1",
			Data: core.SnapshotFrameData{
				Phase: "open",
				Quote: core.MarketDataQuote{HasBid: false, HasAsk: true, BestAsk: 10070, AskQty: 3},
			},
		},
	})

	waitForQuoteWriteCount(t, fakeMarketData, 2, time.Second)

	writes := fakeMarketData.collectQuoteWrites()
	if len(writes) != 2 {
		t.Fatalf("WriteQuote call count = %d, want 2", len(writes))
	}
	if writes[0].quote.BidPrice != 10040 || writes[0].quote.AskPrice != 10060 {
		t.Fatalf("first quote prices = %+v", writes[0].quote)
	}
	if writes[1].quote.BidPrice != 0 || writes[1].quote.AskPrice != 10070 {
		t.Fatalf("second quote must gate the missing bid side to zero; got %+v", writes[1].quote)
	}
	_ = connection.Close()
	<-ingestDone
}

// TestIngestRoutesCandleFrameToAppendCandle: a finalized candle
// stream frame maps into a store.Candle via naive-local → unix-ms
// conversion using the connection's declared timezone, and is
// appended under the right (exchange_id, instrument, timeframe).
func TestIngestRoutesCandleFrameToAppendCandle(t *testing.T) {
	t.Parallel()

	fakeMarketData := newRecordingMarketDataStore()
	runner := ingestTestRunner(t, fakeMarketData)
	connection := newFakeWSConnection()

	ingestDone := make(chan struct{})
	go func() {
		runner.runIngest(context.Background(), "exch_live", "UTC", connection)
		close(ingestDone)
	}()

	connection.FeedFrame(core.IncomingFrame{
		Kind: core.IncomingFrameKindCandle,
		Candle: &core.CandleStreamFrame{
			Instrument: "stock1",
			Timeframe:  core.TimeframeOneMinute,
			Bar: core.MarketDataBar{
				StartNaiveLocal: "2026-10-20T10:00:00",
				EndNaiveLocal:   "2026-10-20T10:01:00",
				Open:            100, High: 103, Low: 99, Close: 102, Volume: 500,
			},
		},
	})

	waitForAppendCount(t, fakeMarketData, 1, time.Second)

	appends := fakeMarketData.collectAppendCandleCalls()
	if len(appends) != 1 {
		t.Fatalf("AppendCandle call count = %d, want 1", len(appends))
	}
	appended := appends[0]
	if appended.exchangeID != "exch_live" || appended.instrument != "stock1" || appended.timeframe != core.TimeframeOneMinute {
		t.Fatalf("AppendCandle routing wrong: %+v", appended)
	}
	if appended.bar.Open != 100 || appended.bar.Close != 102 || appended.bar.Volume != 500 {
		t.Fatalf("AppendCandle bar fields wrong: %+v", appended.bar)
	}
	// 2026-10-20T10:00:00 UTC → non-zero unix-ms, chronological ordering.
	if appended.bar.OpenTime == 0 || appended.bar.CloseTime <= appended.bar.OpenTime {
		t.Fatalf("timestamps not populated or out of order: %+v", appended.bar)
	}
	_ = connection.Close()
	<-ingestDone
}

// TestIngestExitsOnContextCancel: a cancelled ctx forces ReadFrame
// to return an error, the ingest loop logs and exits. The caller
// (Run) relies on this to tear ingest down when the session is
// winding up or has failed.
func TestIngestExitsOnContextCancel(t *testing.T) {
	t.Parallel()

	fakeMarketData := newRecordingMarketDataStore()
	runner := ingestTestRunner(t, fakeMarketData)
	connection := newFakeWSConnection()

	ctx, cancel := context.WithCancel(context.Background())
	ingestDone := make(chan struct{})
	go func() {
		runner.runIngest(ctx, "exch_live", "UTC", connection)
		close(ingestDone)
	}()
	cancel()
	select {
	case <-ingestDone:
	case <-time.After(time.Second):
		t.Fatalf("ingest did not exit after ctx cancel within 1s")
	}
	// Connection must have been closed by the defer in runIngest.
	select {
	case <-connection.closedChannel:
	default:
		t.Fatalf("ingest must close the connection on exit")
	}
}

// TestIngestExitsOnSocketClose: a socket error from ReadFrame
// (what Core sends on EOF or on a protocol fault) also exits the
// loop. Different exit path from ctx cancel; both must land here.
func TestIngestExitsOnSocketClose(t *testing.T) {
	t.Parallel()

	fakeMarketData := newRecordingMarketDataStore()
	runner := ingestTestRunner(t, fakeMarketData)
	connection := newFakeWSConnection()

	ingestDone := make(chan struct{})
	go func() {
		runner.runIngest(context.Background(), "exch_live", "UTC", connection)
		close(ingestDone)
	}()
	_ = connection.Close()
	select {
	case <-ingestDone:
	case <-time.After(time.Second):
		t.Fatalf("ingest did not exit after socket close within 1s")
	}
}

// TestIngestLogsAckErrorAndContinues: an ack with status=error is
// logged (per-sub nack from Core, e.g. unknown instrument) but
// does not stop the ingest loop. Market data writes keep working
// after it.
func TestIngestLogsAckErrorAndContinues(t *testing.T) {
	t.Parallel()

	fakeMarketData := newRecordingMarketDataStore()
	runner := ingestTestRunner(t, fakeMarketData)
	connection := newFakeWSConnection()

	ingestDone := make(chan struct{})
	go func() {
		runner.runIngest(context.Background(), "exch_live", "UTC", connection)
		close(ingestDone)
	}()

	connection.FeedFrame(core.IncomingFrame{
		Kind: core.IncomingFrameKindAck,
		Ack: &core.AckFrame{Results: []core.AckResult{
			{Stream: "snapshot", Instrument: "stock_unknown", Status: core.SubscriptionAckStatusError, Error: "unknown instrument"},
		}},
	})
	connection.FeedFrame(core.IncomingFrame{
		Kind: core.IncomingFrameKindSnapshot,
		Snapshot: &core.SnapshotFrame{
			Instrument: "stock1",
			Data: core.SnapshotFrameData{
				Quote: core.MarketDataQuote{HasBid: true, HasAsk: true, BestBid: 10, BestAsk: 20},
			},
		},
	})
	waitForQuoteWriteCount(t, fakeMarketData, 1, time.Second)
	_ = connection.Close()
	<-ingestDone
}

// TestIngestIgnoresUnknownFrameKindWithoutExiting: forward-
// compatible behavior — a frame with Kind=unknown is logged and
// skipped; subsequent frames still process normally.
func TestIngestIgnoresUnknownFrameKindWithoutExiting(t *testing.T) {
	t.Parallel()

	fakeMarketData := newRecordingMarketDataStore()
	runner := ingestTestRunner(t, fakeMarketData)
	connection := newFakeWSConnection()

	ingestDone := make(chan struct{})
	go func() {
		runner.runIngest(context.Background(), "exch_live", "UTC", connection)
		close(ingestDone)
	}()

	connection.FeedFrame(core.IncomingFrame{Kind: core.IncomingFrameKindUnknown, Raw: json.RawMessage(`{"type":"newthing"}`)})
	connection.FeedFrame(core.IncomingFrame{
		Kind: core.IncomingFrameKindSnapshot,
		Snapshot: &core.SnapshotFrame{
			Instrument: "stock1",
			Data: core.SnapshotFrameData{Quote: core.MarketDataQuote{HasBid: true, BestBid: 5}},
		},
	})
	waitForQuoteWriteCount(t, fakeMarketData, 1, time.Second)
	_ = connection.Close()
	<-ingestDone
}

// --- fixtures and helpers

func ingestTestRunner(t *testing.T, fakeMarketData *recordingMarketDataStore) *Runner {
	t.Helper()
	return testRunnerWithDependencies(t, Dependencies{
		Core:       stubCoreClient{},
		Runtime:    stubRuntimeStore{},
		MarketData: fakeMarketData,
		Scheduler:  stubScheduler{},
		Failure:    stubFailureReporter{},
		Clock:      stubClock{},
		RNGSource:  stubRNGSource{},
		Params:     Params{EntryCurve: DefaultEntryCurveParams()},
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func waitForQuoteWriteCount(t *testing.T, fakeMarketData *recordingMarketDataStore, target int, maximum time.Duration) {
	t.Helper()
	deadline := time.Now().Add(maximum)
	for time.Now().Before(deadline) {
		if len(fakeMarketData.collectQuoteWrites()) >= target {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d WriteQuote calls; have %d", target, len(fakeMarketData.collectQuoteWrites()))
}

func waitForAppendCount(t *testing.T, fakeMarketData *recordingMarketDataStore, target int, maximum time.Duration) {
	t.Helper()
	deadline := time.Now().Add(maximum)
	for time.Now().Before(deadline) {
		if len(fakeMarketData.collectAppendCandleCalls()) >= target {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d AppendCandle calls; have %d",
		target, len(fakeMarketData.collectAppendCandleCalls()))
}

