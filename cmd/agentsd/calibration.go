package main

import (
	cryptoRand "crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"simulation-agents/agent"
	"simulation-agents/orchestrator"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/scenario"
)

// baselineScenarioSeed seeds the one deterministic sampling pass the
// scenario engine takes at startup — the per-knob crowd-width (eta)
// draw. The seed is fixed, deliberately, so the baseline link-space
// is reproducible across restarts; live session randomness is a
// separate (fresh-entropy) stream the orchestrator owns.
const baselineScenarioSeed uint64 = 1

// This file holds the calibration placeholder values the composition
// root passes into the generator and the orchestrator. The project
// discipline is "mechanism in code, numbers in calibration" — so the
// generator and orchestrator packages hold only shape and family, and
// every magnitude dial lives here, in the single place where this
// process is actually assembled. A future calibration path (env vars /
// TOML file / tuned sweep output) replaces this file; the generator and
// orchestrator never change.

// defaultGenParams returns the current placeholder calibration for the
// trait-draw engine. Values are not fitted to any measurement; they
// bracket conventional defaults where the perception generation spec
// grounds a known indicator setting (RSI 14, Bollinger 20/2σ, MA
// 10/50) and are soft placeholders everywhere else.
func defaultGenParams() generator.GenParams {
	return generator.GenParams{
		HerdProbability: 0.1,

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

		// TraitChannelSets is the one placeholder the orientation
		// explicitly flags: the finalized channel-to-trait map is
		// still owed by the perception spec. The mechanism (Dirichlet
		// over the trait's channel list) is final; only the per-trait
		// vectors below are placeholder. Replacing them when the real
		// map lands is the entire swap-in.
		TraitChannelSets: map[string][]string{
			"trend":    {"chart", "volume"},
			"mean_rev": {"range", "volume"},
			"noise":    {"time_of_session"},
		},
	}
}

// defaultOrchestratorConfig returns the orchestrator-side placeholder
// calibration. PopulationSize is the per-bring-up target population;
// LifecycleBuffer sizes the lifecycle-decision channel buffer; the
// capital params are the dial set agent.DrawTradingCapital reads.
// ValidScenarioNames is left empty here — the composition root fills
// it from the loaded scenario registry so this helper does not need to
// know about the scenario package.
func defaultOrchestratorConfig() orchestrator.OrchestratorConfig {
	return orchestrator.OrchestratorConfig{
		PopulationSize:  100,
		LifecycleBuffer: 64,
		CapitalParams: agent.CapitalParams{
			Floor:             1_000,
			JitterSpread:      0.1,
			DefaultFundedCash: 100_000,
		},
	}
}

// defaultPresenceBaseline returns placeholder values for the four
// lifecycle-curve rates the scenario engine shoves. The real values
// live on the (future) curve-builder's own calibration and are
// mirrored here so a scenario can be measured against the same
// baseline the live session will use. All four are strictly positive
// (the Log ruler requires it); the magnitudes are unitless placeholders
// a reader can replace at calibration time without any code change.
func defaultPresenceBaseline() scenario.PresenceBaseline {
	return scenario.PresenceBaseline{
		EntryRate:   10,
		StepOutRate: 2,
		DoneRate:    1,
		ReentryRate: 4,
	}
}

// newUUIDAgentID returns an agent-id source using 128-bit crypto-random
// hex strings. Called at every mint, so wrapped as a closure the
// generator can keep calling without re-reading the crypto source
// descriptor each time.
//
// crypto/rand reading from a trusted system source is treated as
// infallible — a failure here means the host is in a state where any
// further work would be unsafe, so panic rather than silently fall
// back to a weak source.
func newUUIDAgentID() func() agent.AgentID {
	return func() agent.AgentID {
		var randomBytes [16]byte
		if _, err := cryptoRand.Read(randomBytes[:]); err != nil {
			panic(fmt.Sprintf("agentsd: crypto/rand failed to seed agent id: %v", err))
		}
		return agent.AgentID("agent-" + hex.EncodeToString(randomBytes[:]))
	}
}

// systemClock is the clock function the generator stamps
// TraitsUpdatedAt with on every mint. Wrapped as a value rather than
// referenced directly so tests can inject a fixed-time clock without
// reaching into main's state.
func systemClock() time.Time { return time.Now().UTC() }
