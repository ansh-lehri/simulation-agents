package core

import (
	"context"
	"fmt"
	"net/http"
)

// orderPunchEndpointPath is the literal Core path for order submission.
const orderPunchEndpointPath = "/v1/orders"

// Order sides accepted on the wire. Lower-case string enums are the
// convention Core enforces; sending the wrong case is rejected
// server-side with a 422.
const (
	OrderSideBuy  = "buy"
	OrderSideSell = "sell"
)

// Order types accepted on the wire.
const (
	OrderTypeLimit  = "limit"
	OrderTypeMarket = "market"
)

// Time-in-force values this process will send. The agent process
// deliberately uses only two of the three values Core accepts: "gtc"
// for limit orders (rest on the book) and "ioc" for market orders
// (take-and-done). "fok" is accepted by Core but behaves identically
// to "ioc" there, so it is never sent from here — pre-flight will
// reject it alongside any other unrecognized value.
const (
	TimeInForceGoodTillCancel    = "gtc"
	TimeInForceImmediateOrCancel = "ioc"
)

// OrderPunchRequest is the shape sent to Core on an order submission.
// Fields are JSON-tagged to match the wire exactly. Qty and LimitPrice
// are integer ticks/coins — this process never uses float money.
type OrderPunchRequest struct {
	AccountID  string `json:"account_id"`
	Instrument string `json:"instrument"`
	Side       string `json:"side"`
	Type       string `json:"type"`
	TIF        string `json:"tif"`
	Qty        int64  `json:"qty"`
	// LimitPrice is required > 0 when Type == OrderTypeLimit and must
	// be exactly 0 when Type == OrderTypeMarket. Pre-flight rejects any
	// other combination.
	LimitPrice int64 `json:"limit_price"`
}

// PunchOrder submits an order to Core. Fire-and-forget: Core returns
// 202 with an {"order_id": "..."} body, but this process ignores both;
// the live order's outcome (open / filled / rejected) is read fresh via
// GetObservation on the agent's next decision tick.
//
// Does NOT retry: an order is a write, and a retry on a slow success
// risks a duplicate fill the broker cannot un-do. The call-class
// timeout bounds how long submission may block so a slow Core cannot
// stall the trading loop; callers that see any error here treat the
// order as "status unknown" and reconcile via GetObservation.
//
// Pre-flight validates the request against the write contract. The two
// enforced pairings are:
//   - Type == "limit"  ⇒ TIF must be "gtc" and LimitPrice > 0
//   - Type == "market" ⇒ TIF must be "ioc" and LimitPrice == 0
// Any mismatched pairing (including TIF == "fok") is rejected locally
// without hitting Core.
func (client *Client) PunchOrder(ctx context.Context, request OrderPunchRequest) error {
	if err := validateOrderPunchRequest(request); err != nil {
		return err
	}
	return client.executeJSONCall(
		ctx,
		fireAndForgetCall,
		http.MethodPost,
		orderPunchEndpointPath,
		request,
		nil, // response body intentionally drained-and-discarded
	)
}

// validateOrderPunchRequest enforces the local write contract before
// an order touches the network. Every rejection here is a programming
// bug upstream (an agent built a malformed order); surfacing it as an
// error is cheaper and clearer than letting Core return a 422.
func validateOrderPunchRequest(request OrderPunchRequest) error {
	if request.AccountID == "" {
		return fmt.Errorf("core: PunchOrder requires a non-empty account id")
	}
	if request.Instrument == "" {
		return fmt.Errorf("core: PunchOrder requires a non-empty instrument")
	}
	if request.Qty <= 0 {
		return fmt.Errorf("core: PunchOrder qty must be > 0, got %d", request.Qty)
	}
	if request.Side != OrderSideBuy && request.Side != OrderSideSell {
		return fmt.Errorf(
			"core: PunchOrder side must be %q or %q, got %q",
			OrderSideBuy, OrderSideSell, request.Side,
		)
	}
	switch request.Type {
	case OrderTypeLimit:
		if request.TIF != TimeInForceGoodTillCancel {
			return fmt.Errorf(
				"core: PunchOrder with type=%q requires tif=%q, got %q",
				OrderTypeLimit, TimeInForceGoodTillCancel, request.TIF,
			)
		}
		if request.LimitPrice <= 0 {
			return fmt.Errorf(
				"core: PunchOrder with type=%q requires limit_price > 0, got %d",
				OrderTypeLimit, request.LimitPrice,
			)
		}
	case OrderTypeMarket:
		if request.TIF != TimeInForceImmediateOrCancel {
			return fmt.Errorf(
				"core: PunchOrder with type=%q requires tif=%q, got %q",
				OrderTypeMarket, TimeInForceImmediateOrCancel, request.TIF,
			)
		}
		if request.LimitPrice != 0 {
			return fmt.Errorf(
				"core: PunchOrder with type=%q requires limit_price == 0, got %d",
				OrderTypeMarket, request.LimitPrice,
			)
		}
	default:
		return fmt.Errorf(
			"core: PunchOrder type must be %q or %q, got %q",
			OrderTypeLimit, OrderTypeMarket, request.Type,
		)
	}
	return nil
}
