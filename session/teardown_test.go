package session

import (
	"errors"
	"reflect"
	"testing"

	"simulation-agents/agent"
)

func threeAgentViews() []workingAgentView {
	return []workingAgentView{
		{AgentID: "a-1", CapitalHandle: "handle-1", TradingCapital: 10_000},
		{AgentID: "a-2", CapitalHandle: "handle-2", TradingCapital: 20_000},
		{AgentID: "a-3", CapitalHandle: "handle-3", TradingCapital: 30_000},
	}
}

func fixedCashReader(cash int64) func(string) (int64, error) {
	return func(_ string) (int64, error) {
		return cash, nil
	}
}

// When the expected-quit intensity equals the population size, the
// per-agent quit probability clamps to 1 — every agent retires, and
// (A2 before A3, same pass) not one of them also receives a capital
// fold decision.
func TestComputeCanonicalTeardown_FullQuitProbabilityRetiresEveryoneAndFoldsNoOne(t *testing.T) {
	agentViews := threeAgentViews()
	decisions := computeCanonicalTeardown(
		seededRNG(), agentViews, len(agentViews),
		float64(len(agentViews)), // expectedQuitsPerSession == P -> probability 1
		100_000,
		fixedCashReader(150_000),
	)

	if len(decisions) != len(agentViews) {
		t.Fatalf("decision count: got %d want %d (one RETIRE per agent, no folds)", len(decisions), len(agentViews))
	}
	for _, decision := range decisions {
		if decision.Reason != ReasonRetire {
			t.Errorf("agent %s: reason = %q, want %q", decision.AgentID, decision.Reason, ReasonRetire)
		}
	}
}

// When the expected-quit intensity is zero, nobody retires, and every
// agent instead receives a capital fold computed as
// max(0, old + (cash - brokerDefault)).
func TestComputeCanonicalTeardown_ZeroQuitProbabilityFoldsEveryone(t *testing.T) {
	agentViews := threeAgentViews()
	const brokerDefault = int64(100_000)
	const tradingCash = int64(130_000) // +30,000 delta for every agent

	decisions := computeCanonicalTeardown(
		seededRNG(), agentViews, len(agentViews),
		0, // expectedQuitsPerSession == 0 -> probability 0
		brokerDefault,
		fixedCashReader(tradingCash),
	)

	if len(decisions) != len(agentViews) {
		t.Fatalf("decision count: got %d want %d (one CAPITAL per agent, no retires)", len(decisions), len(agentViews))
	}
	byAgent := make(map[agent.AgentID]LifecycleDecision, len(decisions))
	for _, decision := range decisions {
		byAgent[decision.AgentID] = decision
	}
	for _, view := range agentViews {
		decision, ok := byAgent[view.AgentID]
		if !ok {
			t.Fatalf("agent %s: missing decision", view.AgentID)
		}
		if decision.Reason != ReasonCapital {
			t.Errorf("agent %s: reason = %q, want %q", view.AgentID, decision.Reason, ReasonCapital)
		}
		wantCapital := view.TradingCapital + (tradingCash - brokerDefault)
		if decision.NewCapital != wantCapital {
			t.Errorf("agent %s: NewCapital = %d, want %d", view.AgentID, decision.NewCapital, wantCapital)
		}
	}
}

// The fold clamps at zero: a session loss larger than the agent's
// entire carried capital must never produce a negative NewCapital.
func TestComputeCanonicalTeardown_FoldClampsAtZeroOnLargeLoss(t *testing.T) {
	agentViews := []workingAgentView{
		{AgentID: "a-1", CapitalHandle: "handle-1", TradingCapital: 5_000},
	}
	const brokerDefault = int64(100_000)
	const tradingCash = int64(0) // delta of -100,000, far exceeding the 5,000 carried capital

	decisions := computeCanonicalTeardown(
		seededRNG(), agentViews, len(agentViews),
		0, brokerDefault, fixedCashReader(tradingCash),
	)

	if len(decisions) != 1 {
		t.Fatalf("decision count: got %d want 1", len(decisions))
	}
	if decisions[0].NewCapital != 0 {
		t.Errorf("NewCapital = %d, want 0 (clamped)", decisions[0].NewCapital)
	}
}

// An agent whose durable cash read fails receives no decision at all
// — neither a fold nor any other write — leaving its persisted
// capital exactly as it was. Other agents are unaffected.
func TestComputeCanonicalTeardown_CashReadFailureSkipsThatAgentOnly(t *testing.T) {
	agentViews := threeAgentViews()
	readErr := errors.New("transient core blip")

	decisions := computeCanonicalTeardown(
		seededRNG(), agentViews, len(agentViews),
		0, 100_000,
		func(capitalHandle string) (int64, error) {
			if capitalHandle == "handle-2" {
				return 0, readErr
			}
			return 130_000, nil
		},
	)

	if len(decisions) != 2 {
		t.Fatalf("decision count: got %d want 2 (agent a-2 skipped)", len(decisions))
	}
	for _, decision := range decisions {
		if decision.AgentID == "a-2" {
			t.Fatalf("agent a-2 must not receive a decision after its cash read failed")
		}
	}
}

// Running with an identically-seeded RNG and the identical input slice
// twice must produce an identical decision list — the determinism
// every seeded/stateful part of this codebase is required to hold.
func TestComputeCanonicalTeardown_DeterministicUnderFixedSeed(t *testing.T) {
	agentViews := threeAgentViews()
	runOnce := func() []LifecycleDecision {
		return computeCanonicalTeardown(
			seededRNG(), agentViews, len(agentViews),
			1.5, // a mixed probability so both retire and fold paths are exercised
			100_000,
			fixedCashReader(115_000),
		)
	}

	first := runOnce()
	second := runOnce()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("decisions differ across runs under the same seed:\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

// A zero or negative population size must never divide by zero or
// panic — it simply skips the quit roll and folds every agent.
func TestComputeCanonicalTeardown_ZeroPopulationSizeSkipsRollWithoutPanic(t *testing.T) {
	agentViews := threeAgentViews()
	decisions := computeCanonicalTeardown(
		seededRNG(), agentViews, 0,
		0.3, 100_000, fixedCashReader(100_000),
	)
	if len(decisions) != len(agentViews) {
		t.Fatalf("decision count: got %d want %d (roll skipped, every agent folded)", len(decisions), len(agentViews))
	}
	for _, decision := range decisions {
		if decision.Reason != ReasonCapital {
			t.Errorf("agent %s: reason = %q, want %q", decision.AgentID, decision.Reason, ReasonCapital)
		}
	}
}
