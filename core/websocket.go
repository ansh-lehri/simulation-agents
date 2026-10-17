package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// marketDataWebSocketPath is the literal Core path for the live
// market-data WebSocket upgrade.
const marketDataWebSocketPath = "/v1/ws"

// subscribeWriteBudget is the per-call deadline this client applies
// to writing the subscribe frame onto the socket. The subscribe is
// one small text frame; a slow write is a sign the connection has
// gone bad and should fail fast rather than block the setup path.
const subscribeWriteBudget = 5 * time.Second

// MarketDataSubscription is one subscription request on the live
// WebSocket feed. Stream must be MarketDataStreamCandle or
// MarketDataStreamSnapshot. Timeframe is required when Stream is
// MarketDataStreamCandle and must match one of the Timeframe*
// constants; it is required to be empty when Stream is
// MarketDataStreamSnapshot (the snapshot has no timeframe concept).
type MarketDataSubscription struct {
	Stream     string
	Instrument string
	Timeframe  string
}

// WebSocketConnection is a live market-data WebSocket owned by the
// caller after Subscribe returns. The caller drives the read loop
// via ReadFrame and must call Close exactly once when done. The
// client intentionally does not run an ingest loop here — draining
// and routing frames is the orchestrator's job in a later stage.
//
// Safe for concurrent Close with a reader on ReadFrame; the Close
// forces the pending read to return with an error so the reader
// goroutine can exit.
type WebSocketConnection struct {
	socket *websocket.Conn
	closed atomic.Bool
}

// ReadFrame blocks until one frame arrives from Core, decodes it
// against the known frame kinds, and returns the tagged union. If
// ctx carries a deadline it is applied to the underlying socket as a
// read deadline; a cancellation on ctx (no deadline) also forces the
// pending read to return promptly by closing the socket.
//
// On a successful decode the returned IncomingFrame.Raw holds the
// undecoded bytes so a caller can forward them verbatim without
// re-marshaling. On socket close or protocol error the connection
// is left closed and subsequent ReadFrame calls return an error
// immediately.
func (connection *WebSocketConnection) ReadFrame(ctx context.Context) (IncomingFrame, error) {
	if connection.closed.Load() {
		return IncomingFrame{}, errors.New("core: ReadFrame on closed WebSocket connection")
	}
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		_ = connection.socket.SetReadDeadline(deadline)
	} else {
		_ = connection.socket.SetReadDeadline(time.Time{})
	}

	// Guard against ctx cancellation with no deadline: a cancellation
	// here forces the socket closed so the blocking ReadMessage below
	// returns promptly. The stopGoroutine channel keeps the goroutine
	// from leaking on the happy path.
	stopCancellationWatcher := make(chan struct{})
	defer close(stopCancellationWatcher)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.socket.Close()
		case <-stopCancellationWatcher:
		}
	}()

	_, data, readErr := connection.socket.ReadMessage()
	if readErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return IncomingFrame{}, fmt.Errorf("core: ReadFrame cancelled: %w", ctxErr)
		}
		return IncomingFrame{}, fmt.Errorf("core: ReadFrame socket read: %w", readErr)
	}
	return decodeIncomingFrame(data)
}

// Close releases the underlying socket. Safe to call multiple times;
// subsequent calls after the first return nil without touching the
// socket a second time.
func (connection *WebSocketConnection) Close() error {
	if !connection.closed.CompareAndSwap(false, true) {
		return nil
	}
	return connection.socket.Close()
}

// Subscribe dials the market-data WebSocket for sessionID, consumes
// the mandatory ConnectedFrame server greeting, writes exactly one
// subscribe frame carrying the provided subscriptions, and returns
// the connection handle + the ConnectedFrame for the caller to drive.
//
// This client does NOT drain the socket after Subscribe returns.
// The caller (today: the orchestrator in a later stage) owns the
// read loop, the Redis fanout, and reconnect semantics. The scope
// of this method is dial + greeting + initial subscribe only.
//
// Each subscription is pre-flighted locally: unknown Stream, unknown
// Timeframe, Stream=candle without Timeframe, Stream=snapshot WITH
// Timeframe, and empty Instrument are all rejected before the dial
// happens — a malformed sub never reaches Core.
func (client *Client) Subscribe(
	ctx context.Context, sessionID string, subscriptions []MarketDataSubscription,
) (*WebSocketConnection, ConnectedFrame, error) {
	if sessionID == "" {
		return nil, ConnectedFrame{}, fmt.Errorf("core: Subscribe requires a non-empty session id")
	}
	if len(subscriptions) == 0 {
		return nil, ConnectedFrame{}, fmt.Errorf("core: Subscribe requires at least one subscription")
	}
	if err := validateMarketDataSubscriptions(subscriptions); err != nil {
		return nil, ConnectedFrame{}, err
	}

	dialURL, err := client.buildWebSocketDialURL(sessionID)
	if err != nil {
		return nil, ConnectedFrame{}, err
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout: client.config.Timeouts.PreOpenCall,
		Proxy:            http.ProxyFromEnvironment,
	}
	client.logger.Info("core WS dial", "url_host", hostForLog(dialURL), "subs", len(subscriptions))

	socket, _, dialErr := dialer.DialContext(ctx, dialURL, nil)
	if dialErr != nil {
		return nil, ConnectedFrame{}, fmt.Errorf("core: WS dial %s: %w", marketDataWebSocketPath, dialErr)
	}

	// The server opens with the connected frame before accepting any
	// subscribe. Read it first; if the first frame is anything else
	// the connection is in a state this client cannot reason about,
	// so close and fail.
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		_ = socket.SetReadDeadline(deadline)
	}
	_, greetingData, readErr := socket.ReadMessage()
	if readErr != nil {
		_ = socket.Close()
		return nil, ConnectedFrame{}, fmt.Errorf("core: WS read connected frame: %w", readErr)
	}
	greetingFrame, decodeErr := decodeIncomingFrame(greetingData)
	if decodeErr != nil {
		_ = socket.Close()
		return nil, ConnectedFrame{}, decodeErr
	}
	if greetingFrame.Kind != IncomingFrameKindConnected {
		_ = socket.Close()
		return nil, ConnectedFrame{}, fmt.Errorf(
			"core: WS first frame kind = %q, want %q",
			greetingFrame.Kind, IncomingFrameKindConnected,
		)
	}
	var connectedFrame ConnectedFrame
	if err := json.Unmarshal(greetingData, &connectedFrame); err != nil {
		_ = socket.Close()
		return nil, ConnectedFrame{}, fmt.Errorf("core: decode connected frame: %w", err)
	}

	// Write the subscribe batch. One write; the ack frames arrive on
	// the normal ReadFrame path, so this method does not consume them.
	subscribeFrame := buildSubscribeFrame(subscriptions)
	writeDeadline := time.Now().Add(subscribeWriteBudget)
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline && deadline.Before(writeDeadline) {
		writeDeadline = deadline
	}
	_ = socket.SetWriteDeadline(writeDeadline)
	if writeErr := socket.WriteJSON(subscribeFrame); writeErr != nil {
		_ = socket.Close()
		return nil, ConnectedFrame{}, fmt.Errorf("core: WS write subscribe: %w", writeErr)
	}
	// Reset deadlines so subsequent ReadFrame / writes are not stuck
	// with the handshake deadlines.
	_ = socket.SetReadDeadline(time.Time{})
	_ = socket.SetWriteDeadline(time.Time{})

	client.logger.Info(
		"core WS ready",
		"market", connectedFrame.Market,
		"timezone", connectedFrame.Timezone,
		"subs", len(subscriptions),
	)
	return &WebSocketConnection{socket: socket}, connectedFrame, nil
}

// outboundSubscribeFrame is the on-wire shape of the subscribe
// message. Omitempty on Timeframe keeps the snapshot subs clean
// (snapshot has no timeframe).
type outboundSubscribeFrame struct {
	Type string                       `json:"type"`
	Subs []outboundSubscribeFrameItem `json:"subs"`
}

type outboundSubscribeFrameItem struct {
	Stream     string `json:"stream"`
	Instrument string `json:"instrument"`
	Timeframe  string `json:"timeframe,omitempty"`
}

func buildSubscribeFrame(subscriptions []MarketDataSubscription) outboundSubscribeFrame {
	items := make([]outboundSubscribeFrameItem, len(subscriptions))
	for index, subscription := range subscriptions {
		items[index] = outboundSubscribeFrameItem(subscription)
	}
	return outboundSubscribeFrame{Type: "subscribe", Subs: items}
}

// buildWebSocketDialURL concatenates the configured WebSocketBaseURL
// with /v1/ws and the session query. Trailing slash on the base is
// tolerated the same way the HTTP primitive handles BaseURL.
func (client *Client) buildWebSocketDialURL(sessionID string) (string, error) {
	parsedBase, err := url.Parse(client.config.WebSocketBaseURL)
	if err != nil {
		return "", fmt.Errorf("core: parse WebSocketBaseURL: %w", err)
	}
	parsedBase.Path = joinPaths(parsedBase.Path, marketDataWebSocketPath)
	query := parsedBase.Query()
	query.Set("session", sessionID)
	parsedBase.RawQuery = query.Encode()
	return parsedBase.String(), nil
}

// joinPaths concatenates two URL path segments, handling the case
// where either side carries a trailing or leading slash.
func joinPaths(base, suffix string) string {
	switch {
	case base == "" || base == "/":
		return suffix
	case suffix == "":
		return base
	case base[len(base)-1] == '/' && suffix[0] == '/':
		return base + suffix[1:]
	case base[len(base)-1] != '/' && suffix[0] != '/':
		return base + "/" + suffix
	default:
		return base + suffix
	}
}

func validateMarketDataSubscriptions(subscriptions []MarketDataSubscription) error {
	for index, subscription := range subscriptions {
		if subscription.Instrument == "" {
			return fmt.Errorf("core: Subscribe subs[%d] requires a non-empty instrument", index)
		}
		switch subscription.Stream {
		case MarketDataStreamCandle:
			if !isAcceptedCandleTimeframe(subscription.Timeframe) {
				return fmt.Errorf(
					"core: Subscribe subs[%d] stream=%q requires a timeframe in {%q,%q,%q,%q}, got %q",
					index, MarketDataStreamCandle,
					TimeframeOneMinute, TimeframeFiveMinute, TimeframeThirtyMinute, TimeframeSixtyMinute,
					subscription.Timeframe,
				)
			}
		case MarketDataStreamSnapshot:
			if subscription.Timeframe != "" {
				return fmt.Errorf(
					"core: Subscribe subs[%d] stream=%q must not carry a timeframe, got %q",
					index, MarketDataStreamSnapshot, subscription.Timeframe,
				)
			}
		default:
			return fmt.Errorf(
				"core: Subscribe subs[%d] stream must be %q or %q, got %q",
				index, MarketDataStreamCandle, MarketDataStreamSnapshot, subscription.Stream,
			)
		}
	}
	return nil
}
