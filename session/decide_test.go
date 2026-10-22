package session

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"simulation-agents/agent"
	"simulation-agents/core"
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
		present:     map[string]bool{inst: true},
		instruments: []string{inst},
	}
}

// decideTimeTraits returns a TraitVector with the time-of-session channel
// on — sufficient for ShouldReperceive to coast correctly after the first
// Perceive call (same phase → no change → gate returns false) — plus a
// complete flat-path trait set (Disposition/Perception/ThetaAct/
// NeedsConfirm/ConfirmsNeeded) so decision.Decide never panics on a
// missing generated trait. Noise and both lean weights are zero, so the
// agent's only path to a candidate is a cleared mixer verdict — with
// chart/range off, that never happens, matching every existing test's
// "no punch" expectation.
func decideTimeTraits() agent.TraitVector {
	thetaAct, needsConfirm, confirmsNeeded := 0.5, false, 1
	return agent.TraitVector{
		Skill:       1.0,
		Diet:        &agent.Diet{TimeOfSession: true},
		Disposition: &agent.Disposition{Trend: 0, MeanRev: 0, Noise: 0},
		Perception: &agent.PerceptionProfile{
			TimeOfSession:     &agent.ChannelBlock{},
			RereadSensitivity: map[string]float64{},
			DecideSensitivity: map[string]float64{},
		},
		ThetaAct:       &thetaAct,
		NeedsConfirm:   &needsConfirm,
		ConfirmsNeeded: &confirmsNeeded,
	}
}

// decidePerceptionParams returns calibration with zero noise (deterministic
// output) and a standard MA saturation multiple.
func decidePerceptionParams() decision.PerceptionParams {
	return decision.PerceptionParams{SigmaPerc: 0, MASaturationMultiple: 2.0}
}

// testDecisionParams returns calibration with every shave neutralized
// (floor=1, damp=0) so a mixer opinion's conviction passes through
// commit() unshaved — the fixtures below reason directly about
// chart-strength-as-conviction without also accounting for session-time/
// spread/cooldown shaves. SigmaExec=0 keeps 4c's execution-noise
// deterministic too.
func testDecisionParams() decision.Params {
	return decision.Params{
		Mixer:          decision.MixerParams{SecondarySensitivity: 0.5, NoiseImpulseConviction: 0.5},
		Interpretation: decision.InterpretationParams{},
		Gate:           decision.GateParams{WaitReassessThreshold: 5},
		TimeShave:      decision.TimeShaveParams{Floor: 1},
		SpreadShave:    decision.SpreadShaveParams{Floor: 1},
		CooldownShave:  decision.CooldownShaveParams{Floor: 1},
		ThetaAddMargin: 0.1,
		MaxFrac:        0.5,
		Gamma:          0.5,
		ThetaMarket:    0.7,
		KPlace:         0.5,
		SigmaExec:      0,
	}
}

// flatChartTraits is a complete flat-path TraitVector driven entirely by
// the chart/trend channel — Disposition.Trend=1 makes mix() always pick
// the (lone) trend verdict; RereadSensitivity/DecideSensitivity are
// empty so neither gate ever force-recomputes on its own once primed.
func flatChartTraits(thetaAct float64, needsConfirm bool, confirmsNeeded int) agent.TraitVector {
	return agent.TraitVector{
		Skill:       1.0,
		Diet:        &agent.Diet{Chart: true},
		Disposition: &agent.Disposition{Trend: 1, MeanRev: 0, Noise: 0},
		Perception: &agent.PerceptionProfile{
			Chart:             &agent.ChannelBlock{},
			RereadSensitivity: map[string]float64{},
			DecideSensitivity: map[string]float64{},
		},
		ThetaAct:       &thetaAct,
		NeedsConfirm:   &needsConfirm,
		ConfirmsNeeded: &confirmsNeeded,
	}
}

// seededChartMemory pre-seeds an instrumentMemory with a hand-crafted
// chart bundle and non-empty refs so ShouldReperceive coasts on it
// immediately (refs non-zero, RereadSensitivity empty -> no channel
// ever trips) — giving a test full, deterministic control over this
// tick's reading without depending on go-talib's actual computation
// against synthetic candles. The trade-off (real indicator math vs this
// tick's chosen strength) is irrelevant to what these tests check: how
// session.decide wires 4a's output into 4b/4d/4c, not 4a's own
// arithmetic (already covered in decision's own test suite).
func seededChartMemory(direction decision.Direction, strength float64) *instrumentMemory {
	bundle := decision.PerceptionBundle{Chart: &decision.ChartReading{ReadOK: true, Direction: direction, Strength: strength}}
	return &instrumentMemory{
		refs:           store.PerceptionRefs{LastPrice: 1},
		lastBundle:     &bundle,
		lastCommitTick: decision.NoPriorCommit,
	}
}

// decideObserveFunc returns an observe closure reporting obs with no
// error — the shared-happy-path stand-in most fixtures need.
func decideObserveFunc(obs core.AccountObservation) func() (core.AccountObservation, error) {
	return func() (core.AccountObservation, error) { return obs, nil }
}

func makeDecideInput(
	traits agent.TraitVector,
	market *snapshotView,
	timeframes []string,
	runtimeStore RuntimeStore,
) decideInput {
	return decideInput{
		agentState: &workingAgent{
			traits:         traits,
			capitalHandle:  "acct",
			tradingCapital: 100_000,
			agentRNG:       rand.New(rand.NewPCG(42, 0)),
			perception:     make(map[string]*instrumentMemory),
			memoryHandle:   newRuntimeHandle(runtimeStore, context.Background(), "exch", "agent"),
		},
		market:           market,
		observe:          decideObserveFunc(core.AccountObservation{}),
		tick:             decision.TickContext{},
		perceptionParams: decidePerceptionParams(),
		decisionParams:   testDecisionParams(),
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

// --- No-signal agent: still no punch, now because the mixer never
// clears a verdict (chart/range both off diet), not because 4b is a
// stub.
func TestDecide_NoSignalChannelsOnDiet_NoPunch(t *testing.T) {
	input := makeDecideInput(
		decideTimeTraits(),
		decideMarket("X", "1m", 500),
		[]string{"1m"},
		stubRuntimeStore{},
	)
	_, ok := decide(input)
	if ok {
		t.Error("decide must return false when no on-diet channel ever produces a mixer verdict")
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

// --- Three leaves (observe error / zero candidates / 4c decline) ---

func TestDecide_ObserveError_NoPunchNoTraversal(t *testing.T) {
	input := makeDecideInput(
		flatChartTraits(0.1, false, 1),
		decideMarket("X", "1m", 1000),
		[]string{"1m"},
		stubRuntimeStore{},
	)
	input.observe = func() (core.AccountObservation, error) {
		return core.AccountObservation{}, fmt.Errorf("broker unavailable")
	}

	req, ok := decide(input)
	if ok || req.Instrument != "" {
		t.Fatalf("observe error must return no punch; got punch=%v ok=%v", req, ok)
	}
	if len(input.agentState.perception) != 0 {
		t.Errorf("observe error must skip the whole tick with no partial traversal; perception map = %v", input.agentState.perception)
	}
}

func TestDecide_ZeroCandidates_NoPunch(t *testing.T) {
	// A weak chart strength under a high theta_act clears neither the
	// noise coin (zero weight) nor the commit floor -> SKIP, no candidate.
	input := makeDecideInput(
		flatChartTraits(0.9, false, 1),
		decideMarket("X", "1m", 1000),
		[]string{"1m"},
		stubRuntimeStore{},
	)
	input.agentState.perception["X"] = seededChartMemory(decision.DirUp, 0.1)

	req, ok := decide(input)
	if ok || req.Instrument != "" {
		t.Fatalf("zero candidates must return no punch; got punch=%v ok=%v", req, ok)
	}
}

func TestDecide_FourthLeaf_ExecuteDeclines_NoPunchNoCommitment(t *testing.T) {
	// A single strong candidate, but zero trading capital and an
	// existing position already consuming the shared pool -> 4c's
	// A_available <= 0 guard declines. Commitment must not land.
	input := makeDecideInput(
		flatChartTraits(0.1, false, 1),
		decideMarket("X", "1m", 1000),
		[]string{"1m"},
		stubRuntimeStore{},
	)
	input.agentState.perception["X"] = seededChartMemory(decision.DirUp, 0.9)
	input.agentState.tradingCapital = 0
	input.observe = decideObserveFunc(core.AccountObservation{})

	req, ok := decide(input)
	if ok || req.Instrument != "" {
		t.Fatalf("a declined winner must return no punch; got punch=%v ok=%v", req, ok)
	}
	if input.agentState.perception["X"].lastCommitTick != decision.NoPriorCommit {
		t.Errorf("a declined winner must not apply a commitment; lastCommitTick = %d", input.agentState.perception["X"].lastCommitTick)
	}
}

// --- Assessment always applies; commitment only on the resolved winner ---

func TestDecide_AssessmentAppliesEvenForANonCandidateInstrument(t *testing.T) {
	traits := flatChartTraits(0.5, false, 1)
	market := &snapshotView{
		candles:     map[string]map[string][]store.Candle{},
		quote:       map[string]store.Quote{"A": {LastPrice: 1000, HasLast: true, BidPrice: 995, AskPrice: 1005, HasBid: true, HasAsk: true, Phase: "open"}, "B": {LastPrice: 1000, HasLast: true, BidPrice: 995, AskPrice: 1005, HasBid: true, HasAsk: true, Phase: "open"}},
		present:     map[string]bool{"A": true, "B": true},
		instruments: []string{"A", "B"},
	}
	input := makeDecideInput(traits, market, []string{"1m"}, stubRuntimeStore{})
	// A: weak strength, never clears theta_act=0.5 -> SKIP, no candidate.
	input.agentState.perception["A"] = seededChartMemory(decision.DirUp, 0.1)
	// B: strong strength, immediate actor -> COMMIT, the only candidate.
	input.agentState.perception["B"] = seededChartMemory(decision.DirUp, 0.95)

	decide(input)

	memA := input.agentState.perception["A"]
	if memA.lastDecidedReadings == nil {
		t.Error("A's assessment (lastDecidedReadings) must be applied even though A never became a candidate")
	}
	if memA.lastCommitTick != decision.NoPriorCommit {
		t.Errorf("A must never gain a commitment: lastCommitTick = %d", memA.lastCommitTick)
	}
}

func TestDecide_CommitmentOnlyOnResolvedWinner(t *testing.T) {
	traits := flatChartTraits(0.1, false, 1) // low bar: both A and B clear it
	market := &snapshotView{
		candles:     map[string]map[string][]store.Candle{},
		quote:       map[string]store.Quote{"A": {LastPrice: 1000, HasLast: true, BidPrice: 995, AskPrice: 1005, HasBid: true, HasAsk: true, Phase: "open"}, "B": {LastPrice: 1000, HasLast: true, BidPrice: 995, AskPrice: 1005, HasBid: true, HasAsk: true, Phase: "open"}},
		present:     map[string]bool{"A": true, "B": true},
		instruments: []string{"A", "B"},
	}
	input := makeDecideInput(traits, market, []string{"1m"}, stubRuntimeStore{})
	// A: lower conviction; B: higher conviction -> B must win resolution.
	input.agentState.perception["A"] = seededChartMemory(decision.DirUp, 0.3)
	input.agentState.perception["B"] = seededChartMemory(decision.DirUp, 0.95)

	req, ok := decide(input)
	if !ok {
		t.Fatalf("want a punch from the higher-conviction winner; got ok=false")
	}
	if req.Instrument != "B" {
		t.Fatalf("want B (higher conviction) to win resolution; got punch for %s", req.Instrument)
	}

	memA, memB := input.agentState.perception["A"], input.agentState.perception["B"]
	if memA.lastCommitTick != decision.NoPriorCommit || memA.positionContext != nil {
		t.Errorf("A (lower conviction, lost resolution) must show no commitment change; got lastCommitTick=%d positionContext=%+v", memA.lastCommitTick, memA.positionContext)
	}
	if memB.lastCommitTick == decision.NoPriorCommit || memB.positionContext == nil {
		t.Errorf("B (won resolution, real punch) must show a commitment; got lastCommitTick=%d positionContext=%+v", memB.lastCommitTick, memB.positionContext)
	}
}

// --- Root fork exclusivity ---

func TestDecide_FlatInstrument_NeverCallsManage(t *testing.T) {
	// Manage never reports a non-nil Assessment (4d doesn't touch 4b's
	// bookkeeping) — a flat instrument whose assessment gets set at all
	// proves Decide (not Manage) ran.
	traits := flatChartTraits(0.9, false, 1)
	input := makeDecideInput(traits, decideMarket("X", "1m", 1000), []string{"1m"}, stubRuntimeStore{})
	input.agentState.perception["X"] = seededChartMemory(decision.DirUp, 0.1)
	input.observe = decideObserveFunc(core.AccountObservation{}) // no positions -> flat

	decide(input)

	if input.agentState.perception["X"].lastDecidedReadings == nil {
		t.Error("a flat instrument must route to Decide (which always sets an assessment), not Manage")
	}
}

func TestDecide_HoldingInstrument_NeverCallsDecide(t *testing.T) {
	// Decide always sets a non-nil Assessment; Manage never does. A
	// holding instrument whose standing-opinion bookkeeping stays at
	// its untouched zero value proves Manage (not Decide) ran.
	traits := agent.TraitVector{
		Skill:       1.0,
		Exit:        &agent.ExitProfile{Mode: "distance", StopPct: 0.5, TargetPct: 0.5},
		MaxHold:     intPtrSession(1000),
		Disposition: &agent.Disposition{Trend: 1, MeanRev: 0, Noise: 0},
		ThetaAct:    floatPtrSession(0.9), // high bar: the add-check holds
		Diet:        &agent.Diet{Chart: true},
		Perception: &agent.PerceptionProfile{
			Chart:             &agent.ChannelBlock{},
			RereadSensitivity: map[string]float64{},
			DecideSensitivity: map[string]float64{},
		},
	}
	input := makeDecideInput(traits, decideMarket("X", "1m", 1000), []string{"1m"}, stubRuntimeStore{})
	input.agentState.perception["X"] = seededChartMemory(decision.DirFlat, 0) // no reversal, no add signal
	input.observe = decideObserveFunc(core.AccountObservation{Positions: []core.Position{
		{Instrument: "X", Qty: 10, WeightedAvgEntry: 1000},
	}})

	decide(input)

	mem := input.agentState.perception["X"]
	if mem.lastDecidedReadings != nil || mem.standingStance != "" || mem.pendingWatch != nil {
		t.Errorf("a holding instrument must route to Manage, which never touches 4b's bookkeeping; got %+v", mem)
	}
}

// --- 4a runs for holding too ---

func TestDecide_PerceptionRunsForHoldingInstrumentToo(t *testing.T) {
	traits := agent.TraitVector{
		Skill:       1.0,
		Exit:        &agent.ExitProfile{Mode: "distance", StopPct: 0.5, TargetPct: 0.5},
		MaxHold:     intPtrSession(1000),
		Disposition: &agent.Disposition{Trend: 1, MeanRev: 0, Noise: 0},
		ThetaAct:    floatPtrSession(0.9),
		Diet:        &agent.Diet{TimeOfSession: true},
		Perception: &agent.PerceptionProfile{
			TimeOfSession:     &agent.ChannelBlock{},
			RereadSensitivity: map[string]float64{},
			DecideSensitivity: map[string]float64{},
		},
	}
	const inst = "HELD"
	input := makeDecideInput(traits, decideMarket(inst, "1m", 1000), []string{"1m"}, stubRuntimeStore{})
	input.observe = decideObserveFunc(core.AccountObservation{Positions: []core.Position{
		{Instrument: inst, Qty: 10, WeightedAvgEntry: 1000},
	}})

	decide(input)

	mem := input.agentState.perception[inst]
	if mem == nil || mem.lastBundle == nil {
		t.Fatal("perception must run (and populate lastBundle) for a holding instrument too, even though it routes to Manage")
	}
}

// --- Determinism ---

func TestDecide_DeterministicUnderFixedSeedAndStableInstrumentOrder(t *testing.T) {
	build := func() decideInput {
		traits := flatChartTraits(0.3, true, 2)
		market := &snapshotView{
			candles: map[string]map[string][]store.Candle{},
			quote: map[string]store.Quote{
				"A": {LastPrice: 1000, HasLast: true, BidPrice: 995, AskPrice: 1005, HasBid: true, HasAsk: true, Phase: "open"},
				"B": {LastPrice: 2000, HasLast: true, BidPrice: 1995, AskPrice: 2005, HasBid: true, HasAsk: true, Phase: "open"},
				"C": {LastPrice: 3000, HasLast: true, BidPrice: 2995, AskPrice: 3005, HasBid: true, HasAsk: true, Phase: "open"},
			},
			present:     map[string]bool{"A": true, "B": true, "C": true},
			instruments: []string{"A", "B", "C"},
		}
		input := decideInput{
			agentState: &workingAgent{
				traits:         traits,
				capitalHandle:  "acct",
				tradingCapital: 100_000,
				agentRNG:       rand.New(rand.NewPCG(99, 99^0x9E3779B97F4A7C15)),
				perception: map[string]*instrumentMemory{
					"A": seededChartMemory(decision.DirUp, 0.4),
					"B": seededChartMemory(decision.DirUp, 0.7),
					"C": seededChartMemory(decision.DirDown, 0.6),
				},
				memoryHandle: newRuntimeHandle(stubRuntimeStore{}, context.Background(), "exch", "agent"),
			},
			market:           market,
			observe:          decideObserveFunc(core.AccountObservation{}),
			tick:             decision.TickContext{TickIndex: 5},
			perceptionParams: decidePerceptionParams(),
			decisionParams:   testDecisionParams(),
			timeframes:       []string{"1m"},
		}
		return input
	}

	firstInput, secondInput := build(), build()
	firstReq, firstOK := decide(firstInput)
	secondReq, secondOK := decide(secondInput)

	if firstReq != secondReq || firstOK != secondOK {
		t.Fatalf("decide not deterministic: first=(%+v,%v) second=(%+v,%v)", firstReq, firstOK, secondReq, secondOK)
	}
	if !reflect.DeepEqual(firstInput.agentState.perception, secondInput.agentState.perception) {
		t.Fatalf("decide produced different working-memory mutations across identical runs:\nfirst=%+v\nsecond=%+v",
			firstInput.agentState.perception, secondInput.agentState.perception)
	}
}

func intPtrSession(value int) *int           { return &value }
func floatPtrSession(value float64) *float64 { return &value }
