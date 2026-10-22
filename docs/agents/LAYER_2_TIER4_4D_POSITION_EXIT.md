Layer 2 — Tier 4 / 4d: Position & Exit Management (Design Notes, LOCKED)

What this doc is. The design of 4d — the manage-what-I-hold branch: for an agent that already holds a position, decide hold / add / exit each tick, and hand any resulting order to 4c.

Cross-refs. Own-state read endpoint: the broker GET /v1/account/observation surface. Perception fields consumed for reversal: LAYER_2_TIER4_4A_PERCEPTION.md. The mixer reused for the add-check: LAYER_2_TIER4_4B_DECISION.md. Execution of exits/adds: LAYER_2_TIER4_4C_EXECUTION.md. Generation of exit_profile / max_hold: LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md.

1. What 4d is & the branch boundary (LOCKED)
The decision tree forks at the top on "do I hold this instrument?" — flat → 4b (should I enter?), holding → 4d (what do I do with it?). The two are mutually exclusive arms of one branch (each is meaningless in the other's state), so exactly one runs per tick.
4d answers a question 4b can't (it consumes own-position + P&L, which 4b lacks) and produces actions 4b can't (hold / add / exit). It outputs an intent to 4c (add or close); it never forms a punch itself.
2. Step 0 — entry condition & the one read (LOCKED)
obs = GET /v1/account/observation?account_id=<handle>
pos = obs.positions[instrument]
if pos absent or pos.qty == 0 → NOT 4d's tick (flat → 4b runs)
else take:  qty (SIGNED: +long / −short),  weighted_avg_entry,  reserved_qty,  balance.available

This single read feeds every 4d step.

3. Step 1 — live picture (LOCKED)
current_price = 4a tick snapshot price (the same price the agent perceives)
unrealized    = (current_price − weighted_avg_entry) × qty     # signed qty → P&L sign is automatic
entry_dir     = long if qty>0 else short
closeable_qty = |qty| − reserved_qty

4d computes unrealized P&L itself (broker returns mark_available:false / P&L=0 until a later phase; and marking against the agent's own perceived price is the consistent choice).

position_context (Redis working memory, written at entry by whoever opened/added — 4b on OPEN, 4d on ADD — read by 4d):

position_context = { entry_dir, entry_channel, entry_tick }

entry_channel = the dominant trait's primary channel at entry (chart for trend-dominant, range for mean-rev-dominant), frozen at entry. entry_tick feeds the time-stop.

4. Step 2 — exit check (LOCKED; OR-logic, first-trigger-wins, fixed priority)

Priority: stop → signal-reversal → target → time. Any fire → build a close intent.

exit_profile (per-agent frozen trait; generated per the perception-profile doc):

exit_profile = { mode: distance|level|hybrid, stop_pct, target_pct, stop_buffer, target_buffer }

Stop/target prices (long shown; short mirrors):

distance: stop = avg_entry×(1−stop_pct);     target = avg_entry×(1+target_pct)
level:    stop = support×(1−stop_buffer);    target = resistance×(1−target_buffer)   # from 4a key_levels / range_bounds
          IF no levels available this tick → ABRUPT MARKET EXIT (Y-rule; deliberate chart-blind cost)
hybrid:   level formula if levels available, else distance formula

(buffers are % of the level price.)

Triggers:

1. STOP      long: price ≤ stop   | short: price ≥ stop
2. REVERSAL  (reuses THIS tick's 4a output for position_context.entry_channel — no re-perceive)
     entry_channel == chart : chart.direction ACTIVELY opposite entry_dir (long+down / short+up); flat ≠ reversal
     entry_channel == range : current_price OUTSIDE range_bounds (the range BROKE — range has no direction field;
                              non-redundant: fires even in profit, distinct from stop and target)
     entry_channel == noise : no reversal (noise entry read no signal)
     read_ok == false       : no reversal this tick
3. TARGET    long: price ≥ target | short: price ≤ target
4. TIME-STOP current_tick − entry_tick > max_hold

max_hold = per-agent frozen trait (generated per the perception-profile doc).

5. Step 3 — if no exit: hold or add (LOCKED)

The only extra action is add to a winner. The add decision reuses the 4b mixer on this tick's readings (shared mechanism, not a duplicate) → {mix_dir, mix_conviction}:

ADD only if ALL:
   mix_dir == entry_dir            # same direction only
   unrealized > 0                  # add to winners, not losers
   mix_conviction ≥ θ_add          # per-agent add-bar
else → HOLD (emit nothing)

The in-profit + same-dir + conviction gate is what makes add rare/gated, not run-every-tick.

θ_add is OPEN — blocked on θ_act (4b-2, unresolved); intended θ_add ≥ θ_act. Treat as a high-skewed provisional draw until 4b-2 lands.

6. Step 4 — hand to 4c (LOCKED)
EXIT → ExecIntent { action: close, dir: opposite(entry_dir) }   # long→SELL, short→BUY-to-cover
ADD  → ExecIntent { action: add,   dir: entry_dir, conviction: mix_conviction }
HOLD → emit nothing

4c sizes a close from position (closeable_qty, binary full exit) at raw aggressiveness; an add via the normal conviction-scaled path. (See 4c doc.)

7. Step 5 — reactivity hooks (NOTE)

4d was designed with flat reactivity hooks (stop_distance × hook, θ_add × hook, max_hold × hook) intended for 4e to fill. 4e (P&L-reactivity) has been DROPPED, so these hooks stay permanently = 1 (no-op). Harmless; left in place as dead seams should reactivity ever be revived.

8. New state / traits introduced (LOCKED)
exit_profile {mode, stop_pct, target_pct, stop_buffer, target_buffer} — frozen trait.
max_hold — frozen trait (ticks).
θ_add — per-agent dial, OPEN (blocked on θ_act).
position_context {entry_dir, entry_channel, entry_tick} — Redis working memory, written by 4b(open)/4d(add), read by 4d.
4c gains the close action (sizes from position; see 4c doc).

End of 4d (Position & Exit Management) — LOCKED (except θ_add, blocked on 4b-2).