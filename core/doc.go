// Package core is this process's single outbound client to the Core
// process. It owns the pooled HTTP transport used for every
// request-response call (open account, read funded cash, join a canonical
// session, punch an order, read a fresh observation, fetch candle
// history) and the WebSocket transport used for the live market-data
// feed. It is the only package in this process that opens a network
// connection to Core.
//
// Nothing here stores, caches, or interprets responses: the client
// decodes the wire shapes defined by the Core API contract into Go
// values and hands them back to callers (the orchestrator, scenario, and
// so on), which own what happens next. The one exception is the error
// envelope decode, which produces a typed CallError that callers branch
// on by stable status and reason tag — never by free-text message.
package core
