package orchestrator

import (
	"fmt"
	"log/slog"
	"math/rand/v2"

	"simulation-agents/agent"
)

// CapitalResolver fills each agent's session TradingCapital (A) using
// the three-case dispatch the bring-up design pins:
//
//   - New agents (freshly minted this bring-up, whichever population):
//     draw A bounded by the broker's funded cash observed at
//     account-open. The AccountOpenClient has already filled FundedCash
//     per new agent before this step runs.
//
//   - On-demand existing agents (leased from the pool, no account-open
//     this session): draw A fresh against the default funding ceiling
//     — these agents are the leased-pool shape where session money is
//     a per-session draw.
//
//   - Canonical existing agents (revived members of the long-lived
//     population): keep the persisted A untouched. Canonical money
//     evolves across sessions via a later-tier P&L-to-A fold; the
//     capital resolver's job is explicitly NOT to re-draw it.
//
// The resolver is a pure in-memory step over an in-flight Population —
// no I/O, no Core call, no store write. It consumes the FundedCash each
// new agent carries (populated by AccountOpenClient) and the frozen
// CapitalSize each trait vector carries.
type CapitalResolver struct {
	capitalParams agent.CapitalParams
	logger        *slog.Logger
}

// NewCapitalResolver wires a CapitalResolver. The capital params are
// the dial set agent.DrawTradingCapital reads (Floor, JitterSpread,
// DefaultFundedCash); they live on OrchestratorConfig and the
// composition root passes them through.
func NewCapitalResolver(capitalParams agent.CapitalParams, logger *slog.Logger) (*CapitalResolver, error) {
	if logger == nil {
		return nil, fmt.Errorf("orchestrator: logger is required")
	}
	return &CapitalResolver{capitalParams: capitalParams, logger: logger}, nil
}

// Resolve applies the three-case dispatch over a Population in place.
// The sessionType decides whether leased existing agents redraw or
// pass through: on-demand existing redraw against the default ceiling;
// canonical existing keep their persisted A.
func (resolver *CapitalResolver) Resolve(
	population *Population,
	sessionType string,
	rng *rand.Rand,
) {
	// Case 1: new agents — draw A bounded by each agent's broker funded
	// cash. CapitalSize is the frozen scale; FundedCash came from the
	// account-open step just before this resolver ran.
	for index := range population.New {
		population.New[index].TradingCapital = agent.DrawTradingCapital(
			population.New[index].Traits.CapitalSize,
			population.New[index].FundedCash,
			rng,
			resolver.capitalParams,
		)
	}

	switch sessionType {
	case SessionTypeOnDemand:
		// Case 2: on-demand existing — these agents skipped the
		// account-open step this session, so there is no fresh funded
		// cash reading to bound against. Passing 0 as the funded cash
		// deliberately triggers agent.DrawTradingCapital's documented
		// fallback to DefaultFundedCash.
		for index := range population.Existing {
			population.Existing[index].TradingCapital = agent.DrawTradingCapital(
				population.Existing[index].Traits.CapitalSize,
				0,
				rng,
				resolver.capitalParams,
			)
		}
	case SessionTypeCanonical:
		// Case 3: canonical existing — keep persisted TradingCapital
		// untouched. The money evolves across sessions via a later
		// P&L-to-A fold; the resolver must not re-draw it.
	}

	resolver.logger.Info(
		"capital resolve pass end",
		"session_type", sessionType,
		"existing_count", len(population.Existing),
		"new_count", len(population.New),
	)
}
