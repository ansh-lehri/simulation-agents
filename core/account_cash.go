package core

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// accountCashEndpointPath is the literal Core path for the funded-cash
// read.
const accountCashEndpointPath = "/v1/account/cash"

// AccountCash is the response body of GET /v1/account/cash. Both
// amounts are integer coins — Core never emits a float for money, and
// this process holds the same discipline end-to-end.
type AccountCash struct {
	// TradingCash is the funded cash available for trading on this
	// account. It is the ceiling for the per-session trading capital
	// drawn for an agent during bring-up: the agent's session capital
	// is a bounded fraction of TradingCash.
	TradingCash int64 `json:"trading_cash"`

	// BankCash is the account's cash balance held outside the trading
	// venue. Not consumed by this process today; the field is kept
	// because the Core contract always populates it on 2xx and dropping
	// it from the DTO would silently lose data if a later caller ever
	// needs it.
	BankCash int64 `json:"bank_cash"`
}

// GetAccountCash reads the durable cash balances for the account
// identified by accountID. This is a pre-session read — it reports the
// funded cash the broker has recorded for the account, independent of
// any live trading activity. For live, in-session balances (available
// vs reserved, running P&L) use GetObservation instead.
//
// Classed as a pre-open call. Idempotent (pure read), so it runs
// through the retry wrapper and tolerates transient 5xx responses.
func (client *Client) GetAccountCash(ctx context.Context, accountID string) (AccountCash, error) {
	if accountID == "" {
		return AccountCash{}, fmt.Errorf("core: GetAccountCash requires a non-empty account id")
	}
	queryParameters := url.Values{}
	queryParameters.Set("account_id", accountID)
	requestPath := accountCashEndpointPath + "?" + queryParameters.Encode()

	var responseBody AccountCash
	if err := client.executeIdempotentJSONCall(
		ctx,
		preOpenCall,
		http.MethodGet,
		requestPath,
		nil,
		&responseBody,
	); err != nil {
		return AccountCash{}, err
	}
	return responseBody, nil
}
