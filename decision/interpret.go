package decision

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
// from one tick's perception bundle. Each field is a pointer so a
// trait whose lean channel is absent (diet-off) or unreadable this
// tick (ReadOK=false) can be represented as nil — distinct from a
// present verdict whose magnitude happens to be exactly zero (a flat
// chart direction, say). The mixer's branching on how many traits have
// an opinion at all depends on this distinction never collapsing.
type traitVerdicts struct {
	trend   *float64
	meanRev *float64
}
