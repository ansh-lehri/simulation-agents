package scenario

import "simulation-agents/orchestrator/generator"

// This file holds the heart of the scenario engine: the pure
// pipeline that turns a baseline link-space + a scenario blob into a
// set of shifted generator parameters. Three steps:
//
//   shove        u_eff[knob] = u0[knob] + intensity * direction[knob] * eta[knob]
//                              (scalar knobs; vector-wise for disposition)
//   invert       moment_eff = fromLink(ruler, u_eff) ; written back into a
//                              shifted GenParams via setScalarMoment and
//                              setDispositionProportions. Presence knobs are
//                              skipped here (they live outside GenParams)
//                              and consumed by curveFrom instead.
//   switches     applySwitches sets the one supported absolute override
//                              (PhiBimodal) on the shifted EffectiveParams;
//                              it overrides any optimism.center arrow by
//                              construction (bimodal defines two camps,
//                              not a shifted single center).
//
// The output of the pipeline is a *generator.EffectiveParams for the
// trait draw and a *generator.CurveEffective for the lifecycle-curve
// builder; both are freshly allocated and own their contents.

// shove computes u_eff for every scalar knob the catalogue registers
// plus the three disposition components. Scalar knobs read their
// per-knob direction weight from the blob (missing keys are
// implicitly zero → that knob stays at baseline). Disposition reads
// its arrow and derives a signed vector [+1 at Toward, -1 at
// AwayFrom, 0 elsewhere]; a nil arrow leaves the vector all zeros.
func (engine *ScenarioEngine) shove(blob ScenarioBlob) (
	map[string]float64,
	[DispositionComponentCount]float64,
) {
	scalarUEff := make(map[string]float64, len(engine.baseline.scalarU0))
	for knobName, baselineU0 := range engine.baseline.scalarU0 {
		directionWeight := blob.Direction[knobName]
		scalarUEff[knobName] = baselineU0 +
			blob.Intensity*directionWeight*engine.baseline.scalarEta[knobName]
	}

	dispositionArrow := dispositionArrowVector(blob.DispositionDirection)
	var dispositionUEff [DispositionComponentCount]float64
	for componentIndex := range dispositionUEff {
		dispositionUEff[componentIndex] = engine.baseline.dispositionU0[componentIndex] +
			blob.Intensity*dispositionArrow[componentIndex]*engine.baseline.dispositionEta[componentIndex]
	}

	return scalarUEff, dispositionUEff
}

// dispositionArrowVector converts a scenario's two-slot
// DispositionArrow into a signed length-three vector. A nil arrow
// returns the zero vector (no shove). The registry loader has
// already validated the component names and ensured Toward and
// AwayFrom are distinct, so this function is a pure translation.
func dispositionArrowVector(arrow *DispositionArrow) [DispositionComponentCount]float64 {
	var vector [DispositionComponentCount]float64
	if arrow == nil {
		return vector
	}
	vector[dispositionComponentIndex(arrow.Toward)] = +1
	vector[dispositionComponentIndex(arrow.AwayFrom)] = -1
	return vector
}

// invert runs the scalar knobs' uEff values back through their rulers
// to recover shifted moments, writes those moments into a fresh
// shifted GenParams, renormalizes the disposition log-ratios back to
// a simplex via softmax, and applies the blob's switches on top.
// Presence knobs are not written here — they are consumed by
// curveFrom and have no corresponding GenParams field.
func (engine *ScenarioEngine) invert(
	scalarUEff map[string]float64,
	dispositionUEff [DispositionComponentCount]float64,
	blob ScenarioBlob,
) *generator.EffectiveParams {
	shiftedParams := engine.baseParams

	for knobName, uEff := range scalarUEff {
		if isPresenceKnob(knobName) {
			continue
		}
		ruler, _ := engine.catalogue.RulerFor(knobName)
		shiftedMoment := fromLink(ruler, uEff)
		setScalarMoment(&shiftedParams, knobName, shiftedMoment)
	}

	shiftedProportions := fromLogRatio(dispositionUEff[:])
	shiftedParams.DispositionDirichletAlphas = setDispositionProportions(
		shiftedParams.DispositionDirichletAlphas,
		shiftedProportions,
	)

	effective := &generator.EffectiveParams{
		ShiftedParams: &shiftedParams,
	}
	applySwitches(effective, blob.Switches)
	return effective
}

// isPresenceKnob is the small routing predicate the invert step uses
// to skip presence rates — those are consumed by curveFrom (which
// writes them onto a *generator.CurveEffective) and have no entry in
// GenParams at all.
func isPresenceKnob(knobName string) bool {
	switch knobName {
	case KnobPresenceEntryRate,
		KnobPresenceStepOutRate,
		KnobPresenceDoneRate,
		KnobPresenceReentryRate:
		return true
	}
	return false
}

// applySwitches writes the blob's absolute overrides onto a shifted
// EffectiveParams. Only the PhiBimodal switch is consumed by the
// current generator; the two boolean switches on Switches (Tipper,
// MinNoiseFloor) describe later-stage runtime behavior and have no
// corresponding field on EffectiveParams yet — they are left to a
// future wiring and the shipped presets do not set them.
//
// The bimodal switch's precedence rule — bimodal ON overrides any
// optimism.center direction arrow — is implemented by construction
// here: the invert step has already written any shifted optimism
// center into shiftedParams.OptimismBeta, but drawTraits consults
// PhiBimodal first (when non-nil) and never reads the shifted center
// in that case.
func applySwitches(effective *generator.EffectiveParams, switches Switches) {
	if switches.PhiBimodal != nil {
		effective.PhiBimodal = translateBimodal(*switches.PhiBimodal)
	}
}

// translateBimodal converts a scenario-side BimodalConfig into the
// generator-side BimodalOptimism the trait draw consumes. The two
// camps are centered at +Separation and -Separation on the (-1, +1)
// axis; the generator draws its Beta samples in (0, 1) and stretches
// to (-1, +1) via 2x - 1, so each camp's Beta mean is
// (center + 1) / 2.
//
// WithinSpread is adopted as the Beta concentration α + β of each
// camp — a positive dial where larger values give tighter camps
// around their centers. The field name reflects the design's "camp
// spread" vocabulary; the comment makes the chosen convention
// explicit so a reader does not have to guess which direction the
// dial moves tightness.
func translateBimodal(config BimodalConfig) *generator.BimodalOptimism {
	bullBetaMean := (1 + config.Separation) / 2
	bearBetaMean := (1 - config.Separation) / 2
	campConcentration := config.WithinSpread
	return &generator.BimodalOptimism{
		MixWeight: config.BullWeight,
		FirstModeBeta: generator.BetaPair{
			Alpha: bullBetaMean * campConcentration,
			Beta:  (1 - bullBetaMean) * campConcentration,
		},
		SecondModeBeta: generator.BetaPair{
			Alpha: bearBetaMean * campConcentration,
			Beta:  (1 - bearBetaMean) * campConcentration,
		},
	}
}

// curveFrom reads the shifted presence-rate uEff values and inverts
// them through the Log ruler to recover strictly positive shifted
// rates. The four fields on the returned struct are what the
// lifecycle-curve builder consumes downstream; this is the only
// scenario output that reaches past the trait draw into the running
// session.
func (engine *ScenarioEngine) curveFrom(scalarUEff map[string]float64) *generator.CurveEffective {
	return &generator.CurveEffective{
		EntryRate:   fromLink(Log, scalarUEff[KnobPresenceEntryRate]),
		StepOutRate: fromLink(Log, scalarUEff[KnobPresenceStepOutRate]),
		DoneRate:    fromLink(Log, scalarUEff[KnobPresenceDoneRate]),
		ReentryRate: fromLink(Log, scalarUEff[KnobPresenceReentryRate]),
	}
}
