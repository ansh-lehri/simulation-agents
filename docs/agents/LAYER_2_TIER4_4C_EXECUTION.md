Layer 2 — Tier 4 / 4c: Execution (Design Notes, LOCKED)

What this doc is. The design of 4c — the execute stage: it turns a committed intent into an actual order (the punch payload). 4c is the shared execution engine for both fresh entries (from 4b) and position changes (from 4d).

Cross-refs. Punch payload & 3a dispatch: decision log / LAYER_2_SCHEDULER. Axes & framing: LAYER_2_TIER4_FRAMING.md. Intent producers: LAYER_2_TIER4_4B_DECISION.md (open/add) and LAYER_2_TIER4_4D_POSITION_EXIT.md (add/close).

1. What 4c is (LOCKED)
The execute stage: committed intent → order. First stage that touches money & the book.
The punch payload is already locked (3a): {account_id, instrument, side, type (limit|market), qty, limit_price}. 4c's entire job is to fill type, qty, limit_price. account_id (=capital_handle), instrument, and side (= dir) are already known/ambient.
4c is the shared executor — both 4b (flat entry) and 4d (holding manage) feed it the same intent shape; 4c doesn't care who sent it.
Axes consumed: capital → qty ceiling; aggressiveness → type + price; skill → execution-noise on (qty, price). Disposition does NOT enter 4c (execution is disposition-agnostic — same conviction + capital execute the same way regardless of strategy).
2. Input contract (LOCKED)
ExecIntent { dir: long|short,  conviction: float[0,1],  action: open | add | close }
Senders: 4b → {open}; 4d → {add, close}.
Ambient (4c reads itself, not in the intent): account_id, instrument, current position across all instruments.
conviction is used for open/add; ignored for close.
3. Position cases (LOCKED)

4c reads current position in the instrument:

flat          + any dir → OPEN
holding long  + long     → ADD   (same math as open, nonzero start)
holding short + short    → ADD
holding long  + short  } → IGNORE (drop; reduce/flip is 4d's exit branch, 4c never trims/flips)
holding short + long   }
4. qty — capital + conviction (LOCKED, formulation Y)
A_available   = A − Σ( cost of ALL open positions, across ALL instruments )   # one shared capital pool
base_notional = A_available × max_frac × conviction
qty           = floor( base_notional / current_price )
Capital is one shared pool; instruments compete for it. An agent already heavily deployed sizes down / zeroes naturally.
No separate ceiling clamp needed: conviction, max_frac ≤ 1 ⇒ base_notional ≤ A_available by construction.
max_frac = calibration (max fraction of free capital one full-conviction entry deploys).
5. type — market vs limit, from aggressiveness (LOCKED)

Conviction warps aggressiveness (chosen over plain addition, which breaks [0,1] and lets conviction fully substitute aggressiveness):

effective_aggr = aggressiveness ^ (1 − γ · conviction)      # asymmetric / up-only
type = market  if effective_aggr ≥ θ_market  else  limit
Power form keeps [0,1] by construction; warp is proportional to headroom (bites hardest at the margin); a patient agent stays patient (conviction tilts, never overrides). Asymmetric = conviction only pushes toward market, never toward patient.
γ ∈ [0,1), θ_market ∈ [0,1] = calibration.
effective_aggr carries forward into limit_price (one consistent personality).
6. limit_price — spread-fraction placement (LOCKED; limit orders only, market → 0/omitted)
depth_into_spread = effective_aggr × k_place
BUY : limit_price = round_to_tick( best_bid + depth_into_spread × spread )   # round DOWN (less aggressive)
SELL: limit_price = round_to_tick( best_ask − depth_into_spread × spread )   # round UP
Aggressive (≈1) → sits at the far touch (crosses, fast fill); passive (≈0) → near touch (rests, better price, may not fill).
Spread-relative → naturally tighter in liquid books. best_bid/ask/spread from the 4a accessor. k_place ≈ [0,1] calibration (1 = aggressive reaches opposite touch, not beyond).
7. Skill — execution-noise (LOCKED; 2nd skill channel, perception-noise was 4a)

Perturbs magnitudes only (qty, limit_price), never type/dir/side (decisions/categoricals, same principle as 4a).

s = σ_exec × (1 − skill)                                           # noise width
qty_final   = clamp( round( qty × (1 + N(0,s)) ), 1, floor(A_available / price) )  # multiplicative; capital clamp returns as post-noise safety
limit_final = round_to_tick( limit_price + N(0,s) × spread )        # additive-in-spread; may cross ask = "low-skill pays up"
qty & price noise are independent fresh seeded draws.
σ_exec = GLOBAL config constant, NOT per-agent (per-agent variation already lives in skill; making σ per-agent double-encodes skill). Calibration placeholder ~0.1–0.2.
Seeded RNG (live = fresh, tuning = fixed seed).
8. close action — added for 4d exits (LOCKED)

Differs from open/add only in sizing; type/price/skill machinery reused verbatim (why 4c was extended, not duplicated):

qty   = closeable_qty = |position_qty| − reserved_qty     # binary full exit; reserved units can't be re-closed
side  = opposite of position                              # long → SELL, short → BUY-to-cover
capital math SKIPPED (unwinding, not spending);  conviction IGNORED
type / limit_price = from RAW aggressiveness (no conviction warp — exit urgency = base temperament)
skill-noise applies as normal
9. Output (LOCKED)

{ type, qty_final, limit_price_final } (+ ambient account_id, instrument, side = dir) → handed to the 3a dispatcher.

End of 4c (Execution) — LOCKED.