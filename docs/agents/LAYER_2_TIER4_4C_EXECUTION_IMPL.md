Layer 2 — Tier 4 / 4c Execution — Implementation Design (LOCKED)

What this doc is. The HOW-in-code for 4c, against the real simulation-agents codebase. Complements the design doc LAYER_2_TIER4_4C_EXECUTION.md (WHAT/WHY — fully LOCKED, so 4c is pure implementation, no open design). Sibling impl docs: LAYER_2_TIER4_4A_PERCEPTION_IMPL.md (the accessor, workingAgent, the seam), LAYER_2_TIER4_4B_DECISION_IMPL.md (the ExecIntent producer, decision.Params). Punch contract: core/order_punch.go. Alternatives: LAYER_2_TIER4_IMPL_ALTERNATIVES.md.

One-line scope. 4c is the execute stage: it turns a committed ExecIntent into a core.OrderPunchRequest, filling the three unknown punch fields {type, qty, limit_price} (plus side/TIF); account_id, instrument are ambient. It's the shared executor both 4b (open) and 4d (add, close) feed. Axes: capital → qty, aggressiveness → type+price, skill → execution-noise; disposition does not enter.

1. Where 4c runs

Pure logic in decision/execute.go: Execute(intent, ambient, acc, traits, rng, cfg) → (core.OrderPunchRequest, bool) (bool=false = no order). It is not inside 4b or 4d — it's the executor they hand to, invoked from the tree-assembly phase once that phase has picked the agent's single candidate intent (the one-punch resolution). session.decide supplies the observation + accessor + traits + RNG and sends the result to the existing dispatcher (punchCh → runDispatcher → core.PunchOrder). 4c itself never touches the network or the channel.

2. Input contract & provenance master table

decision.ExecIntent{ Dir (long|short), Conviction float64[0,1], Action (open|add|close) } — from 4b (open) or 4d (add,close). Ambient (4c reads itself, not in the intent): account_id, instrument, current position across all instruments.

Input	Access path	Source
Dir, Conviction, Action	ExecIntent	runtime (4b/4d)
account_id	agentState.capitalHandle	ambient (workingAgent)
instrument	the candidate's instrument	ambient
A (trading capital)	agentState.tradingCapital (int64 coins)	session (set at bring-up)
Σ open-position cost, pos.Qty (signed), pos.ReservedQty	observe() → core.AccountObservation.Positions[]	runtime (broker)
aggressiveness, skill	agentState.traits.Aggressiveness / Skill	generated
best_bid, best_ask, spread, current_price	4a accessor (CurrentPrice, Spread, quote)	runtime (4a)
max_frac, γ, θ_market, k_place, σ_exec	decision.Params (composition root)	global calibration
RNG (two noise draws)	agentState.agentRNG	per-agent seeded

Note: θ_market is a global dial, not a per-agent trait — the per-agent variation in market-vs-limit comes from each agent's own aggressiveness.

3. Position-case dispatch

4c reads the agent's position in the instrument and routes. Because formulation Y nets out existing positions, OPEN and ADD are the same math, so it collapses to three paths:

pos := observation.Positions[instrument]        // Qty signed; absent ⇒ flat
switch:
  Action == close                         → CLOSE      (§8; size from position)
  holding AND sign(pos.Qty) != sign(Dir)  → IGNORE      (return _, false — reduce/flip is 4d's job)
  else (flat, or holding same-dir)        → OPEN/ADD    (§4–§7)

close is explicit (only 4d sends it); open-vs-add is derived from position but runs one formula. In the integrated flow the root dispatch already sends holding→4d, so IGNORE is a safety guard, not a hot path.

4. qty — OPEN/ADD path (formulation Y, §4)
A_available   = A − Σ( pos.WeightedAvgEntry · |pos.Qty| over ALL observation.Positions )   // one shared pool
base_notional = A_available × max_frac × Conviction
qty           = floor( base_notional / current_price )
A_available is free capital across all instruments (positions compete for one pool).
max_frac, Conviction ≤ 1 ⇒ base_notional ≤ A_available, so no separate ceiling clamp — OPEN and ADD differ only in how much A_available is left.
Edges: A_available ≤ 0 or qty == 0 → return _, false. current_price unavailable → skip.
Dimensional check: price(ticks)·qty(units)=cost(coins) → coins / price = units holds on the system's integer coin/tick scale.
5. type — market vs limit (§5)
effective_aggr = aggressiveness ^ (1 − γ · Conviction)      // asymmetric/up-only; stays in [0,1]
type = (effective_aggr ≥ θ_market) ? market : limit
Conviction warps aggressiveness upward only (toward market); power form keeps [0,1], bites hardest near the cutoff, leaves deeply patient/aggressive agents ~unchanged.
effective_aggr carries into limit_price (one consistent personality per trade).
TIF pairing (codebase requirement): market → TIF=core.TimeInForceImmediateOrCancel, LimitPrice=0; limit → TIF=core.TimeInForceGoodTillCancel, LimitPrice>0.
6. limit_price — spread-fraction placement (limit only; market → 0) (§6)
depth_into_spread = effective_aggr × k_place
BUY : limit_price = round_to_tick( best_bid + depth_into_spread × spread )   // round DOWN
SELL: limit_price = round_to_tick( best_ask − depth_into_spread × spread )   // round UP
Aggressive (≈1) → far touch (crosses, fast fill, worse price); passive (≈0) → own touch (rests, better price, may miss). Spread-relative → tighter in liquid books, free.
Rounding toward less-aggressive (BUY down / SELL up) so a tick-snap never accidentally crosses.
round_to_tick: prices are int64 ticks (CLAUDE value-discipline), so iteration 1 rounds to the nearest whole tick (grid step 1). A coarser per-instrument tick size is not in store.Quote/the snapshot; if one arrives upstream later, thread it in and snap to its multiple.
Edge: needs a two-sided book (HasBid && HasAsk); if absent → no limit can be placed → return _, false (market orders unaffected).
7. Skill execution-noise (§7)
s = σ_exec × (1 − skill)                                              // width
qty_final   = clamp( round( qty × (1 + N(0,s)) ), 1, floor(A_available / current_price) )  // multiplicative
limit_final = round_to_tick( limit_price + N(0,s) × spread )          // additive-in-spread; limit only
Two independent fresh seeded draws (size, price). Perturbs magnitudes only — never type/Dir/side (same categorical guardrail as 4a).
σ_exec is a global constant, not per-agent — per-agent variation already lives in skill (mirrors nothing else double-encoding skill).
qty_final is re-clamped against free capital (the one place formulation Y's dropped clamp returns, as post-noise safety) and floored at 1.
A perturbed buy price may cross the ask = low-skill agent accidentally pays up — accepted realism.
8. The close action (§8) — 4d exits route here

Differs from open/add only in sizing; type/price/skill reused verbatim (why 4c was extended, not duplicated):

qty   = closeable_qty = |pos.Qty| − pos.ReservedQty       // binary full exit; reserved units can't be re-closed
side  = opposite of position                              // long → SELL, short → BUY-to-cover
// capital math SKIPPED (unwinding); Conviction IGNORED
effective_aggr = aggressiveness                           // RAW — no conviction warp (exit urgency = base temperament)
type/limit_price per §5/§6 from raw effective_aggr
qty_final = clamp( round( closeable_qty × (1 + N(0,s)) ), 1, closeable_qty )   // ceiling = closeable_qty, not A_available

Binary full exit for iteration 1 (partial trim/scale-out deferred). TIF pairs with type as §5.

9. Output assembly + pre-flight wiring

Pack computed + ambient into core.OrderPunchRequest:

Field	Value
AccountID	agentState.capitalHandle
Instrument	the instrument
Side	open/add: Dir long→OrderSideBuy, short→OrderSideSell; close: opposite of position
Type	OrderTypeMarket / OrderTypeLimit
TIF	ioc (market) / gtc (limit)
Qty	qty_final
LimitPrice	limit_final (limit) / 0 (market)
No-order (bool=false): IGNORE, qty==0, A_available≤0, or a limit with no two-sided book.
Pre-flight relationship: 4c builds valid-by-construction — it mirrors the exact contract core.validateOrderPunchRequest enforces (limit⇒gtc & price>0; market⇒ioc & price==0; Qty>0; Side∈{buy,sell}). So PunchOrder's pre-flight is a guard, never a filter — it should never fire on a 4c-built order. We never rely on it to fix anything; a rejected or failed punch is logged-and-dropped (fire-and-forget) and reconciled via GetObservation next tick — never retried (a write retry risks a duplicate fill).
10. New calibration (composition root, decision.Params)

All placeholders; shapes are design, numbers are calibration: MaxFrac (max fraction of free capital one full-conviction entry deploys), Gamma (conviction→aggressiveness warp), ThetaMarket (market/limit cutoff, global), KPlace (limit placement reach), SigmaExec (execution-noise width, global; placeholder ~0.1–0.2).

11. Codebase changes 4c requires (summary)
New decision/execute.go + decision.ExecIntent type.
decision.Params gains MaxFrac, Gamma, ThetaMarket, KPlace, SigmaExec (set in cmd/agentsd/calibration.go).
No agent/store/core changes — 4c only reads existing surfaces (traits, observation, accessor) and writes the existing core.OrderPunchRequest. (The TIF field already exists; 4c just sets it — it was unmentioned in the design text, not missing from code.)
12. What 4c hands on / deferred
Hands up: (core.OrderPunchRequest, bool) to the tree-assembly phase → dispatcher → Core.
Deferred: per-instrument coarser tick size (step-1 now); partial trim/scale-out on close (binary now); the IGNORE guard becomes live only if a future flow lets 4b fire while holding.

End of 4c (Execution) implementation design — LOCKED. Alternatives in LAYER_2_TIER4_IMPL_ALTERNATIVES.md.