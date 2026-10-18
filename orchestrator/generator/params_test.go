package generator

import (
	"strings"
	"testing"
)

// completeGenParams returns a GenParams whose every field passes Validate.
// Each table row mutates exactly one field so the row's assertion isolates
// the rule under test. Values are placeholders — this file tests structural
// invariants, never magnitude calibration.
func completeGenParams() GenParams {
	return GenParams{
		HerdProbability:    0.1,
		SkillBeta:          BetaPair{Alpha: 2, Beta: 2},
		AggressivenessBeta: BetaPair{Alpha: 2, Beta: 2},
		PnlReactivityBeta:  BetaPair{Alpha: 2, Beta: 2},
		PActBeta:           BetaPair{Alpha: 2, Beta: 5},
		ActProbBeta:        BetaPair{Alpha: 2, Beta: 2},

		CapitalSizeLogNormal: LogNormalPair{Mu: 10, Sigma: 1},

		DispositionDirichletAlphas:  DispositionAlphas{Trend: 1, MeanReversion: 1, Noise: 1},
		DietOptionalFlagProbability: 0.5,

		ReactivityBeta: BetaPair{Alpha: 2, Beta: 2},
		OptimismBeta:   BetaPair{Alpha: 2, Beta: 2},

		SigmaIdio: 0.05,

		MethodAnchorsAndDemands: map[string]MethodAnchor{
			"chart.ma":        {ReactivityAnchor: 0.2, SkillDemand: 0},
			"chart.rsi":       {ReactivityAnchor: 0.55, SkillDemand: 0.3},
			"chart.structure": {ReactivityAnchor: 0.85, SkillDemand: 1},
		},
		MethodProximityTau: 0.25,
		MethodSkillGateEta: 1,

		PerceptionRanges: map[string]NumericRange{
			"chart.ma.fast_period": {Low: 5, High: 20},
			"chart.ma.slow_period": {Low: 20, High: 100},
		},
		BiasTiltStrengths: map[string]float64{
			"chart.direction": 0.2,
			"chart.strength":  0.1,
		},
		RereadSensitivityRanges: map[string]NumericRange{
			"chart": {Low: 0.001, High: 0.01},
		},
		DecideSensitivityRanges: map[string]NumericRange{
			"chart.direction": {Low: 0.05, High: 0.3},
		},

		ExitModeAnchors: map[string]float64{
			"distance": 0.2,
			"hybrid":   0.55,
			"level":    0.85,
		},
		ExitModeSelectionTau:  0.25,
		ExitStopPctRange:      NumericRange{Low: 0.005, High: 0.03},
		ExitRewardRiskRange:   NumericRange{Low: 1, High: 3},
		ExitStopBufferRange:   NumericRange{Low: 0.001, High: 0.01},
		ExitTargetBufferRange: NumericRange{Low: 0.001, High: 0.01},

		MaxHoldRange: IntRange{Low: 10, High: 600},

		ChannelWeightsDirichletConcentration: 1,
		TraitChannelSets: map[string][]string{
			"trend":    {"chart", "volume"},
			"mean_rev": {"range", "volume"},
			"noise":    {"time_of_session"},
		},
	}
}

func TestValidateAcceptsCompleteGenParams(t *testing.T) {
	if err := completeGenParams().Validate(); err != nil {
		t.Fatalf("complete GenParams rejected: %v", err)
	}
}

// mutateCase names a single-field mutation and the error substring that
// mutation must trip. Each row isolates one invariant; the row's name is
// the invariant under test, phrased as the mutation.
type mutateCase struct {
	name          string
	mutate        func(*GenParams)
	errorContains string
}

func TestValidateRejectsStructuralViolations(t *testing.T) {
	cases := []mutateCase{
		{
			name:          "herd probability above one",
			mutate:        func(params *GenParams) { params.HerdProbability = 1.5 },
			errorContains: "HerdProbability",
		},
		{
			name:          "herd probability negative",
			mutate:        func(params *GenParams) { params.HerdProbability = -0.1 },
			errorContains: "HerdProbability",
		},
		{
			name:          "skill beta alpha zero",
			mutate:        func(params *GenParams) { params.SkillBeta.Alpha = 0 },
			errorContains: "SkillBeta.Alpha",
		},
		{
			name:          "aggressiveness beta beta negative",
			mutate:        func(params *GenParams) { params.AggressivenessBeta.Beta = -1 },
			errorContains: "AggressivenessBeta.Beta",
		},
		{
			name:          "capital size log-normal sigma zero",
			mutate:        func(params *GenParams) { params.CapitalSizeLogNormal.Sigma = 0 },
			errorContains: "CapitalSizeLogNormal.Sigma",
		},
		{
			name:          "disposition trend alpha zero",
			mutate:        func(params *GenParams) { params.DispositionDirichletAlphas.Trend = 0 },
			errorContains: "DispositionDirichletAlphas.Trend",
		},
		{
			name:          "diet optional flag probability negative",
			mutate:        func(params *GenParams) { params.DietOptionalFlagProbability = -0.1 },
			errorContains: "DietOptionalFlagProbability",
		},
		{
			name:          "sigma idio negative",
			mutate:        func(params *GenParams) { params.SigmaIdio = -0.01 },
			errorContains: "SigmaIdio",
		},
		{
			name:          "method proximity tau zero",
			mutate:        func(params *GenParams) { params.MethodProximityTau = 0 },
			errorContains: "MethodProximityTau",
		},
		{
			name:          "method skill gate eta negative",
			mutate:        func(params *GenParams) { params.MethodSkillGateEta = -1 },
			errorContains: "MethodSkillGateEta",
		},
		{
			name:          "method anchors empty",
			mutate:        func(params *GenParams) { params.MethodAnchorsAndDemands = map[string]MethodAnchor{} },
			errorContains: "MethodAnchorsAndDemands",
		},
		{
			name: "method anchor reactivity above one",
			mutate: func(params *GenParams) {
				params.MethodAnchorsAndDemands["chart.ma"] = MethodAnchor{ReactivityAnchor: 1.2, SkillDemand: 0}
			},
			errorContains: "ReactivityAnchor",
		},
		{
			name: "method anchor skill demand negative",
			mutate: func(params *GenParams) {
				params.MethodAnchorsAndDemands["chart.ma"] = MethodAnchor{ReactivityAnchor: 0.2, SkillDemand: -0.1}
			},
			errorContains: "SkillDemand",
		},
		{
			name:          "perception ranges empty",
			mutate:        func(params *GenParams) { params.PerceptionRanges = map[string]NumericRange{} },
			errorContains: "PerceptionRanges",
		},
		{
			name: "perception range reversed",
			mutate: func(params *GenParams) {
				params.PerceptionRanges["chart.ma.fast_period"] = NumericRange{Low: 20, High: 5}
			},
			errorContains: "PerceptionRanges[chart.ma.fast_period]",
		},
		{
			name:          "bias tilt strengths empty",
			mutate:        func(params *GenParams) { params.BiasTiltStrengths = map[string]float64{} },
			errorContains: "BiasTiltStrengths",
		},
		{
			name:          "bias tilt strength negative",
			mutate:        func(params *GenParams) { params.BiasTiltStrengths["chart.direction"] = -0.1 },
			errorContains: "BiasTiltStrengths",
		},
		{
			name: "reread sensitivity range reversed",
			mutate: func(params *GenParams) {
				params.RereadSensitivityRanges["chart"] = NumericRange{Low: 0.01, High: 0.001}
			},
			errorContains: "RereadSensitivityRanges",
		},
		{
			name: "decide sensitivity range reversed",
			mutate: func(params *GenParams) {
				params.DecideSensitivityRanges["chart.direction"] = NumericRange{Low: 0.3, High: 0.05}
			},
			errorContains: "DecideSensitivityRanges",
		},
		{
			name:          "exit mode anchors empty",
			mutate:        func(params *GenParams) { params.ExitModeAnchors = map[string]float64{} },
			errorContains: "ExitModeAnchors",
		},
		{
			name:          "exit mode anchor above one",
			mutate:        func(params *GenParams) { params.ExitModeAnchors["distance"] = 1.1 },
			errorContains: "ExitModeAnchors",
		},
		{
			name:          "exit mode selection tau zero",
			mutate:        func(params *GenParams) { params.ExitModeSelectionTau = 0 },
			errorContains: "ExitModeSelectionTau",
		},
		{
			name:          "exit stop pct range reversed",
			mutate:        func(params *GenParams) { params.ExitStopPctRange = NumericRange{Low: 0.03, High: 0.005} },
			errorContains: "ExitStopPctRange",
		},
		{
			name:          "exit reward risk range reversed",
			mutate:        func(params *GenParams) { params.ExitRewardRiskRange = NumericRange{Low: 3, High: 1} },
			errorContains: "ExitRewardRiskRange",
		},
		{
			name:          "exit stop buffer range reversed",
			mutate:        func(params *GenParams) { params.ExitStopBufferRange = NumericRange{Low: 1, High: 0} },
			errorContains: "ExitStopBufferRange",
		},
		{
			name:          "exit target buffer range reversed",
			mutate:        func(params *GenParams) { params.ExitTargetBufferRange = NumericRange{Low: 1, High: 0} },
			errorContains: "ExitTargetBufferRange",
		},
		{
			name:          "max hold range negative low",
			mutate:        func(params *GenParams) { params.MaxHoldRange = IntRange{Low: -1, High: 10} },
			errorContains: "MaxHoldRange.Low",
		},
		{
			name:          "max hold range reversed",
			mutate:        func(params *GenParams) { params.MaxHoldRange = IntRange{Low: 600, High: 10} },
			errorContains: "MaxHoldRange",
		},
		{
			name:          "channel weights dirichlet concentration zero",
			mutate:        func(params *GenParams) { params.ChannelWeightsDirichletConcentration = 0 },
			errorContains: "ChannelWeightsDirichletConcentration",
		},
		{
			name:          "trait channel sets empty",
			mutate:        func(params *GenParams) { params.TraitChannelSets = map[string][]string{} },
			errorContains: "TraitChannelSets",
		},
		{
			name:          "trait channel set with empty channels",
			mutate:        func(params *GenParams) { params.TraitChannelSets["trend"] = nil },
			errorContains: "TraitChannelSets[\"trend\"]",
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			params := completeGenParams()
			testCase.mutate(&params)
			err := params.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", testCase.errorContains)
			}
			if !strings.Contains(err.Error(), testCase.errorContains) {
				t.Fatalf("expected error containing %q, got %q", testCase.errorContains, err.Error())
			}
		})
	}
}
