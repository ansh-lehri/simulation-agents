package core

import (
	"context"
	"fmt"
	"net/http"
)

// openAccountEndpointPath is the literal Core path for the account-open
// call. Kept as a package-level constant so test assertions and the
// implementation reference the same string.
const openAccountEndpointPath = "/v1/account/open"

// openAccountRequestEnvelope is the JSON body shape POST
// /v1/account/open expects. Email is always sent empty because an
// agent's identity on the Core side is its user_id string; email is a
// legacy field required on the wire by the shared account-open contract
// used by human sign-up. owner_type is always "agent" — this client
// represents only agents; Core defaults an omitted owner_type to
// "human" for its other (frontend signup/login) caller on this same
// endpoint.
type openAccountRequestEnvelope struct {
	User openAccountRequestUser `json:"user"`
}

type openAccountRequestUser struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	OwnerType string `json:"owner_type"`
}

// openAccountResponseEnvelope is the JSON body shape POST
// /v1/account/open returns. Only account_id is populated; the account's
// funded trading cash lives on a separate GET and is fetched later
// through GetAccountCash. The private name keeps callers of OpenAccount
// focused on the single scalar they actually need.
type openAccountResponseEnvelope struct {
	AccountID string `json:"account_id"`
}

// OpenAccount opens (or re-opens, idempotently) an account on Core for
// the agent identified by agentUserID. The returned accountID is the
// opaque capital handle this process stores against the agent and
// supplies as account_id on every later call.
//
// The underlying Core endpoint is get-or-create keyed on agentUserID,
// so a repeated call with the same user id is safe and returns the
// same handle. That property is why OpenAccount runs through the
// idempotent-retry path: a 5xx or a transport blip can be retried
// without risking a duplicate account.
//
// Classed as a pre-open call: it runs during the bring-up spine, well
// before trading begins, and tolerates the slack configured in
// TimeoutConfig.PreOpenCall.
func (client *Client) OpenAccount(ctx context.Context, agentUserID string) (string, error) {
	if agentUserID == "" {
		return "", fmt.Errorf("core: OpenAccount requires a non-empty agent user id")
	}
	requestBody := openAccountRequestEnvelope{
		User: openAccountRequestUser{UserID: agentUserID, Email: "", OwnerType: "agent"},
	}
	var responseBody openAccountResponseEnvelope
	if err := client.executeIdempotentJSONCall(
		ctx,
		preOpenCall,
		http.MethodPost,
		openAccountEndpointPath,
		requestBody,
		&responseBody,
	); err != nil {
		return "", err
	}
	if responseBody.AccountID == "" {
		// Core promises account_id is always populated on 2xx; an
		// empty one is a contract break on the server side, not a
		// business reject, so surface it as a plain error rather than
		// a CallError.
		return "", fmt.Errorf(
			"core: OpenAccount for user_id %q returned 2xx with empty account_id",
			agentUserID,
		)
	}
	return responseBody.AccountID, nil
}
