package generator

import "fmt"

// GenParams holds the full calibration dial set for the trait-draw engine.
// Distribution families are fixed by code; every numeric value here is a
// calibration placeholder tuned at measurement time, never derived at
// runtime. Scenario-shifted dials ride on *EffectiveParams and shadow the
// corresponding GenParams field when non-nil.
//
// The whole struct is designed to be built once at process startup, passed
// through to the generator by value, and never mutated during a session.
type GenParams struct {
	// HerdProbability is the coin weight for drawing an agent as herd kind
	// versus endogenous. Herd agents follow tips rather than reading the
	// market themselves. Must be in [0, 1].
	HerdProbability float64

	// Universal Beta parameters (alpha, beta) for the five scalar axes
	// drawn for every agent — plus the herd-only credulity axis.
	SkillBeta          BetaPair
	AggressivenessBeta BetaPair
	PnlReactivityBeta  BetaPair
	PActBeta           BetaPair
	ActProbBeta        BetaPair // consulted only when agent kind is herd

	// CapitalSizeLogNormal shapes the frozen capital scale. A log-normal
	// produces the long-tail-of-whales / short-tail-of-minnows profile the
	// trading surface needs to avoid uniform-size order books.
	CapitalSizeLogNormal LogNormalPair

	// DispositionDirichletAlphas shapes the three-way disposition simplex
	// (trend / mean-reversion / noise) for endogenous agents.
	DispositionDirichletAlphas DispositionAlphas

	// DietOptionalFlagProbability is the independent coin probability for
	// each diet flag that the dominant disposition does NOT force on. The
	// mandatory-forced flags are set deterministically from the disposition
	// simplex at draw time, so this dial only governs the optional slots.
	DietOptionalFlagProbability float64

	// ReactivityBeta shapes the per-agent reactivity latent (0 = deliberate,
	// 1 = jumpy). This latent governs every perception window / cutoff
	// width / sensitivity threshold / max-hold value through the shared
	// range-placement rule, so a single Beta draw here couples a coherent
	// temperament across the agent's whole perception profile.
	ReactivityBeta BetaPair

	// OptimismBeta shapes the per-agent optimism latent before it is
	// stretched to [-1, +1]. A symmetric Beta (equal alpha and beta) keeps
	// most agents neutral and a few extreme on each side; an asymmetric
	// Beta leans the population one way.
	OptimismBeta BetaPair

	// SigmaIdio is the per-field Gaussian jitter width used by the range
	// placement rule, expressed as a fraction of each range's width. A
	// small value (a few percent) gives idiosyncratic texture around the
	// reactivity-placed center without collapsing same-reactivity agents
	// onto one point. Must be non-negative; zero disables jitter.
	SigmaIdio float64

	// MethodAnchorsAndDemands keys method identifiers to their lag anchor
	// on the reactivity axis plus the skill demand that filters low-skill
	// agents away from sophisticated methods. Keys are of the form
	// "<channel>.<method>", for example "chart.ma", "chart.structure",
	// "chart.rsi", "range.channel", "range.bollinger", "range.rsi".
	MethodAnchorsAndDemands map[string]MethodAnchor

	// MethodProximityTau controls how sharply a method's reactivity anchor
	// attracts matching agents during method selection — a small tau means
	// the anchor dominates, a large one spreads selection across methods.
	// Must be positive.
	MethodProximityTau float64

	// MethodSkillGateEta scales the skill-demand exponent during method
	// selection — a larger value filters low-skill agents harder away from
	// high-demand methods. Must be non-negative.
	MethodSkillGateEta float64

	// PerceptionRanges keys perception numeric fields to their allowed
	// [low, high] box. Keys are of the form "<channel>.<method>.<field>"
	// for method-forked channels (for example "chart.ma.fast_period",
	// "chart.ma.slow_period", "chart.rsi.period") and "<channel>.<field>"
	// for fixed-calc channels (for example "volume.avg_window",
	// "liquidity.depth_levels").
	PerceptionRanges map[string]NumericRange

	// BiasTiltStrengths keys the four directional perception fields to
	// their per-field tilt-strength constant. Each directional-field bias
	// is produced by multiplying the agent's optimism latent by this value;
	// the constants are kept small relative to each field's own range so
	// bias tilts the reading rather than fabricating one. Keys name the
	// directional fields, for example "chart.direction", "chart.strength",
	// "range.position_in_range", "range.stretch".
	BiasTiltStrengths map[string]float64

	// RereadSensitivityRanges keys each perception channel to the allowed
	// [low, high] box of its re-read threshold — the market-side change
	// that triggers a fresh perception for that channel. Not all channels
	// need entries here (the time-of-session channel has no threshold at
	// all), so the draw path looks up by channel key.
	RereadSensitivityRanges map[string]NumericRange

	// DecideSensitivityRanges keys each consumed directional field to the
	// allowed [low, high] box of its re-decide threshold — the reading-side
	// change that triggers a fresh decision.
	DecideSensitivityRanges map[string]NumericRange

	// ExitModeAnchors keys each exit-profile mode identifier to its anchor
	// on the skill axis. High-skill agents concentrate around the
	// level-mode anchor; low-skill agents around the distance-mode anchor.
	// Values must be in [0, 1].
	ExitModeAnchors map[string]float64

	// ExitModeSelectionTau controls how sharply the skill axis attracts
	// agents to the matching exit-mode anchor during exit-mode selection.
	// Must be positive.
	ExitModeSelectionTau float64

	// ExitStopPctRange is the free-draw range of the stop-loss percentage
	// trait, as a fraction of entry price.
	ExitStopPctRange NumericRange

	// ExitRewardRiskRange is the free-draw range of the reward-to-risk
	// multiplier. The target percent is derived as stop percent times this
	// multiplier, which guarantees target > stop by construction.
	ExitRewardRiskRange NumericRange

	// ExitStopBufferRange is the free-draw range of the level-mode offset
	// beyond a support/resistance level before the stop fires, as a
	// fraction of the level price.
	ExitStopBufferRange NumericRange

	// ExitTargetBufferRange is the free-draw range of the level-mode
	// offset short of a target before the exit fires, as a fraction of the
	// level price.
	ExitTargetBufferRange NumericRange

	// MaxHoldRange is the inclusive integer tick range for the frozen
	// max-hold trait. The actual value is placed inside this range using
	// the shared reactivity placement rule — jumpy agents land on short
	// holds, deliberate agents on long holds. Low must be non-negative and
	// less than or equal to High.
	MaxHoldRange IntRange

	// ThetaActRange is the inclusive [Low, High] box for the commit-bar trait.
	// Both bounds must be within [0, 1] and Low must be <= High. The draw uses
	// aggressiveness as the placement driver: aggressive agents land near Low
	// (low bar), cautious agents near High (high bar).
	ThetaActRange NumericRange

	// ConfirmsNeededRange is the inclusive integer [Low, High] box for the
	// confirmation-count trait. Low must be >= 1 (at least one confirmation
	// required for any waiter) and Low must be <= High. The draw uses
	// aggressiveness as the placement driver: aggressive agents land near Low
	// (fewer confirmations), cautious agents near High.
	ConfirmsNeededRange IntRange

	// ChannelWeightsDirichletConcentration shapes the per-trait channel-
	// weight simplex: a high concentration pulls weights toward equal
	// across channels, a low one toward lopsided. Must be positive.
	ChannelWeightsDirichletConcentration float64

	// TraitChannelSets is the placeholder channel-to-trait mapping —
	// which channels each disposition-labeled trait consumes. The
	// per-trait channel lists in this map ARE the only placeholder; the
	// Dirichlet-over-the-trait's-channels mechanism that reads this map is
	// final and does not change when the finalized map lands. Replacing the
	// vectors here is the entire swap-in at calibration time.
	TraitChannelSets map[string][]string
}

// BetaPair is the shape-parameter pair of a Beta distribution. Both values
// must be positive. The naming matches the standard statistical convention
// so a reader with a probability background recognizes the shape at sight.
type BetaPair struct {
	Alpha float64
	Beta  float64
}

// LogNormalPair is the parameter pair of a log-normal distribution,
// expressed in log space: Mu is the mean of the underlying normal, Sigma
// its standard deviation. Sigma must be positive.
type LogNormalPair struct {
	Mu    float64
	Sigma float64
}

// DispositionAlphas names the three Dirichlet concentration parameters
// that shape the trend / mean-reversion / noise disposition simplex. All
// three must be positive.
type DispositionAlphas struct {
	Trend         float64
	MeanReversion float64
	Noise         float64
}

// NumericRange is an inclusive [Low, High] box for a continuous draw.
// Low must be less than or equal to High.
type NumericRange struct {
	Low  float64
	High float64
}

// IntRange is an inclusive [Low, High] box for an integer draw. Low must
// be less than or equal to High.
type IntRange struct {
	Low  int
	High int
}

// MethodAnchor pins a method's position on the reactivity axis and its
// skill demand. ReactivityAnchor must be in [0, 1]; SkillDemand must be
// non-negative. During method selection the proximity score pulls agents
// whose reactivity sits near this anchor, and the skill gate suppresses
// agents whose skill is below this demand.
type MethodAnchor struct {
	ReactivityAnchor float64
	SkillDemand      float64
}

// Validate returns a non-nil error if any field violates the structural
// invariants documented alongside it. It is the single entry point the
// composition root calls before passing a GenParams into the generator so
// bad calibration is caught at startup rather than at first draw.
func (params GenParams) Validate() error {
	if !isProbability(params.HerdProbability) {
		return fmt.Errorf("generator: HerdProbability %v must be in [0,1]", params.HerdProbability)
	}

	betaPairs := []struct {
		name string
		pair BetaPair
	}{
		{"SkillBeta", params.SkillBeta},
		{"AggressivenessBeta", params.AggressivenessBeta},
		{"PnlReactivityBeta", params.PnlReactivityBeta},
		{"PActBeta", params.PActBeta},
		{"ActProbBeta", params.ActProbBeta},
		{"ReactivityBeta", params.ReactivityBeta},
		{"OptimismBeta", params.OptimismBeta},
	}
	for _, entry := range betaPairs {
		if err := entry.pair.validateShapes(entry.name); err != nil {
			return err
		}
	}

	if params.CapitalSizeLogNormal.Sigma <= 0 {
		return fmt.Errorf("generator: CapitalSizeLogNormal.Sigma %v must be positive", params.CapitalSizeLogNormal.Sigma)
	}

	dispositionAlphas := []struct {
		name  string
		value float64
	}{
		{"DispositionDirichletAlphas.Trend", params.DispositionDirichletAlphas.Trend},
		{"DispositionDirichletAlphas.MeanReversion", params.DispositionDirichletAlphas.MeanReversion},
		{"DispositionDirichletAlphas.Noise", params.DispositionDirichletAlphas.Noise},
	}
	for _, entry := range dispositionAlphas {
		if entry.value <= 0 {
			return fmt.Errorf("generator: %s %v must be positive", entry.name, entry.value)
		}
	}

	if !isProbability(params.DietOptionalFlagProbability) {
		return fmt.Errorf("generator: DietOptionalFlagProbability %v must be in [0,1]", params.DietOptionalFlagProbability)
	}
	if params.SigmaIdio < 0 {
		return fmt.Errorf("generator: SigmaIdio %v must be non-negative", params.SigmaIdio)
	}
	if params.MethodProximityTau <= 0 {
		return fmt.Errorf("generator: MethodProximityTau %v must be positive", params.MethodProximityTau)
	}
	if params.MethodSkillGateEta < 0 {
		return fmt.Errorf("generator: MethodSkillGateEta %v must be non-negative", params.MethodSkillGateEta)
	}

	if len(params.MethodAnchorsAndDemands) == 0 {
		return fmt.Errorf("generator: MethodAnchorsAndDemands must not be empty")
	}
	for key, anchor := range params.MethodAnchorsAndDemands {
		if !isProbability(anchor.ReactivityAnchor) {
			return fmt.Errorf("generator: MethodAnchorsAndDemands[%q].ReactivityAnchor %v must be in [0,1]", key, anchor.ReactivityAnchor)
		}
		if anchor.SkillDemand < 0 {
			return fmt.Errorf("generator: MethodAnchorsAndDemands[%q].SkillDemand %v must be non-negative", key, anchor.SkillDemand)
		}
	}

	if len(params.PerceptionRanges) == 0 {
		return fmt.Errorf("generator: PerceptionRanges must not be empty")
	}
	for key, rangeBox := range params.PerceptionRanges {
		if err := rangeBox.validateOrdered("PerceptionRanges[" + key + "]"); err != nil {
			return err
		}
	}

	if len(params.BiasTiltStrengths) == 0 {
		return fmt.Errorf("generator: BiasTiltStrengths must not be empty")
	}
	for key, tilt := range params.BiasTiltStrengths {
		if tilt < 0 {
			return fmt.Errorf("generator: BiasTiltStrengths[%q] %v must be non-negative", key, tilt)
		}
	}

	for key, rangeBox := range params.RereadSensitivityRanges {
		if err := rangeBox.validateOrdered("RereadSensitivityRanges[" + key + "]"); err != nil {
			return err
		}
	}
	for key, rangeBox := range params.DecideSensitivityRanges {
		if err := rangeBox.validateOrdered("DecideSensitivityRanges[" + key + "]"); err != nil {
			return err
		}
	}

	if len(params.ExitModeAnchors) == 0 {
		return fmt.Errorf("generator: ExitModeAnchors must not be empty")
	}
	for key, anchor := range params.ExitModeAnchors {
		if !isProbability(anchor) {
			return fmt.Errorf("generator: ExitModeAnchors[%q] %v must be in [0,1]", key, anchor)
		}
	}
	if params.ExitModeSelectionTau <= 0 {
		return fmt.Errorf("generator: ExitModeSelectionTau %v must be positive", params.ExitModeSelectionTau)
	}

	exitRanges := []struct {
		name     string
		rangeBox NumericRange
	}{
		{"ExitStopPctRange", params.ExitStopPctRange},
		{"ExitRewardRiskRange", params.ExitRewardRiskRange},
		{"ExitStopBufferRange", params.ExitStopBufferRange},
		{"ExitTargetBufferRange", params.ExitTargetBufferRange},
	}
	for _, entry := range exitRanges {
		if err := entry.rangeBox.validateOrdered(entry.name); err != nil {
			return err
		}
	}

	if params.MaxHoldRange.Low < 0 {
		return fmt.Errorf("generator: MaxHoldRange.Low %d must be non-negative", params.MaxHoldRange.Low)
	}
	if params.MaxHoldRange.Low > params.MaxHoldRange.High {
		return fmt.Errorf("generator: MaxHoldRange low %d must be <= high %d",
			params.MaxHoldRange.Low, params.MaxHoldRange.High)
	}

	if !isProbability(params.ThetaActRange.Low) || !isProbability(params.ThetaActRange.High) {
		return fmt.Errorf("generator: ThetaActRange [%v, %v] must have both bounds within [0,1]",
			params.ThetaActRange.Low, params.ThetaActRange.High)
	}
	if params.ThetaActRange.Low > params.ThetaActRange.High {
		return fmt.Errorf("generator: ThetaActRange low %v must be <= high %v",
			params.ThetaActRange.Low, params.ThetaActRange.High)
	}

	if params.ConfirmsNeededRange.Low < 1 {
		return fmt.Errorf("generator: ConfirmsNeededRange.Low %d must be >= 1", params.ConfirmsNeededRange.Low)
	}
	if params.ConfirmsNeededRange.Low > params.ConfirmsNeededRange.High {
		return fmt.Errorf("generator: ConfirmsNeededRange low %d must be <= high %d",
			params.ConfirmsNeededRange.Low, params.ConfirmsNeededRange.High)
	}

	if params.ChannelWeightsDirichletConcentration <= 0 {
		return fmt.Errorf("generator: ChannelWeightsDirichletConcentration %v must be positive",
			params.ChannelWeightsDirichletConcentration)
	}

	if len(params.TraitChannelSets) == 0 {
		return fmt.Errorf("generator: TraitChannelSets must not be empty")
	}
	for trait, channels := range params.TraitChannelSets {
		if len(channels) == 0 {
			return fmt.Errorf("generator: TraitChannelSets[%q] must list at least one channel", trait)
		}
	}

	return nil
}

func (pair BetaPair) validateShapes(name string) error {
	if pair.Alpha <= 0 {
		return fmt.Errorf("generator: %s.Alpha %v must be positive", name, pair.Alpha)
	}
	if pair.Beta <= 0 {
		return fmt.Errorf("generator: %s.Beta %v must be positive", name, pair.Beta)
	}
	return nil
}

func (rangeBox NumericRange) validateOrdered(name string) error {
	if rangeBox.Low > rangeBox.High {
		return fmt.Errorf("generator: %s low %v must be <= high %v", name, rangeBox.Low, rangeBox.High)
	}
	return nil
}

func isProbability(value float64) bool { return value >= 0 && value <= 1 }
