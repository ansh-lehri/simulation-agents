package generator

// EffectiveParams carries scenario-shifted overrides that the trait draw
// consults in place of the baseline calibration. Every field is a pointer
// so a nil entry signals "no shift for this dial"; a nil *EffectiveParams
// as a whole signals "baseline for every dial" and is the standing shape
// the agent process runs with when no scenario is attached to a session.
//
// Two shifts live on this struct. ShiftedParams carries the full shifted
// GenParams the trait draw reads for every root dial — a scenario's shove
// is a redraw against the shifted dials, not a mutation of already-drawn
// values. PhiBimodal carries the one SHAPE-changing override, replacing
// the unimodal optimism draw with a two-mode mixture for scenarios whose
// character is "two camps" rather than a shifted single center.
type EffectiveParams struct {
	// ShiftedParams, when non-nil, is the full shifted GenParams the
	// trait draw consults in place of the baseline calibration. The
	// scenario engine produces this by shoving every root dial on its
	// ruler, inverting back to real distribution parameters, and
	// writing them into a copy of baseline GenParams. A nil pointer
	// means every root dial stays at baseline — the no-scenario path.
	ShiftedParams *GenParams

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
// parameters; the generator does not consume it. A nil *CurveEffective
// (empty scenario) means every rate stays at baseline — the session
// parameter writer applies no curve shift.
//
// The four rates are shoved on the Log ruler by the scenario engine and
// land here in their inverted (strictly positive) form, so the lifecycle
// curve builder downstream reads them unchanged whether a scenario is
// attached or not.
type CurveEffective struct {
	// EntryRate is the shifted arrival rate of agents into the live
	// session.
	EntryRate float64

	// StepOutRate is the shifted rate at which present agents
	// temporarily step away from the floor.
	StepOutRate float64

	// DoneRate is the shifted rate at which agents finish for the
	// session.
	DoneRate float64

	// ReentryRate is the shifted rate at which stepped-out agents
	// return to the floor.
	ReentryRate float64
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
