package core

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ClientConfig is the full set of knobs the Core client needs at
// construction time. The composition root fills it in; the client reads
// no environment variables itself. Build it once at startup and pass it
// to NewClient; the Client takes a copy and does not retain a reference
// to the original.
type ClientConfig struct {
	// BaseURL is the http(s) origin of the Core process. Every
	// request-response call this client makes is a path appended to this
	// origin.
	BaseURL string

	// WebSocketBaseURL is the ws(s) origin for the market-data subscribe
	// path. It is held separately from BaseURL because real deployments
	// may terminate TLS or route the two protocols differently — for
	// example, HTTP via a reverse proxy and WebSocket direct to the Core
	// process.
	WebSocketBaseURL string

	// Timeouts bounds each class of HTTP call independently. A single
	// global timeout would either starve the hot-path observation read
	// or underpress the slower pre-open setup calls, so each class
	// carries its own budget.
	Timeouts TimeoutConfig

	// MaxIdempotentRetries caps retries for calls that are safe to
	// repeat — pure reads and the get-or-create account open. Writes
	// (session join, order punch) are never retried regardless of this
	// value, because a slow success on Core's side is indistinguishable
	// from a timed-out failure and the caller must reconcile via a
	// fresh read instead.
	MaxIdempotentRetries int

	// IdempotentRetryBackoff is the base delay inserted between retry
	// attempts. Kept linear and small because the number of attempts is
	// bounded and the surface area is narrow.
	IdempotentRetryBackoff time.Duration

	// HTTPClient is an optional transport injection for tests that want
	// to control dialing or wrap the round-tripper. When nil, the Core
	// client constructs a pooled transport sized for this process.
	HTTPClient *http.Client
}

// TimeoutConfig sizes each HTTP call class separately. Three classes
// because this process makes three qualitatively different kinds of call
// to Core: slow pre-open setup, tight live reads during trading, and
// fire-and-forget order punches whose response body is discarded.
type TimeoutConfig struct {
	// PreOpenCall bounds the slower setup calls — opening an agent
	// account, reading funded trading cash, joining a canonical session,
	// and pulling candle history before go-live. These tolerate
	// noticeable slack because they run outside the trading loop.
	PreOpenCall time.Duration

	// HotPathCall bounds the fresh observation read a running agent
	// issues during trading. It must stay short so a slow Core cannot
	// stall the per-agent decision loop.
	HotPathCall time.Duration

	// FireAndForgetCall bounds order submission. The response body is
	// drained and discarded (the caller reconciles outcomes via a fresh
	// observation read), but the submission itself is still bounded so
	// a slow Core cannot block the trading loop.
	FireAndForgetCall time.Duration
}

// Validate returns the first problem it finds with the configuration,
// or nil if everything is well-formed. NewClient calls it; the
// composition root may also call it directly so startup fails fast on
// a bad config.
func (config ClientConfig) Validate() error {
	if err := validateHTTPBaseURL(config.BaseURL); err != nil {
		return err
	}
	if err := validateWebSocketBaseURL(config.WebSocketBaseURL); err != nil {
		return err
	}
	if config.Timeouts.PreOpenCall <= 0 {
		return errors.New("core: Timeouts.PreOpenCall must be > 0")
	}
	if config.Timeouts.HotPathCall <= 0 {
		return errors.New("core: Timeouts.HotPathCall must be > 0")
	}
	if config.Timeouts.FireAndForgetCall <= 0 {
		return errors.New("core: Timeouts.FireAndForgetCall must be > 0")
	}
	if config.MaxIdempotentRetries < 0 {
		return errors.New("core: MaxIdempotentRetries must be >= 0")
	}
	if config.MaxIdempotentRetries > 0 && config.IdempotentRetryBackoff <= 0 {
		return errors.New("core: IdempotentRetryBackoff must be > 0 when retries are enabled")
	}
	return nil
}

func validateHTTPBaseURL(rawBaseURL string) error {
	if rawBaseURL == "" {
		return errors.New("core: BaseURL is empty")
	}
	parsedBaseURL, err := url.Parse(rawBaseURL)
	if err != nil {
		return fmt.Errorf("core: BaseURL parse: %w", err)
	}
	if parsedBaseURL.Scheme != "http" && parsedBaseURL.Scheme != "https" {
		return fmt.Errorf("core: BaseURL scheme must be http or https, got %q", parsedBaseURL.Scheme)
	}
	if parsedBaseURL.Host == "" {
		return errors.New("core: BaseURL host is empty")
	}
	return nil
}

func validateWebSocketBaseURL(rawWebSocketBaseURL string) error {
	if rawWebSocketBaseURL == "" {
		return errors.New("core: WebSocketBaseURL is empty")
	}
	parsedWebSocketBaseURL, err := url.Parse(rawWebSocketBaseURL)
	if err != nil {
		return fmt.Errorf("core: WebSocketBaseURL parse: %w", err)
	}
	if parsedWebSocketBaseURL.Scheme != "ws" && parsedWebSocketBaseURL.Scheme != "wss" {
		return fmt.Errorf("core: WebSocketBaseURL scheme must be ws or wss, got %q", parsedWebSocketBaseURL.Scheme)
	}
	if parsedWebSocketBaseURL.Host == "" {
		return errors.New("core: WebSocketBaseURL host is empty")
	}
	return nil
}
