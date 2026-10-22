Layer 2 — Tier 4 / Tree Assembly — Implementation Design (LOCKED)

What this doc is. The HOW-in-code for the piece that turns the four Tier-4 nodes into one runnable decision procedure: the body of session.decide — the root hold-check fork, the per-instrument traversal + tree binding (4a → 4b/4d → 4c), and the one-punch resolution that collapses per-instrument candidates into the single order the Tier-3 seam allows. Against the real simulation-agents codebase (actual package/type/func names).

Cross-refs. The seam it fills: session/scheduler.go runActionPhase (:175) + the decideFunc/decideInput in session/scheduler_types.go. The nodes it binds: LAYER_2_TIER4_4A_PERCEPTION_IMPL.md (perceive/coast, accessor, instrumentMemory, tickContext), LAYER_2_TIER4_4B_DECISION_IMPL.md (the flat-path Decide, ExecIntent), LAYER_2_TIER4_4D_POSITION_EXIT_IMPL.md (the holding-path Manage), LAYER_2_TIER4_4C_EXECUTION_IMPL.md (Execute → core.OrderPunchRequest). Framing of the gated tree: LAYER_2_TIER4_FRAMING.md §2. Alternatives: LAYER_2_TIER4_IMPL_ALTERNATIVES.md.

One-line scope. Node docs 4a–4d each design one box; this doc designs the wiring — how the boxes run in order, how the flat/holding fork chooses between 4b and 4d per instrument, and how the multiple things an agent might want to do in one tick collapse to the one order the loop permits.

1. The hard constraint that shapes the whole assembly

The Tier-3 seam is decideFunc func(decideInput) (core.OrderPunchRequest, bool) (session/scheduler_types.go:141; called once per acting agent at session/scheduler.go:190, result appended to batch). It returns exactly one order, or none, per agent per tick. So the entire per-agent tree — 4a over all instruments, the fork, 4b/4d per instrument, down to a single punch — runs inside one decide() call and must collapse to ≤1 punch. That collapse is the one-punch resolution (§5). It is forced by the seam, not a choice.

The act-coin (ws.sessionRNG.Float64() >= traits.PAct, scheduler.go:179) and the cross-agent seeded shuffle (scheduler.go:194) are Tier-3's, upstream/downstream of decide() — unchanged here.

2. Where it runs — the I/O orchestrator + the pure nodes

Same split as every node doc. session.decide is the I/O orchestrator: the one observe(), the per-instrument memory reads + write-on-change Redis mirror (agentState.memoryHandle), the accessor build, and the call into decision.Execute. All computation — perceive, the flat-path decision, the holding-path management, the resolution rule, and execution — is pure in the decision package.

New pure decision surface this phase adds: decision.ResolveOnePunch (§5) and the shared nodeOutcome / delta types (§4). The node entry points become side-effect-free (§6).

3. Provenance / dependency table — what the assembly consumes
Input	Access path	Source
live own-state (balance, positions)	obs := input.observe() → core.AccountObservation (core/observation.go; observe bound to GetObservation at scheduler.go:186)	runtime (broker), read once
the instrument set, in stable order	input.market.instruments []string — new field on snapshotView (populated by snapshot() from ws.instruments; 4a owns this seam addition)	session
per-instrument quote presence	acc.Present(instrument) → input.market.present[instrument]	runtime (4a)
per-instrument carried memory	input.agentState.perception[instrument] (*instrumentMemory)	working memory
tick index / session progress	input.tick (tickContext, 4a seam)	runtime
per-agent RNG	input.agentState.agentRNG	per-agent seeded
account id (for 4c ambient)	input.agentState.capitalHandle	session
node calibration	decision.Params / decision.PerceptionParams (composition root)	global calibration

The assembly introduces no new generated trait and no new core call. Its only new codebase surface is the nodeOutcome types, ResolveOnePunch, and the dependency on snapshotView.instruments (added by 4a).

4. The two state classes — nodeOutcome (the assessment/commitment split)

Each node (Decide/Manage) returns, per instrument, a nodeOutcome that tags its state writes by class so the loop never confuses "what the agent thought" with "what the agent did":

go
type nodeOutcome struct {
    assessment assessmentDelta   // ALWAYS applied, every instrument, win or lose
    candidate  *decision.ExecIntent   // nil ⇒ coast / skip / hold / wait (no order wanted)
    commitment *commitmentDelta   // non-nil ONLY for an open/add; applied ONLY if this candidate
                                   //        wins resolution AND 4c emits a real punch
}
assessmentDelta — the agent's thinking this tick. Claims nothing about holdings; safe to persist whether or not the instrument gets ordered. Carries the 4b/4a working-memory updates: standingOpinion/standingStance, pendingWatch, waitStreak, lastDecidedReadings (and the 4a perception refs/lastBundle, already refreshed in the perceive step). Written during the loop (§7 stage 1c), write-on-change, mirrored via memoryHandle.
commitmentDelta — the record that an order was actually placed: lastCommitTick and the positionContext stamp {EntryDir, EntryChannel, EntryTick} (store reshape in the 4b/4d docs). Written only for the winning instrument, after 4c returns a real punch (§7 stage 4).
OPEN (4b) → {lastCommitTick: current_tick, positionContext: {dir, channelOf(dominant), current_tick}}.
ADD (4d) → re-stamp positionContext (EntryTick=current_tick; EntryChannel from the add mixer's dominant) + lastCommitTick.
CLOSE (4d) → commitment == nil (we do not clear positionContext on emit — it is overwritten on the next open, per 4d impl §9; the close may not have filled yet).

This is the trap the assembly exists to avoid: if instrument A produced an open but instrument B's candidate won the single order slot, writing A's commitment would make the agent believe it holds A when it placed no order. Deferring commitment to post-resolution + post-4c-success closes that trap.

5. One-punch resolution (decision.ResolveOnePunch, pure)
ResolveOnePunch(candidates []instrumentCandidate, rng *rand.Rand) instrumentCandidate

Rule (LOCKED with the user):

Any close candidate beats any open/add. Risk-off first — exiting a stopped/reversed position outranks a fresh bet.
Within the winning class (multiple closes, or — no close present — the opens/adds), tiebreak by higher ExecIntent.Conviction, then an agentRNG coin on an exact tie.

Pure and deterministic given the stable candidate order (§7) + the per-agent seeded agentRNG. Degenerate today: the single-instrument demo yields ≤1 candidate, so resolution is a pass-through; the rule is designed now so the multi-instrument path is correct and reproducible when it arrives (the "DS multi, action [0]" discipline).

6. The side-effect-free refactor the assembly forces on 4b/4d

For the assessment/commitment split to work, the pure nodes must return deltas, not mutate memory:

decision.Decide (4b) and decision.Manage (4d) each return a nodeOutcome; they read carried state but write nothing.
In particular, 4b's inner commit no longer stamps lastCommitTick inline (as LAYER_2_TIER4_4B_DECISION_IMPL.md §7/§9 implied) — it reports the intended commitmentDelta, and session.decide applies it only for the dispatched instrument. (This supersedes that inline write; a one-line note belongs in the 4b doc.)
The loop (session.decide) is the single place that applies deltas and mirrors to Redis — keeping all I/O in session and all computation pure, per the project discipline.
7. The assembled decide() body (the full tree)

Replaces the stub return core.OrderPunchRequest{}, false in session/scheduler_types.go:151.

decide(input decideInput) (core.OrderPunchRequest, bool):

  ── 0. ONE own-state read (shared by the whole tree) ──────────────────────
  obs, err := input.observe()
  if err != nil: return {}, false                 // hot-path rule: error ⇒ agent skips this tick
  acc := decision.NewAccessor(input.market, input.tick)
  tf  := agent timeframe (configured[0] degenerate)

  ── 1. PER-INSTRUMENT TRAVERSAL (STABLE ORDER) ────────────────────────────
  candidates := []instrumentCandidate{}
  for _, instrument := range input.market.instruments {   // stable slice, NOT a map range (determinism)
      if !acc.Present(instrument): continue               // no quote this tick ⇒ skip instrument

      // 1a. PERCEPTION — always, flat OR holding (4d reversal re-reads the held bundle)
      mem := input.agentState.perception[instrument]
      if decision.ShouldReperceive(input.agentState.traits, acc, instrument, tf, mem.refs):
          bundle, refs := decision.Perceive(input.agentState.traits, acc, instrument, tf, input.agentState.agentRNG)
          mem.lastBundle, mem.refs = bundle, refs
          _ = input.agentState.memoryHandle.SetPerceptionRefs(instrument, refs)   // assessment mirror
      bundle := mem.lastBundle

      // 1b. ROOT FORK on the shared observation
      pos, holding := positionFor(obs, instrument)          // §8
      var outcome nodeOutcome
      if holding:
          outcome = decision.Manage(ManageInput{ pos, bundle, mem.positionContext,
                        input.agentState.traits, acc.CurrentPrice(instrument), input.tick,
                        roles, input.agentState.agentRNG, cfg })          // 4d
      else:
          outcome = decision.Decide(DecideInput{ bundle, mem /*4b carried state*/,
                        input.agentState.traits, roles, input.tick, input.agentState.agentRNG, cfg })   // 4b

      // 1c. ASSESSMENT — apply NOW, for every instrument (win or lose)
      applyAssessment(mem, outcome.assessment)
      mirrorAssessment(input.agentState.memoryHandle, instrument, outcome.assessment)   // write-on-change

      // 1d. Park a candidate if this instrument wants to act
      if outcome.candidate != nil:
          candidates = append(candidates, instrumentCandidate{instrument, outcome})
  }

  ── 2. ONE-PUNCH RESOLUTION ───────────────────────────────────────────────
  if len(candidates) == 0: return {}, false        // all coasted / skipped / held / waited
  winner := decision.ResolveOnePunch(candidates, input.agentState.agentRNG)   // close > {conviction, then RNG}

  ── 3. EXECUTE the winner (4c) ────────────────────────────────────────────
  req, ok := decision.Execute(winner.outcome.candidate,
                 ambient{ accountID: input.agentState.capitalHandle, instrument: winner.instrument, acc },
                 obs, input.agentState.traits, input.agentState.agentRNG, cfg)
  if !ok: return {}, false                          // no capital / no two-sided book ⇒ emit nothing

  ── 4. COMMITMENT — ONLY now, the order is real ───────────────────────────
  if winner.outcome.commitment != nil:              // nil for a close; set for open/add
      applyCommitment(input.agentState.perception[winner.instrument], winner.outcome.commitment)
      mirrorCommitment(input.agentState.memoryHandle, winner.instrument, winner.outcome.commitment)

  return req, true

Stage notes.

0 — one read serves the fork, 4c capital, and 4d position. An observe() error sits the agent out (CLAUDE: "any error causes the agent to skip its action"); fire-and-forget means next tick retries.
1a — 4a runs first and unconditionally; the held instrument needs a fresh bundle for 4d's reversal and level-mode exits. Perceive consumes agentRNG, which is why stage 1 must iterate the stable input.market.instruments slice, not the randomized map — otherwise the per-agent draw order (and thus the decisions) would differ run-to-run, violating the LOCKED reproducibility rule.
1b — the only fork in the tree: holding → Manage (4d), flat → Decide (4b), mutually exclusive.
1c/1d — assessment persists for all; only act-wanting instruments become candidates.
2 — your rule, applied to the parked candidates.
3/4 — 4c runs once on the winner; commitment lands only on a real punch.

Three leaves return {}, false: observe error (0); zero candidates (2); 4c declines (3).

8. positionFor helper (pure)
positionFor(obs core.AccountObservation, instrument string) (core.Position, bool):
    for _, p := range obs.Positions:
        if p.Instrument == instrument && p.Qty != 0: return p, true
    return core.Position{}, false      // absent or flat ⇒ 4b

core.AccountObservation.Positions is normalized non-nil by GetObservation (core/observation.go:195), so the range is always safe. Qty is signed; 0/absent ⇒ flat.

9. Codebase changes the assembly requires (summary)
session/scheduler_types.go: replace the decide stub body with §7 (the I/O orchestrator); add the instrumentCandidate + ambient local types.
New pure decision surface: nodeOutcome, assessmentDelta, commitmentDelta, ResolveOnePunch, and the applyAssessment/applyCommitment/mirror* helpers (the mirrors live in session; the appliers mutate instrumentMemory).
decision.Decide (4b) and decision.Manage (4d) return nodeOutcome and become side-effect-free (§6) — 4b's commit reports its commitmentDelta instead of stamping lastCommitTick inline.
Dependency (owned by 4a): snapshotView gains instruments []string (stable order), populated by snapshot() from ws.instruments; stage 1 iterates it. (Ansh is patching the 4a doc + its codebase-changes list for this.)
No new core call, no new generated trait.
10. What the assembly completes / deferred
Completes Tier 4: 4a→4b/4d→4c now run as one gated tree inside the real decide(), reproducible and collapsing to one punch — the node docs' "hands to the tree-assembly phase" lines all resolve here.
Deferred: the real instrument-attention mechanic (degenerate instruments[0]/≤1 candidate now); a 4c-decline fallback to the next-best candidate (iteration 1 emits nothing and retries next tick); the DECIDE worker-pool for heavy ticks (Tier-3's deferred item — this body is the per-agent unit it would parallelize); restart-rebuild of instrumentMemory/positionContext (Tier 6).

End of Tier-4 Tree Assembly implementation design — LOCKED. Alternatives in LAYER_2_TIER4_IMPL_ALTERNATIVES.md.