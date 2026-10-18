package orchestrator

import (
	"math/rand/v2"
	"reflect"
	"testing"

	"simulation-agents/agent"
)

// capitalResolverTestParams returns a CapitalParams tuned so the test
// assertions can see the three-case ceiling differences clearly. The
// sentinel DefaultFundedCash is used to prove the "funded=0 falls back
// to default" branch; Floor is 1 so no clamp fires in the happy cases.
const sentinelDefaultFundedCash = int64(999_999_999)

func capitalResolverTestParams() agent.CapitalParams {
	return agent.CapitalParams{
		Floor:             1,
		JitterSpread:      0,
		DefaultFundedCash: sentinelDefaultFundedCash,
	}
}

func mustBuildCapitalResolver(t *testing.T) *CapitalResolver {
	t.Helper()
	resolver, err := NewCapitalResolver(capitalResolverTestParams(), discardLogger())
	if err != nil {
		t.Fatalf("NewCapitalResolver: %v", err)
	}
	return resolver
}

// seededRng builds a *rand.Rand with a known seed so draws replay.
func seededRng(seedWordOne, seedWordTwo uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seedWordOne, seedWordTwo))
}

// ---- NewCapitalResolver -------------------------------------------

func TestNewCapitalResolverRejectsNilLogger(t *testing.T) {
	if _, err := NewCapitalResolver(capitalResolverTestParams(), nil); err == nil {
		t.Fatalf("expected nil logger error, got nil")
	}
}

// ---- Case 1: new agents -------------------------------------------

func TestResolveDrawsNewAgentCapitalBoundedByFundedCash(t *testing.T) {
	resolver := mustBuildCapitalResolver(t)

	// A new agent whose frozen scale (CapitalSize) is 50_000 and whose
	// broker-funded cash is 25_000. With JitterSpread=0 the draw is
	// exactly min(scale, funded) = 25_000 (bounded by the ceiling).
	population := Population{
		New: []agent.Agent{
			{
				ID:         "new-1",
				Population: agent.PopulationCanonical,
				Traits:     agent.TraitVector{CapitalSize: 50_000},
				FundedCash: 25_000,
			},
		},
	}
	resolver.Resolve(&population, SessionTypeCanonical, seededRng(1, 1))

	if population.New[0].TradingCapital != 25_000 {
		t.Fatalf("new agent capital = %d, want 25_000 (bounded by funded)", population.New[0].TradingCapital)
	}
}

func TestResolveNewAgentCapitalUsesFundedCeilingNotDefault(t *testing.T) {
	// A new agent with FundedCash < DefaultFundedCash must use the
	// funded cash as the ceiling — the fallback is only for the
	// on-demand-existing branch where funded cash is unknown.
	resolver := mustBuildCapitalResolver(t)
	population := Population{
		New: []agent.Agent{
			{
				ID:         "new-1",
				Traits:     agent.TraitVector{CapitalSize: sentinelDefaultFundedCash * 2}, // scale exceeds both ceilings
				FundedCash: 10_000,
			},
		},
	}
	resolver.Resolve(&population, SessionTypeCanonical, seededRng(1, 1))

	if population.New[0].TradingCapital != 10_000 {
		t.Fatalf("new agent capital = %d, want 10_000 (funded ceiling, not default)",
			population.New[0].TradingCapital)
	}
}

// ---- Case 2: on-demand existing -----------------------------------

func TestResolveOnDemandExistingUsesDefaultFundedCashCeiling(t *testing.T) {
	// An on-demand existing agent skipped the account-open step, so
	// FundedCash is zero. The resolver must fall back to
	// DefaultFundedCash as the ceiling. Scale > default so the clamp
	// fires at exactly DefaultFundedCash — the proof of the fallback.
	resolver := mustBuildCapitalResolver(t)
	population := Population{
		Existing: []agent.Agent{
			{
				ID:         "lease-1",
				Population: agent.PopulationOnDemand,
				Traits:     agent.TraitVector{CapitalSize: sentinelDefaultFundedCash + 1_000_000},
				FundedCash: 0,
			},
		},
	}
	resolver.Resolve(&population, SessionTypeOnDemand, seededRng(2, 2))

	if population.Existing[0].TradingCapital != sentinelDefaultFundedCash {
		t.Fatalf("on-demand existing capital = %d, want %d (default fallback)",
			population.Existing[0].TradingCapital, sentinelDefaultFundedCash)
	}
}

// ---- Case 3: canonical existing -----------------------------------

func TestResolveCanonicalExistingKeepsPersistedCapital(t *testing.T) {
	// A revived canonical agent carries a persisted TradingCapital
	// that has evolved across sessions. The resolver must NOT touch
	// it. Sentinel pre-set value: if the resolver mutates it, the
	// test fails loudly.
	const persistedCapital = int64(777_777)
	resolver := mustBuildCapitalResolver(t)
	population := Population{
		Existing: []agent.Agent{
			{
				ID:             "revived-1",
				Population:     agent.PopulationCanonical,
				Traits:         agent.TraitVector{CapitalSize: 50_000},
				TradingCapital: persistedCapital,
			},
		},
	}
	resolver.Resolve(&population, SessionTypeCanonical, seededRng(3, 3))

	if population.Existing[0].TradingCapital != persistedCapital {
		t.Fatalf("canonical existing capital mutated: got %d, want %d",
			population.Existing[0].TradingCapital, persistedCapital)
	}
}

// ---- Mixed cases over one population ------------------------------

func TestResolveMixedCanonicalPopulationNewDrawnExistingPreserved(t *testing.T) {
	// A canonical bring-up with 2 revived members and 3 freshly minted
	// ones. The 2 revived keep their persisted A; the 3 new draw fresh
	// A against their respective funded cash ceilings.
	resolver := mustBuildCapitalResolver(t)
	population := Population{
		Existing: []agent.Agent{
			{ID: "rev-1", Traits: agent.TraitVector{CapitalSize: 100_000}, TradingCapital: 50_000},
			{ID: "rev-2", Traits: agent.TraitVector{CapitalSize: 100_000}, TradingCapital: 60_000},
		},
		New: []agent.Agent{
			{ID: "new-1", Traits: agent.TraitVector{CapitalSize: 100_000}, FundedCash: 10_000},
			{ID: "new-2", Traits: agent.TraitVector{CapitalSize: 100_000}, FundedCash: 20_000},
			{ID: "new-3", Traits: agent.TraitVector{CapitalSize: 100_000}, FundedCash: 30_000},
		},
	}
	resolver.Resolve(&population, SessionTypeCanonical, seededRng(4, 4))

	wantExisting := []int64{50_000, 60_000}
	for index, wantValue := range wantExisting {
		if population.Existing[index].TradingCapital != wantValue {
			t.Fatalf("existing[%d] capital = %d, want %d (preserved)",
				index, population.Existing[index].TradingCapital, wantValue)
		}
	}
	wantNew := []int64{10_000, 20_000, 30_000}
	for index, wantValue := range wantNew {
		if population.New[index].TradingCapital != wantValue {
			t.Fatalf("new[%d] capital = %d, want %d (bounded by funded)",
				index, population.New[index].TradingCapital, wantValue)
		}
	}
}

func TestResolveMixedOnDemandPopulationNewUsesFundedExistingUsesDefault(t *testing.T) {
	// On-demand: existing (leased) use default fallback; new (minted)
	// use their broker-funded ceiling. Prove both branches in one
	// population.
	resolver := mustBuildCapitalResolver(t)
	population := Population{
		Existing: []agent.Agent{
			{
				ID:     "lease-1",
				Traits: agent.TraitVector{CapitalSize: sentinelDefaultFundedCash + 10},
			},
			{
				ID:     "lease-2",
				Traits: agent.TraitVector{CapitalSize: sentinelDefaultFundedCash + 10},
			},
		},
		New: []agent.Agent{
			{ID: "new-1", Traits: agent.TraitVector{CapitalSize: 100_000}, FundedCash: 15_000},
		},
	}
	resolver.Resolve(&population, SessionTypeOnDemand, seededRng(5, 5))

	for index, existingAgent := range population.Existing {
		if existingAgent.TradingCapital != sentinelDefaultFundedCash {
			t.Fatalf("on-demand existing[%d] capital = %d, want %d (default fallback)",
				index, existingAgent.TradingCapital, sentinelDefaultFundedCash)
		}
	}
	if population.New[0].TradingCapital != 15_000 {
		t.Fatalf("on-demand new capital = %d, want 15_000 (funded ceiling)",
			population.New[0].TradingCapital)
	}
}

// ---- Determinism --------------------------------------------------

func TestResolveIsDeterministicUnderFixedSeed(t *testing.T) {
	buildPopulation := func() *Population {
		return &Population{
			New: []agent.Agent{
				{Traits: agent.TraitVector{CapitalSize: 123_456}, FundedCash: 50_000},
				{Traits: agent.TraitVector{CapitalSize: 789_012}, FundedCash: 80_000},
			},
		}
	}

	resolverWithJitter, err := NewCapitalResolver(
		agent.CapitalParams{Floor: 1, JitterSpread: 0.2, DefaultFundedCash: sentinelDefaultFundedCash},
		discardLogger(),
	)
	if err != nil {
		t.Fatalf("NewCapitalResolver: %v", err)
	}

	firstPopulation := buildPopulation()
	secondPopulation := buildPopulation()
	resolverWithJitter.Resolve(firstPopulation, SessionTypeCanonical, seededRng(7, 7))
	resolverWithJitter.Resolve(secondPopulation, SessionTypeCanonical, seededRng(7, 7))

	if !reflect.DeepEqual(firstPopulation, secondPopulation) {
		t.Fatalf("determinism broken: first=%+v second=%+v", firstPopulation, secondPopulation)
	}
}

// ---- Edge: on-demand with zero existing (shortfall only) ----------

func TestResolveOnDemandWithZeroExistingSkipsExistingLoop(t *testing.T) {
	// The leased-pool skipped-open branch only fires when there is at
	// least one existing agent. An all-new on-demand bring-up must
	// behave identically to a canonical all-new bring-up on the New
	// slice.
	resolver := mustBuildCapitalResolver(t)
	population := Population{
		New: []agent.Agent{
			{Traits: agent.TraitVector{CapitalSize: 100_000}, FundedCash: 40_000},
		},
	}
	resolver.Resolve(&population, SessionTypeOnDemand, seededRng(8, 8))

	if population.New[0].TradingCapital != 40_000 {
		t.Fatalf("new agent capital = %d, want 40_000", population.New[0].TradingCapital)
	}
}
