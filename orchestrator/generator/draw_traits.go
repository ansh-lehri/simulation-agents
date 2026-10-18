package generator

import (
	"math/rand/v2"

	"simulation-agents/agent"
)

// drawTraitVector produces one agent's frozen trait vector. The function
// is pure: given a seeded rng, a validated GenParams, and an optional
// scenario-shifted overrides pointer, it returns a complete TraitVector.
// This stage leaves the perception block nil (perception draws land in
// the next stage); every other block of the vector is populated.
//
// The draw order is kind-first: the agent-kind coin decides which branch
// runs, which gates every subsequent kind-specific draw AND holds the
// RNG consumption predictable from the seed alone. Within each branch
// the sub-order is fixed so a seed replays identically run-to-run.
//
// Order (kind, then universal, then kind-specific):
//
//  1. agent kind (herd vs endogenous)
//  2. skill, aggressiveness, capital size, pnl reactivity, p_act
//  3. if herd: act_prob and return
//  4. if endogenous: disposition → diet (coupled) → reactivity →
//     optimism → exit profile (uses skill) → max_hold (uses reactivity)
//
// is_tipper is left false here. For a canonical population the
// generator's top level picks exactly one endogenous agent and flips the
// flag after the whole population is drawn (promoting a revived carrier
// when one exists). For an on-demand population the tipper is a runtime
// per-session assignment made in a later phase, never a frozen trait.
func drawTraitVector(rng *rand.Rand, params GenParams, scenarioShifts *EffectiveParams) agent.TraitVector {
	kind := drawAgentKind(rng, params.HerdProbability)

	traits := agent.TraitVector{
		AgentKind:      kind,
		Skill:          drawSkill(rng, params.SkillBeta),
		Aggressiveness: drawAggressiveness(rng, params.AggressivenessBeta),
		CapitalSize:    drawCapitalSize(rng, params.CapitalSizeLogNormal),
		PnlReactivity:  drawPnlReactivity(rng, params.PnlReactivityBeta),
		PAct:           drawPAct(rng, params.PActBeta),
	}

	if kind == agent.AgentKindHerd {
		credulity := drawActProb(rng, params.ActProbBeta)
		traits.ActProb = &credulity
		return traits
	}

	// Endogenous branch.
	disposition := drawDisposition(rng, params.DispositionDirichletAlphas)
	traits.Disposition = &disposition

	diet := coupleDietFromDisposition(rng, disposition, params.DietOptionalFlagProbability)
	traits.Diet = &diet

	reactivity := drawReactivity(rng, params.ReactivityBeta)
	traits.Rho = &reactivity

	optimism := drawOptimism(rng, params.OptimismBeta, bimodalShift(scenarioShifts))
	traits.Phi = &optimism

	// Perception block lands in the next stage of the trait-draw build.
	// Kept nil here so the shape is final from stage one.

	exitProfile := drawExitProfile(rng, params, traits.Skill)
	traits.Exit = &exitProfile

	maxHold := drawMaxHold(rng, params.MaxHoldRange, reactivity, params.SigmaIdio)
	traits.MaxHold = &maxHold

	return traits
}

// bimodalShift pulls the one scenario-aware override the trait draw
// consults — the bimodal optimism shape — out of the overrides struct,
// returning nil when no scenario is attached. A nil overrides pointer AND
// a non-nil overrides pointer with a nil PhiBimodal both resolve to nil,
// which the baseline optimism draw treats as "use the unimodal Beta".
func bimodalShift(scenarioShifts *EffectiveParams) *BimodalOptimism {
	if scenarioShifts == nil {
		return nil
	}
	return scenarioShifts.PhiBimodal
}
