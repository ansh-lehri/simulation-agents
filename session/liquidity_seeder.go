package session

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/decision"
	"simulation-agents/store"
	"simulation-agents/transport"
)

// seederSide marks which side of the book one seeder identity works.
type seederSide int

const (
	seederSell seederSide = iota
	seederBuy
)

// seederIdentity is one dumb operator account — no trait struct, no
// Tier-4 decision tree, just an account id and which side it works.
type seederIdentity struct {
	userID    string
	accountID string
	side      seederSide
}

// seedLevel is one price level a seeder round will punch an order at.
type seedLevel struct {
	side  string // core.OrderSideBuy / core.OrderSideSell
	price int64
}

// runLiquiditySeeder bootstraps a two-sided, moving book for one
// exchange — see docs/agents/LIQUIDITY_SEEDER.md. Spawned as its own
// goroutine from Run, independent of the real scheduler: it opens a
// handful of operator identities, seeds sellers with inventory, then
// repeatedly computes price levels and punches resting/crossing limit
// orders — continuously for the first LiquiditySeeder.ContinuousFraction
// of the real session length, then as sparse random top-ups until
// session end. Never blocks or is blocked by the real agent tick loop.
func (runner *Runner) runLiquiditySeeder(
	ctx context.Context,
	request transport.BringUpRequest,
	rng *rand.Rand,
) {
	params := runner.dependencies.Params.LiquiditySeeder
	if params.SellerCount <= 0 && params.BuyerCount <= 0 {
		return
	}

	identities, setupErr := runner.openSeederIdentities(ctx, request, params)
	if setupErr != nil {
		runner.dependencies.Logger.Warn("session: liquidity seeder setup failed, skipping",
			"exchange_id", request.ExchangeID,
			"err", setupErr,
		)
		return
	}
	runner.dependencies.Logger.Info("session: liquidity seeder identities ready",
		"exchange_id", request.ExchangeID,
		"seller_count", params.SellerCount,
		"buyer_count", params.BuyerCount,
	)

	if waitErr := runner.dependencies.Clock.WaitUntil(ctx, request.SessionStart); waitErr != nil {
		return
	}

	sessionDuration := request.SessionEnd.Sub(request.SessionStart)
	continuousUntil := request.SessionStart.Add(time.Duration(float64(sessionDuration) * params.ContinuousFraction))

	for {
		if ctx.Err() != nil {
			return
		}
		now := runner.dependencies.Clock.Now()
		if now.After(request.SessionEnd) {
			return
		}

		runner.seedOneRound(ctx, request, identities, params, rng)

		var sleepDuration time.Duration
		if now.Before(continuousUntil) {
			sleepDuration = 1 * time.Second
		} else {
			cadenceRange := params.ReentryCadenceMax - params.ReentryCadenceMin
			sleepDuration = params.ReentryCadenceMin
			if cadenceRange > 0 {
				sleepDuration += time.Duration(rng.Float64() * float64(cadenceRange))
			}
		}
		select {
		case <-time.After(sleepDuration):
		case <-ctx.Done():
			return
		}
	}
}

// openSeederIdentities opens and joins every seeder's operator account
// onto this session. Any failure aborts the whole seeder (no partial
// liquidity bootstrap) — the real agent population is unaffected
// either way, since the seeder is fully independent of it.
func (runner *Runner) openSeederIdentities(
	ctx context.Context,
	request transport.BringUpRequest,
	params LiquiditySeederParams,
) ([]*seederIdentity, error) {
	identities := make([]*seederIdentity, 0, params.SellerCount+params.BuyerCount)
	for i := 0; i < params.SellerCount; i++ {
		identity, err := runner.openOneSeeder(ctx, request, fmt.Sprintf("seeder-sell-%s-%d", request.ExchangeID, i), seederSell)
		if err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	for i := 0; i < params.BuyerCount; i++ {
		identity, err := runner.openOneSeeder(ctx, request, fmt.Sprintf("seeder-buy-%s-%d", request.ExchangeID, i), seederBuy)
		if err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	return identities, nil
}

func (runner *Runner) openOneSeeder(
	ctx context.Context,
	request transport.BringUpRequest,
	userID string,
	side seederSide,
) (*seederIdentity, error) {
	accountID, openErr := runner.dependencies.Core.OpenOperatorAccount(ctx, userID)
	if openErr != nil {
		return nil, fmt.Errorf("session: open operator account %q: %w", userID, openErr)
	}

	switch request.SessionType {
	case agent.PopulationCanonical:
		if _, joinErr := runner.dependencies.Core.JoinCanonicalAsOperator(ctx, userID, request.Country); joinErr != nil {
			return nil, fmt.Errorf("session: seeder join canonical %q: %w", userID, joinErr)
		}
	case agent.PopulationOnDemand:
		if _, joinErr := runner.dependencies.Core.JoinOnDemandAsOperator(ctx, userID, request.ExchangeID); joinErr != nil {
			return nil, fmt.Errorf("session: seeder join on-demand %q: %w", userID, joinErr)
		}
	default:
		return nil, fmt.Errorf("session: seeder received unknown session type %q", request.SessionType)
	}
	return &seederIdentity{userID: userID, accountID: accountID, side: side}, nil
}

// seedOneRound computes fresh price levels per instrument and punches
// one order per (seeder, level) pair this round. Sellers seed
// inventory before their first ask on an instrument (tracked
// in-memory per round — a seller re-seeding every round is wasteful
// but harmless, since seed-inventory only credits additional units).
func (runner *Runner) seedOneRound(
	ctx context.Context,
	request transport.BringUpRequest,
	identities []*seederIdentity,
	params LiquiditySeederParams,
	rng *rand.Rand,
) {
	tf := runner.dependencies.Params.ConfiguredTimeframes[0]
	for _, instrument := range request.Instruments {
		levels := runner.computeSeedLevelsForInstrument(ctx, request, instrument, tf, params, rng)
		if len(levels) == 0 {
			continue
		}
		buyLevels := filterLevelsBySide(levels, core.OrderSideBuy)
		sellLevels := filterLevelsBySide(levels, core.OrderSideSell)

		for _, identity := range identities {
			// The order's side is the identity's own designated role,
			// never re-derived from the level — a buyer identity never
			// has inventory seeded, so letting it place a sell (the
			// original bug here) always fails "insufficient position".
			var orderSide string
			var sideLevels []seedLevel
			if identity.side == seederSell {
				orderSide = core.OrderSideSell
				sideLevels = sellLevels
			} else {
				orderSide = core.OrderSideBuy
				sideLevels = buyLevels
			}
			if len(sideLevels) == 0 {
				continue
			}
			level := sideLevels[rng.IntN(len(sideLevels))]

			qtyRange := params.OrderSizeMax - params.OrderSizeMin
			qty := params.OrderSizeMin
			if qtyRange > 0 {
				qty += rng.Int64N(qtyRange)
			}

			logFields := []any{
				"exchange_id", request.ExchangeID,
				"account_id", identity.accountID,
				"instrument", instrument,
				"role", identitySideLabel(identity.side),
				"side", orderSide,
				"qty", qty,
				"price", level.price,
			}

			if identity.side == seederSell {
				if _, seedErr := runner.dependencies.Core.SeedInventory(ctx, core.SeedInventoryRequest{
					AccountID:  identity.accountID,
					Instrument: instrument,
					Qty:        qty,
					EntryPrice: level.price,
				}); seedErr != nil {
					runner.dependencies.Logger.Warn("session: liquidity seeder seed-inventory failed",
						append(logFields, "err", seedErr)...,
					)
					continue
				}
			}
			punchErr := runner.dependencies.Core.PunchOrder(ctx, core.OrderPunchRequest{
				AccountID:  identity.accountID,
				Instrument: instrument,
				Side:       orderSide,
				Type:       core.OrderTypeLimit,
				TIF:        core.TimeInForceGoodTillCancel,
				Qty:        qty,
				LimitPrice: level.price,
			})
			if punchErr != nil {
				runner.dependencies.Logger.Warn("session: liquidity seeder punch failed",
					append(logFields, "err", punchErr)...,
				)
				continue
			}
			runner.dependencies.Logger.Info("session: liquidity seeder punched", logFields...)
		}
	}
}

// filterLevelsBySide returns the subset of levels matching side.
func filterLevelsBySide(levels []seedLevel, side string) []seedLevel {
	filtered := make([]seedLevel, 0, len(levels))
	for _, level := range levels {
		if level.side == side {
			filtered = append(filtered, level)
		}
	}
	return filtered
}

// identitySideLabel renders a seederSide for logs.
func identitySideLabel(side seederSide) string {
	if side == seederSell {
		return "seller"
	}
	return "buyer"
}

// computeSeedLevelsForInstrument builds this round's price levels for
// one instrument, reusing decision.Accessor exactly as the real
// decide() path does: CurrentPrice decides whether there is any
// usable price at all (live quote, else candle fallback), and Candles
// supplies the latest bar's H/L/C for the floor-trader pivot formula.
// Returns nil when there is truly no data yet (cold canonical genesis
// with no reference price plumbed through the bring-up request yet —
// on-demand never hits this, it always has canonical history to pull).
func (runner *Runner) computeSeedLevelsForInstrument(
	ctx context.Context,
	request transport.BringUpRequest,
	instrument string,
	timeframe string,
	params LiquiditySeederParams,
	rng *rand.Rand,
) []seedLevel {
	candles, _ := runner.dependencies.MarketData.ReadCandles(ctx, request.ExchangeID, instrument, timeframe, 1)
	quote, present, _ := runner.dependencies.MarketData.ReadQuote(ctx, request.ExchangeID, instrument)

	acc := decision.NewAccessor(
		map[string]map[string][]store.Candle{instrument: {timeframe: candles}},
		map[string]store.Quote{instrument: quote},
		map[string]bool{instrument: present},
		decision.TickContext{
			Now:          runner.dependencies.Clock.Now(),
			SessionStart: request.SessionStart,
			SessionEnd:   request.SessionEnd,
		},
	)

	anchorClose, priceOK := acc.CurrentPrice(instrument, timeframe)
	if !priceOK {
		return nil
	}

	bars := acc.Candles(instrument, timeframe)
	if len(bars) == 0 {
		// A live quote exists but no candle bar does — fall back to a
		// flat pivot (no H/L spread to work from) centered on the
		// live price, still randomized within the usual band.
		return randomLevelsAround(anchorClose, params, rng)
	}
	latestBar := bars[len(bars)-1]
	return pivotLevels(latestBar.High, latestBar.Low, anchorClose, params, rng)
}

// pivotLevels computes the classic floor-trader pivot set (up to
// PivotLevelCount of R1..R3 as sell/resistance levels and S1..S3 as
// buy/support levels) plus RandomLevelCount extra orders scattered
// within RandomLevelSpreadBps of the anchor close.
func pivotLevels(high, low, closePrice int64, params LiquiditySeederParams, rng *rand.Rand) []seedLevel {
	pivot := (high + low + closePrice) / 3
	resistances := []int64{
		2*pivot - low,
		pivot + (high - low),
		high + 2*(pivot-low),
	}
	supports := []int64{
		2*pivot - high,
		pivot - (high - low),
		low - 2*(high-pivot),
	}

	levels := make([]seedLevel, 0, params.PivotLevelCount*2+params.RandomLevelCount)
	for i := 0; i < params.PivotLevelCount && i < len(resistances); i++ {
		levels = append(levels, seedLevel{side: core.OrderSideSell, price: resistances[i]})
	}
	for i := 0; i < params.PivotLevelCount && i < len(supports); i++ {
		levels = append(levels, seedLevel{side: core.OrderSideBuy, price: supports[i]})
	}
	return append(levels, randomLevelsAround(closePrice, params, rng)...)
}

// randomLevelsAround scatters RandomLevelCount levels within
// RandomLevelSpreadBps of anchor, side chosen by coin flip.
func randomLevelsAround(anchor int64, params LiquiditySeederParams, rng *rand.Rand) []seedLevel {
	spread := anchor * params.RandomLevelSpreadBps / 10_000
	levels := make([]seedLevel, 0, params.RandomLevelCount)
	for i := 0; i < params.RandomLevelCount; i++ {
		offset := int64((rng.Float64()*2 - 1) * float64(spread))
		side := core.OrderSideBuy
		if rng.Float64() < 0.5 {
			side = core.OrderSideSell
		}
		levels = append(levels, seedLevel{side: side, price: anchor + offset})
	}
	return levels
}
