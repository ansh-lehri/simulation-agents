package decision

import (
	"math"

	"simulation-agents/agent"
)

// TraitChannelRole describes how one trait consumes the perception
// channels: exactly one channel supplies its signed directional lean,
// and zero or more other channels supply unsigned conviction
// multipliers. This is a fixed fact about the trading-style design —
// not generated per agent, not a calibration dial — because which
// channel tells a trend-follower "which way" versus "how hard" is a
// mechanism decision, not a tunable number. Noise has no entry here:
// it reads no channel at all, so it never produces a verdict for
// interpret to compute (its impulse comes from the pre-mixer coin
// instead).
type TraitChannelRole struct {
	Lean  string   // the channel that supplies the signed direction
	Mults []string // channels that supply unsigned conviction multipliers
}

// TraitChannelRoles is the fixed channel→trait map: trend reads its
// direction from the chart channel and scales conviction by volume;
// mean-reversion reads its direction from the range channel and also
// scales by volume. This supersedes the generator's TraitChannelSets
// placeholder as the source of truth for "which channel means what" —
// TraitChannelSets still drives the per-agent channel_weights draw
// (an orthogonal concern: how much an agent personally trusts one
// on-channel over another), but the lean-versus-multiplier role a
// channel plays is fixed here, identically for every agent.
var TraitChannelRoles = map[string]TraitChannelRole{
	"trend":    {Lean: "chart", Mults: []string{"volume"}},
	"mean_rev": {Lean: "range", Mults: []string{"volume"}},
}

// traitVerdicts holds the per-trait signed opinions interpret produces
// for one agent's perception bundle on one instrument this tick — every
// agent gets its own traitVerdicts from its own bundle and traits, even
// within the same tick against the same market. Each field is a
// pointer so a trait whose lean channel is absent (diet-off) or
// unreadable this tick (ReadOK=false) can be represented as nil — distinct from a
// present verdict whose magnitude happens to be exactly zero (a flat
// chart direction, say). The mixer's branching on how many traits have
// an opinion at all depends on this distinction never collapsing.
type traitVerdicts struct {
	trend   *float64
	meanRev *float64
}

// interpret turns one agent's perception bundle — that agent's own
// diet-gated readings for one instrument this tick — into a signed
// opinion per trait. Nothing here is shared across agents: called
// again for a different agent against the identical market this same
// tick, it runs on that agent's own bundle and traits and can return a
// completely different verdict. Every trait shares the same
// composition: a verdict is the lean's own sign times its magnitude,
// scaled by that trait's condition multipliers and clamped to [0,1] —
// a multiplier can shrink or amplify the magnitude but never decides
// which way the trade points. A trait whose lean channel is absent
// (diet-off) or unreadable this tick (ReadOK=false) returns nil, never
// a zero-magnitude verdict — the mixer downstream depends on telling
// "this trait has nothing to say" apart from "this trait says nothing
// is happening."
//
// traits matches the documented seam but is not read by this
// implementation: channel_weights would only matter once a trait
// reads more than one lean channel, and today each trait has exactly
// one, so the weight is always the trivial renormalized 1 the
// generator already produces. Wiring that no-op in now would be
// completing a reserved mechanism this stage is explicitly not meant
// to finish.
func interpret(bundle PerceptionBundle, traits agent.TraitVector, roles map[string]TraitChannelRole, params InterpretationParams) traitVerdicts {
	return traitVerdicts{
		trend:   interpretTrend(bundle, roles["trend"], params),
		meanRev: interpretMeanRev(bundle, roles["mean_rev"], params),
	}
}

// interpretTrend reads the chart channel as its directional lean and
// scales the resulting magnitude by cleanliness (always, since it is a
// field of the chart reading itself, not a separate channel) and by
// volume (only when the trend role's multiplier list actually includes
// it — looping over role.Mults rather than reading bundle.Volume
// unconditionally keeps TraitChannelRoles the single place that
// decides which multipliers apply).
func interpretTrend(bundle PerceptionBundle, role TraitChannelRole, params InterpretationParams) *float64 {
	if bundle.Chart == nil || !bundle.Chart.ReadOK {
		return nil
	}
	lean := directionSign(bundle.Chart.Direction) * bundle.Chart.Strength

	multiplier := 1 + params.TrendCleanlinessBoost*bundle.Chart.Cleanliness
	for _, channelName := range role.Mults {
		if channelName == "volume" {
			multiplier *= trendVolumeMultiplier(bundle.Volume, params)
		}
	}

	verdict := signedMagnitude(lean, multiplier)
	return &verdict
}

// interpretMeanRev reads the range channel as its directional lean —
// sign-flipped, since a position near the top of the range is a fade
// (short) setup and a position near the bottom is a fade (long) setup,
// the opposite of what the raw position number would suggest — and
// scales the resulting magnitude by volume when the mean-reversion
// role's multiplier list includes it.
func interpretMeanRev(bundle PerceptionBundle, role TraitChannelRole, params InterpretationParams) *float64 {
	if bundle.Range == nil || !bundle.Range.ReadOK {
		return nil
	}
	lean := -bundle.Range.PositionInRange * bundle.Range.Stretch

	multiplier := 1.0
	for _, channelName := range role.Mults {
		if channelName == "volume" {
			multiplier *= meanRevVolumeMultiplier(bundle.Volume, bundle.Range.Stretch, params)
		}
	}

	verdict := signedMagnitude(lean, multiplier)
	return &verdict
}

// trendVolumeMultiplier rewards a chart move that volume actually
// backs up and penalizes one that does not: a confirmed move (a real
// price change on at-or-above-baseline volume) strengthens the trend
// verdict, a move on thin volume weakens it, and anything in between
// leaves it unscaled. An absent or unreadable volume reading is
// treated as neutral — an agent with volume off its diet should never
// be penalized for a channel it never perceives.
func trendVolumeMultiplier(volume *VolumeReading, params InterpretationParams) float64 {
	if volume == nil || !volume.ReadOK {
		return 1
	}
	switch {
	case volume.Confirming:
		return 1 + params.TrendVolumeConfirmBoost
	case volume.Level == VolLow:
		return 1 - params.TrendVolumeLowDamp
	default:
		return 1
	}
}

// meanRevVolumeMultiplier boosts a mean-reversion verdict when thin
// volume coincides with a range reading stretched beyond its normal
// band — the classic exhaustion setup a fade trader is looking for.
// Any other combination, or an absent/unreadable volume reading,
// leaves the verdict unscaled.
func meanRevVolumeMultiplier(volume *VolumeReading, stretch float64, params InterpretationParams) float64 {
	if volume == nil || !volume.ReadOK {
		return 1
	}
	if volume.Level == VolLow && stretch > 0 {
		return 1 + params.MeanRevVolumeLowExtremeBoost
	}
	return 1
}

// directionSign converts a channel's categorical Direction into the
// signed float the lean formula multiplies by. Direction's own
// underlying values (DirUp=+1, DirFlat=0, DirDown=-1) already encode
// the sign; this exists so the lean formula reads as a direction times
// a strength, not as a raw type conversion.
func directionSign(direction Direction) float64 {
	return float64(direction)
}

// signedMagnitude applies the verdict shape every trait shares: the
// lean's sign passes through untouched, and only the magnitude is
// scaled by the accumulated condition multiplier and clamped to
// [0,1]. This is what structurally guarantees a condition multiplier
// can dampen or boost a verdict but never flip which way it points.
func signedMagnitude(lean, multiplier float64) float64 {
	return directionOf(lean) * clamp01(math.Abs(lean)*multiplier)
}

// directionOf returns the sign of a signed float as -1, 0, or +1.
func directionOf(value float64) float64 {
	switch {
	case value > 0:
		return 1
	case value < 0:
		return -1
	default:
		return 0
	}
}
