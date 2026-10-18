// Package agent holds the pure data types that describe a single trading
// agent: identity + durable-mutable state + the frozen trait vector + the
// capital draw helper. It is a leaf package — no I/O, no store calls, no
// network.
package agent

import "time"

// AgentID is the stable primary key for a durable identity row and is also
// the owner_id the economy account links back on. Wrapped as a distinct
// string type so a plain string parameter cannot be mistaken for one.
type AgentID string

// Population values. The two session kinds share the identity shape; what
// differs is reuse policy (canonical persists and evolves; ondemand's row
// exists so the economy account is reusable, with traits redrawn fresh).
const (
	PopulationCanonical = "canonical"
	PopulationOnDemand  = "ondemand"
)

// Origin records provenance of a durable row.
const (
	OriginBoot = "boot"
	OriginMint = "mint"
)

// Agent is the in-memory carrier that flows through bring-up: identity +
// durable-mutable state + the frozen trait vector. It is the source for
// both the Postgres row and the initial Redis runtime snapshot. The hot
// path never mutates Traits — immutability is enforced in code, not SQL.
type Agent struct {
	// identity
	ID         AgentID
	Population string // PopulationCanonical | PopulationOnDemand
	Country    string
	Origin     string // OriginBoot | OriginMint

	// durable-mutable
	CapitalHandle   string     // economy account_id, opaque to this package
	TradingCapital  int64      // session money; canonical persists and evolves, ondemand lives only in Redis
	TraitsVersion   int        // bumped only by a future adaptation pass, never the trading hot path
	TraitsUpdatedAt time.Time  // paired with TraitsVersion; stamped at mint and at any later adaptation rewrite
	RetiredAt       *time.Time // nil = live member; non-nil = retired for good, never revived

	// frozen trait vector (serialized whole into the agents.traits JSONB column)
	Traits TraitVector

	// FundedCash is the broker's funding ceiling observed at account-open.
	// It feeds the capital draw and is never persisted to any store.
	FundedCash int64
}
