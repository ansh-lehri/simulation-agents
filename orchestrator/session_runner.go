package orchestrator

import (
	"context"

	"simulation-agents/orchestrator/generator"
	"simulation-agents/session"
	"simulation-agents/transport"
)

// SessionRunner is the orchestrator's view of the per-exchange run
// goroutine. Defined as an interface so the bring-up tail can spawn
// a spy in tests and so the orchestrator's dependency surface is
// spelled out at the point of use. The real implementation is
// *session.Runner; the composition root injects it on
// NewOrchestrator and the bring-up tail calls Run exactly once per
// bring-up as the final step of the tail (persist → snapshot →
// enlist → spawn).
//
// The channel the bring-up tail creates and passes here flows the
// lifecycle decisions produced by the tick loop back to the
// orchestrator, which writes the durable consequence (the
// decide/write split). The channel's element type lives alongside
// the Runner because the Runner's downstream consumer — the tick
// loop — is the type's producer.
type SessionRunner interface {
	Run(
		ctx context.Context,
		request transport.BringUpRequest,
		population generator.Population,
		curveEffective *generator.CurveEffective,
		lifecycleChannel chan<- session.LifecycleDecision,
	)
}

// Compile-time witness: the real Runner satisfies the orchestrator's
// view of it. Signature drift between the two packages would fail at
// build rather than at first bring-up spawn.
var _ SessionRunner = (*session.Runner)(nil)
