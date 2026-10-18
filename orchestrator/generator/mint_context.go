// Package generator holds the pure, seeded trait-draw engine that produces a
// population of trading agents at bring-up time. It is a leaf-ish package:
// its only outbound reach is the narrow store/lease/WAL interfaces its top
// level wires (added in later stages) and a scenario-provider seam. This
// file pins the per-mint context carried through every draw.
package generator

// MintContext carries the per-mint invariants the trait draw depends on:
// which durable-identity bucket the agent lands in, which country keys the
// generation distributions, and which exchange's bring-up initiated this
// mint — recorded in the write-ahead log so a crash mid-chain is
// recoverable.
//
// ExchangeID is set for every mint, including canonical shortfall mints,
// because every mint happens inside one specific bring-up call and the
// write-ahead log carries the field identically for both populations.
type MintContext struct {
	// Population is the durable-identity bucket, one of
	// agent.PopulationCanonical or agent.PopulationOnDemand. It flows into
	// the Agent record and tags the staged write-ahead log entry so a
	// recovery pass routes the agent back to the right generation path.
	Population string

	// Country keys the generation distributions. The current calibration is
	// country-blind; the parameter is carried so a future per-country
	// calibration drops in without reshaping the draw signature.
	Country string

	// ExchangeID is the exchange whose bring-up initiated this mint.
	// Written into the write-ahead log so a mid-chain crash can be
	// reconciled against the right session.
	ExchangeID string
}
