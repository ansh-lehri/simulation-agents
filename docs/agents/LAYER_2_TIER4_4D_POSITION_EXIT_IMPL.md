Layer 2 — Tier 4 / 4d Position & Exit Management — Implementation Design (LOCKED)

What this doc is. The HOW-in-code for 4d, against the real simulation-agents codebase (actual package/type/func names). Complements the design doc LAYER_2_TIER4_4D_POSITION_EXIT.md (the WHAT/WHY — fully LOCKED except θ_add, which was blocked on θ_act and is now resolved: θ_add = θ_act + margin, see §6). Siblings it leans on: LAYER_2_TIER4_4A_PERCEPTION_IMPL.md (the accessor, instrumentMemory, tickContext, the PerceptionBundle 4d re-reads for reversal/ levels), LAYER_2_TIER4_4B_DECISION_IMPL.md (the mixer 4d reuses for the add-check, ExecIntent, θ_act, the per-instrument store reshape pattern), LAYER_2_TIER4_4C_EXECUTION_IMPL.md (the close/add executor 4d hands to). Generated traits it reads (exit_profile, max_hold) come from LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md. Own-state read: core/observation.go. Alternatives: LAYER_2_TIER4_IMPL_ALTERNATIVES.md.

One-line scope. 4d is the manage-what-I-hold branch: for an instrument the agent already holds, decide hold / add / exit this tick and hand any resulting order to 4c. It consumes own-position

computed P&L (which 4b lacks) and produces actions 4b can't (hold/add/exit). Intent only — the punch is 4c. It is pure implementation: the design is locked, this doc just names the code.
1. Where 4d runs

Pure logic in a new decision/manage.go: Manage(in ManageInput) (ExecIntent, bool) (bool=false = hold / emit nothing). Like 4a/4b/4c, all I/O stays in session.decide (session/scheduler_types.go, replacing the stub) and all computation is pure in decision.

Per the root-dispatch (built in the tree-assembly phase), decide() reads the agent's live own-state once via the injected input.observe() (func() (core.AccountObservation, error), bound to core.Client.GetObservation per session/interfaces.go), then routes each instrument: flat → 4b, holding → 4d. So 4d does not issue its own read — it receives that one core.AccountObservation and its own core.Position. (Same observation 4c reads for capital; one read serves root-dispatch + 4b-sizing + 4d.)

obs, err := input.observe()                 // ONE read per decide(), in root-dispatch
// ... root-dispatch: for each instrument, find its position in obs.Positions
pos, holding := positionFor(obs, instrument)   // holding ⇒ 4d
if holding:
    intent, ok := decision.Manage(ManageInput{
        pos:        pos,                                        // core.Position (SIGNED Qty)
        bundle:     input.agentState.perception[instrument].lastBundle,  // THIS tick's 4a output
        posCtx:     input.agentState.perception[instrument].positionContext,
        traits:     input.agentState.traits,
        price:      <acc.CurrentPrice(instrument)>,             // 4a accessor
        tick:       input.tick,                                 // tickContext (4a seam)
        roles:      decision.TraitChannelRoles{…},              // 4b map (for the add mixer)
        rng:        input.agentState.agentRNG,                  // 4b/4c per-agent stream
        cfg:        <decision.Params>,
    })
    // intent (close|add) → tree-assembly one-punch resolution → 4c.Execute
2. Provenance master table — every 4d input and exactly where it comes from
Input	Access path	Source
pos.Qty (signed), pos.WeightedAvgEntry, pos.ReservedQty	obs.Positions[i] where Positions[i].Instrument == instrument; obs = input.observe() → core.AccountObservation (core/observation.go)	runtime (broker)
balance.available	obs.Balance.Available (core.AccountBalance.Available int64)	runtime (broker) — read, but add-capital math is 4c's
current_price	4a accessor acc.CurrentPrice(instrument) (int64, bool) (decision/accessor.go)	runtime (4a)
Chart.Direction, Chart.KeyLevels, Chart.ReadOK	input.agentState.perception[instrument].lastBundle.Chart (*decision.ChartReading)	runtime (4a, this tick)
Range.RangeBounds, Range.ReadOK	…lastBundle.Range (*decision.RangeReading)	runtime (4a, this tick)
entry_dir, entry_channel, entry_tick	input.agentState.perception[instrument].positionContext (*store.PositionContext{EntryDir,EntryChannel,EntryTick})	working memory (stamped at open/add; Redis mirror)
current_tick	input.tick.tickIndex (tickContext, 4a seam)	runtime
exit_profile {Mode, StopPct, TargetPct, StopBuffer, TargetBuffer}	input.agentState.traits.Exit (*agent.ExitProfile, agent/traits.go:107)	generated (already in codebase; orchestrator/generator/draw_exit_and_hold.go)
max_hold	input.agentState.traits.MaxHold (*int, agent/traits.go:33)	generated (already; draw_exit_and_hold.go)
θ_act	input.agentState.traits.ThetaAct (*float64)	generated — new trait added by 4b (4b impl §10)
margin (θ_add = θ_act+margin), mixer k/NoiseConviction, interpret coeffs	decision.Params (composition root, cmd/agentsd/calibration.go)	global calibration
Disposition{Trend,MeanRev,Noise}, ChannelWeights (add mixer)	input.agentState.traits.Disposition.*, …traits.Perception.ChannelWeights	generated (already)
RNG	input.agentState.agentRNG	per-agent seeded (4a seam)

Rule of thumb: position = broker observation; price + bundle = 4a this tick; entry conditions = position_context working memory; exit dials + max_hold = already-generated traits; θ_act = the new 4b trait; margin/mixer coeffs = global calibration.

3. Step 0 — the position (no own read)

4d receives pos core.Position from the single root-dispatch observe(). Guard (defensive — root dispatch already proved holding): pos.Qty == 0 → not 4d's tick. Else it takes pos.Qty (signed: +long / −short), pos.WeightedAvgEntry, pos.ReservedQty, and obs.Balance.Available. pos.MarkAvailable / pos.LTP / pos.GrossUnrealized are ignored — the broker ships them as 0 (mark cache misses until a later phase, core/observation.go:68); 4d marks itself (§4).

4. Step 1 — the live picture (decision/manage.go)
entry_dir     = long  if pos.Qty > 0 else short
unrealized    = (current_price − pos.WeightedAvgEntry) × pos.Qty    // SIGNED qty → sign is automatic, no branch
closeable_qty = abs(pos.Qty) − pos.ReservedQty                      // reserved units sit behind a resting order
All int64 ticks/coins; no float (CLAUDE value discipline). unrealized is coins.
4d computes unrealized itself against the agent's own perceived current_price — the consistent choice, and forced anyway while MarkAvailable=false.
position_context is read from input.agentState.perception[instrument].positionContext (§9); if it is nil (recovery edge — a position exists but no stamp, e.g. after an L2 restart) 4d treats entry_channel as unknown → reversal is skipped that tick, and entry_tick defaults to current_tick (time-stop disarmed until re-stamped). Harmless degrade, never a crash.
5. Step 2 — the exit check (OR-logic, first-trigger-wins, fixed priority)

Priority stop → signal-reversal → target → time-stop. The first trigger that fires builds a close intent and the rest are not evaluated. Prices are built from exit_profile first.

5.1 Stop/target prices from exit_profile (long shown; short mirrors: stop above, target below)
switch traits.Exit.Mode {                                  // "distance" | "level" | "hybrid"
case "distance":
    stop   = WeightedAvgEntry × (1 − StopPct)
    target = WeightedAvgEntry × (1 + TargetPct)            // TargetPct = StopPct×rewardRisk at generation → target>stop guaranteed
case "level":
    (support, resistance), ok = levelsFor(entry_channel, lastBundle)   // §5.2
    if !ok:  → ABRUPT MARKET EXIT this tick (Y-rule; the deliberate chart-blind cost)
    stop   = support    × (1 − StopBuffer)
    target = resistance × (1 − TargetBuffer)
case "hybrid":
    use level formula if levelsFor(...) ok, else the distance formula
}

Integer-tick arithmetic: compute on int64 with the percentage applied as v − v×pct using the project's tick scale, rounded once (math.Round at the boundary, consistent with 4a's level rounding).

5.2 levelsFor(entry_channel, bundle) — where support/resistance come from (impl resolution of the design's "4a key_levels / range_bounds")
entry_channel == "chart":  need bundle.Chart!=nil && Chart.ReadOK && len(Chart.KeyLevels) with one
                           level below price (→ support = nearest below) and one above (→ resistance = nearest above)
entry_channel == "range":  need bundle.Range!=nil && Range.ReadOK
                           support = RangeBounds[0] (lower), resistance = RangeBounds[1] (upper)
entry_channel == "noise":  no level channel → ok=false  (noise entry read no structure)
any required side missing / channel nil / ReadOK=false → ok=false   // → Y-rule for level mode; → distance for hybrid

Chart.KeyLevels []int64 and Range.RangeBounds [2]int64 are exactly the fields 4a rides through for 4c/4d (4a impl §7, §13).

5.3 Triggers (long shown; short mirrors the inequality)
1. STOP      pos long: current_price ≤ stop      | short: current_price ≥ stop
2. REVERSAL  (reuses THIS tick's bundle for position_context.entry_channel — no re-perceive)
     entry_channel=="chart": bundle.Chart.ReadOK && bundle.Chart.Direction is ACTIVELY opposite entry_dir
                             (long & DirDown, or short & DirUp); DirFlat ≠ reversal
     entry_channel=="range": bundle.Range.ReadOK && current_price OUTSIDE [RangeBounds[0],RangeBounds[1]]
                             (the range BROKE; range has no direction — fires even in profit, so it is
                              non-redundant with stop/target)
     entry_channel=="noise": no reversal       | ReadOK==false / bundle channel nil: no reversal this tick
3. TARGET    pos long: current_price ≥ target   | short: current_price ≤ target
4. TIME-STOP current_tick − entry_tick > *traits.MaxHold          // tick cap; dereference the *int

Any fire → exit = true, remember nothing else. (Reversal reading the same bundle 4a already produced this tick for this held instrument is why 4a's §1 loop perceives all instruments, not just flat ones.)

6. Step 3 — no exit: hold or add

The only non-exit action is add to a winner. 4d reuses the 4b mixer on this tick's bundle (shared mechanism, not a duplicate): interpret(bundle, traits, roles) → mix(verdicts, traits, rng, cfg) (4b impl §4–§5) → {mix_dir, mix_conviction}.

ADD only if ALL:
    mix_dir == entry_dir                 // same direction — scale the position, never reverse here
    unrealized > 0                       // add to winners, not losers
    mix_conviction ≥ θ_add               // θ_add = *traits.ThetaAct + cfg.Margin   (RESOLVED; was blocked on θ_act)
else → HOLD (return _, false)
θ_add populates the agent/traits.go reserved/commented ThetaAdd seam now that θ_act exists; it is runtime-derived (*traits.ThetaAct + cfg.Margin), not a stored/generated trait — so the generator's "do NOT populate theta_add" rule (generator CLAUDE.md) still holds. Margin ≥ 0 ⇒ θ_add ≥ θ_act, so the add bar is always at least the entry bar.
Add-check skips the noise-coin bypass (impl resolution): 4d calls the mixer's signal verdict path only. An add is a signal-driven scaling decision; the noise coin is an entry-liveness impulse (4b/FRAMING §7), not a reason to pyramid a live position. (Logged in alternatives.)
The in-profit + same-dir + θ_add triad is what makes adds rare, not every-tick.
7. Step 4 — hand to 4c
EXIT → ExecIntent{ Action: close, Dir: opposite(entry_dir) }            // long→SELL, short→BUY-to-cover
ADD  → ExecIntent{ Action: add,   Dir: entry_dir, Conviction: mix_conviction }
HOLD → return _, false

decision.ExecIntent{Dir, Conviction, Action} is 4b/4c's existing type (4c impl §2). 4c sizes a close from closeable_qty at raw aggressiveness (4c impl §8) and an add via the normal conviction-scaled formulation-Y path (4c impl §4). 4d never forms a core.OrderPunchRequest — the tree-assembly phase routes the chosen intent into decision.Execute.

8. Step 5 — reactivity hooks (dead seams)

4e (P&L-reactivity) is dropped, so the designed hooks (stop_distance × hook, θ_add × hook, max_hold × hook) stay permanently = 1. Implementation keeps them absent (no multiply by a literal 1 to maintain) — a note, not code. If reactivity is ever revived it reintroduces the multipliers here.

9. position_context — working memory + the store reshape

instrumentMemory (on workingAgent, from 4a/4b) gains the 4d field:

type instrumentMemory struct {
    refs       store.PerceptionRefs            // 4a
    lastBundle *decision.PerceptionBundle       // 4a
    // …4b fields (standingOpinion, pendingWatch, waitStreak, lastCommitTick, …)…
    positionContext *store.PositionContext       // 4d — {EntryDir, EntryChannel, EntryTick}; nil = flat/unknown
}

store.PositionContext{EntryDir, EntryChannel string; EntryTick int64} already exists (store/runtime_store.go:49). In-memory authoritative; Redis = crash-recovery mirror only.

store reshape (per-instrument, mirroring 4a's PerceptionRefs and 4b's reshape):

SetPositionContext(ctx, exchangeID, agentID, *PositionContext) → gains an instrument arg; writes one blob per instrument under the agent hash RuntimeKey(exchangeID, agentID) (field position_ctx:{instrument}), replacing the single flat FieldPositionContext. The RuntimeHandle.SetPositionContext wrapper (session/scheduler_types.go:47) gains the same arg.
Write-on-change, per CLAUDE discipline — only when a stamp changes (open/add).

Who stamps it (coupling 4d requires on the shared commit path):

4b on COMMIT (open) — 4b's commit path (4b impl §9) already knows the mixer's dominant trait, so it stamps positionContext = {EntryDir: dir, EntryChannel: channelOf(dominant), EntryTick: current_tick} where channelOf: trend→"chart", mean_rev→"range", noise-bypass→"noise". This is the one addition 4d forces onto 4b's COMMIT (4b stamped lastCommitTick but not positionContext).
4d on ADD — re-stamps the same way (EntryChannel re-resolved from the add mixer's dominant; EntryTick = current_tick, so the time-stop clock restarts from the last add, and the stop/target recompute off the broker's new WeightedAvgEntry automatically).

Not cleared at close-emit. The close order may not have filled yet, so clearing on emit would lose entry_channel/entry_tick for the still-open position next tick. positionContext is simply overwritten by the next open/add; a stale stamp for a since-closed instrument is harmless (the flat path = 4b, which ignores it and overwrites on its next COMMIT).

Stamp-at-commit vs fill (honest edge). The stamp is written at the decision that opens/adds, not at the broker fill. If a limit open never fills the agent stays flat, 4d never runs, and the stamp is unused until the next COMMIT overwrites it. It is only ever read while actually holding, so this is harmless.

SUPERSEDED by LAYER_2_TIER4_TREE_ASSEMBLY_IMPL.md §6: 4d does not stamp positionContext directly on ADD as described above — decision.Manage is side-effect-free and reports the re-stamp as a commitmentDelta instead (same EntryChannel/EntryTick derivation), applied by the tree-assembly phase only for the resolved winner, after a real punch. The add-check's computation (the three-condition gate, the reused mixer call) is unchanged — only how the result gets reported changed.

10. Codebase changes 4d requires (summary)
New decision/manage.go + ManageInput (and a small levelsFor/channelOf helper). Reuses existing decision surfaces: interpret, mix, TraitChannelRoles, ExecIntent, the accessor, PerceptionBundle.
agent/traits.go: populate the reserved ThetaAdd conceptually — but it is runtime-derived (ThetaAct + cfg.Margin), so no new generated field; ThetaAct itself is added by 4b (4b impl §10).
decision.Params: add Margin (θ_add offset). All other dials 4d uses are the 4b mixer's (k, NoiseConviction, interpret coeffs) — already present.
workingAgent.instrumentMemory: add positionContext *store.PositionContext (§9).
store: per-instrument reshape of SetPositionContext (+ RuntimeHandle.SetPositionContext); the RuntimeSnapshot.PositionContext field goes per-instrument. store.PositionContext type already exists — no new type.
decision commit path (4b): 4b's COMMIT also stamps positionContext (§9). No core change — 4d only reads core.AccountObservation and hands an ExecIntent to 4c, which builds the punch.
11. What 4d hands on / deferred
Hands to the tree-assembly phase: per held instrument, one ExecIntent{close|add} or nothing → one-punch resolution → decision.Execute (4c) → dispatcher → Core.
Deferred: partial trim / scale-out on exit (binary full exit now, per 4c §8); per-instrument tick size for level math (step-1 rounding now, per 4c §6); reactivity hooks (4e dropped, §8); the recovery-rebuild that would repopulate positionContext after an L2 restart (Tier 6 — §4 degrades safely until then).

End of 4d (Position & Exit Management) implementation design — LOCKED. Alternatives in LAYER_2_TIER4_IMPL_ALTERNATIVES.md.