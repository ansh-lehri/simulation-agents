package decision

import (
	"math"
	"math/rand/v2"

	"simulation-agents/agent"
	"simulation-agents/core"
)

// AmbientInputs bundles the context Execute reads itself rather than
// through the committed intent — the intent only ever carries
// direction, conviction, and action; everything about which account,
// which instrument, how much capital is in play, and what the agent
// already holds elsewhere is supplied separately, because the same
// executor runs for every instrument and every caller.
type AmbientInputs struct {
	// AccountID identifies the agent's capital account on the broker.
	AccountID string

	// Instrument is the instrument the committed intent is for.
	Instrument string

	// Timeframe is the candle timeframe used as the current-price
	// fallback when the live quote has not printed a trade yet.
	Timeframe string

	// TradingCapital is the agent's total session money, independent
	// of what is currently committed to open positions.
	TradingCapital int64

	// Positions is the agent's full set of open positions across every
	// instrument, as read fresh from the broker this tick. An
	// instrument with no entry in this slice is flat.
	Positions []core.Position
}

// Execute turns one committed ExecIntent into an order ready for the
// dispatcher, or reports that no order should be sent. It is the one
// executor shared by every stage that can produce an ExecIntent — it
// never looks at where the intent came from, only at what it says and
// at the ambient account/position/market state it reads itself.
//
// A false second return means: send nothing. That covers an
// opposite-direction hold (reduce/flip is not this executor's job), a
// sizing result of zero, capital already fully committed elsewhere, or
// a limit placement with no two-sided book to place against.
func Execute(
	intent ExecIntent,
	ambient AmbientInputs,
	acc *Accessor,
	traits agent.TraitVector,
	rng *rand.Rand,
	cfg Params,
) (core.OrderPunchRequest, bool) {
	position := positionIn(ambient.Instrument, ambient.Positions)

	if intent.Action == ExecIntentActionClose {
		return buildCloseOrder(position, acc, traits, rng, ambient, cfg)
	}

	if position.Qty != 0 && oppositeDirection(position.Qty, intent.Direction) {
		// A position-management stage, not this executor, trims or
		// flips a holding — an opposing intent reaching here is a
		// safety guard tripping, not the normal path.
		return core.OrderPunchRequest{}, false
	}

	return buildOpenOrAddOrder(intent, acc, traits, rng, ambient, cfg)
}

// positionIn finds the agent's position in instrument among its full
// position slice. The zero Position (Qty==0) stands for flat, matching
// an instrument that has no entry in the slice at all.
func positionIn(instrument string, positions []core.Position) core.Position {
	for _, position := range positions {
		if position.Instrument == instrument {
			return position
		}
	}
	return core.Position{}
}

// oppositeDirection reports whether a signed position quantity and a
// signed trade direction point opposite ways. Both are assumed
// non-zero by the caller — a flat position has nothing to be opposite
// to, and direction is always +1 or -1 by the ExecIntent contract.
func oppositeDirection(positionQty int64, direction float64) bool {
	return (positionQty > 0 && direction < 0) || (positionQty < 0 && direction > 0)
}

// freeCapital computes the agent's available capital across every
// instrument as one shared pool: total trading capital minus the cost
// basis of every open position, not just the instrument being sized.
// Positions compete for the same pool, so an agent already heavily
// deployed elsewhere sizes down (or to zero) here automatically.
func freeCapital(tradingCapital int64, positions []core.Position) int64 {
	var committed int64
	for _, position := range positions {
		// Both WeightedAvgEntry (ticks) and tradingCapital (paise) are in the
		// same integer unit — no conversion factor.
		committed += position.WeightedAvgEntry * absInt64(position.Qty)
	}
	return tradingCapital - committed
}

// absInt64 returns the absolute value of a signed tick/quantity count.
func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

// sideForDirection maps a signed trade direction to the wire side. A
// long entry buys; a short entry sells. Direction is always +1 or -1
// by the ExecIntent contract, so the boundary at zero is unreachable
// in practice.
func sideForDirection(direction float64) string {
	if direction < 0 {
		return core.OrderSideSell
	}
	return core.OrderSideBuy
}

// closingSide maps a signed position quantity to the side that
// unwinds it: a long position is closed by selling, a short position
// by buying it back.
func closingSide(positionQty int64) string {
	if positionQty > 0 {
		return core.OrderSideSell
	}
	return core.OrderSideBuy
}

// warpedAggressiveness is the conviction-warped aggressiveness used to
// choose order type and limit placement for an open/add entry.
// Conviction can only pull aggressiveness up toward taking the market,
// never down toward patience — the power stays in [0,1] by
// construction, and the warp bites hardest for an agent already near
// the market/limit cutoff, leaving a deeply patient or deeply
// aggressive agent close to its own baseline.
func warpedAggressiveness(baseAggressiveness, conviction, gamma float64) float64 {
	return math.Pow(baseAggressiveness, 1-gamma*conviction)
}

// buildOpenOrAddOrder sizes and prices a brand-new or adding entry.
// Formulation Y nets out whatever the agent already holds in this
// instrument through the shared capital pool, so a flat start and an
// already-open same-direction position run the identical formula here
// — there is no separate "add" branch to keep in sync.
func buildOpenOrAddOrder(
	intent ExecIntent,
	acc *Accessor,
	traits agent.TraitVector,
	rng *rand.Rand,
	ambient AmbientInputs,
	cfg Params,
) (core.OrderPunchRequest, bool) {
	currentPrice, priceOK := acc.CurrentPrice(ambient.Instrument, ambient.Timeframe)
	if !priceOK {
		return core.OrderPunchRequest{}, false
	}

	available := freeCapital(ambient.TradingCapital, ambient.Positions)
	if available <= 0 {
		return core.OrderPunchRequest{}, false
	}

	// max_frac and conviction are both already bounded to [0,1], so
	// base notional can never exceed the available pool — no separate
	// ceiling clamp belongs here. The only clamp against this pool
	// comes back after execution noise, as a post-noise safety net.
	baseNotional := float64(available) * cfg.MaxFrac * intent.Conviction
	qty := int64(math.Floor(baseNotional / float64(currentPrice)))
	if qty <= 0 {
		return core.OrderPunchRequest{}, false
	}

	side := sideForDirection(intent.Direction)
	effectiveAggressiveness := warpedAggressiveness(traits.Aggressiveness, intent.Conviction, cfg.Gamma)

	orderType, limitPrice, spread, ok := resolveTypeAndPrice(side, effectiveAggressiveness, ambient.Instrument, acc, cfg)
	if !ok {
		return core.OrderPunchRequest{}, false
	}

	noisyQty := applyQtyNoise(qty, traits.Skill, rng, cfg.SigmaExec)
	noisyLimitPrice := limitPrice
	if orderType == core.OrderTypeLimit {
		noisyLimitPrice = applyPriceNoise(limitPrice, spread, traits.Skill, rng, cfg.SigmaExec)
	}

	ceiling := int64(math.Floor(float64(available) / float64(currentPrice)))
	finalQty := clampInt64(noisyQty, 1, ceiling)

	return assembleOrder(ambient.AccountID, ambient.Instrument, side, orderType, finalQty, noisyLimitPrice), true
}

// buildCloseOrder sizes and prices a full exit from an existing
// position. It reuses the same type/price/noise machinery as open/add
// but ignores conviction entirely and sizes against the closeable
// quantity rather than the capital pool — unwinding a position is not
// spending capital, and exit urgency reflects the agent's base
// temperament, not this particular commit's conviction.
func buildCloseOrder(
	position core.Position,
	acc *Accessor,
	traits agent.TraitVector,
	rng *rand.Rand,
	ambient AmbientInputs,
	cfg Params,
) (core.OrderPunchRequest, bool) {
	closeableQty := absInt64(position.Qty) - position.ReservedQty
	if closeableQty <= 0 {
		return core.OrderPunchRequest{}, false
	}

	side := closingSide(position.Qty)

	orderType, limitPrice, spread, ok := resolveTypeAndPrice(side, traits.Aggressiveness, ambient.Instrument, acc, cfg)
	if !ok {
		return core.OrderPunchRequest{}, false
	}

	noisyQty := applyQtyNoise(closeableQty, traits.Skill, rng, cfg.SigmaExec)
	noisyLimitPrice := limitPrice
	if orderType == core.OrderTypeLimit {
		noisyLimitPrice = applyPriceNoise(limitPrice, spread, traits.Skill, rng, cfg.SigmaExec)
	}

	finalQty := clampInt64(noisyQty, 1, closeableQty)

	return assembleOrder(ambient.AccountID, ambient.Instrument, side, orderType, finalQty, noisyLimitPrice), true
}

// resolveTypeAndPrice chooses market vs. limit from the given
// effective aggressiveness, and — for a limit order only — the
// spread-fraction placement price. Shared verbatim by open/add (warped
// aggressiveness) and close (raw aggressiveness); callers differ only
// in which aggressiveness value they pass in. ok is false when a limit
// is wanted but the book is one-sided — a market order never fails
// here.
func resolveTypeAndPrice(
	side string,
	effectiveAggressiveness float64,
	instrument string,
	acc *Accessor,
	cfg Params,
) (orderType string, limitPrice int64, spread int64, ok bool) {
	if effectiveAggressiveness >= cfg.ThetaMarket {
		return core.OrderTypeMarket, 0, 0, true
	}

	bestBid, bestAsk, twoSidedBook := acc.bestBidAsk(instrument)
	if !twoSidedBook {
		// Limit-price placement requires both best bid and ask to compute
		// depthIntoSpread × spread. When the book is one-sided or empty,
		// fall back to a market IOC rather than skipping the agent entirely.
		// The design assumed the seeder always keeps a two-sided book; in
		// practice seeder GTC orders cross each other immediately, draining
		// the book between pulses. An IOC on an empty book cancels cleanly;
		// an IOC on a one-sided book fills at the available price — both are
		// better than permanently silencing a limit-personality agent.
		return core.OrderTypeMarket, 0, 0, true
	}

	spread = bestAsk - bestBid
	depthIntoSpread := effectiveAggressiveness * cfg.KPlace

	// Rounding always moves toward the less-aggressive price (buy
	// down, sell up) so a tick snap never turns a resting order into
	// one that accidentally crosses the book.
	switch side {
	case core.OrderSideBuy:
		limitPrice = int64(math.Floor(float64(bestBid) + depthIntoSpread*float64(spread)))
	case core.OrderSideSell:
		limitPrice = int64(math.Ceil(float64(bestAsk) - depthIntoSpread*float64(spread)))
	}
	return core.OrderTypeLimit, limitPrice, spread, true
}

// bestBidAsk returns the best bid and ask ticks for instrument, and
// whether both sides of the book are present. Spread() only exposes
// the ask-minus-bid distance, which is enough for the perception
// channels but not for placing a resting order at one specific side —
// that needs the two raw touches themselves.
func (acc *Accessor) bestBidAsk(instrument string) (bestBid, bestAsk int64, twoSidedBook bool) {
	quote, present := acc.quotes[instrument]
	if !present || !quote.HasBid || !quote.HasAsk {
		return 0, 0, false
	}
	return quote.BidPrice, quote.AskPrice, true
}

// executionNoiseWidth is the standard deviation of the skill-driven
// noise applied to a sized quantity or placed price: full width at
// skill zero, shrinking to zero as skill approaches one. Global by
// calibration — per-agent variation already lives in the skill trait
// itself.
func executionNoiseWidth(skill float64, sigmaExec float64) float64 {
	return sigmaExec * (1 - skill)
}

// applyQtyNoise perturbs a sized quantity by a fresh multiplicative
// draw. Perturbs magnitude only — the caller still decides side/type
// before and clamps the result after.
func applyQtyNoise(qty int64, skill float64, rng *rand.Rand, sigmaExec float64) int64 {
	width := executionNoiseWidth(skill, sigmaExec)
	if width <= 0 {
		return qty
	}
	return int64(math.Round(float64(qty) * (1 + rng.NormFloat64()*width)))
}

// applyPriceNoise perturbs a placed limit price by a fresh additive
// draw scaled by the instrument's spread. A perturbed buy may end up
// crossing the ask — a low-skill agent accidentally paying up is the
// accepted realism this draw buys — but the draw must never carry the
// price to zero or below: a limit order's price is required strictly
// positive by the broker contract, so this is the one post-noise
// floor every limit price gets, mirroring the post-noise ceiling
// already reapplied to a sized quantity below.
func applyPriceNoise(limitPrice, spread int64, skill float64, rng *rand.Rand, sigmaExec float64) int64 {
	width := executionNoiseWidth(skill, sigmaExec)
	if width <= 0 {
		return limitPrice
	}
	noisyLimitPrice := int64(math.Round(float64(limitPrice) + rng.NormFloat64()*width*float64(spread)))
	return floorInt64(noisyLimitPrice, 1)
}

// floorInt64 raises value up to floor, never down. Used as the
// post-noise safety net for a placed limit price: a noise draw may
// perturb magnitude in either direction, but may never be allowed to
// carry a price to zero or below.
func floorInt64(value, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
}

// clampInt64 restricts value to [floor, ceiling]. Used as the
// post-noise safety net: the one place a capital- or
// closeable-quantity-based ceiling is reapplied after the noise draw
// may have pushed a sized quantity past it.
func clampInt64(value, floor, ceiling int64) int64 {
	if value < floor {
		return floor
	}
	if value > ceiling {
		return ceiling
	}
	return value
}

// assembleOrder packs the computed fields plus the ambient account/
// instrument into the wire request, pairing type with its required
// time-in-force and zeroing the limit price for a market order. This
// is the single place that pairing is enforced, so every request this
// package builds already satisfies the broker's pre-flight contract
// by construction — pre-flight should never have anything to catch.
func assembleOrder(accountID, instrument, side, orderType string, qty, limitPrice int64) core.OrderPunchRequest {
	if orderType == core.OrderTypeMarket {
		return core.OrderPunchRequest{
			AccountID:  accountID,
			Instrument: instrument,
			Side:       side,
			Type:       orderType,
			TIF:        core.TimeInForceImmediateOrCancel,
			Qty:        qty,
			LimitPrice: 0,
		}
	}
	return core.OrderPunchRequest{
		AccountID:  accountID,
		Instrument: instrument,
		Side:       side,
		Type:       orderType,
		TIF:        core.TimeInForceGoodTillCancel,
		Qty:        qty,
		LimitPrice: limitPrice,
	}
}
