package session

import (
	"context"
	"math/rand/v2"
	"time"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/store"
	"simulation-agents/transport"
)

// CoreClient is the narrow subset of simulation-agents/core.Client the
// Runner consults. Keeping the surface narrow lets a test fake stub
// only the methods the Runner actually calls, rather than mirror
// every method on the real client.
//
// The real *core.Client satisfies this structurally for every method
// except Subscribe, whose concrete return type is wrapped by a thin
// composition-root adapter so the Runner holds a WSConnection
// interface rather than the concrete *core.WebSocketConnection type.
// That adapter lives in the composition root; this interface is the
// shape the Runner codes against.
type CoreClient interface {
	// JoinCanonical installs an agent's account on a canonical shard
	// and returns the identifiers tied to that install. Called in
	// the per-agent session-install op during a canonical bring-up.
	JoinCanonical(ctx context.Context, agentUserID, marketTag string) (core.CanonicalSessionJoin, error)

	// JoinOnDemand installs an agent's account on the one already-live
	// on-demand market identified by exchangeID — the market a
	// human's StartOnDemand call already created. Called in the
	// per-agent session-install op during an on-demand bring-up;
	// never spins up a new on-demand session itself.
	JoinOnDemand(ctx context.Context, agentUserID, exchangeID string) (core.OnDemandSessionJoin, error)

	// GetCandles reads one page of candle history and is driven in a
	// paged loop by the history-pull op until the response's
	// NextCursor is empty.
	GetCandles(ctx context.Context, request core.CandlesRequest) (core.CandlesPage, error)

	// Subscribe dials the live market-data WebSocket for the given
	// feed session id, consumes the mandatory connected frame,
	// writes the subscribe frame, and returns the connection handle
	// plus the greeting. The Runner spawns an ingest goroutine on
	// the returned connection.
	Subscribe(
		ctx context.Context,
		sessionID string,
		subscriptions []core.MarketDataSubscription,
	) (WSConnection, core.ConnectedFrame, error)

	// GetObservation reads an agent's live own-state from Core:
	// balance, open positions, working orders, and a tail of recent
	// closed trades. Called inside decide() on the hot action-tick
	// path; any error causes the agent to skip its action this tick.
	GetObservation(ctx context.Context, accountID string) (core.AccountObservation, error)

	// PunchOrder submits a trade order fire-and-forget. Core returns
	// 202; this process ignores both the status and the order id.
	// The order's live outcome is reconciled via GetObservation on a
	// later tick. Errors are also ignored — a failed punch is treated
	// as "status unknown" and reconciled the same way.
	PunchOrder(ctx context.Context, request core.OrderPunchRequest) error

	// GetAccountCash reads an account's durable, persisted cash figure.
	// Used only in the canonical close-effects tail, after the broker
	// has already finalized and unloaded every account for the day —
	// at that point GetObservation's live read would 409, so this is
	// the one call in this interface that reads the post-close state
	// rather than an in-session one.
	GetAccountCash(ctx context.Context, accountID string) (core.AccountCash, error)

	// JoinCanonicalAsOperator / JoinOnDemandAsOperator are
	// JoinCanonical/JoinOnDemand for a liquidity-seeder identity
	// (owner_type "agent_operator" instead of "agent"). A seeder must
	// use these, not the plain agent variants — EnsureAccount is keyed
	// by (owner_type, owner_id), so joining under the wrong owner_type
	// installs the session on a different account row than the one
	// OpenOperatorAccount already opened, and every later order/
	// seed-inventory call against the seeder's real account 409s
	// "no session".
	JoinCanonicalAsOperator(ctx context.Context, operatorUserID, marketTag string) (core.CanonicalSessionJoin, error)
	JoinOnDemandAsOperator(ctx context.Context, operatorUserID, exchangeID string) (core.OnDemandSessionJoin, error)

	// OpenOperatorAccount opens a liquidity-seeder identity (Core's
	// OwnerAgentOperator owner type, funded well above a normal agent).
	// Called once per seeder at session bringup, before it joins and
	// starts resting/lifting orders.
	OpenOperatorAccount(ctx context.Context, operatorUserID string) (string, error)

	// SeedInventory mints opening inventory on a seller seeder's
	// account so it can rest a sell-side limit order without ever
	// having bought anything (a SELL reserves units, not cash, and
	// positions can't go negative). Called once per seller seeder per
	// instrument before its first ask.
	SeedInventory(ctx context.Context, request core.SeedInventoryRequest) (core.SeedInventoryResponse, error)

	// CancelOrder sends a cancel request for a resting order. Used by
	// the liquidity seeder's cancel-before-replace loop. A 409 with
	// reason not_cancellable / unknown_order / cancel_pending means the
	// order is already gone; the seeder treats those as success.
	CancelOrder(ctx context.Context, accountID, orderID string) error

	// PunchOrderTracked submits a trade order and returns the broker-
	// minted order_id from the 202 response. Used by the liquidity seeder
	// so it can track and cancel its own resting orders each round.
	// The agent decision pipeline uses PunchOrder (fire-and-forget).
	PunchOrderTracked(ctx context.Context, request core.OrderPunchRequest) (string, error)
}

// WSConnection is the connection-handle shape the Runner consumes
// from a Subscribe call: ReadFrame drives the ingest loop; Close
// releases the socket at teardown; WriteSubscribe lets the ingest
// goroutine retry subscriptions that were nacked with "unknown
// instrument". The real *core.WebSocketConnection satisfies all three
// methods directly; the composition-root adapter returns it typed as
// WSConnection so tests can inject a fake socket.
type WSConnection interface {
	ReadFrame(ctx context.Context) (core.IncomingFrame, error)
	WriteSubscribe(subscriptions []core.MarketDataSubscription) error
	Close() error
}

// RuntimeStore is the Redis runtime surface the Runner writes to. It
// extends the store package's RuntimeStore interface with one extra
// write op the pre-open session-install path needs — recording that a
// single agent's install failed partially so the tick loop can skip
// it at activate time (the population is statistical; a few failed
// installs do not abort the bring-up).
type RuntimeStore interface {
	store.RuntimeStore

	// MarkSessionLoadFailed records that one agent's session-install
	// did not succeed. Written by the session-install op's per-agent
	// error branch; read by the tick-loop scheduler to skip the
	// agent at activate time. Reason is the free-text error so an
	// operator inspecting Redis sees why the install failed.
	MarkSessionLoadFailed(
		ctx context.Context,
		exchangeID string,
		agentID agent.AgentID,
		reason string,
	) error
}

// MarketDataStore is the Redis-backed market-data cache the Runner
// seeds in the history pull and extends in the live ingest loop. The
// store package already defines a narrow surface over these two
// writes; aliasing it here keeps a single authoritative shape rather
// than drifting duplicate definitions.
type MarketDataStore = store.MarketDataStore

// Scheduler is the tick-loop entry point the Runner hands off to
// once the pre-open ops have joined and the session's start wall-clock
// has been reached. The current phase ships a logging no-op; the real
// tick loop replaces it later. One StartScheduler call per bring-up
// spawn — the scheduler owns the session from there.
type Scheduler interface {
	StartScheduler(
		ctx context.Context,
		request transport.BringUpRequest,
		population generator.Population,
		curveEffective *generator.CurveEffective,
		lifecycleChannel chan<- LifecycleDecision,
	)
}

// FailureReporter is the surface the Runner calls on a fatal pre-open
// error (FailBringUp) and on a successful go-live (ReportReady). The
// orchestrator implements it: the decide/write split means the Runner
// decides a failure has occurred and the orchestrator writes the
// consequence (and, later, the cleanup when the resilience layer
// lands). Nothing in the Runner performs its own cleanup.
type FailureReporter interface {
	FailBringUp(request transport.BringUpRequest, cause error)
	ReportReady(request transport.BringUpRequest)
}

// Clock is the Runner's wall-clock dependency. Factored as an
// interface so tests fast-forward the go-live wait without sleeping.
// WaitUntil blocks until the deadline is reached or ctx is cancelled;
// Now returns the current wall-clock time for ad-hoc reads a later
// stage may need.
type Clock interface {
	WaitUntil(ctx context.Context, deadline time.Time) error
	Now() time.Time
}

// RNGSource is the Runner's view of the random-source factory. It
// matches the orchestrator's RNGSource shape structurally so the
// composition root injects the same concrete source into both
// packages. A fresh *rand.Rand is drawn per bring-up spawn and is
// then consumed for the per-agent entry-fraction draws.
type RNGSource interface {
	Fresh() *rand.Rand
}
