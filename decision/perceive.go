package decision

import (
	"math/rand/v2"

	"simulation-agents/agent"
	"simulation-agents/store"
)

// Perceive computes one agent's perception of one instrument for the current
// tick. Each agent call is independent: traits carries that agent's frozen
// perception profile (methods, windows, biases) and agentRNG is its own
// seeded noise stream, so two agents reading the same snapshot produce
// different bundles.
//
// Diet-off channels yield a nil pointer in the bundle (absent ≠ neutral).
// On-channel but unreadable (thin history, absent instrument, no quote) yields
// a non-nil pointer with ReadOK=false.
//
// Herd agents and agents with nil Diet or nil Perception receive an empty
// bundle and zero refs — their impulse comes from the herd path in 4b, not
// perception readings.
func Perceive(
	traits agent.TraitVector,
	acc *Accessor,
	inst, tf string,
	agentRNG *rand.Rand,
	params PerceptionParams,
) (PerceptionBundle, store.PerceptionRefs) {
	var bundle PerceptionBundle

	diet := traits.Diet
	perception := traits.Perception

	if diet == nil || perception == nil {
		return bundle, store.PerceptionRefs{}
	}

	if diet.Chart {
		reading := perceiveChart(perception.Chart, acc, inst, tf, traits, agentRNG, params)
		bundle.Chart = &reading
	}

	if diet.MeanReversion {
		reading := perceiveRange(perception.Range, acc, inst, tf, traits, agentRNG, params)
		bundle.Range = &reading
	}

	if diet.OrderBook {
		reading := perceiveLiquidity(perception.Liquidity, acc, inst, tf)
		bundle.Liquidity = &reading
	}

	if diet.Volume {
		reading := perceiveVolume(perception.Volume, acc, inst, tf)
		bundle.Volume = &reading
	}

	if diet.TimeOfSession {
		reading := perceiveTime(acc, inst)
		bundle.Time = &reading
	}

	refs := buildPerceptionRefs(acc, inst, tf, bundle)
	return bundle, refs
}

// buildPerceptionRefs snapshots the current market state into a PerceptionRefs
// so the gate can compare on the next tick without re-running any indicator.
func buildPerceptionRefs(acc *Accessor, inst, tf string, bundle PerceptionBundle) store.PerceptionRefs {
	refs := store.PerceptionRefs{}

	if price, ok := acc.CurrentPrice(inst, tf); ok {
		refs.LastPrice = price
	}
	if spread, ok := acc.Spread(inst); ok {
		refs.LastSpread = spread
	}
	if bars := acc.Candles(inst, tf); len(bars) > 0 {
		refs.LastVolume = bars[len(bars)-1].Volume
	}
	refs.LastPhase = acc.Phase(inst)

	if bundle.Range != nil && bundle.Range.ReadOK {
		refs.LastRangePos = bundle.Range.PositionInRange
	}

	return refs
}
