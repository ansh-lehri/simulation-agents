package core

import "fmt"

// CallError is the typed error produced for every non-2xx response from
// Core. Callers branch on HTTPStatus and Reason; Message is free-form
// text intended for logs and incident forensics only, never for
// branching.
//
// The split exists because the Core error envelope carries two kinds of
// signal: an "error" string meant for humans, and (on business rejects)
// a snake_case "reason" tag meant for machines. The tag is stable and
// safe to switch on; the free text is not.
type CallError struct {
	// HTTPStatus is the response status returned by Core. Together with
	// Reason it is the only safe thing to branch on.
	HTTPStatus int

	// Reason is the "reason" field extracted from the error envelope.
	// Present on business rejects (for example, an order placed against
	// an account that has not yet installed a session carries
	// ReasonNoSession). Empty when Core did not supply one.
	Reason string

	// Message is the "error" free-text field. Log it; do not branch on
	// it. The string can change between Core releases without warning.
	Message string

	// HTTPMethod and RequestPath identify the call that produced this
	// error, so a single boundary-log line is enough to trace it back
	// to the exact outbound call without correlating against another
	// store.
	HTTPMethod  string
	RequestPath string
}

// Error renders the call error for logs. It intentionally emits the
// stable fields (method, path, status, reason) up front, and the
// free-text message only when there is no stable reason tag that
// already identifies the reject.
func (callError *CallError) Error() string {
	if callError.Reason != "" {
		return fmt.Sprintf(
			"core: %s %s rejected with status %d, reason %q",
			callError.HTTPMethod, callError.RequestPath, callError.HTTPStatus, callError.Reason,
		)
	}
	if callError.Message != "" {
		return fmt.Sprintf(
			"core: %s %s failed with status %d (%s)",
			callError.HTTPMethod, callError.RequestPath, callError.HTTPStatus, callError.Message,
		)
	}
	return fmt.Sprintf(
		"core: %s %s failed with status %d",
		callError.HTTPMethod, callError.RequestPath, callError.HTTPStatus,
	)
}

// Reason tags that appear in the Core error envelope and that this
// process branches on. Only tags we actually consume are declared here;
// the client never manufactures a reason itself. These constants exist
// so switch statements elsewhere in the process do not duplicate the
// literal strings and risk a typo drift from the Core contract.
const (
	// ReasonNoSession is returned when an operation targets an account
	// that has not yet installed a trading session on its shard. Any
	// order punch or observation read against that account will fail
	// with this reason until a successful join call installs the
	// session.
	ReasonNoSession = "no_session"

	// ReasonInsufficientFunds is returned when the broker rejects an
	// operation because the account cannot cover the required capital.
	ReasonInsufficientFunds = "insufficient_funds"

	// ReasonBadQty is returned when an order quantity violates the
	// instrument's lot or tick constraints enforced on Core's side.
	ReasonBadQty = "bad_qty"

	// ReasonNoMarketPrice is returned when a market order lands on an
	// instrument that has not yet printed a trade in the current
	// session, so no reference price exists to clear against.
	ReasonNoMarketPrice = "no_market_price"
)
