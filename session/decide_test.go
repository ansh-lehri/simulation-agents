package session

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"

	"simulation-agents/agent"
	"simulation-agents/decision"
	"simulation-agents/store"
)

// decideMarket builds a minimal snapshot for one instrument: 25 flat
// candles at the given price, full bid/ask, phase "open", instrument present.
func decideMarket(inst, tf string, price int64) *snapshotView {
	candles := make([]store.Candle, 25)
	for i := range candles {
		candles[i] = store.Candle{
			Open: price, High: price + 2, Low: price - 2,
			Close: price, Volume: 100,
		}
	}
	return &snapshotView{
		candles: map[string]map[string][]store.Candle{
			inst: {tf: candles},
		},
		quote: map[string]store.Quote{
			inst: {
				LastPrice: price, HasLast: true,
				BidPrice: price - 5, AskPrice: price + 5,
				HasBid: true, HasAsk: true,
				Phase: "open",
			},
		},
		present: map[string]bool{inst: true},
	}
}

// decideTimeTraits returns a TraitVector with the time-of-session channel
// on — sufficient for ShouldReperceive to coast correctly after the first
// Perceive call (same phase → no change → gate returns false).
func decideTimeTraits() agent.TraitVector {
	return agent.TraitVector{
		Skill: 1.0,
		Diet:  &agent.Diet{TimeOfSession: true},
		Perception: &agent.PerceptionProfile{
			TimeOfSession:     &agent.ChannelBlock{},
			RereadSensitivity: map[string]float64{},
		},
	}
}

// decidePerceptionParams returns calibration with zero noise (deterministic
// output) and a standard MA saturation multiple.
func decidePerceptionParams() decision.PerceptionParams {
	return decision.PerceptionParams{SigmaPerc: 0, MASaturationMultiple: 2.0}
}

func makeDecideInput(
	traits agent.TraitVector,
	market *snapshotView,
	timeframes []string,
	runtimeStore RuntimeStore,
) decideInput {
	return decideInput{
		agentState: &workingAgent{
			traits:       traits,
			agentRNG:     rand.New(rand.NewPCG(42, 0)),
			perception:   make(map[string]*instrumentMemory),
			memoryHandle: newRuntimeHandle(runtimeStore, context.Background(), "exch", "agent"),
		},
		market:           market,
		tick:             decision.TickContext{},
		perceptionParams: decidePerceptionParams(),
		timeframes:       timeframes,
	}
}

// --- Early-exit guards ---

func TestDecide_EmptyTimeframes_NoPanic(t *testing.T) {
	input := makeDecideInput(
		decideTimeTraits(),
		decideMarket("X", "1m", 1000),
		nil, // no timeframes
		stubRuntimeStore{},
	)
	req, ok := decide(input)
	if ok || req.Instrument != "" {
		t.Errorf("empty timeframes must return no punch; got punch=%v ok=%v", req, ok)
	}
}

func TestDecide_EmptyMarket_NoPanic(t *testing.T) {
	input := makeDecideInput(
		decideTimeTraits(),
		&snapshotView{
			candles: map[string]map[string][]store.Candle{},
			quote:   map[string]store.Quote{},
			present: map[string]bool{},
		},
		[]string{"1m"},
		stubRuntimeStore{},
	)
	req, ok := decide(input)
	if ok || req.Instrument != "" {
		t.Errorf("empty market must return no punch; got punch=%v ok=%v", req, ok)
	}
}

// --- Perception memory ---

func TestDecide_FirstCallPopulatesMemory(t *testing.T) {
	const inst = "TICK"
	input := makeDecideInput(
		decideTimeTraits(),
		decideMarket(inst, "1m", 2000),
		[]string{"1m"},
		stubRuntimeStore{},
	)
	decide(input)

	mem := input.agentState.perception[inst]
	if mem == nil {
		t.Fatal("perception memory for instrument must be non-nil after first decide call")
	}
	if mem.lastBundle == nil {
		t.Error("lastBundle must be non-nil after first decide call")
	}
	if mem.refs.LastPhase == "" {
		t.Error("refs.LastPhase must be populated after first decide call")
	}
}

func TestDecide_SecondCallCoastsOnUnchangedMarket(t *testing.T) {
	// First call: empty refs → gate fires → Perceive runs → refs populated.
	// Second call: same snapshot, same phase → gate returns false → Perceive
	// not called → lastBundle pointer must remain the same object.
	const inst = "TICK"
	input := makeDecideInput(
		decideTimeTraits(),
		decideMarket(inst, "1m", 2000),
		[]string{"1m"},
		stubRuntimeStore{},
	)
	decide(input)

	bundleAfterFirst := input.agentState.perception[inst].lastBundle
	if bundleAfterFirst == nil {
		t.Fatal("lastBundle must be non-nil after first decide call")
	}

	decide(input)

	if input.agentState.perception[inst].lastBundle != bundleAfterFirst {
		t.Error("lastBundle pointer must not change on second call when market is unchanged (gate must coast)")
	}
}

// --- Stub returns ---

func TestDecide_AlwaysReturnsNoPunch(t *testing.T) {
	input := makeDecideInput(
		decideTimeTraits(),
		decideMarket("X", "1m", 500),
		[]string{"1m"},
		stubRuntimeStore{},
	)
	_, ok := decide(input)
	if ok {
		t.Error("decide must return false (4b not yet implemented)")
	}
}

// --- Error resilience ---

type errOnPerceptionRefsStore struct {
	stubRuntimeStore
}

func (errOnPerceptionRefsStore) SetPerceptionRefs(_ context.Context, _ string, _ agent.AgentID, _ string, _ store.PerceptionRefs) error {
	return fmt.Errorf("redis unavailable")
}

func TestDecide_SetPerceptionRefsError_NoCrash(t *testing.T) {
	// A Redis write error from SetPerceptionRefs must be silently swallowed;
	// the perception loop must complete and update in-memory state regardless.
	const inst = "ERR"
	input := makeDecideInput(
		decideTimeTraits(),
		decideMarket(inst, "1m", 1500),
		[]string{"1m"},
		errOnPerceptionRefsStore{},
	)
	decide(input)

	// In-memory state must still be updated despite the Redis failure.
	if input.agentState.perception[inst] == nil {
		t.Fatal("perception memory must be populated even when Redis write fails")
	}
	if input.agentState.perception[inst].refs.LastPhase == "" {
		t.Error("refs.LastPhase must be populated even when Redis write fails")
	}
}
