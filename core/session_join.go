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
// /session/canonical expects.
type canonicalSessionJoinRequestEnvelope struct {
	User   canonicalSessionJoinRequestUser `json:"user"`
	Market string                          `json:"market"`
}

type canonicalSessionJoinRequestUser struct {
	UserID string `json:"user_id"`
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
		User:   canonicalSessionJoinRequestUser{UserID: agentUserID},
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
