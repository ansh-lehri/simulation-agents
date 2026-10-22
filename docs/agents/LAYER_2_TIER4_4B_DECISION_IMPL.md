Layer 2 — Tier 4 / 4b Decision — Implementation Design (LOCKED)

What this doc is. The HOW-in-code for 4b, against the real simulation-agents codebase (actual package/type/func names). Complements the design docs: LAYER_2_TIER4_4B_DECISION.md (the WHAT/WHY — note its 4b-2 was marked OPEN there; it is now resolved and locked, see §7), LAYER_2_TIER4_FRAMING.md (§2 the gated tree, §7–§8 noise-as-liveness), and the sibling LAYER_2_TIER4_4A_PERCEPTION_IMPL.md (the seam, instrumentMemory, the accessor, the PerceptionBundle 4b consumes). Generated traits it reads come from LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md. Alternatives weighed are in LAYER_2_TIER4_IMPL_ALTERNATIVES.md.

One-line scope. 4b turns 4a's per-instrument PerceptionBundle + the agent's disposition into an intent — {dir, conviction} to COMMIT, or WAIT, or SKIP. Intent only: the order is 4c, managing a held position is 4d. In code it is the block inside the real decide() that runs for an instrument the agent is flat in (holding → 4d; the flat/holding fork is the root-dispatch stage, built in the separate tree-assembly phase).

1. Where 4b runs

Inside session.decide (the Tier-4 body replacing the stub in session/scheduler_types.go), the pure logic lives in the decision package. Per flat instrument, the chain is: noise coin → 4b-gate → mixer (4b-1) → 4b-2 commit gate, producing at most one candidate open-intent (§9). New decision files: interpret.go, mix.go, gate_decide.go, commit.go (plus the carried-state additions to session's workingAgent and the store reshape).

2. Provenance master table — every 4b input and exactly where it comes from
Input	Access path	Source
Chart.{Direction,Strength,Cleanliness}, Range.{PositionInRange,Stretch}, Volume.{Confirming,Level}, Liquidity.Spread	this tick's PerceptionBundle (4a)	runtime (4a)
session_progress, current_tick	input.tick (tickContext added in 4a)	runtime
Disposition{Trend,MeanRev,Noise} (mixer weights)	input.agentState.traits.Disposition.{Trend,MeanRev,Noise} (*agent.Disposition, endogenous-only)	generated (already in codebase; agent/traits.go)
ChannelWeights (lean weights, inert at one lean)	input.agentState.traits.Perception.ChannelWeights (map[string]map[string]float64)	generated (already)
DecideSensitivity[field] (4b-gate thresholds)	input.agentState.traits.Perception.DecideSensitivity[field] (keys chart.direction/strength, range.position_in_range/stretch)	generated (already)
Aggressiveness (drives θ_act/patience)	input.agentState.traits.Aggressiveness	generated (already); coupling applied at θ_act generation
Diet.OrderBook (spread-shave gate)	input.agentState.traits.Diet.OrderBook	generated (already)
θ_act, needs_confirm, confirms_needed	input.agentState.traits.ThetaAct / NeedsConfirm / ConfirmsNeeded	NEW generated traits — designed/locked (4b-2 thread), absent from code; add to TraitVector + generator (§10)
mixer k, NoiseConviction; multiplier shape coeffs; 4b-2 draw-down consts (open_damp,close_damp,a,b,time_floor,k_spread,spread_ref,spread_floor,cool_damp,cool_tau,cool_floor); WAIT_REASSESS_THRESHOLD	new decision.Params built in cmd/agentsd beside defaultGenParams()	global calibration
RNG	input.agentState.agentRNG (added in 4a)	per-agent seeded
standingOpinion/stance, lastDecidedReadings, pendingWatch, waitStreak, lastCommitTick	input.agentState.perception[instrument] (*instrumentMemory)	working memory (Redis mirror, recovery-only)

Rule of thumb: readings = 4a; weights/sensitivities/aggressiveness/diet = already-generated traits; θ_act/needs_confirm/confirms_needed = new generated traits; multiplier & draw-down coefficients = global calibration; counters/watches = working memory.

3. The channel→trait map

Finalized (replaces the cmd/agentsd/calibration.go TraitChannelSets placeholder trend→{chart,volume}, mean_rev→{range,volume}, noise→{time_of_session}). The map encodes each channel's role — one directional lean channel vs zero-plus multiplier channels — which the placeholder did not:

trend → lean chart; multiplier volume (+ chart.cleanliness as a within-lean multiplier).
mean_rev → lean range; multiplier volume.
noise → no channels (pre-mixer bypass coin; reads nothing).
global condition channels (NOT per-trait — applied once in 4b-2's eff_conv shave): liquidity (spread) and time. Consistent with the signed/unsigned × trait-specific/global split in LAYER_2_TIER4_4B_DECISION.md §4.

Lives as a fixed decision.TraitChannelRoles{ trend:{Lean:"chart",Mults:["volume"]}, mean_rev:{Lean:"range",Mults:["volume"]} } (noise absent). Generated ChannelWeights stays but drops the noise entry (so the generator's TraitChannelSets/Validate no longer requires one); with one lean per trait the weights renormalize to 1 and are inert now — kept for a future multi-lean case.

Order-book scope note (remember this): the order-book/liquidity channel is partly used and partly reserved. spread → used, as a global condition in 4b-2 (spread_mult). depth → unused (rolling baseline was dropped). imbalance (the directional part of the book) → perceived but consumed by no trait, because trend/mean_rev/noise don't trade order-flow direction; it is a reserved seam for a future order-flow/scalper trait. So order-book isn't missing — its condition part feeds 4b-2, its directional part has no trait yet.

4. Interpretation — bundle → per-trait verdicts (decision/interpret.go)

Pure: interpret(bundle, traits, roles) → traitVerdicts{ trend, meanRev *float64 }. Each verdict is a signed value in [-1,+1] (sign = direction, magnitude = conviction), or nil = absent when that trait's lean channel is nil/ReadOK=false. Noise produces no verdict here (mixer's coin).

Per trait: verdict = sign(lean) · clamp(|lean| · Π multipliers, 0, 1); lean signed in [-1,1], each multiplier in [0,~1.5].

trend (lean chart, mults volume + cleanliness):
lean = dirSign(Chart.Direction) · Chart.Strength (up→+1, down→−1, flat→0).
cleanliness multiplier rises with Chart.Cleanliness (clean trend → stronger).
volume multiplier (trend reading): Confirming → boost, Level==Low → dampen, else 1.
Chart nil / ReadOK=false → verdict nil.
mean_rev (lean range, mult volume):
lean = −Range.PositionInRange · Range.Stretch (near top + stretched → strong fade/short; the sign flip is what makes it counter-trend).
volume multiplier (mean_rev reading): low Level at an extreme → boost.
Range nil / ReadOK=false → verdict nil.

Multiplier shapes are the design; their coefficients are calibration (§11). absent (nil) ≠ neutral (0-magnitude) — the mixer's "≥2 verdicts present" rule depends on the distinction.

5. Mixer 4b-1 (decision/mix.go) — Option C (LAYER_2_TIER4_4B_DECISION.md §5)

Combines {V_trend, V_meanRev} + the noise coin into one {dir, conviction}.

u := agentRNG.Float64()
if u < w_noise:                                  // pre-mixer noise coin (every acting tick)
    dir := ±1 by agentRNG
    return opinion{dir, NoiseConviction}, bypass=true   // skips gate+4b-2 → straight COMMIT

present := non-nil of {trend:V_trend, mean_rev:V_meanRev}
if len(present)==0: return SKIP                  // flat, nothing perceived
if len(present)==1: v := the one; return opinion{sign(v), |v|}, bypass=false

dominant := argmax(w_trend, w_mr) over present (tie → agentRNG coin)
dir  := sign(V_dom); conv := |V_dom|
for each secondary present trait t:
    align := +1 if sign(V_t)==dir else −1
    conv  *= 1 + align · w_t · |V_t| · k         // dampen/boost; NEVER flips direction
conv = clamp(conv, 0, 1)
return opinion{dir, conv}, bypass=false
Noise is a per-tick bypass probability, not a blended verdict — because noise reads nothing, it has no directional verdict to combine; its weight instead sets how often the agent acts on impulse. An agent's "nature" is the aggregate over its many ticks (a mixture), not any single tick; and a high-w_noise agent has a thin diet (FRAMING §8) so on a coin-miss it usually has no verdict → SKIP, so it cannot masquerade as a competent signal trader. The bypass is load-bearing for liveness: a signalless agent can still trade (FRAMING §7).
bypass=true → caller commits the impulse directly (skips gate+4b-2). SKIP → both traits absent.
k = secondary sensitivity (calibration); NoiseConviction = the impulse's conviction so 4c can size it (calibration).
6. 4b-gate (decision/gate_decide.go) — re-decide vs coast (…4B_DECISION.md §6)

Sits below the noise coin, above the mixer. Keys off perception-output change (not market change — that's 4a's gate), so the two gates desync.

if pendingWatch != nil:                          // agent is WAITing
    if waitStreak > WAIT_REASSESS_THRESHOLD: RE-DECIDE          // force reassess
    else:                                    waitStreak++; COAST  // stay WAIT (quiet-confirm fix)
else:                                            // flat-idle
    stale := false
    for each consumed directional field f:       // f in DecideSensitivity keys the agent's diet turns on
        if |cur[f] − lastDecidedReadings[f]| > DecideSensitivity[f]: stale = true
    if stale: RE-DECIDE else: COAST
RE-DECIDE → run mixer → 4b-2; then set lastDecidedReadings = cur, waitStreak = 0, update standing/pendingWatch per the 4b-2 outcome.
COAST → reuse the standing stance; no fresh decision (flat-idle → nothing; waiter → stays WAIT).
Why a waiter must not re-decide every tick: the quiet consolidation that is the confirmation would read as "nothing changed" and be coasted past forever; so a waiter coasts and counts ticks, forcing a reassess only past WAIT_REASSESS_THRESHOLD.
Two counters, distinct: waitStreak = consecutive coasted WAIT ticks (anti-freeze, here); confirmsSeen = confirmations toward COMMIT (inside 4b-2). Any RE-DECIDE resets waitStreak=0.
7. 4b-2 commit gate (decision/commit.go) — LOCKED (was OPEN; resolved in the 4b-2 thread)

Turns the mixer opinion into COMMIT / WAIT / SKIP. Conditions only shave conviction (no hard gate in iteration 1).

Step 0 — shave (each a floored [0,1] multiplier):

time_mult     = clamp(1 − open_damp·(1−p)^a − close_damp·p^b, time_floor, 1)   // U-shape; p=session_progress; close dip deeper
spread_mult   = Diet.OrderBook ? clamp(1 − k_spread·(Liquidity.Spread/spread_ref), spread_floor, 1) : 1
cooldown_mult = (lastCommitTick==sentinel) ? 1 : clamp(1 − cool_damp·exp(−gap/cool_tau), cool_floor, 1)   // gap = current_tick − lastCommitTick
eff_conv      = conviction × time_mult × spread_mult × cooldown_mult

Floors mean conditions can shrink conviction hard but never to zero alone — a genuinely strong view survives a rough environment.

Step 1–3 — floor, then the patience split:

if eff_conv < θ_act:              clear pendingWatch;  → SKIP
if !needs_confirm:                clear pendingWatch;  → COMMIT{dir, eff_conv}
// waiter:
if pendingWatch == nil:           pendingWatch = {dir, confirmsSeen:1, sinceTick:current_tick}; → WAIT
elif pendingWatch.dir == dir:     confirmsSeen++; if confirmsSeen ≥ confirms_needed { clear; → COMMIT{dir, eff_conv} } else → WAIT
else:                             pendingWatch = {dir, confirmsSeen:1, sinceTick:current_tick}; → WAIT   // view flipped, restart
Reads only {dir, conviction} from the mixer — conviction already encodes trait agreement/disagreement (Option C bakes it in), so 4b-2 never re-reads mixer internals.
COMMIT carries eff_conv (not raw) → a thin-book / cooldown commit also sizes smaller in 4c.
On COMMIT set lastCommitTick = current_tick (feeds cooldown next time).
θ_act, needs_confirm, confirms_needed are the new generated traits (§10). Aggressiveness drives θ_act/patience at generation, not as a conviction multiplier here.
θ_add (4d's add-gate) = θ_act + margin — unblocked by θ_act existing; detail in the 4d doc.
8. Carried state + store reshape

instrumentMemory (on workingAgent, from 4a) gains the 4b fields:

type instrumentMemory struct {
    refs       store.PerceptionRefs            // 4a gate refs
    lastBundle *decision.PerceptionBundle       // 4a coasted readings
    standingOpinion     float64                 // 4b
    standingStance      string                  // "FLAT_IDLE" | "WAIT"
    lastDecidedReadings map[string]float64
    pendingWatch        *decision.PendingWatch  // {Dir, ConfirmsSeen, SinceTick}; nil = not waiting
    waitStreak          int
    lastCommitTick      int64                   // sentinel = never committed
}

In-memory authoritative; Redis = crash-recovery mirror (live loop never reads it back — Tier 6).

store reshape (all per-instrument, mirroring 4a's PerceptionRefs reshape):

SetStandingOpinion, SetWaitStreak → gain an instrument arg (per-instrument blob under the agent hash RuntimeKey(exchangeID, agentID)).
New setters: SetPendingWatch(inst, *PendingWatch), SetLastCommitTick(inst, int64), SetLastDecidedReadings(inst, map) (LastDecidedReadings had a field but no writer; the other two are new).
Add store.PendingWatch{Dir, ConfirmsSeen, SinceTick}; the RuntimeSnapshot 4b fields go per-instrument. Write-on-change only (on a RE-DECIDE outcome), per the CLAUDE discipline.
9. Control-flow chain — the 4b slice of decide() (per flat instrument)

Precondition: the root-dispatch stage (tree-assembly phase) has already sent holding instruments to 4d; this runs for a flat instrument after 4a produced/coasted its bundle.

u := agentRNG.Float64()
if u < w_noise:                                  // noise coin — EVERY acting tick, above the gate
    dir := ±1 by agentRNG
    candidate = {open, dir, NoiseConviction}; lastCommitTick = current_tick
    standingStance = "FLAT_IDLE"; clear pendingWatch        // bypasses gate+mixer+4b-2
else:
    switch gateDecide(...):
    case COAST:      no candidate (reuse stance: idle→nothing, waiter→stays WAIT)
    case RE-DECIDE:
        opinion := mix(...)                      // bypass=false here
        if opinion == SKIP: clear pendingWatch; standingStance="FLAT_IDLE"; no candidate
        else switch commit(opinion, ...):
             case COMMIT: candidate = {open, dir, eff_conv}; lastCommitTick = current_tick
             case WAIT:   carry pendingWatch; standingStance = "WAIT"; no candidate
             case SKIP:   clear pendingWatch; no candidate
        lastDecidedReadings = cur; waitStreak = 0            // always on RE-DECIDE

Output of 4b: per flat instrument, either one decision.ExecIntent{action:open, dir, conviction:eff_conv} or none. Not here (tree-assembly phase, after 4d): the flat/holding root dispatch and the candidate→one-punch resolution across instruments.

10. New generated traits Tier-4 introduces (codebase additions)

Designed/locked already (4b-2 thread); absent from code. Add to agent.TraitVector as endogenous-only omitempty pointers (same pattern as Rho/Phi), and draw them in orchestrator/generator (a draw_commit_gate.go beside draw_perception.go), values from new GenParams ranges in cmd/agentsd/calibration.go:

ThetaAct *float64 ∈ [0,1] — the commit bar. Aggressiveness-coupled at draw (aggressive → lower θ_act; cautious → higher).
NeedsConfirm *bool — immediate actor vs confirmation-waiter. (Aggressive → more likely immediate.)
ConfirmsNeeded *int — if waiter, confirmations required.
(ThetaAdd for 4d = θ_act + margin — the traits.go reserved/commented field gets populated now that θ_act exists; detail in the 4d impl doc.)
11. New global calibration (composition root, decision.Params)

All placeholders now; shapes are the design, numbers are calibration:

Mixer: k (secondary sensitivity), NoiseConviction.
Interpretation multiplier coefficients (cleanliness→mult, volume→mult for trend/mean_rev).
4b-gate: WAIT_REASSESS_THRESHOLD.
4b-2 draw-downs: open_damp, close_damp, a, b, time_floor (time U); k_spread, spread_ref, spread_floor (spread; spread_ref = a static per-instrument "normal spread" — the one echo of the dropped depth baseline); cool_damp, cool_tau, cool_floor (cooldown).
θ_add margin. And the new generation ranges for ThetaAct/NeedsConfirm/ConfirmsNeeded in GenParams.
12. Codebase changes 4b requires (summary)
New decision files: interpret.go, mix.go, gate_decide.go, commit.go + TraitChannelRoles and PendingWatch/ExecIntent types.
agent.TraitVector: add ThetaAct, NeedsConfirm, ConfirmsNeeded (populate reserved ThetaAdd).
orchestrator/generator: draw_commit_gate.go (draw the three, aggressiveness→θ_act coupling); drop noise from TraitChannelSets/Validate; add their GenParams ranges.
workingAgent.instrumentMemory: add the 4b fields (§8).
store: per-instrument reshape of SetStandingOpinion/SetWaitStreak; new SetPendingWatch/SetLastCommitTick/SetLastDecidedReadings; store.PendingWatch.
cmd/agentsd/calibration.go: finalize TraitChannelSets→roles; new decision.Params; new generation ranges.
13. What 4b hands on / deferred
Hands to the tree-assembly phase: a per-flat-instrument candidate open-intent (or none) → that phase does root dispatch + candidate→one-punch resolution → 4c.
Deferred: order-flow/scalper trait to consume imbalance (§3); multi-lean ChannelWeights (inert now); the real instrument/candidate-selection mechanic (degenerate [0] now).

End of 4b (Decision) implementation design — LOCKED. Alternatives in LAYER_2_TIER4_IMPL_ALTERNATIVES.md.