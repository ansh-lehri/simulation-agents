package core

import (
	"context"
	"fmt"
	"net/http"
)

// seedInventoryEndpointPath is the literal Core path for crediting
// opening inventory onto a liquidity-seeder account with no cash side
// (BROKER_SEED_INVENTORY.md). Used only by L2's own seeder identities
// before they rest sell-side limit orders — a SELL reserves units, not
// cash, and positions can never go negative, so a seller needs
// inventory before it can rest an ask at all.
const seedInventoryEndpointPath = "/v1/account/seed-inventory"

// SeedInventoryRequest carries one (account, instrument, qty, entry
// price) seed. EntryPrice is the cost basis stamped on the minted
// position — the seeder's own chosen level, not a market read.
type SeedInventoryRequest struct {
	AccountID  string
	Instrument string
	Qty        int64
	EntryPrice int64
}

type seedInventoryRequestEnvelope struct {
	AccountID  string `json:"account_id"`
	Instrument string `json:"instrument"`
	Qty        int64  `json:"qty"`
	EntryPrice int64  `json:"entry_price"`
}

// SeedInventoryResponse is the 200 body: the instrument and the
// resulting available (free, unreserved) quantity after the credit.
type SeedInventoryResponse struct {
	Instrument string `json:"instrument"`
	Available  int64  `json:"available"`
}

// SeedInventory mints opening inventory on a liquidity-seeder account
// so it can rest sell-side limit orders without ever having bought
// anything. This is a write — not retried on transport failure or a
// 5xx, same discipline as JoinCanonical/JoinOnDemand/PunchOrder; a
// caller that sees a transient failure reconciles via a fresh read
// before resting the ask, rather than blindly retrying the credit.
func (client *Client) SeedInventory(ctx context.Context, request SeedInventoryRequest) (SeedInventoryResponse, error) {
	if request.AccountID == "" {
		return SeedInventoryResponse{}, fmt.Errorf("core: SeedInventory requires a non-empty account id")
	}
	if request.Instrument == "" {
		return SeedInventoryResponse{}, fmt.Errorf("core: SeedInventory requires a non-empty instrument")
	}
	if request.Qty <= 0 {
		return SeedInventoryResponse{}, fmt.Errorf("core: SeedInventory requires a positive qty, got %d", request.Qty)
	}
	requestBody := seedInventoryRequestEnvelope{
		AccountID:  request.AccountID,
		Instrument: request.Instrument,
		Qty:        request.Qty,
		EntryPrice: request.EntryPrice,
	}
	var responseBody SeedInventoryResponse
	if err := client.executeJSONCall(
		ctx,
		preOpenCall,
		http.MethodPost,
		seedInventoryEndpointPath,
		requestBody,
		&responseBody,
	); err != nil {
		return SeedInventoryResponse{}, err
	}
	return responseBody, nil
}
