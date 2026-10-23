package core

import (
	"context"
	"fmt"
	"net/http"
)

// canonicalSessionJoinEndpointPath is the literal Core path for a
// canonical-session join. The /session/* routes are intentionally
// unversioned on Core's side — using the literal path here (rather
// than prefixing /v1) matches the shipped contract.
const canonicalSessionJoinEndpointPath = "/session/canonical"

// CanonicalSessionJoin is the response body returned when an agent's
// account is successfully installed on the shard for a canonical
// market session.
type CanonicalSessionJoin struct {
	// SessionID is the identifier this process will later use when
	// opening a WebSocket to subscribe to the live market feed for
	// this session.
	SessionID string `json:"session_id"`

	// ParticipantID is injected broker-side at order time; this
	// process never passes it on an outbound call (orders carry the
	// agent's account_id instead). The field is kept on the DTO
	// because Core always populates it on 2xx and dropping it from
	// the decoded shape would silently lose data a future caller
	// might need.
	ParticipantID string `json:"participant_id"`

	// Status is the lifecycle flag Core returns on a successful
	// install — "active" on current Core builds. Surfaced verbatim so
	// callers that want to assert on it can.
	Status string `json:"status"`
}

// canonicalSessionJoinRequestEnvelope is the JSON body shape POST
// /session/canonical expects. owner_type is always "agent" — this
// client represents only agents; Core defaults an omitted owner_type
// to "human" for its other (frontend) caller on this same endpoint.
type canonicalSessionJoinRequestEnvelope struct {
	User   canonicalSessionJoinRequestUser `json:"user"`
	Market string                          `json:"market"`
}

type canonicalSessionJoinRequestUser struct {
	UserID    string `json:"user_id"`
	OwnerType string `json:"owner_type"`
}

// JoinCanonical installs an agent's account on the shard serving a
// canonical market session and returns the identifiers tied to that
// install. The returned SessionID is the handle used to open the live
// market-data WebSocket for the session.
//
// This is a write on Core's side — the install mutates server state.
// It therefore does NOT go through the idempotent-retry wrapper: a
// slow success is indistinguishable from a timed-out failure, and
// retrying risks installing twice. Callers that see a transient
// failure (5xx, transport blip) should reconcile by making a fresh
// state read before deciding whether to re-join.
//
// Classed as a pre-open call: it runs during the bring-up spine, well
// before trading begins, and tolerates the slack configured in
// TimeoutConfig.PreOpenCall.
func (client *Client) JoinCanonical(ctx context.Context, agentUserID, marketTag string) (CanonicalSessionJoin, error) {
	if agentUserID == "" {
		return CanonicalSessionJoin{}, fmt.Errorf("core: JoinCanonical requires a non-empty agent user id")
	}
	if marketTag == "" {
		return CanonicalSessionJoin{}, fmt.Errorf("core: JoinCanonical requires a non-empty market tag")
	}
	requestBody := canonicalSessionJoinRequestEnvelope{
		User:   canonicalSessionJoinRequestUser{UserID: agentUserID, OwnerType: "agent"},
		Market: marketTag,
	}
	var responseBody CanonicalSessionJoin
	if err := client.executeJSONCall(
		ctx,
		preOpenCall,
		http.MethodPost,
		canonicalSessionJoinEndpointPath,
		requestBody,
		&responseBody,
	); err != nil {
		return CanonicalSessionJoin{}, err
	}
	return responseBody, nil
}

// onDemandSessionJoinEndpointPath is the literal Core path for a
// per-agent on-demand-session join. Distinct from the on-demand
// session-creation endpoint (POST /session/ondemand) — that one spins
// up a brand-new session; this one attaches one more participant to a
// session that already exists, identified by exact exchange id.
const onDemandSessionJoinEndpointPath = "/session/ondemand/join"

// OnDemandSessionJoin is the response body returned when an agent's
// account is successfully installed on the shard for an on-demand
// market session. Same shape as CanonicalSessionJoin; kept as its own
// type so the two install paths stay independently evolvable.
type OnDemandSessionJoin struct {
	SessionID     string `json:"session_id"`
	ParticipantID string `json:"participant_id"`
	Status        string `json:"status"`
}

// onDemandSessionJoinRequestEnvelope is the JSON body shape POST
// /session/ondemand/join expects. No owner_type field — this client
// is the endpoint's only caller, so Core resolves OwnerAgent itself.
type onDemandSessionJoinRequestEnvelope struct {
	User       onDemandSessionJoinRequestUser `json:"user"`
	ExchangeID string                         `json:"exchange_id"`
}

type onDemandSessionJoinRequestUser struct {
	UserID string `json:"user_id"`
}

// JoinOnDemand installs an agent's account on the shard serving an
// already-live on-demand market session and returns the identifiers
// tied to that install. exchangeID identifies the one on-demand market
// instance a human's StartOnDemand call already created — this call
// never creates a session itself.
//
// Same write/no-retry and pre-open timeout classification as
// JoinCanonical: a slow success is indistinguishable from a timed-out
// failure, so a transient failure is reconciled by a fresh state read,
// never a blind retry.
func (client *Client) JoinOnDemand(ctx context.Context, agentUserID, exchangeID string) (OnDemandSessionJoin, error) {
	if agentUserID == "" {
		return OnDemandSessionJoin{}, fmt.Errorf("core: JoinOnDemand requires a non-empty agent user id")
	}
	if exchangeID == "" {
		return OnDemandSessionJoin{}, fmt.Errorf("core: JoinOnDemand requires a non-empty exchange id")
	}
	requestBody := onDemandSessionJoinRequestEnvelope{
		User:       onDemandSessionJoinRequestUser{UserID: agentUserID},
		ExchangeID: exchangeID,
	}
	var responseBody OnDemandSessionJoin
	if err := client.executeJSONCall(
		ctx,
		preOpenCall,
		http.MethodPost,
		onDemandSessionJoinEndpointPath,
		requestBody,
		&responseBody,
	); err != nil {
		return OnDemandSessionJoin{}, err
	}
	return responseBody, nil
}
