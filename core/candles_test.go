package core

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validCandlesRequest() CandlesRequest {
	return CandlesRequest{
		Market:         "IN",
		Instrument:     "INST-A",
		Timeframe:      TimeframeOneMinute,
		FromNaiveLocal: "2026-10-01T09:15:00",
		ToNaiveLocal:   "2026-10-01T15:30:00",
	}
}

func TestGetCandlesSendsExpectedQueryAndDecodesPage(t *testing.T) {
	t.Parallel()

	var observedQuery atomic.Value // url.Values
	var observedPath atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		observedPath.Store(request.URL.Path)
		observedQuery.Store(request.URL.Query())

		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{
			"market":"IN",
			"instrument":"INST-A",
			"timeframe":"1m",
			"timezone":"Asia/Kolkata",
			"from":"2026-10-01T09:15:00",
			"to":"2026-10-01T15:30:00",
			"candles":[
				{"start":"2026-10-01T09:15:00","end":"2026-10-01T09:16:00","open":25000,"high":25100,"low":24950,"close":25080,"volume":1200},
				{"start":"2026-10-01T09:16:00","end":"2026-10-01T09:17:00","open":25080,"high":25200,"low":25050,"close":25150,"volume":800}
			],
			"next_cursor":"2026-10-01T09:17:00"
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	request := validCandlesRequest()
	request.Cursor = "2026-10-01T09:15:00"
	request.Limit = 200

	page, err := client.GetCandles(context.Background(), request)
	if err != nil {
		t.Fatalf("GetCandles: %v", err)
	}

	if got := observedPath.Load().(string); got != candlesEndpointPath {
		t.Fatalf("path = %q, want %q", got, candlesEndpointPath)
	}
	query := observedQuery.Load().(url.Values)
	for _, expected := range []struct{ key, value string }{
		{"market", "IN"},
		{"instrument", "INST-A"},
		{"timeframe", "1m"},
		{"from", "2026-10-01T09:15:00"},
		{"to", "2026-10-01T15:30:00"},
		{"cursor", "2026-10-01T09:15:00"},
		{"limit", "200"},
	} {
		if got := query.Get(expected.key); got != expected.value {
			t.Fatalf("query %q = %q, want %q", expected.key, got, expected.value)
		}
	}

	if page.Market != "IN" || page.Instrument != "INST-A" || page.Timeframe != "1m" {
		t.Fatalf("page envelope = %+v", page)
	}
	if page.Timezone != "Asia/Kolkata" {
		t.Fatalf("Timezone = %q", page.Timezone)
	}
	if page.NextCursor != "2026-10-01T09:17:00" {
		t.Fatalf("NextCursor = %q", page.NextCursor)
	}
	if len(page.Candles) != 2 {
		t.Fatalf("len(Candles) = %d, want 2", len(page.Candles))
	}
	firstCandle := page.Candles[0]
	if firstCandle.StartNaiveLocal != "2026-10-01T09:15:00" {
		t.Fatalf("first candle Start = %q", firstCandle.StartNaiveLocal)
	}
	if firstCandle.Open != 25000 || firstCandle.High != 25100 || firstCandle.Low != 24950 || firstCandle.Close != 25080 {
		t.Fatalf("first candle OHLC = %+v", firstCandle)
	}
	if firstCandle.Volume != 1200 {
		t.Fatalf("first candle Volume = %d", firstCandle.Volume)
	}
}

// Empty Cursor and zero Limit must be omitted from the wire so Core
// can apply its own defaults. A test server that saw these keys would
// mean the client is sending useless noise.
func TestGetCandlesOmitsOptionalParamsWhenUnset(t *testing.T) {
	t.Parallel()

	var observedQuery atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		observedQuery.Store(request.URL.Query())
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"market":"IN","instrument":"INST-A","timeframe":"1m","timezone":"Asia/Kolkata","from":"","to":"","candles":[],"next_cursor":""}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	if _, err := client.GetCandles(context.Background(), validCandlesRequest()); err != nil {
		t.Fatalf("GetCandles: %v", err)
	}

	query := observedQuery.Load().(url.Values)
	if query.Has("cursor") {
		t.Fatalf("cursor should be omitted when empty, got %q", query.Get("cursor"))
	}
	if query.Has("limit") {
		t.Fatalf("limit should be omitted when zero, got %q", query.Get("limit"))
	}
}

func TestGetCandlesNormalizesNullCandlesToEmptySlice(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"market":"IN","instrument":"INST-A","timeframe":"1m","timezone":"Asia/Kolkata","from":"","to":"","candles":null,"next_cursor":""}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	page, err := client.GetCandles(context.Background(), validCandlesRequest())
	if err != nil {
		t.Fatalf("GetCandles: %v", err)
	}
	if page.Candles == nil {
		t.Fatalf("Candles should be an empty slice, not nil")
	}
	if len(page.Candles) != 0 {
		t.Fatalf("expected 0 candles on null, got %d", len(page.Candles))
	}
}

func TestGetCandlesPreservesInt64PrecisionNearMaxInt(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{
			"market":"IN","instrument":"INST-A","timeframe":"1m","timezone":"Asia/Kolkata",
			"from":"","to":"",
			"candles":[{"start":"","end":"","open":9223372036854775800,"high":0,"low":0,"close":0,"volume":9223372036854775800}],
			"next_cursor":""
		}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	page, err := client.GetCandles(context.Background(), validCandlesRequest())
	if err != nil {
		t.Fatalf("GetCandles: %v", err)
	}
	wantValue := int64(math.MaxInt64 - 7)
	if page.Candles[0].Open != wantValue {
		t.Fatalf("Open = %d, want %d", page.Candles[0].Open, wantValue)
	}
	if page.Candles[0].Volume != wantValue {
		t.Fatalf("Volume = %d, want %d", page.Candles[0].Volume, wantValue)
	}
}

func TestGetCandlesPreFlightRejectsMalformedRequests(t *testing.T) {
	t.Parallel()

	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		serverHits.Add(1)
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	cases := []struct {
		name              string
		mutate            func(*CandlesRequest)
		wantErrorContains string
	}{
		{"empty market", func(request *CandlesRequest) { request.Market = "" }, "non-empty market"},
		{"empty instrument", func(request *CandlesRequest) { request.Instrument = "" }, "non-empty instrument"},
		{"unknown timeframe", func(request *CandlesRequest) { request.Timeframe = "2m" }, "timeframe must be"},
		{"empty timeframe", func(request *CandlesRequest) { request.Timeframe = "" }, "timeframe must be"},
		{"empty from", func(request *CandlesRequest) { request.FromNaiveLocal = "" }, "from timestamp"},
		{"empty to", func(request *CandlesRequest) { request.ToNaiveLocal = "" }, "to timestamp"},
		{"negative limit", func(request *CandlesRequest) { request.Limit = -1 }, "limit must be >= 0"},
		{"limit over max", func(request *CandlesRequest) { request.Limit = candlesLimitMaximum + 1 }, "limit must be <="},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			request := validCandlesRequest()
			testCase.mutate(&request)
			_, err := client.GetCandles(context.Background(), request)
			if err == nil {
				t.Fatalf("expected pre-flight error, got nil")
			}
			if !strings.Contains(err.Error(), testCase.wantErrorContains) {
				t.Fatalf("error %q does not contain %q", err.Error(), testCase.wantErrorContains)
			}
			if _, isCallError := err.(*CallError); isCallError {
				t.Fatalf("pre-flight error must not be *CallError: %v", err)
			}
		})
	}
	if serverHits.Load() != 0 {
		t.Fatalf("server was hit %d time(s); pre-flight errors must never reach the network", serverHits.Load())
	}
}

func TestGetCandlesAcceptsAllDeclaredTimeframes(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"market":"IN","instrument":"INST-A","timeframe":"1m","timezone":"Asia/Kolkata","from":"","to":"","candles":[],"next_cursor":""}`))
	}))
	t.Cleanup(server.Close)
	client := newTestClient(t, server)

	for _, timeframe := range []string{
		TimeframeOneMinute, TimeframeFiveMinute, TimeframeThirtyMinute, TimeframeSixtyMinute,
	} {
		request := validCandlesRequest()
		request.Timeframe = timeframe
		if _, err := client.GetCandles(context.Background(), request); err != nil {
			t.Fatalf("timeframe %q should pass pre-flight: %v", timeframe, err)
		}
	}
}

func TestGetCandlesReturnsCallErrorOn404(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	client := newTestClient(t, server)
	_, err := client.GetCandles(context.Background(), validCandlesRequest())
	var callError *CallError
	if !errors.As(err, &callError) {
		t.Fatalf("expected *CallError, got %v", err)
	}
	if callError.HTTPStatus != http.StatusNotFound {
		t.Fatalf("HTTPStatus = %d", callError.HTTPStatus)
	}
}

func TestGetCandlesRetriesTransient5xxThenSucceeds(t *testing.T) {
	t.Parallel()
	var serverHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		attemptNumber := serverHits.Add(1)
		if attemptNumber == 1 {
			responseWriter.WriteHeader(http.StatusInternalServerError)
			return
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"market":"IN","instrument":"INST-A","timeframe":"1m","timezone":"Asia/Kolkata","from":"","to":"","candles":[],"next_cursor":""}`))
	}))
	t.Cleanup(server.Close)

	client := newTestClientWithRetryPolicy(t, server, 2, 1*time.Millisecond)
	if _, err := client.GetCandles(context.Background(), validCandlesRequest()); err != nil {
		t.Fatalf("GetCandles: %v", err)
	}
	if got := serverHits.Load(); got != 2 {
		t.Fatalf("server hits = %d, want 2", got)
	}
}
