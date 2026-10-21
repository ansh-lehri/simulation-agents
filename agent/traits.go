package agent

// AgentKind values.
const (
	AgentKindEndogenous = "endogenous"
	AgentKindHerd       = "herd"
)

// TraitVector is the frozen per-agent descriptor serialized whole into the
// agents.traits JSONB column. Universal fields are always present; every
// kind-gated block is a pointer with omitempty so herd rows stay lean on
// the wire and off channels are literally absent (not JSON null).
//
// Written once at mint. A future adaptation pass is the only rewriter —
// which is why the paired TraitsVersion and TraitsUpdatedAt live on Agent.
type TraitVector struct {
	AgentKind string `json:"agent_kind"` // AgentKindEndogenous | AgentKindHerd

	// Universal (present on both kinds).
	Skill          float64 `json:"skill"`           // in [0,1]
	Aggressiveness float64 `json:"aggressiveness"`  // in [0,1]
	CapitalSize    int64   `json:"capital_size"`    // int64 because every money-shaped value in this system is integer coins; CapitalSize is money-shaped scale, not a ratio. frozen scale (whale ↔ minnow); session money lives on Agent.TradingCapital, not here
	PnlReactivity  float64 `json:"pnl_reactivity"`  // in [0,1]
	PAct           float64 `json:"p_act"`           // per-tick act probability in [0,1]

	// Endogenous-only.
	Disposition *Disposition       `json:"disposition,omitempty"`
	Diet        *Diet              `json:"diet,omitempty"`
	Rho         *float64           `json:"rho,omitempty"` // reactivity latent in [0,1]: drives most perception fields
	Phi         *float64           `json:"phi,omitempty"` // optimism latent in [-1,+1]: drives directional bias
	Perception  *PerceptionProfile `json:"perception_profile,omitempty"`
	Exit        *ExitProfile       `json:"exit_profile,omitempty"`
	MaxHold        *int     `json:"max_hold,omitempty"`         // position hold cap, in ticks
	ThetaAct       *float64 `json:"theta_act,omitempty"`        // commit bar in [0,1]: aggressive→low, cautious→high
	NeedsConfirm   *bool    `json:"needs_confirm,omitempty"`    // false=immediate actor, true=waits for confirmations
	ConfirmsNeeded *int     `json:"confirms_needed,omitempty"`  // confirmation count required when needs_confirm=true; drawn unconditionally

	// Herd-only.
	ActProb *float64 `json:"act_prob,omitempty"` // tip credulity in [0,1]; not derived from other axes

	// Canonical-only. Set on exactly one endogenous agent per country for
	// the canonical population. The ondemand tipper is assigned at
	// runtime in Redis, never in durable traits — so this stays false on
	// ondemand rows.
	IsTipper bool `json:"is_tipper,omitempty"`
}

// Disposition is the endogenous agent's mix over trading styles. The three
// weights form a Dirichlet simplex and sum to one.
type Disposition struct {
	Trend   float64 `json:"trend"`
	MeanRev float64 `json:"mean_rev"`
	Noise   float64 `json:"noise"`
}

// Diet is the endogenous agent's set of on/off market channels. Flags are
// coupled from Disposition at generation time; all five are kept on the
// wire so a reader sees explicitly which channels are off.
type Diet struct {
	Chart         bool `json:"chart"`
	OrderBook     bool `json:"order_book"`
	MeanReversion bool `json:"mean_reversion"`
	Volume        bool `json:"volume"`
	TimeOfSession bool `json:"time_of_session"`
}

// PerceptionProfile is the shape of the perception block. Per-channel
// sub-blocks are pointers so off channels stay nil (and hence absent on
// the wire). Contents are filled by the generator in the next phase; this
// package only pins the shape.
//
// ChannelWeights is kept as a nested map because the channel-to-trait map
// (which trait consumes which channel) is still owed by the generator
// design — a map absorbs that without forcing a schema change later.
type PerceptionProfile struct {
	// Method-forked channels: present only when the matching diet flag is on.
	Chart *ChannelBlock `json:"chart,omitempty"`
	Range *ChannelBlock `json:"range,omitempty"`

	// Fixed-calc channels: present only when the matching diet flag is on.
	Liquidity     *ChannelBlock `json:"liquidity,omitempty"`
	Volume        *ChannelBlock `json:"volume,omitempty"`
	TimeOfSession *ChannelBlock `json:"time_of_session,omitempty"`

	Bias              map[string]float64            `json:"bias,omitempty"`               // per directional field; derived from Phi
	ChannelWeights    map[string]map[string]float64 `json:"channel_weights,omitempty"`    // per trait → per channel weights
	RereadSensitivity map[string]float64            `json:"reread_sensitivity,omitempty"` // per on channel; threshold in market units for re-perceiving
	DecideSensitivity map[string]float64            `json:"decide_sensitivity,omitempty"` // per consumed directional field; threshold in reading units for re-deciding
}

// ChannelBlock holds the per-channel perception parameters. It covers both
// method-forked channels (chart/range: Method plus reading params and
// cutoffs; the chosen method's parameters land in the maps) and fixed-calc
// channels (liquidity/volume/time: Method is empty, only the parameter
// maps are populated).
type ChannelBlock struct {
	Method         string             `json:"method,omitempty"` // empty for fixed-calc channels
	ReadingParams  map[string]float64 `json:"reading_params,omitempty"`
	ReadingCutoffs map[string]float64 `json:"reading_cutoffs,omitempty"`
}

// ExitProfile is the endogenous agent's position-exit dial set. Mode
// selects one of "distance", "level", "hybrid"; the four numeric dials
// are ratio-shaped (not money), so they are float64.
type ExitProfile struct {
	Mode         string  `json:"mode"`
	StopPct      float64 `json:"stop_pct"`
	TargetPct    float64 `json:"target_pct"`
	StopBuffer   float64 `json:"stop_buffer"`
	TargetBuffer float64 `json:"target_buffer"`
}
