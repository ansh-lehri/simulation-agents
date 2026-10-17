package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// defaultReadHeaderTimeout bounds how long a client has to finish sending
// request headers before the server closes the connection. Without this,
// slow-header clients can hold goroutines open indefinitely.
const defaultReadHeaderTimeout = 5 * time.Second

// Server is the inbound HTTP surface of this process. It owns an http.Server
// configured with routes for the liveness probe and the two orchestrator
// seam entry points. It holds no agent state; it carries only the
// orchestrator it forwards to, a logger, and the underlying http.Server.
//
// The TLS termination decision for this phase is deliberately deferred:
// the server listens as plain HTTP. In deployment a TLS-terminating proxy
// sits in front; when the real agent orchestrator ships, the proxy pattern
// can be preserved or the Server can be upgraded to bind TLS directly
// without any change to the orchestrator interface or the handlers.
type Server struct {
	httpServer   *http.Server
	orchestrator AgentOrchestrator
	logger       *slog.Logger
}

// New constructs a Server bound to listenAddr, forwarding bring-up and
// wind-down calls to the given orchestrator. Both orchestrator and logger
// must be non-nil: without the orchestrator the server has no destination
// for inbound calls, and without the logger it cannot produce the boundary
// trail its spec requires. A nil for either is treated as a composition-root
// invariant violation and panics here.
func New(listenAddr string, orchestrator AgentOrchestrator, logger *slog.Logger) *Server {
	if orchestrator == nil {
		panic("transport: Server requires a non-nil orchestrator")
	}
	if logger == nil {
		panic("transport: Server requires a non-nil logger")
	}
	server := &Server{
		orchestrator: orchestrator,
		logger:       logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.handleHealthz)
	mux.HandleFunc("POST /bringup", server.handleBringUp)
	mux.HandleFunc("POST /winddown", server.handleWindDown)
	server.httpServer = &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
	}
	return server
}

// Handler returns the http.Handler the Server routes requests through. It
// exists so tests (and future injection points such as a TLS-terminating
// front-end) can exercise the routing without binding a listener.
func (server *Server) Handler() http.Handler {
	return server.httpServer.Handler
}

// Addr returns the address the Server is configured to bind on. It reflects
// the string passed to New, which may be unresolved (e.g. "127.0.0.1:0")
// until a listener is actually opened.
func (server *Server) Addr() string {
	return server.httpServer.Addr
}

// Start opens a TCP listener on the configured address and serves on it.
// It blocks until the server is shut down. A graceful shutdown surfaces as
// a nil return; any other listener or serve error is wrapped and returned.
func (server *Server) Start() error {
	listener, err := net.Listen("tcp", server.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("transport: listen on %q: %w", server.httpServer.Addr, err)
	}
	server.logger.Info("http server listening", "listen_addr", listener.Addr().String())
	return server.Serve(listener)
}

// Serve runs the http.Server against an already-opened listener. It is the
// hook callers use when they need to control the listener themselves — for
// instance tests that bind to an OS-chosen free port, or future deployment
// paths that pass in a wrapped listener. A graceful shutdown surfaces as
// nil; any other serve error is wrapped and returned.
func (server *Server) Serve(listener net.Listener) error {
	err := server.httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("transport: http server stopped unexpectedly: %w", err)
}

// Shutdown stops accepting new connections and waits for in-flight requests
// to finish, bounded by the given context's deadline. It must be called at
// most once; calling it after a successful Shutdown is a no-op on the
// underlying http.Server.
func (server *Server) Shutdown(ctx context.Context) error {
	server.logger.Info("http server shutting down")
	if err := server.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("transport: http server shutdown: %w", err)
	}
	return nil
}
