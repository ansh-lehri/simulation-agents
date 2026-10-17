package core

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// observationEndpointPath is the literal Core path for the fresh
// own-state read a running agent uses to see its live balance,
// positions, open orders, and recent closed trades.
const observationEndpointPath = "/v1/account/observation"

// Order status values emitted by Core on the observation read. The
// slice returned under "orders" only contains working orders —
// terminal orders are removed from the shard's track at settlement —
// so a well-behaved stream will show Working or PartiallyFilled.
// OrderStatusUnknown appears only when Core sees an out-of-range
// internal enum and chooses to make the bug loud in the response body
// rather than silently stringify to a plausible value; callers that
// see it should log and move on, not branch on business meaning.
const (
	OrderStatusWorking         = "working"
	OrderStatusPartiallyFilled = "partially_filled"
	OrderStatusFilled          = "filled"
	OrderStatusCancelled       = "cancelled"
	OrderStatusRejected        = "rejected"
	OrderStatusUnknown         = "unknown"
)

// Trade direction values emitted by Core on recent-trade rows. The
// direction captures the position-side the trade closed, which is
// distinct from the order side that opened it — a short trade is
// opened by a sell and closed by a buy, so neither OrderSideBuy nor
// OrderSideSell is a correct substitute here.
const (
	TradeDirectionLong  = "long"
	TradeDirectionShort = "short"
)

// AccountBalance is the "balance" sub-object of the observation
// response. All amounts are integer coins — the Core contract never
// emits float money, and this process holds the same discipline
// end-to-end. Available may be negative on an account carrying debt.
type AccountBalance struct {
	// Available is the cash available to back new orders.
	Available int64 `json:"available"`

	// Reserved is the cash held against open orders that have not
	// yet been filled or cancelled.
	Reserved int64 `json:"reserved"`

	// Settled is cash from fills that has settled into the account.
	Settled int64 `json:"settled"`

	// Bank is the account's cash balance held outside the trading
	// venue, mirrored from the broker.
	Bank int64 `json:"bank"`
}

// Position is one holding on the agent's account at the moment of the
// observation read.
//
// MarkAvailable gates the price-dependent fields: the market-price
// cache misses until the instrument has traded on a wired instance,
// and while it misses LTP and GrossUnrealized ship as zero. Callers
// that want to show "unknown" instead of 0 should branch on
// MarkAvailable rather than on the zero itself.
//
// WeightedAvgEntry is display-only — it is a quantity-weighted average
// across the open lots and does not drive realization math (Core's
// realization basis is FIFO, decided server-side).
type Position struct {
	Instrument       string `json:"instrument"`
	Qty              int64  `json:"qty"`
	ReservedQty      int64  `json:"reserved_qty"`
	WeightedAvgEntry int64  `json:"weighted_avg_entry"`
	MarkAvailable    bool   `json:"mark_available"`
	LTP              int64  `json:"ltp"`
	GrossUnrealized  int64  `json:"gross_unrealized"`
}

// OpenOrder is one working order (status Working or PartiallyFilled)
// on the agent's account at the moment of the observation read.
// Terminal orders (Filled / Cancelled / Rejected) are already filtered
// out server-side.
//
// FilledQty is computed server-side as OrigQty − RemainingQty — Core
// does not carry a derived quantity on its internal track and derives
// it only at the response edge. ReservedCash and ReservedUnits are
// side-exclusive: a buy order reserves cash and leaves ReservedUnits
// at 0, a sell order reserves units and leaves ReservedCash at 0.
// LimitPrice is 0 exactly when IsMarket is true.
type OpenOrder struct {
	OrderID        string `json:"order_id"`
	Instrument     string `json:"instrument"`
	Side           string `json:"side"`
	IsMarket       bool   `json:"is_market"`
	LimitPrice     int64  `json:"limit_price"`
	OrigQty        int64  `json:"orig_qty"`
	RemainingQty   int64  `json:"remaining_qty"`
	FilledQty      int64  `json:"filled_qty"`
	ReservedCash   int64  `json:"reserved_cash"`
	ReservedUnits  int64  `json:"reserved_units"`
	Status         string `json:"status"`
	CancelInFlight bool   `json:"cancel_in_flight"`
}

// RecentTrade is one closed trade row from the durable store, merged
// into the observation response alongside the live own-state. Core
// caps the slice length server-side; the agent process gets a bounded
// tail of history without a separate call.
//
// EntryAt and ExitAt arrive as timezone-carrying ISO strings on the
// wire and decode into time.Time via the standard json package —
// Core's server-side type is time.Time and Go's default marshaling
// (RFC3339-nano shape) is what hits the wire.
//
// PnlPct is a display-only percentage expressed as a float. Every
// money field (GrossPnl, TotalCosts, NetPnl, EntryPrice, ExitPrice)
// stays int64. GrossPnl and NetPnl are signed; TotalCosts is
// non-negative.
type RecentTrade struct {
	TradeID    string    `json:"trade_id"`
	Instrument string    `json:"instrument"`
	Direction  string    `json:"direction"`
	Quantity   int64     `json:"quantity"`
	EntryPrice int64     `json:"entry_price"`
	ExitPrice  int64     `json:"exit_price"`
	EntryAt    time.Time `json:"entry_at"`
	ExitAt     time.Time `json:"exit_at"`
	GrossPnl   int64     `json:"gross_pnl"`
	TotalCosts int64     `json:"total_costs"`
	NetPnl     int64     `json:"net_pnl"`
	PnlPct     float64   `json:"pnl_pct"`
	ExitReason string    `json:"exit_reason"`
}

// AccountObservation is the full response of GET
// /v1/account/observation — the live balance / positions / open
// orders assembled in one shard visit, merged with a tail of recent
// closed trades.
//
// TradesDegraded indicates that the recent-trades leg failed on
// Core's side (durable store blip) and RecentTrades is empty as a
// result; the live own-state (balance / positions / orders) is still
// authoritative in that case. Agents that need trade history on a
// degraded tick should degrade their reasoning accordingly rather
// than assume no trades happened.
type AccountObservation struct {
	Balance        AccountBalance `json:"balance"`
	Positions      []Position     `json:"positions"`
	Orders         []OpenOrder    `json:"orders"`
	RecentTrades   []RecentTrade  `json:"recent_trades"`
	TradesDegraded bool           `json:"trades_degraded"`
}

// GetObservation reads the agent's live own-state from Core: cash
// balance split into available / reserved / settled / bank, plus
// Positions, Orders (working only), RecentTrades (durable tail), and
// a trades_degraded flag. This is the hot path a running agent uses
// to decide its next action, so its budget is HotPathCall — short on
// purpose.
//
// Idempotent (pure read); runs through the retry wrapper so a
// transient 5xx or network blip does not kill the current tick.
//
// 409 with reason "no_session" is the common business reject here —
// the agent's account has not yet installed a session on the shard.
// Callers that observe this reconcile by re-joining and retrying on a
// later tick rather than looping tightly here.
func (client *Client) GetObservation(ctx context.Context, accountID string) (AccountObservation, error) {
	if accountID == "" {
		return AccountObservation{}, fmt.Errorf("core: GetObservation requires a non-empty account id")
	}
	queryParameters := url.Values{}
	queryParameters.Set("account_id", accountID)
	requestPath := observationEndpointPath + "?" + queryParameters.Encode()

	var responseBody AccountObservation
	if err := client.executeIdempotentJSONCall(
		ctx,
		hotPathCall,
		http.MethodGet,
		requestPath,
		nil,
		&responseBody,
	); err != nil {
		return AccountObservation{}, err
	}
	// Normalize collections so callers can range over them even on a
	// malformed upstream that emits null in place of an empty array.
	if responseBody.Positions == nil {
		responseBody.Positions = []Position{}
	}
	if responseBody.Orders == nil {
		responseBody.Orders = []OpenOrder{}
	}
	if responseBody.RecentTrades == nil {
		responseBody.RecentTrades = []RecentTrade{}
	}
	return responseBody, nil
}
