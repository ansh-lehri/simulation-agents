package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"simulation-agents/agent"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/store"
	"simulation-agents/transport"
)

// ---- Shared test fakes for the generator's narrow seams -----------
//
// The orchestrator package tests exercise the orchestrator's own
// surface, but constructing one requires a real *generator.Generator
// behind the inbound seam. Rather than depending on test exports from
// the generator package, each test file in this package carries small
// in-memory implementations of the three narrow seams the generator
// consumes. They are stripped-down versions of the richer fakes used
// in the generator package's own tests.

type nopAgentStore struct{}

func (nopAgentStore) LiveMembers(ctx context.Context, country, population string) ([]agent.Agent, error) {
	_, _, _ = ctx, country, population
	return nil, nil
}
func (nopAgentStore) LoadMany(ctx context.Context, agentIDs []agent.AgentID) ([]agent.Agent, error) {
	_, _ = ctx, agentIDs
	return nil, nil
}

type nopLeaseAllocator struct{}

func (nopLeaseAllocator) SelectAvailable(ctx context.Context, country string, count int, exchangeID string) ([]agent.AgentID, error) {
	_, _, _, _ = ctx, country, count, exchangeID
	return nil, nil
}

type nopWAL struct {
	mutex       sync.Mutex
	stagedCount int
}

func (wal *nopWAL) Stage(ctx context.Context, agentID agent.AgentID, record store.StageRecord) error {
	_, _, _ = ctx, agentID, record
	wal.mutex.Lock()
	defer wal.mutex.Unlock()
	wal.stagedCount++
	return nil
}

// completeGenParams is the generator's own completeGenParams reproduced
// here with the single call site this file needs. It uses the exported
// generator types so changes in the generator package surface here as a
// compile error.
func completeGenParams() generator.GenParams {
	return generator.GenParams{
		HerdProbability:    0.1,
		SkillBeta:          generator.BetaPair{Alpha: 2, Beta: 2},
		AggressivenessBeta: generator.BetaPair{Alpha: 2, Beta: 2},
		PnlReactivityBeta:  generator.BetaPair{Alpha: 2, Beta: 2},
		PActBeta:           generator.BetaPair{Alpha: 2, Beta: 5},
		ActProbBeta:        generator.BetaPair{Alpha: 2, Beta: 2},

		CapitalSizeLogNormal: generator.LogNormalPair{Mu: 10, Sigma: 1},

		DispositionDirichletAlphas:  generator.DispositionAlphas{Trend: 1, MeanReversion: 1, Noise: 1},
		DietOptionalFlagProbability: 0.5,

		ReactivityBeta: generator.BetaPair{Alpha: 2, Beta: 2},
		OptimismBeta:   generator.BetaPair{Alpha: 2, Beta: 2},

		SigmaIdio: 0.05,

		MethodAnchorsAndDemands: map[string]generator.MethodAnchor{
			"chart.ma":        {ReactivityAnchor: 0.2, SkillDemand: 0},
			"chart.rsi":       {ReactivityAnchor: 0.55, SkillDemand: 0.3},
			"chart.structure": {ReactivityAnchor: 0.85, SkillDemand: 1},
			"range.channel":   {ReactivityAnchor: 0.25, SkillDemand: 0},
			"range.bollinger": {ReactivityAnchor: 0.5, SkillDemand: 0.3},
			"range.rsi":       {ReactivityAnchor: 0.75, SkillDemand: 0.3},
		},
		MethodProximityTau: 0.25,
		MethodSkillGateEta: 1,

		PerceptionRanges: map[string]generator.NumericRange{
			"chart.ma.fast_period":           {Low: 5, High: 20},
			"chart.ma.slow_period":           {Low: 20, High: 100},
			"chart.ma.flat_band":             {Low: 0.001, High: 0.01},
			"chart.rsi.period":               {Low: 7, High: 21},
			"chart.rsi.mid_band":             {Low: 3, High: 15},
			"chart.structure.swing_window":   {Low: 3, High: 15},
			"chart.structure.min_swing_pct":  {Low: 0.002, High: 0.02},
			"chart.structure.confirm_swings": {Low: 1, High: 3},
			"range.bollinger.period":         {Low: 10, High: 40},
			"range.bollinger.sigma_mult":     {Low: 1.0, High: 2.5},
			"range.bollinger.ob_os_band":     {Low: 0.2, High: 0.5},
			"range.channel.window":           {Low: 10, High: 50},
			"range.channel.extreme_band":     {Low: 0.3, High: 0.5},
			"range.rsi.period":               {Low: 7, High: 21},
			"range.rsi.ob_os_levels":         {Low: 10, High: 30},
			"volume.avg_window":              {Low: 10, High: 50},
			"volume.spike_multiple":          {Low: 1.5, High: 4.0},
			"volume.band_width":              {Low: 0.1, High: 0.5},
			"liquidity.depth_levels":         {Low: 1, High: 5},
		},
		BiasTiltStrengths: map[string]float64{
			"chart.direction":         0.2,
			"chart.strength":          0.1,
			"range.position_in_range": 0.2,
			"range.stretch":           0.1,
		},
		RereadSensitivityRanges: map[string]generator.NumericRange{
			"chart":     {Low: 0.001, High: 0.01},
			"range":     {Low: 0.05, High: 0.3},
			"volume":    {Low: 0.1, High: 0.5},
			"liquidity": {Low: 0.05, High: 0.3},
		},
		DecideSensitivityRanges: map[string]generator.NumericRange{
			"chart.direction":         {Low: 0.05, High: 0.3},
			"chart.strength":          {Low: 0.05, High: 0.3},
			"range.position_in_range": {Low: 0.05, High: 0.3},
			"range.stretch":           {Low: 0.05, High: 0.3},
		},

		ExitModeAnchors: map[string]float64{
			"distance": 0.2,
			"hybrid":   0.55,
			"level":    0.85,
		},
		ExitModeSelectionTau:  0.25,
		ExitStopPctRange:      generator.NumericRange{Low: 0.005, High: 0.03},
		ExitRewardRiskRange:   generator.NumericRange{Low: 1, High: 3},
		ExitStopBufferRange:   generator.NumericRange{Low: 0.001, High: 0.01},
		ExitTargetBufferRange: generator.NumericRange{Low: 0.001, High: 0.01},

		MaxHoldRange: generator.IntRange{Low: 10, High: 600},

		ChannelWeightsDirichletConcentration: 1,
		TraitChannelSets: map[string][]string{
			"trend":    {"chart", "volume"},
			"mean_rev": {"range", "volume"},
			"noise":    {"time_of_session"},
		},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// counterIDMaker produces deterministic agent ids for tests.
func counterIDMaker() func() agent.AgentID {
	counter := 0
	return func() agent.AgentID {
		counter++
		return agent.AgentID(fmt.Sprintf("agent-%06d", counter))
	}
}

func buildTestGenerator(t *testing.T) *generator.Generator {
	t.Helper()
	g, err := generator.NewGenerator(
		nopAgentStore{},
		nopLeaseAllocator{},
		&nopWAL{},
		generator.NoOpScenarioProvider{},
		completeGenParams(),
		counterIDMaker(),
		func() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) },
		discardLogger(),
	)
	if err != nil {
		t.Fatalf("buildTestGenerator: %v", err)
	}
	return g
}

// ---- NewOrchestrator ---------------------------------------------

func TestNewOrchestratorRejectsNilDependencies(t *testing.T) {
	testGenerator := buildTestGenerator(t)
	rngSource := NewFixedSeedSource(1, 2)
	config := completeOrchestratorConfig()
	logger := discardLogger()

	cases := []struct {
		name string
		call func() error
	}{
		{"nil generator", func() error {
			_, err := NewOrchestrator(nil, rngSource, config, logger)
			return err
		}},
		{"nil rngSource", func() error {
			_, err := NewOrchestrator(testGenerator, nil, config, logger)
			return err
		}},
		{"nil logger", func() error {
			_, err := NewOrchestrator(testGenerator, rngSource, config, nil)
			return err
		}},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.call(); err == nil {
				t.Fatalf("expected error for %s, got nil", testCase.name)
			}
		})
	}
}

func TestNewOrchestratorRejectsInvalidConfig(t *testing.T) {
	bad := completeOrchestratorConfig()
	bad.PopulationSize = 0
	_, err := NewOrchestrator(buildTestGenerator(t), NewFixedSeedSource(1, 2), bad, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "PopulationSize") {
		t.Fatalf("expected PopulationSize error, got %v", err)
	}
}

// ---- BringUp (skeleton behaviour) --------------------------------

func TestBringUpReturnsAccountsReadyOnValidRequest(t *testing.T) {
	orchestrator := mustBuildOrchestrator(t, discardLogger())

	ack, err := orchestrator.BringUp(context.Background(), completeBringUpRequest())
	if err != nil {
		t.Fatalf("BringUp: %v", err)
	}
	if ack.Status != transport.StatusAccountsReady {
		t.Fatalf("ack status = %q, want %q", ack.Status, transport.StatusAccountsReady)
	}
	if ack.ExchangeID != "exch-IN-A" {
		t.Fatalf("ack exchange id = %q, want exch-IN-A", ack.ExchangeID)
	}
}

func TestBringUpSurfacesValidationError(t *testing.T) {
	orchestrator := mustBuildOrchestrator(t, discardLogger())
	bad := completeBringUpRequest()
	bad.SessionType = ""

	ack, err := orchestrator.BringUp(context.Background(), bad)
	if err == nil {
		t.Fatalf("expected validation error, got nil (ack=%+v)", ack)
	}
	if !strings.Contains(err.Error(), "SessionType") {
		t.Fatalf("expected SessionType error, got %v", err)
	}
	if ack != (transport.BringUpAck{}) {
		t.Fatalf("expected zero ack on validation error, got %+v", ack)
	}
}

func TestBringUpWarnsOnCanonicalWithScenario(t *testing.T) {
	// Capture the logger's output so we can assert the warn line fires
	// when a canonical trigger carries a scenario name — the carry is
	// honored on-demand only, and operators deserve a signal that
	// their scenario had no effect.
	var logSink bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logSink, &slog.HandlerOptions{Level: slog.LevelWarn}))
	orchestrator := mustBuildOrchestrator(t, logger)

	request := completeBringUpRequest()
	request.Scenario = "flash_crash"

	if _, err := orchestrator.BringUp(context.Background(), request); err != nil {
		t.Fatalf("BringUp: %v", err)
	}
	if !bytes.Contains(logSink.Bytes(), []byte("ignoring scenario on canonical session")) {
		t.Fatalf("expected canonical+scenario warn, got %s", logSink.String())
	}
	if !bytes.Contains(logSink.Bytes(), []byte("flash_crash")) {
		t.Fatalf("expected warn to carry the scenario name, got %s", logSink.String())
	}
}

func TestBringUpOnDemandWithScenarioDoesNotWarn(t *testing.T) {
	var logSink bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logSink, &slog.HandlerOptions{Level: slog.LevelWarn}))
	orchestrator := mustBuildOrchestrator(t, logger)

	request := completeBringUpRequest()
	request.SessionType = SessionTypeOnDemand
	request.Scenario = "polarized_optimism"

	if _, err := orchestrator.BringUp(context.Background(), request); err != nil {
		t.Fatalf("BringUp: %v", err)
	}
	if bytes.Contains(logSink.Bytes(), []byte("ignoring scenario on canonical session")) {
		t.Fatalf("on-demand request produced a canonical-scenario warn: %s", logSink.String())
	}
}

// ---- WindDown stub ------------------------------------------------

func TestWindDownReturnsNil(t *testing.T) {
	orchestrator := mustBuildOrchestrator(t, discardLogger())
	if err := orchestrator.WindDown(context.Background(), "exch-A", "graceful"); err != nil {
		t.Fatalf("WindDown: %v", err)
	}
}

// ---- Harness ------------------------------------------------------

func mustBuildOrchestrator(t *testing.T, logger *slog.Logger) *Orchestrator {
	t.Helper()
	orchestrator, err := NewOrchestrator(
		buildTestGenerator(t),
		NewFixedSeedSource(42, 7),
		completeOrchestratorConfig(),
		logger,
	)
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}
	return orchestrator
}
