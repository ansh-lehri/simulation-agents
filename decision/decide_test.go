package decision

import (
	"math/rand/v2"
	"reflect"
	"testing"

	"pgregory.net/rapid"

	"simulation-agents/agent"
	"simulation-agents/core"
	"simulation-agents/store"
)

// newDecideRNG builds the same seeded stream shape every other test
// file in this package uses, so a fixed seed replays byte-identically.
func newDecideRNG(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
}

// decideTraits builds a minimal valid TraitVector for Decide's tests:
// Disposition/Perception (both required, panics otherwise) plus the
// three 4b-2 commit-gate traits commit() itself requires.
func decideTraits(trend, meanRev, noise, thetaAct float64, needsConfirm bool, confirmsNeeded int) agent.TraitVector {
	return agent.TraitVector{
		Disposition:    &agent.Disposition{Trend: trend, MeanRev: meanRev, Noise: noise},
		Perception:     &agent.PerceptionProfile{DecideSensitivity: map[string]float64{readingFieldChartDirection: 0.5, readingFieldChartStrength: 0.1}},
		Diet:           &agent.Diet{OrderBook: false},
		ThetaAct:       &thetaAct,
		NeedsConfirm:   &needsConfirm,
		ConfirmsNeeded: &confirmsNeeded,
	}
}

func TestDecide_MissingGeneratedTraitsPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Decide: want panic on a TraitVector missing disposition/perception_profile")
		}
	}()
	Decide(DecideInput{Traits: agent.TraitVector{}, RNG: newDecideRNG(1), Params: testChainParams()})
}

// A noise-coin hit bypasses the gate/mixer/commit-gate entirely and
// commits directly: the candidate is an OPEN, the commitment's entry
// channel is "noise" (there is no structure to stamp), and Changed is
// true (a real re-decide happened this tick, not a coast).
func TestDecide_NoiseImpulseBypassesGateAndCommitsDirectly(t *testing.T) {
	traits := decideTraits(0, 0, 1, 0.1, true, 5) // noise=1 guarantees a hit
	input := DecideInput{
		Bundle:      PerceptionBundle{},
		Traits:      traits,
		Roles:       TraitChannelRoles,
		CurrentTick: 42,
		RNG:         newDecideRNG(1),
		Params:      testChainParams(),
	}

	outcome := Decide(input)
	if outcome.Candidate == nil || outcome.Candidate.Action != ExecIntentActionOpen {
		t.Fatalf("want an OPEN candidate on a noise hit, got %+v", outcome)
	}
	if outcome.Commitment == nil || outcome.Commitment.PositionContext.EntryChannel != "noise" {
		t.Fatalf("want a commitment stamped entry_channel=noise, got %+v", outcome.Commitment)
	}
	if outcome.Commitment.LastCommitTick != 42 {
		t.Errorf("LastCommitTick = %d, want 42", outcome.Commitment.LastCommitTick)
	}
	if outcome.Assessment == nil || !outcome.Assessment.Changed {
		t.Fatalf("want Assessment.Changed=true on a noise hit, got %+v", outcome.Assessment)
	}
}

// A COMMIT off the signal path stamps the commitment's entry channel
// from the mixer's dominant trait — chart for a lone trend verdict —
// not a hardcoded value.
func TestDecide_CommitStampsEntryChannelFromDominantTrait(t *testing.T) {
	traits := decideTraits(1, 0, 0, 0.1, false, 1) // noise=0, immediate actor
	input := DecideInput{
		Bundle:          PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirUp, Strength: 0.9}},
		Traits:          traits,
		Roles:           TraitChannelRoles,
		CurrentTick:     7,
		SessionProgress: 0.5,
		LastCommitTick:  NoPriorCommit,
		RNG:             newDecideRNG(2),
		Params:          testChainParams(),
	}

	outcome := Decide(input)
	if outcome.Candidate == nil || outcome.Candidate.Action != ExecIntentActionOpen || outcome.Candidate.Direction != 1 {
		t.Fatalf("want an OPEN/long candidate, got %+v", outcome)
	}
	if outcome.Commitment == nil {
		t.Fatalf("want a commitment on COMMIT, got nil")
	}
	if outcome.Commitment.PositionContext.EntryChannel != "chart" {
		t.Errorf("EntryChannel = %q, want %q (trend's lean channel)", outcome.Commitment.PositionContext.EntryChannel, "chart")
	}
	if outcome.Commitment.PositionContext.EntryDir != entryDirectionLong {
		t.Errorf("EntryDir = %q, want %q", outcome.Commitment.PositionContext.EntryDir, entryDirectionLong)
	}
	if outcome.Commitment.PositionContext.EntryTick != 7 || outcome.Commitment.LastCommitTick != 7 {
		t.Errorf("entry/commit tick = (%d, %d), want (7, 7)", outcome.Commitment.PositionContext.EntryTick, outcome.Commitment.LastCommitTick)
	}
}

// A COAST tick (gate says nothing moved enough) must report
// Changed=false and must echo the carried-in standing memory forward
// unchanged — it must never silently reset standingOpinion or
// lastDecidedReadings just because nothing new was computed.
func TestDecide_CoastEchoesCarriedMemoryUnchanged(t *testing.T) {
	traits := decideTraits(1, 0, 0, 0.9, false, 1) // noise=0, high theta_act so a weak view coasts
	lastDecided := map[string]float64{readingFieldChartDirection: 1, readingFieldChartStrength: 0.5}
	input := DecideInput{
		// Identical readings to lastDecided and under every sensitivity
		// threshold -> gate must coast.
		Bundle:              PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirUp, Strength: 0.5}},
		Traits:              traits,
		Roles:               TraitChannelRoles,
		CurrentTick:         10,
		StandingOpinion:     0.42,
		StandingStance:      standingStanceFlatIdle,
		LastDecidedReadings: lastDecided,
		WaitStreak:          0,
		RNG:                 newDecideRNG(3),
		Params:              testChainParams(),
	}

	outcome := Decide(input)
	if outcome.Candidate != nil {
		t.Fatalf("want no candidate on a coast tick, got %+v", outcome.Candidate)
	}
	if outcome.Commitment != nil {
		t.Fatalf("want no commitment on a coast tick, got %+v", outcome.Commitment)
	}
	if outcome.Assessment == nil {
		t.Fatal("want a non-nil assessment even on coast")
	}
	if outcome.Assessment.Changed {
		t.Error("want Changed=false on a coast tick")
	}
	if outcome.Assessment.StandingOpinion != 0.42 {
		t.Errorf("StandingOpinion = %v, want unchanged 0.42", outcome.Assessment.StandingOpinion)
	}
	if !reflect.DeepEqual(outcome.Assessment.LastDecidedReadings, lastDecided) {
		t.Errorf("LastDecidedReadings = %v, want unchanged %v", outcome.Assessment.LastDecidedReadings, lastDecided)
	}
}

// Decide is pure and seeded: identical inputs (including a
// freshly-constructed RNG from the same seed) must produce
// byte-identical output on repeat calls.
func TestDecide_Determinism(t *testing.T) {
	buildInput := func() DecideInput {
		return DecideInput{
			Bundle:          PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirUp, Strength: 0.9}},
			Traits:          decideTraits(0.5, 0.5, 0.2, 0.3, true, 2),
			Roles:           TraitChannelRoles,
			CurrentTick:     100,
			SessionProgress: 0.5,
			LastCommitTick:  NoPriorCommit,
			RNG:             newDecideRNG(9),
			Params:          testChainParams(),
		}
	}

	first := Decide(buildInput())
	second := Decide(buildInput())
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Decide not deterministic: first=%+v, second=%+v", first, second)
	}
}

// Decide must never mutate its own inputs — the LastDecidedReadings map
// and PendingWatch the caller passed in must be byte-identical after
// the call, whatever Decide itself returns.
func TestDecide_DoesNotMutateInputs(t *testing.T) {
	lastDecided := map[string]float64{readingFieldChartDirection: 1}
	lastDecidedSnapshot := map[string]float64{readingFieldChartDirection: 1}
	watch := &PendingWatch{Direction: 1, ConfirmsSeen: 1, SinceTick: 5}
	watchSnapshot := *watch

	Decide(DecideInput{
		Bundle:              PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirUp, Strength: 0.9}},
		Traits:              decideTraits(1, 0, 0, 0.9, true, 5),
		Roles:               TraitChannelRoles,
		CurrentTick:         10,
		LastDecidedReadings: lastDecided,
		PendingWatch:        watch,
		RNG:                 newDecideRNG(4),
		Params:              testChainParams(),
	})

	if !reflect.DeepEqual(lastDecided, lastDecidedSnapshot) {
		t.Errorf("Decide mutated the caller's LastDecidedReadings map: got %v, want %v", lastDecided, lastDecidedSnapshot)
	}
	if *watch != watchSnapshot {
		t.Errorf("Decide mutated the caller's PendingWatch: got %+v, want %+v", *watch, watchSnapshot)
	}
}

// Manage must never mutate its own inputs either — same regression
// guard as Decide's, for the holding-path node.
func TestManage_DoesNotMutateInputs(t *testing.T) {
	posCtx := &store.PositionContext{EntryDir: "LONG", EntryChannel: "chart", EntryTick: 1}
	posCtxSnapshot := *posCtx
	bundle := &PerceptionBundle{Chart: &ChartReading{ReadOK: true, Direction: DirUp, Strength: 0.9}}

	Manage(ManageInput{
		Position:        core.Position{Qty: 10, WeightedAvgEntry: 100},
		Bundle:          bundle,
		PositionContext: posCtx,
		Traits: agent.TraitVector{
			Exit:        &agent.ExitProfile{Mode: exitModeDistance, StopPct: 0.5, TargetPct: 0.5},
			MaxHold:     intPtr(1000),
			Disposition: &agent.Disposition{Trend: 1, MeanRev: 0, Noise: 0},
			ThetaAct:    floatPtr(0.5),
		},
		CurrentPrice: 110,
		Tick:         TickContext{TickIndex: 50},
		Roles:        TraitChannelRoles,
		RNG:          newDecideRNG(5),
		Params:       Params{ThetaAddMargin: 0.1},
	})

	if *posCtx != posCtxSnapshot {
		t.Errorf("Manage mutated the caller's PositionContext: got %+v, want %+v", *posCtx, posCtxSnapshot)
	}
}

// ResolveOnePunch: any close candidate beats any open/add candidate
// regardless of relative conviction — a low-conviction close must
// still win over a high-conviction open.
func TestResolveOnePunch_CloseAlwaysBeatsOpenOrAdd(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		closeConviction := rapid.Float64Range(0, 1).Draw(rt, "close_conviction")
		openConviction := rapid.Float64Range(0, 1).Draw(rt, "open_conviction")
		seed := rapid.Uint64().Draw(rt, "seed")

		candidates := []InstrumentCandidate{
			{Instrument: "CLOSE_INST", Outcome: NodeOutcome{Candidate: &ExecIntent{Action: ExecIntentActionClose, Conviction: closeConviction}}},
			{Instrument: "OPEN_INST", Outcome: NodeOutcome{Candidate: &ExecIntent{Action: ExecIntentActionOpen, Conviction: openConviction}}},
		}

		winner := ResolveOnePunch(candidates, newDecideRNG(seed))
		if winner.Instrument != "CLOSE_INST" {
			rt.Fatalf("close_conviction=%v open_conviction=%v: winner=%s, want CLOSE_INST", closeConviction, openConviction, winner.Instrument)
		}
	})
}

// Within one class, the higher-conviction candidate wins; an exact tie
// resolves via the seeded RNG deterministically (same seed -> same
// winner, across repeated runs).
func TestResolveOnePunch_HighestConvictionWinsWithinClass(t *testing.T) {
	candidates := []InstrumentCandidate{
		{Instrument: "LOW", Outcome: NodeOutcome{Candidate: &ExecIntent{Action: ExecIntentActionOpen, Conviction: 0.3}}},
		{Instrument: "HIGH", Outcome: NodeOutcome{Candidate: &ExecIntent{Action: ExecIntentActionOpen, Conviction: 0.9}}},
	}
	winner := ResolveOnePunch(candidates, newDecideRNG(1))
	if winner.Instrument != "HIGH" {
		t.Fatalf("winner = %s, want HIGH", winner.Instrument)
	}
}

func TestResolveOnePunch_ExactTieResolvesDeterministicallyBySeed(t *testing.T) {
	buildCandidates := func() []InstrumentCandidate {
		return []InstrumentCandidate{
			{Instrument: "A", Outcome: NodeOutcome{Candidate: &ExecIntent{Action: ExecIntentActionOpen, Conviction: 0.5}}},
			{Instrument: "B", Outcome: NodeOutcome{Candidate: &ExecIntent{Action: ExecIntentActionOpen, Conviction: 0.5}}},
		}
	}
	first := ResolveOnePunch(buildCandidates(), newDecideRNG(123))
	second := ResolveOnePunch(buildCandidates(), newDecideRNG(123))
	if first.Instrument != second.Instrument {
		t.Fatalf("same seed produced different tie-break winners: %s vs %s", first.Instrument, second.Instrument)
	}
}

// PositionFor: Qty != 0 reports holding with the matching position;
// Qty == 0 or an absent instrument reports flat with a zero-value
// Position.
func TestPositionFor(t *testing.T) {
	obs := core.AccountObservation{Positions: []core.Position{
		{Instrument: "LONG", Qty: 10},
		{Instrument: "FLATTENED", Qty: 0},
	}}

	position, holding := PositionFor(obs, "LONG")
	if !holding || position.Qty != 10 {
		t.Errorf("LONG: got (position=%+v, holding=%v), want (Qty=10, holding=true)", position, holding)
	}

	if _, holding := PositionFor(obs, "FLATTENED"); holding {
		t.Error("FLATTENED (Qty=0): want holding=false")
	}

	if _, holding := PositionFor(obs, "ABSENT"); holding {
		t.Error("ABSENT instrument: want holding=false")
	}
}
