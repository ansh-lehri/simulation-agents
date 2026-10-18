package generator

// EffectiveParams carries scenario-shifted overrides that the trait draw
// consults in place of the baseline calibration. Every field is a pointer
// so a nil entry signals "no shift for this dial"; a nil *EffectiveParams
// as a whole signals "baseline for every dial" and is the standing shape
// the agent process runs with when no scenario is attached to a session.
//
// Only one shift lives on this struct today: the bimodal optimism override,
// which replaces the baseline unimodal optimism draw with a two-mode
// mixture. The optimism latent is the one draw whose SHAPE a scenario
// changes; every other scenario effect is a numeric shift to a calibration
// dial, folded into this struct as pointer fields alongside PhiBimodal as
// the scenario engine lands.
type EffectiveParams struct {
	// PhiBimodal, when non-nil, replaces the baseline unimodal optimism
	// draw with a two-mode mixture. Used to shape sessions that run with a
	// polarized population (clear optimists AND clear pessimists at once)
	// rather than the near-neutral baseline.
	PhiBimodal *BimodalOptimism
}

// BimodalOptimism replaces the baseline unimodal optimism draw with a
// two-mode mixture of Beta distributions. MixWeight is the probability of
// picking the first mode on any single agent's draw; the two Beta pairs
// are the shape parameters of the two modes. Each sampled value is then
// mapped onto [-1, +1] the same way the baseline optimism draw is.
type BimodalOptimism struct {
	// MixWeight is the probability of drawing from FirstModeBeta on any
	// given agent; the complementary probability selects SecondModeBeta.
	// Must be in [0, 1]; the top-level consumer validates this before use.
	MixWeight float64

	// FirstModeBeta is the Beta shape pair for the first mode of the
	// mixture.
	FirstModeBeta BetaPair

	// SecondModeBeta is the Beta shape pair for the second mode of the
	// mixture.
	SecondModeBeta BetaPair
}

// CurveEffective carries scenario-shifted lifecycle-curve overrides. It
// rides from the scenario resolver to a later handoff that writes session
// parameters; the generator does not consume it. The shape is pinned here
// so the scenario provider's return type is stable across phases, and so a
// no-op implementation can return the right zero value today.
type CurveEffective struct {
	// Fields land as the lifecycle-curve shifting mechanism is designed.
}

// ScenarioProvider resolves a scenario name against a country into the two
// output shapes the orchestrator consumes: trait-draw overrides for the
// generator (EffectiveParams) and lifecycle-curve overrides for the
// session handoff (CurveEffective). An empty scenario name means baseline
// on both outputs; both return values are nil in that case.
type ScenarioProvider interface {
	Effective(country, scenarioName string) (*EffectiveParams, *CurveEffective)
}

// NoOpScenarioProvider is the baseline provider: every call returns nil
// for both outputs so every draw runs against the baseline calibration
// and no lifecycle-curve shift is carried through to the session handoff.
// This is the provider the composition root wires until the scenario
// engine lands.
type NoOpScenarioProvider struct{}

// Effective returns the baseline no-shift pair for any input. The inputs
// are accepted to satisfy the ScenarioProvider interface; they are not
// consulted.
func (NoOpScenarioProvider) Effective(country, scenarioName string) (*EffectiveParams, *CurveEffective) {
	_, _ = country, scenarioName
	return nil, nil
}
