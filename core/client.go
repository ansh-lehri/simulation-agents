package core

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// Transport pool sizing for the default HTTP client. Chosen for this
// process's expected concurrency profile — a few dozen agents each
// making one pre-open call during setup and then a short hot-path read
// loop during trading. The values keep connection reuse healthy without
// hoarding sockets.
const (
	defaultMaxIdleConnections        = 128
	defaultMaxIdleConnectionsPerHost = 64
	defaultIdleConnectionTimeout     = 90 * time.Second
	defaultDialTimeout               = 5 * time.Second
	defaultTLSHandshakeTimeout       = 5 * time.Second
	defaultKeepAlive                 = 30 * time.Second
)

// Client is this process's single outbound surface to Core. It owns a
// pooled HTTP transport used for every request-response call, and will
// later own the per-exchange WebSocket connections used for live market
// data. Build one at process startup and share it across goroutines;
// it is safe for concurrent use.
type Client struct {
	config     ClientConfig
	httpClient *http.Client
	logger     *slog.Logger
	closed     atomic.Bool
}

// NewClient validates the configuration and returns a Client ready to
// make calls. The logger must be non-nil because every call this client
// makes produces a boundary-log line; a nil logger would silently drop
// that trail and is treated as a composition-root invariant violation,
// surfaced as an error so the process fails at startup rather than on
// the first call.
func NewClient(config ClientConfig, logger *slog.Logger) (*Client, error) {
	if logger == nil {
		return nil, errors.New("core: NewClient requires a non-nil logger")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Transport: newPooledHTTPTransport(),
			// No client-level Timeout here on purpose: per-call deadlines
			// are applied via the request context, sized by call class,
			// so one client can serve pre-open, hot-path, and
			// fire-and-forget calls with their own distinct budgets.
		}
	}
	logger.Info(
		"core client ready",
		"base_url_host", hostForLog(config.BaseURL),
		"websocket_base_url_host", hostForLog(config.WebSocketBaseURL),
		"pre_open_call_timeout", config.Timeouts.PreOpenCall,
		"hot_path_call_timeout", config.Timeouts.HotPathCall,
		"fire_and_forget_call_timeout", config.Timeouts.FireAndForgetCall,
		"max_idempotent_retries", config.MaxIdempotentRetries,
	)
	return &Client{
		config:     config,
		httpClient: httpClient,
		logger:     logger,
	}, nil
}

// Close releases pooled HTTP connections held by the default transport.
// Safe to call multiple times; subsequent calls after the first are
// no-ops. Does not affect an injected HTTPClient whose Transport is not
// an *http.Transport (tests sometimes inject a custom RoundTripper).
func (client *Client) Close() {
	if !client.closed.CompareAndSwap(false, true) {
		return
	}
	if pooledTransport, ok := client.httpClient.Transport.(*http.Transport); ok {
		pooledTransport.CloseIdleConnections()
	}
}

func newPooledHTTPTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   defaultDialTimeout,
			KeepAlive: defaultKeepAlive,
		}).DialContext,
		MaxIdleConns:        defaultMaxIdleConnections,
		MaxIdleConnsPerHost: defaultMaxIdleConnectionsPerHost,
		IdleConnTimeout:     defaultIdleConnectionTimeout,
		TLSHandshakeTimeout: defaultTLSHandshakeTimeout,
		ForceAttemptHTTP2:   true,
	}
}

// hostForLog returns just the host:port of a URL for structured
// logging. The full URL is intentionally not logged because in some
// deployments the base URL carries credentials in its userinfo
// component, which must never appear in a log line.
func hostForLog(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Host == "" {
		return "unparseable"
	}
	return parsedURL.Host
}
