package core

import (
	"encoding/json"
	"fmt"
)

// Market-data stream names accepted on the subscribe frame. Core
// rejects any other value with a per-sub ack entry whose status is
// "error", so pre-flight rejects them locally too.
const (
	MarketDataStreamCandle   = "candle"
	MarketDataStreamSnapshot = "snapshot"
)

// Per-sub ack status values Core emits in response to a subscribe
// frame. Each subscribe may fan out into multiple ack results — Core
// validates subs individually so a batch can partially fail.
const (
	SubscriptionAckStatusOK    = "ok"
	SubscriptionAckStatusError = "error"
)

// Incoming frame kinds this client decodes on the WS connection.
// IncomingFrameKindUnknown covers the case where a future Core
// release ships a new frame type: the frame is still handed back to
// the caller as Raw, with Kind == IncomingFrameKindUnknown, so a
// forward-compatible consumer can log it rather than crash.
const (
	IncomingFrameKindConnected = "connected"
	IncomingFrameKindAck       = "ack"
	IncomingFrameKindSnapshot  = "snapshot"
	IncomingFrameKindCandle    = "candle"
	IncomingFrameKindUnknown   = "unknown"
)

// ConnectedFrame is the first frame every WebSocket connection
// receives from Core. It establishes the market and the IANA
// timezone every naive-local time in the session will be expressed
// in.
type ConnectedFrame struct {
	Market   string `json:"market"`
	Timezone string `json:"timezone"`
}

// AckResult is one per-sub validation result inside an AckFrame. On
// Status "error", the Error field carries a free-text reason (e.g.
// "unknown instrument"); on Status "ok" it is empty.
type AckResult struct {
	Stream     string `json:"stream"`
	Instrument string `json:"instrument"`
	Timeframe  string `json:"timeframe,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

// AckFrame is the response Core emits after processing one subscribe
// batch. One result per sub in the originating batch, in input order.
type AckFrame struct {
	Results []AckResult `json:"results"`
}

// MarketDataQuote is the top-of-book slice of a snapshot frame.
// HasBid / HasAsk gate the sided fields — a book with no bids has
// HasBid false and BestBid/BidQty must be ignored rather than treated
// as zero prices.
type MarketDataQuote struct {
	BestBid int64 `json:"best_bid"`
	BidQty  int64 `json:"bid_qty"`
	HasBid  bool  `json:"has_bid"`
	BestAsk int64 `json:"best_ask"`
	AskQty  int64 `json:"ask_qty"`
	HasAsk  bool  `json:"has_ask"`
}

// MarketDataDepthLevel is one price-level row inside the Depth
// slices. Price is integer ticks; Qty is the aggregated resting
// quantity at that price.
type MarketDataDepthLevel struct {
	Price int64 `json:"price"`
	Qty   int64 `json:"qty"`
}

// MarketDataDepth is the ladder sliced into bid and ask sides. Both
// sides may be empty.
type MarketDataDepth struct {
	Bids []MarketDataDepthLevel `json:"bids"`
	Asks []MarketDataDepthLevel `json:"asks"`
}

// SnapshotFrameData is the body of a snapshot frame — the whole
// picture Core re-emits on any field change. HasLast gates LastPrice
// and ChangePct; while HasLast is false the instrument has not yet
// printed a trade on the tape and LastPrice / ChangePct ship as
// zero.
//
// ChangePct is the one float emitted on the wire — a display
// percentage, not a price. All price and quantity fields stay int64.
type SnapshotFrameData struct {
	Phase     string          `json:"phase"`
	LastPrice int64           `json:"last_price"`
	HasLast   bool            `json:"has_last"`
	ChangePct float64         `json:"change_pct"`
	Quote     MarketDataQuote `json:"quote"`
	Depth     MarketDataDepth `json:"depth"`
}

// SnapshotFrame is one snapshot push for one instrument. Core emits
// a snapshot every time any field under Data changes (whole-picture
// re-emit, not deltas), so a consumer can throw away the previous
// snapshot without reconciling.
type SnapshotFrame struct {
	Instrument string            `json:"instrument"`
	Data       SnapshotFrameData `json:"data"`
}

// MarketDataBar is one finalized bar on the candle stream. Start and
// End are naive-local strings in the connection's declared timezone
// — the ConnectedFrame.Timezone carried on the first frame is the
// authority for parsing them.
type MarketDataBar struct {
	StartNaiveLocal string `json:"start"`
	EndNaiveLocal   string `json:"end"`
	Open            int64  `json:"open"`
	High            int64  `json:"high"`
	Low             int64  `json:"low"`
	Close           int64  `json:"close"`
	Volume          int64  `json:"volume"`
}

// CandleStreamFrame is one finalized-bar push on a candle stream.
// Core only publishes finalized bars; the live movement of the
// currently-forming bar rides SnapshotFrameData.LastPrice.
type CandleStreamFrame struct {
	Instrument string        `json:"instrument"`
	Timeframe  string        `json:"timeframe"`
	Bar        MarketDataBar `json:"bar"`
}

// IncomingFrame is the tagged union callers receive from
// WebSocketConnection.ReadFrame. Exactly one of Ack / Snapshot /
// Candle is non-nil for the matching Kind; for Kind
// IncomingFrameKindUnknown all three are nil and Raw carries the
// undecoded bytes so a consumer can log them or decode against a
// future schema.
//
// Raw is always populated for every Kind, which lets a caller forward
// the frame verbatim (for example to Redis) without re-marshaling.
type IncomingFrame struct {
	Kind     string
	Ack      *AckFrame
	Snapshot *SnapshotFrame
	Candle   *CandleStreamFrame
	Raw      json.RawMessage
}

// decodeIncomingFrame parses one text frame off the socket, dispatches
// on the "type" field, and returns the typed decode plus the raw bytes.
// Unknown frame types are not an error — they come back with Kind
// IncomingFrameKindUnknown and Raw set, so a forward-compatible caller
// can still see them.
func decodeIncomingFrame(data []byte) (IncomingFrame, error) {
	var kindEnvelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &kindEnvelope); err != nil {
		return IncomingFrame{}, fmt.Errorf("core: decode WS frame envelope: %w", err)
	}
	frame := IncomingFrame{Raw: append(json.RawMessage(nil), data...)}
	switch kindEnvelope.Type {
	case IncomingFrameKindConnected:
		frame.Kind = IncomingFrameKindConnected
		return frame, nil
	case IncomingFrameKindAck:
		var ack AckFrame
		if err := json.Unmarshal(data, &ack); err != nil {
			return IncomingFrame{}, fmt.Errorf("core: decode WS ack frame: %w", err)
		}
		frame.Kind = IncomingFrameKindAck
		frame.Ack = &ack
		return frame, nil
	case IncomingFrameKindSnapshot:
		var snapshot SnapshotFrame
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return IncomingFrame{}, fmt.Errorf("core: decode WS snapshot frame: %w", err)
		}
		frame.Kind = IncomingFrameKindSnapshot
		frame.Snapshot = &snapshot
		return frame, nil
	case IncomingFrameKindCandle:
		var candle CandleStreamFrame
		if err := json.Unmarshal(data, &candle); err != nil {
			return IncomingFrame{}, fmt.Errorf("core: decode WS candle frame: %w", err)
		}
		frame.Kind = IncomingFrameKindCandle
		frame.Candle = &candle
		return frame, nil
	default:
		frame.Kind = IncomingFrameKindUnknown
		return frame, nil
	}
}
