package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// callClass tags each outbound HTTP call with the kind of work it is
// doing. The client uses the tag to pick the right timeout from
// TimeoutConfig; it is also folded into every boundary-log line so an
// operator reading the log sees which class of call is slow or failing.
type callClass int

const (
	// preOpenCall covers the slower setup calls that run during
	// bring-up, before trading begins — opening accounts, reading
	// funded cash, joining a session, pulling candle history.
	preOpenCall callClass = iota

	// hotPathCall covers the fresh observation read a running agent
	// issues during trading. Short timeout, must not stall the
	// per-agent decision loop.
	hotPathCall

	// fireAndForgetCall covers order submission, whose response body
	// is drained and discarded. Submission is still bounded so a slow
	// Core cannot block the trading loop.
	fireAndForgetCall
)

func (class callClass) String() string {
	switch class {
	case preOpenCall:
		return "pre_open"
	case hotPathCall:
		return "hot_path"
	case fireAndForgetCall:
		return "fire_and_forget"
	default:
		return "unknown"
	}
}

// errorEnvelopeMaxBytes caps how much of a non-2xx response body the
// client will read when building a CallError. Core errors are tiny JSON
// objects; the cap exists so a misbehaving upstream cannot force the
// client to buffer an unbounded body on an error path.
const errorEnvelopeMaxBytes = 64 * 1024

// errorMessageMaxLength caps the length of the free-text Message
// carried on a CallError, applied only when the body is not JSON (so
// the raw body is kept for forensic context). Prevents a huge
// non-JSON response from bloating the error chain and the log lines
// that render it.
const errorMessageMaxLength = 256

// executeJSONCall is the shared HTTP primitive. Every request-response
// endpoint on Client goes through here so the three cross-cutting
// behaviors — call-class timeout, JSON encode/decode, and error
// envelope decode — are defined exactly once.
//
// requestBody may be nil (GET and other bodyless calls); if non-nil it
// is JSON-encoded and Content-Type is set. responseOut may be nil
// (fire-and-forget), in which case the response body is drained and
// discarded; otherwise it is JSON-decoded into responseOut on a 2xx
// response.
//
// On a non-2xx response, the envelope is read, decoded leniently
// (either "error" or "reason" field may be absent), and returned as a
// *CallError carrying the stable fields a caller can branch on.
// Transport errors (dial failure, deadline exceeded) are wrapped with
// fmt.Errorf so errors.Is on context.DeadlineExceeded still works.
func (client *Client) executeJSONCall(
	ctx context.Context,
	class callClass,
	httpMethod string,
	requestPath string,
	requestBody any,
	responseOut any,
) error {
	boundedCtx, cancelBoundedCtx := client.applyCallClassTimeout(ctx, class)
	defer cancelBoundedCtx()

	requestURL := client.buildRequestURL(requestPath)
	request, err := buildJSONRequest(boundedCtx, httpMethod, requestURL, requestBody)
	if err != nil {
		return err
	}

	startedAt := time.Now()
	response, err := client.httpClient.Do(request)
	elapsed := time.Since(startedAt)
	if err != nil {
		client.logger.Warn("core call transport error",
			"method", httpMethod,
			"path", requestPath,
			"call_class", class.String(),
			"elapsed_ms", elapsed.Milliseconds(),
			"error", err.Error(),
		)
		return fmt.Errorf("core: %s %s transport: %w", httpMethod, requestPath, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		callErr := decodeCallError(response, httpMethod, requestPath)
		client.logger.Warn("core call returned error status",
			"method", httpMethod,
			"path", requestPath,
			"call_class", class.String(),
			"status", callErr.HTTPStatus,
			"reason", callErr.Reason,
			"message", callErr.Message,
			"elapsed_ms", elapsed.Milliseconds(),
		)
		return callErr
	}

	client.logger.Debug("core call ok",
		"method", httpMethod,
		"path", requestPath,
		"call_class", class.String(),
		"status", response.StatusCode,
		"elapsed_ms", elapsed.Milliseconds(),
	)

	if responseOut == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(responseOut); err != nil {
		return fmt.Errorf("core: %s %s decode response: %w", httpMethod, requestPath, err)
	}
	return nil
}

// applyCallClassTimeout wraps ctx with the call-class budget unless the
// caller already supplied a shorter deadline. The returned cancel is
// safe to defer in both cases (it is a no-op when the original ctx is
// returned unchanged).
func (client *Client) applyCallClassTimeout(ctx context.Context, class callClass) (context.Context, context.CancelFunc) {
	budget := client.callClassBudget(class)
	if existingDeadline, hasDeadline := ctx.Deadline(); hasDeadline && time.Until(existingDeadline) <= budget {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, budget)
}

func (client *Client) callClassBudget(class callClass) time.Duration {
	switch class {
	case preOpenCall:
		return client.config.Timeouts.PreOpenCall
	case hotPathCall:
		return client.config.Timeouts.HotPathCall
	case fireAndForgetCall:
		return client.config.Timeouts.FireAndForgetCall
	default:
		// callClass is a private, exhaustive enum; reaching here means
		// a programmer added a new class and forgot this switch.
		panic(fmt.Sprintf("core: unknown call class %d", int(class)))
	}
}

// buildRequestURL concatenates the configured BaseURL with the
// endpoint-local path. BaseURL is allowed to carry a trailing slash;
// path is allowed to omit a leading slash — both are normalized here
// so endpoint code can use the exact literal path the contract names.
func (client *Client) buildRequestURL(requestPath string) string {
	trimmedBase := strings.TrimRight(client.config.BaseURL, "/")
	if !strings.HasPrefix(requestPath, "/") {
		requestPath = "/" + requestPath
	}
	return trimmedBase + requestPath
}

func buildJSONRequest(
	ctx context.Context, httpMethod, requestURL string, requestBody any,
) (*http.Request, error) {
	var bodyReader io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return nil, fmt.Errorf("core: encode request body: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, httpMethod, requestURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("core: build request %s %s: %w", httpMethod, requestURL, err)
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	return request, nil
}

// decodeCallError reads the response body on a non-2xx response and
// extracts the Core error envelope's "error" and "reason" fields.
// Either field may be absent on any given reject (5xx often omits
// both; a 4xx may carry only one), so the decode is lenient: whatever
// is present is carried onto the returned *CallError, whose empty
// string fields reflect that absence. When the body is non-JSON, the
// raw body is retained as the Message (truncated for log sanity) so
// forensic context is not lost.
func decodeCallError(response *http.Response, httpMethod, requestPath string) *CallError {
	callError := &CallError{
		HTTPStatus:  response.StatusCode,
		HTTPMethod:  httpMethod,
		RequestPath: requestPath,
	}
	rawBody, readErr := io.ReadAll(io.LimitReader(response.Body, errorEnvelopeMaxBytes))
	if readErr != nil || len(bytes.TrimSpace(rawBody)) == 0 {
		return callError
	}
	var envelope struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		callError.Message = truncateErrorBodyForLog(string(rawBody))
		return callError
	}
	callError.Message = envelope.Error
	callError.Reason = envelope.Reason
	return callError
}

func truncateErrorBodyForLog(rawBody string) string {
	trimmed := strings.TrimSpace(rawBody)
	if len(trimmed) <= errorMessageMaxLength {
		return trimmed
	}
	return trimmed[:errorMessageMaxLength] + "…"
}
