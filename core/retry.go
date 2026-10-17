package core

import (
	"context"
	"errors"
	"time"
)

// executeIdempotentJSONCall is the retry-wrapping variant of
// executeJSONCall. It performs up to MaxIdempotentRetries additional
// attempts beyond the first when the previous attempt failed with a
// retryable condition — a 5xx response or a transport-level failure
// that was not caused by the outer context expiring.
//
// Writes (session join, order punch) do NOT go through this path.
// They stay on executeJSONCall directly, because a slow success on
// Core's side is indistinguishable from a timed-out failure for a
// write and the caller must reconcile via a fresh read instead of
// risking a duplicate action.
//
// Backoff between attempts is linear (IdempotentRetryBackoff). The
// outer context is honored during backoff waits — a caller that
// cancels mid-retry sees the previous attempt's error returned
// immediately rather than waiting for the backoff to elapse.
func (client *Client) executeIdempotentJSONCall(
	ctx context.Context,
	class callClass,
	httpMethod string,
	requestPath string,
	requestBody any,
	responseOut any,
) error {
	maxAttempts := client.config.MaxIdempotentRetries + 1
	var lastErr error
	for attemptIndex := 0; attemptIndex < maxAttempts; attemptIndex++ {
		if attemptIndex > 0 {
			if err := waitBeforeRetry(ctx, client.config.IdempotentRetryBackoff); err != nil {
				return lastErr
			}
		}
		lastErr = client.executeJSONCall(ctx, class, httpMethod, requestPath, requestBody, responseOut)
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			// The outer context expired; retrying would hit the same
			// cancellation immediately. Surface the latest error so
			// the caller sees why the final attempt failed.
			return lastErr
		}
		if !isIdempotentRetryable(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

// isIdempotentRetryable decides whether a failed attempt is worth
// trying again under the idempotent-retry policy.
//
// A CallError is retryable only if its HTTPStatus is 5xx — 4xx carries
// a stable business reject that will reproduce verbatim on the next
// try, so retrying it is wasted work.
//
// A non-CallError means the call did not reach a response at all —
// a dial failure, TLS handshake error, premature EOF, or a per-attempt
// deadline expiring. These are all plausibly transient at the network
// level. The outer-context check inside executeIdempotentJSONCall
// ensures a caller cancellation is not confused for a per-attempt
// timeout here.
func isIdempotentRetryable(err error) bool {
	var callError *CallError
	if errors.As(err, &callError) {
		return callError.HTTPStatus >= 500
	}
	return true
}

// waitBeforeRetry blocks for the configured backoff duration, aborting
// early if the outer context is cancelled. It returns the context's
// error on early cancellation and nil when the full backoff elapsed.
func waitBeforeRetry(ctx context.Context, backoff time.Duration) error {
	if backoff <= 0 {
		return nil
	}
	backoffTimer := time.NewTimer(backoff)
	defer backoffTimer.Stop()
	select {
	case <-backoffTimer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
