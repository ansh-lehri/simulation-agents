package decision

import "simulation-agents/agent"

// perceiveLiquidity reads spread, book depth, and imbalance from the live
// order-book snapshot. All three are condition observations with no
// directional bias applied — the book carries no inherent directional lean.
//
// depth_levels in ReadingParams controls how many book levels are summed.
// ReadOK is false when no valid bid and ask are present.
func perceiveLiquidity(block *agent.ChannelBlock, acc *Accessor, inst, tf string) LiquidityReading {
	if !acc.Present(inst) {
		return LiquidityReading{}
	}

	depthLevels := 1
	if block != nil && block.ReadingParams != nil {
		if v, ok := block.ReadingParams["depth_levels"]; ok && v >= 1 {
			depthLevels = int(v)
		}
	}

	spreadTicks, spreadOK := acc.Spread(inst)
	if !spreadOK {
		return LiquidityReading{}
	}

	midPrice, priceOK := acc.CurrentPrice(inst, tf)
	if !priceOK || midPrice <= 0 {
		return LiquidityReading{}
	}

	bidDepth := acc.DepthQty(inst, depthLevels, sideBid)
	askDepth := acc.DepthQty(inst, depthLevels, sideAsk)

	imbalance, _ := acc.Imbalance(inst, depthLevels)

	return LiquidityReading{
		Spread:    float64(spreadTicks) / float64(midPrice),
		Depth:     float64(bidDepth + askDepth),
		Imbalance: imbalance,
		ReadOK:    true,
	}
}

// perceiveLiquidityDiet is the diet-gated entry point.
func perceiveLiquidityDiet(diet *agent.Diet, block *agent.ChannelBlock, acc *Accessor, inst, tf string) *LiquidityReading {
	if diet == nil || !diet.OrderBook {
		return nil
	}
	reading := perceiveLiquidity(block, acc, inst, tf)
	return &reading
}
