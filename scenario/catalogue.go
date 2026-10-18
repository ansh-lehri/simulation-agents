package scenario

// Knob names. A knob is the smallest unit a scenario can shove — a
// root generation dial (or lifecycle-curve rate) that re-derives
// everything downstream when its value changes. The strings are
// stable identifiers: the scenario registry references them, the
// baseline builder looks them up, and the merge engine routes each
// one to its ruler. Keeping them as named constants means a typo in
// one caller site is a compile error rather than a silent miss at
// draw time.
const (
	// KnobAggressivenessMean shifts the mean of the aggressiveness
	// Beta — a higher mean draws a more order-crossing crowd.
	KnobAggressivenessMean = "aggressiveness.mean"

	// KnobSkillMean shifts the mean of the skill Beta — higher skill
	// pushes agents toward sophisticated perception methods and the
	// level-mode exit profile.
	KnobSkillMean = "skill.mean"

	// KnobPnlReactivityMean shifts the mean of the P&L reactivity
	// Beta — higher reactivity makes agents cut winners / losers
	// faster against their frozen plan.
	KnobPnlReactivityMean = "pnl_reactivity.mean"

	// KnobReactivityMean shifts the mean of the per-agent temperament
	// latent used by the (1 - reactivity) placement rule — higher
	// values give a jumpier crowd with shorter perception windows and
	// shorter max-hold.
	KnobReactivityMean = "reactivity.mean"

	// KnobPActMean shifts the mean of the per-agent per-tick action
	// probability Beta — higher values give a crowd that fires more
	// often inside its own window.
	KnobPActMean = "p_act.mean"

	// KnobHerdProbability shifts the coin weight that splits the
	// population between endogenous and herd agents — higher values
	// draw a more tip-following crowd.
	KnobHerdProbability = "herd_probability"

	// KnobCapitalMedian shifts the median of the capital log-normal —
	// higher values give a deeper-pocketed crowd.
	KnobCapitalMedian = "capital.median"

	// KnobCapitalSpread shifts the log-space spread of the capital
	// log-normal — higher values stretch the tails (more whales AND
	// more minnows at the same time).
	KnobCapitalSpread = "capital.spread"

	// KnobDisposition shifts the three-way disposition simplex
	// (trend / mean-reversion / noise). This is the one vector-valued
	// knob; the merge engine shoves its log-ratios component-wise and
	// softmax-renormalizes back to a simplex summing to 1.
	KnobDisposition = "disposition"

	// KnobOptimismCenter shifts the center of the optimism latent on
	// (-1, +1), expressed through the arctanh ruler. A positive shove
	// leans the population bullish; a negative shove, bearish. The
	// phi-bimodal switch, when ON, overrides this knob — a bimodal
	// scenario is defined by its two camps, not a single center.
	KnobOptimismCenter = "optimism.center"

	// KnobPresenceEntryRate shifts the baseline arrival rate of
	// agents into the live session — higher values give a busier
	// floor on average.
	KnobPresenceEntryRate = "presence.entry_rate"

	// KnobPresenceStepOutRate shifts the baseline rate at which
	// present agents temporarily step away — higher values give a
	// thinner average attendance against the same arrival rate.
	KnobPresenceStepOutRate = "presence.stepout_rate"

	// KnobPresenceDoneRate shifts the baseline rate at which agents
	// finish for the session — higher values empty the floor sooner.
	KnobPresenceDoneRate = "presence.done_rate"

	// KnobPresenceReentryRate shifts the baseline rate at which a
	// stepped-out agent returns to the floor — higher values keep
	// recurring participants rotating back in faster.
	KnobPresenceReentryRate = "presence.reentry_rate"
)

// KnobCatalogue maps every valid knob name to the ruler the merge
// engine must apply to it. Only knob names present in the catalogue
// are shoveable; the registry loader validates every direction key
// against this set at startup so a typo in a scenario preset fails
// the process early rather than silently at the first on-demand
// bring-up.
type KnobCatalogue map[string]Ruler

// DefaultCatalogue returns the canonical catalogue of shoveable
// knobs. The ruler assignments are the only property-of-the-knob
// decisions in the whole scenario layer: a probability goes on
// Logit, a positive scale on Log, a signed latent on Arctanh, the
// disposition simplex on LogRatio. Everything downstream (η sampling,
// shove math, invert, generator consumption) reads this map rather
// than carrying its own knob list.
func DefaultCatalogue() KnobCatalogue {
	return KnobCatalogue{
		KnobAggressivenessMean:  Logit,
		KnobSkillMean:           Logit,
		KnobPnlReactivityMean:   Logit,
		KnobReactivityMean:      Logit,
		KnobPActMean:            Logit,
		KnobHerdProbability:     Logit,
		KnobCapitalMedian:       Log,
		KnobCapitalSpread:       Log,
		KnobDisposition:         LogRatio,
		KnobOptimismCenter:      Arctanh,
		KnobPresenceEntryRate:   Log,
		KnobPresenceStepOutRate: Log,
		KnobPresenceDoneRate:    Log,
		KnobPresenceReentryRate: Log,
	}
}

// Has reports whether the given knob name is registered in the
// catalogue. The registry loader uses this to reject unknown direction
// keys at startup.
func (catalogue KnobCatalogue) Has(knobName string) bool {
	_, present := catalogue[knobName]
	return present
}

// RulerFor returns the ruler associated with the given knob name.
// The boolean second return is true when the knob is present; a
// caller that already validated the name via Has can ignore it.
func (catalogue KnobCatalogue) RulerFor(knobName string) (Ruler, bool) {
	ruler, present := catalogue[knobName]
	return ruler, present
}
