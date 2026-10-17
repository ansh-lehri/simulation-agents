package main

import (
	"fmt"
	"net"
	"os"
)

const (
	// envListenAddr names the environment variable that selects the host:port
	// the HTTP server binds on.
	envListenAddr = "AGENTSD_LISTEN_ADDR"

	// defaultListenAddr binds to loopback so an unconfigured development run
	// cannot accidentally expose the inbound orchestrator seam on a routable
	// interface. Deployment overrides via envListenAddr.
	defaultListenAddr = "127.0.0.1:8080"
)

// loadListenAddr reads this executable's listen address from the environment,
// applies the loopback default when the variable is empty or unset, and
// verifies the result parses as a host:port pair. It is the only place this
// executable touches os.Getenv — the Postgres and Redis settings are loaded
// through the vendored connectivity package's own Load() entry point.
func loadListenAddr() (string, error) {
	listenAddr := os.Getenv(envListenAddr)
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}
	if _, _, err := net.SplitHostPort(listenAddr); err != nil {
		return "", fmt.Errorf(
			"agentsd: %s=%q is not a valid host:port: %w",
			envListenAddr, listenAddr, err,
		)
	}
	return listenAddr, nil
}
