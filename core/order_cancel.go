package core

import (
	"context"
	"net/http"
)

const orderCancelEndpointPath = "/v1/orders/cancel"

type orderCancelRequest struct {
	AccountID string `json:"account_id"`
	OrderID   string `json:"order_id"`
}

// CancelOrder sends a cancel request to Core for a resting order owned
// by accountID. Core returns 202 meaning "accepted, not yet executed"
// — the exchange processes the cancel asynchronously and the outcome
// (Cancelled / CancelRejected) arrives via settlement.
//
// Business-reject 409s (not_cancellable, unknown_order, cancel_pending)
// are returned as a *CallError so callers can branch on Reason.
// The seeder treats these as "order already gone" and proceeds to place
// a fresh order.
func (client *Client) CancelOrder(ctx context.Context, accountID, orderID string) error {
	return client.executeJSONCall(
		ctx,
		fireAndForgetCall,
		http.MethodPost,
		orderCancelEndpointPath,
		orderCancelRequest{AccountID: accountID, OrderID: orderID},
		nil,
	)
}
