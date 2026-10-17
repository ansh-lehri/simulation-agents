package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// webSocketScript is a fake Core WebSocket server used by the Stage 8
// tests. Each test configures a script of {received, sent} frames
// through the hooks and the server plays them in order after
// upgrading the connection.
type webSocketScript struct {
	// Observed captures the subscribe frame bytes the server read off
	// the socket, in the order they arrived.
	Observed       [][]byte
	ObservedMutex  sync.Mutex
	ObservedQuery  url.Values
	ObservedPath   string
	HeldReadsDone  chan struct{}
	outgoingFrames [][]byte
	skipGreeting   bool
	badGreeting    []byte
}

func newWebSocketScript() *webSocketScript {
	return &webSocketScript{HeldReadsDone: make(chan struct{})}
}

// recordObserved appends the latest-read subscribe bytes.
func (script *webSocketScript) recordObserved(data []byte) {
	script.ObservedMutex.Lock()
	defer script.ObservedMutex.Unlock()
	script.Observed = append(script.Observed, append([]byte(nil), data...))
}

// enqueueOutgoing adds one text frame the server will send to the
// client. The connected frame is prepended automatically unless
// script.skipGreeting is true (used by negative tests).
func (script *webSocketScript) enqueueOutgoing(frame []byte) {
	script.outgoingFrames = append(script.outgoingFrames, frame)
}

// startFakeWebSocketServer returns an httptest.Server whose /v1/ws
// endpoint upgrades and runs the given script. Caller must Close
// the server (via t.Cleanup) when the test finishes.
func startFakeWebSocketServer(t *testing.T, script *webSocketScript) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		script.ObservedPath = request.URL.Path
		script.ObservedQuery = request.URL.Query()

		socket, upgradeErr := upgrader.Upgrade(responseWriter, request, nil)
		if upgradeErr != nil {
			t.Errorf("server upgrade: %v", upgradeErr)
			return
		}
		defer func() { _ = socket.Close() }()

		switch {
		case script.badGreeting != nil:
			_ = socket.WriteMessage(websocket.TextMessage, script.badGreeting)
		case !script.skipGreeting:
			greeting, _ := json.Marshal(map[string]any{
				"type": "connected", "market": "IN", "timezone": "Asia/Kolkata",
			})
			_ = socket.WriteMessage(websocket.TextMessage, greeting)
		}

		// Read the subscribe frame if the test opted into recording it.
		_, data, readErr := socket.ReadMessage()
		if readErr == nil {
			script.recordObserved(data)
		}

		// Flush pre-queued outgoing frames.
		for _, frame := range script.outgoingFrames {
			if writeErr := socket.WriteMessage(websocket.TextMessage, frame); writeErr != nil {
				return
			}
		}

		// Hold the connection open until the test tells us to close.
		<-script.HeldReadsDone
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-script.HeldReadsDone:
		default:
			close(script.HeldReadsDone)
		}
	})
	return server
}

func newTestClientForWebSocket(t *testing.T, httpTestServerURL string) *Client {
	t.Helper()
	// httptest.Server's URL is http://..., which must be flipped to ws://
	// for the WS dialer config.
	webSocketBaseURL := strings.Replace(httpTestServerURL, "http://", "ws://", 1)
	config := ClientConfig{
		BaseURL:                httpTestServerURL,
		WebSocketBaseURL:       webSocketBaseURL,
		Timeouts:               validTestTimeouts(),
		MaxIdempotentRetries:   0,
		IdempotentRetryBackoff: 0,
	}
	client, err := NewClient(config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

func TestSubscribeDialsWithSessionQueryAndSendsExpectedSubscribeFrame(t *testing.T) {
	t.Parallel()

	script := newWebSocketScript()
	server := startFakeWebSocketServer(t, script)
	client := newTestClientForWebSocket(t, server.URL)

	subscriptions := []MarketDataSubscription{
		{Stream: MarketDataStreamCandle, Instrument: "INST-A", Timeframe: TimeframeOneMinute},
		{Stream: MarketDataStreamSnapshot, Instrument: "INST-B"},
	}
	connection, connectedFrame, err := client.Subscribe(context.Background(), "sess-abc", subscriptions)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	if connectedFrame.Market != "IN" || connectedFrame.Timezone != "Asia/Kolkata" {
		t.Fatalf("ConnectedFrame = %+v", connectedFrame)
	}
	if script.ObservedPath != marketDataWebSocketPath {
		t.Fatalf("path = %q, want %q", script.ObservedPath, marketDataWebSocketPath)
	}
	if got := script.ObservedQuery.Get("session"); got != "sess-abc" {
		t.Fatalf("session query = %q", got)
	}
	// Give the server a moment to record the subscribe frame.
	for attempt := 0; attempt < 50; attempt++ {
		script.ObservedMutex.Lock()
		haveSubscribe := len(script.Observed) > 0
		script.ObservedMutex.Unlock()
		if haveSubscribe {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	script.ObservedMutex.Lock()
	defer script.ObservedMutex.Unlock()
	if len(script.Observed) != 1 {
		t.Fatalf("expected 1 subscribe frame, got %d", len(script.Observed))
	}
	var inboundSubscribe outboundSubscribeFrame
	if err := json.Unmarshal(script.Observed[0], &inboundSubscribe); err != nil {
		t.Fatalf("decode observed subscribe: %v", err)
	}
	if inboundSubscribe.Type != "subscribe" {
		t.Fatalf("subscribe.Type = %q", inboundSubscribe.Type)
	}
	if len(inboundSubscribe.Subs) != 2 {
		t.Fatalf("subs len = %d, want 2", len(inboundSubscribe.Subs))
	}
	if inboundSubscribe.Subs[0] != (outboundSubscribeFrameItem{
		Stream: MarketDataStreamCandle, Instrument: "INST-A", Timeframe: TimeframeOneMinute,
	}) {
		t.Fatalf("subs[0] = %+v", inboundSubscribe.Subs[0])
	}
	if inboundSubscribe.Subs[1].Timeframe != "" {
		t.Fatalf("snapshot sub must omit timeframe, got %q", inboundSubscribe.Subs[1].Timeframe)
	}
}

func TestSubscribeDecodesAckSnapshotCandleFrames(t *testing.T) {
	t.Parallel()

	script := newWebSocketScript()
	ackPayload, _ := json.Marshal(map[string]any{
		"type": "ack",
		"results": []map[string]any{
			{"stream": "candle", "instrument": "INST-A", "timeframe": "1m", "status": "ok"},
			{"stream": "snapshot", "instrument": "INST-X", "status": "error", "error": "unknown instrument"},
		},
	})
	snapshotPayload, _ := json.Marshal(map[string]any{
		"type":       "snapshot",
		"instrument": "INST-A",
		"data": map[string]any{
			"phase": "OPEN", "last_price": 25400, "has_last": true, "change_pct": 0.78,
			"quote": map[string]any{
				"best_bid": 25395, "bid_qty": 100, "has_bid": true,
				"best_ask": 25405, "ask_qty": 200, "has_ask": true,
			},
			"depth": map[string]any{
				"bids": []map[string]any{{"price": 25395, "qty": 100}},
				"asks": []map[string]any{{"price": 25405, "qty": 200}},
			},
		},
	})
	candlePayload, _ := json.Marshal(map[string]any{
		"type":       "candle",
		"instrument": "INST-A",
		"timeframe":  "1m",
		"bar": map[string]any{
			"start": "2026-10-17T09:15:00", "end": "2026-10-17T09:16:00",
			"open": 25000, "high": 25100, "low": 24950, "close": 25080, "volume": 1200,
		},
	})
	script.enqueueOutgoing(ackPayload)
	script.enqueueOutgoing(snapshotPayload)
	script.enqueueOutgoing(candlePayload)

	server := startFakeWebSocketServer(t, script)
	client := newTestClientForWebSocket(t, server.URL)

	connection, _, err := client.Subscribe(context.Background(), "sess-abc",
		[]MarketDataSubscription{{Stream: MarketDataStreamSnapshot, Instrument: "INST-A"}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ackFrame, err := connection.ReadFrame(readCtx)
	if err != nil {
		t.Fatalf("ReadFrame ack: %v", err)
	}
	if ackFrame.Kind != IncomingFrameKindAck {
		t.Fatalf("Kind = %q, want %q", ackFrame.Kind, IncomingFrameKindAck)
	}
	if ackFrame.Ack == nil || len(ackFrame.Ack.Results) != 2 {
		t.Fatalf("ack results = %+v", ackFrame.Ack)
	}
	if ackFrame.Ack.Results[0].Status != SubscriptionAckStatusOK {
		t.Fatalf("results[0].Status = %q", ackFrame.Ack.Results[0].Status)
	}
	if ackFrame.Ack.Results[1].Status != SubscriptionAckStatusError {
		t.Fatalf("results[1].Status = %q", ackFrame.Ack.Results[1].Status)
	}
	if ackFrame.Ack.Results[1].Error != "unknown instrument" {
		t.Fatalf("results[1].Error = %q", ackFrame.Ack.Results[1].Error)
	}

	snapshotFrame, err := connection.ReadFrame(readCtx)
	if err != nil {
		t.Fatalf("ReadFrame snapshot: %v", err)
	}
	if snapshotFrame.Kind != IncomingFrameKindSnapshot {
		t.Fatalf("Kind = %q, want %q", snapshotFrame.Kind, IncomingFrameKindSnapshot)
	}
	if snapshotFrame.Snapshot.Instrument != "INST-A" {
		t.Fatalf("Snapshot.Instrument = %q", snapshotFrame.Snapshot.Instrument)
	}
	snapshotData := snapshotFrame.Snapshot.Data
	if snapshotData.Phase != "OPEN" || snapshotData.LastPrice != 25400 || !snapshotData.HasLast {
		t.Fatalf("Snapshot.Data = %+v", snapshotData)
	}
	if snapshotData.ChangePct != 0.78 {
		t.Fatalf("ChangePct = %v", snapshotData.ChangePct)
	}
	if snapshotData.Quote.BestBid != 25395 || snapshotData.Quote.BestAsk != 25405 {
		t.Fatalf("Quote = %+v", snapshotData.Quote)
	}
	if len(snapshotData.Depth.Bids) != 1 || snapshotData.Depth.Bids[0].Price != 25395 {
		t.Fatalf("Depth.Bids = %+v", snapshotData.Depth.Bids)
	}

	candleFrame, err := connection.ReadFrame(readCtx)
	if err != nil {
		t.Fatalf("ReadFrame candle: %v", err)
	}
	if candleFrame.Kind != IncomingFrameKindCandle {
		t.Fatalf("Kind = %q, want %q", candleFrame.Kind, IncomingFrameKindCandle)
	}
	if candleFrame.Candle.Instrument != "INST-A" || candleFrame.Candle.Timeframe != "1m" {
		t.Fatalf("Candle = %+v", candleFrame.Candle)
	}
	if candleFrame.Candle.Bar.Open != 25000 || candleFrame.Candle.Bar.Volume != 1200 {
		t.Fatalf("Bar = %+v", candleFrame.Candle.Bar)
	}
	if candleFrame.Candle.Bar.StartNaiveLocal != "2026-10-17T09:15:00" {
		t.Fatalf("Bar.StartNaiveLocal = %q", candleFrame.Candle.Bar.StartNaiveLocal)
	}
}

func TestSubscribeFailsAndClosesConnectionWhenFirstFrameIsNotConnected(t *testing.T) {
	t.Parallel()
	script := newWebSocketScript()
	script.badGreeting, _ = json.Marshal(map[string]any{"type": "ack", "results": []any{}})
	server := startFakeWebSocketServer(t, script)
	client := newTestClientForWebSocket(t, server.URL)

	_, _, err := client.Subscribe(context.Background(), "sess-abc",
		[]MarketDataSubscription{{Stream: MarketDataStreamSnapshot, Instrument: "INST-A"}})
	if err == nil {
		t.Fatalf("expected error on non-connected first frame")
	}
	if !strings.Contains(err.Error(), "first frame kind") {
		t.Fatalf("expected first-frame error, got %v", err)
	}
}

func TestSubscribePreFlightRejectsMalformedSubscriptions(t *testing.T) {
	t.Parallel()

	script := newWebSocketScript()
	server := startFakeWebSocketServer(t, script)
	client := newTestClientForWebSocket(t, server.URL)

	cases := []struct {
		name              string
		sessionID         string
		subscriptions     []MarketDataSubscription
		wantErrorContains string
	}{
		{
			name:              "empty session id",
			sessionID:         "",
			subscriptions:     []MarketDataSubscription{{Stream: MarketDataStreamSnapshot, Instrument: "X"}},
			wantErrorContains: "non-empty session id",
		},
		{
			name:              "empty subscriptions",
			sessionID:         "sess-abc",
			subscriptions:     nil,
			wantErrorContains: "at least one subscription",
		},
		{
			name:      "empty instrument",
			sessionID: "sess-abc",
			subscriptions: []MarketDataSubscription{
				{Stream: MarketDataStreamSnapshot, Instrument: ""},
			},
			wantErrorContains: "non-empty instrument",
		},
		{
			name:      "unknown stream",
			sessionID: "sess-abc",
			subscriptions: []MarketDataSubscription{
				{Stream: "orderbook", Instrument: "X"},
			},
			wantErrorContains: "stream must be",
		},
		{
			name:      "candle stream missing timeframe",
			sessionID: "sess-abc",
			subscriptions: []MarketDataSubscription{
				{Stream: MarketDataStreamCandle, Instrument: "X"},
			},
			wantErrorContains: "requires a timeframe",
		},
		{
			name:      "candle stream with unknown timeframe",
			sessionID: "sess-abc",
			subscriptions: []MarketDataSubscription{
				{Stream: MarketDataStreamCandle, Instrument: "X", Timeframe: "2m"},
			},
			wantErrorContains: "requires a timeframe",
		},
		{
			name:      "snapshot stream with a timeframe",
			sessionID: "sess-abc",
			subscriptions: []MarketDataSubscription{
				{Stream: MarketDataStreamSnapshot, Instrument: "X", Timeframe: TimeframeOneMinute},
			},
			wantErrorContains: "must not carry a timeframe",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := client.Subscribe(context.Background(), testCase.sessionID, testCase.subscriptions)
			if err == nil {
				t.Fatalf("expected pre-flight error, got nil")
			}
			if !strings.Contains(err.Error(), testCase.wantErrorContains) {
				t.Fatalf("error %q does not contain %q", err.Error(), testCase.wantErrorContains)
			}
		})
	}
}

func TestWebSocketConnectionCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	script := newWebSocketScript()
	server := startFakeWebSocketServer(t, script)
	client := newTestClientForWebSocket(t, server.URL)

	connection, _, err := client.Subscribe(context.Background(), "sess-abc",
		[]MarketDataSubscription{{Stream: MarketDataStreamSnapshot, Instrument: "INST-A"}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := connection.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("third Close: %v", err)
	}

	// A ReadFrame after Close must fail without panicking.
	_, readErr := connection.ReadFrame(context.Background())
	if readErr == nil {
		t.Fatalf("expected error on ReadFrame after Close")
	}
}

func TestReadFrameReturnsCtxErrOnCancellation(t *testing.T) {
	t.Parallel()
	script := newWebSocketScript()
	server := startFakeWebSocketServer(t, script)
	client := newTestClientForWebSocket(t, server.URL)

	connection, _, err := client.Subscribe(context.Background(), "sess-abc",
		[]MarketDataSubscription{{Stream: MarketDataStreamSnapshot, Instrument: "INST-A"}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	readResult := make(chan error, 1)
	go func() {
		_, err := connection.ReadFrame(ctx)
		readResult <- err
	}()

	// Give the goroutine a chance to block on ReadMessage.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-readResult:
		if err == nil {
			t.Fatalf("expected error on cancelled read, got nil")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "closed") {
			t.Fatalf("expected cancellation or close in error chain, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ReadFrame did not return after ctx cancellation")
	}
}

func TestDecodeIncomingFrameSurfacesUnknownKindsAsRaw(t *testing.T) {
	t.Parallel()
	unknownPayload := []byte(`{"type":"future_frame_kind","hello":"world"}`)
	frame, err := decodeIncomingFrame(unknownPayload)
	if err != nil {
		t.Fatalf("decodeIncomingFrame: %v", err)
	}
	if frame.Kind != IncomingFrameKindUnknown {
		t.Fatalf("Kind = %q, want %q", frame.Kind, IncomingFrameKindUnknown)
	}
	if string(frame.Raw) != string(unknownPayload) {
		t.Fatalf("Raw = %q, want original payload preserved", frame.Raw)
	}
}

func TestDecodeIncomingFrameSurfacesBadJSONAsError(t *testing.T) {
	t.Parallel()
	_, err := decodeIncomingFrame([]byte(`{not json}`))
	if err == nil {
		t.Fatalf("expected error on malformed JSON")
	}
}

func TestJoinPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                   string
		base, suffix, wantPath string
	}{
		{"empty base", "", "/v1/ws", "/v1/ws"},
		{"root base", "/", "/v1/ws", "/v1/ws"},
		{"base no slash + suffix leading slash", "/api", "/v1/ws", "/api/v1/ws"},
		{"base trailing slash + suffix leading slash", "/api/", "/v1/ws", "/api/v1/ws"},
		{"base no slash + suffix no leading slash", "/api", "v1/ws", "/api/v1/ws"},
		{"base trailing slash + suffix no leading slash", "/api/", "v1/ws", "/api/v1/ws"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := joinPaths(testCase.base, testCase.suffix)
			if got != testCase.wantPath {
				t.Fatalf("joinPaths(%q,%q) = %q, want %q", testCase.base, testCase.suffix, got, testCase.wantPath)
			}
		})
	}
}

