package scenario

import (
	"fmt"
	"log/slog"

	"simulation-agents/orchestrator/generator"
)

// ScenarioEngine resolves a scenario name into shifted generation
// parameters and shifted lifecycle-curve rates. It is built once at
// process startup — the registry and baseline are read-only after
// construction, and Effective is a pure function of its inputs plus
// that fixed state.
//
// The engine satisfies generator.ScenarioProvider: the composition
// root passes this struct where that interface is required, and the
// generator calls Effective at on-demand bring-up time.
type ScenarioEngine struct {
	registry   Registry
	baseline   BaselineLinkSpace
	catalogue  KnobCatalogue
	baseParams generator.GenParams
	logger     *slog.Logger
}

// NewEngine wires a ScenarioEngine from its parts and precomputes
// the baseline link-space + per-knob eta. Validation runs on the
// baseline GenParams (via the generator's own Validate) so a bad
// calibration fails startup rather than at first on-demand session.
//
// baselineSeed is a dedicated calibration seed — the baseline η
// sampling must be reproducible across restarts, so it is deliberately
// not sourced from the live fresh-entropy pool.
func NewEngine(
	params generator.GenParams,
	presence PresenceBaseline,
	catalogue KnobCatalogue,
	registry Registry,
	baselineSeed uint64,
	logger *slog.Logger,
) (*ScenarioEngine, error) {
	if logger == nil {
		return nil, fmt.Errorf("scenario: logger is required")
	}
	if len(catalogue) == 0 {
		return nil, fmt.Errorf("scenario: catalogue is empty")
	}
	if err := params.Validate(); err != nil {
		return nil, fmt.Errorf("scenario: invalid baseline GenParams: %w", err)
	}
	baseline := BuildBaseline(params, presence, catalogue, baselineSeed)
	return &ScenarioEngine{
		registry:   registry,
		baseline:   baseline,
		catalogue:  catalogue,
		baseParams: params,
		logger:     logger,
	}, nil
}

// Effective resolves a scenario name into shifted generation params
// and shifted lifecycle-curve rates. An empty name returns (nil, nil)
// — the identity path the no-scenario on-demand bring-up uses — so
// the baseline draw stays byte-identical to the pre-scenario path and
// nothing allocates. A non-empty name is looked up in the registry,
// shoved on the baseline link-space, inverted back to real parameters,
// and the switches applied on top.
//
// An unknown name is an invariant violation: intake must validate
// against the registry's Names set before the engine is called, so
// reaching this layer with a bad name is a programming error and the
// panic surfaces it loudly. (The silent fallback-to-baseline path is
// deliberately not provided — it is the exact lie scenarios must
// avoid.)
//
// country is logged for correlation but has no effect on the shift
// in this stage; a future per-country baseline would consult it to
// pick the baseline link-space.
func (engine *ScenarioEngine) Effective(
	country string,
	scenarioName string,
) (*generator.EffectiveParams, *generator.CurveEffective) {
	if scenarioName == "" {
		return nil, nil
	}
	if !engine.registry.Has(scenarioName) {
		panic(fmt.Sprintf(
			"scenario: Effective called with unregistered name %q; "+
				"intake must reject before engine",
			scenarioName,
		))
	}
	blob := engine.registry.Get(scenarioName)

	engine.logger.Info(
		"scenario resolve",
		"country", country,
		"scenario", scenarioName,
	)

	scalarUEff, dispositionUEff := engine.shove(blob)
	effective := engine.invert(scalarUEff, dispositionUEff, blob)
	curve := engine.curveFrom(scalarUEff)
	return effective, curve
}

// RegistryNames exposes the registered scenario names in sorted
// order. The intake-side name validation reads this to populate its
// accept-set at composition time.
func (engine *ScenarioEngine) RegistryNames() []string {
	return engine.registry.Names()
}
