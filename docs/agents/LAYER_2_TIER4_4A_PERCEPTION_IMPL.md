Layer 2 — Tier 4 / 4a Perception — Implementation Design (LOCKED)

What this doc is. The HOW-in-code for 4a, written against the real simulation-agents codebase (actual package / type / func names). It complements — does not replace — LAYER_2_TIER4_4A_PERCEPTION.md (the WHAT/WHY design) and LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md (how the per-agent profile is drawn; LOCKED and already implemented in orchestrator/generator). Alternatives weighed and not chosen are logged in LAYER_2_TIER4_IMPL_ALTERNATIVES.md.

One-line scope. 4a turns the per-tick market snapshot into an agent's disposition-agnostic readings (one fixed struct per channel), through that agent's frozen perception_profile, plus the cheap gate that decides re-read vs coast. It is the first block inside the real Tier-4 decide().

1. Where 4a runs — the decision package + the session.decide seam
A new decision package holds all the pure Tier-4 logic (snapshot + traits + carried state → readings), no I/O, fully unit-testable — matching the project's golden-test discipline. 4a files: accessor.go, bundle.go, gate.go, perceive.go, signals_chart.go, signals_range.go, signals_liquidity.go, signals_volume.go, signals_time.go, bias_noise.go, talib_adapt.go.
The existing session.decide(decideInput) (core.OrderPunchRequest, bool) stub becomes the I/O orchestrator: it builds the accessor over the shared snapshotView, reads the agent's carried state off workingAgent, calls decision.*, writes carried state back (and mirrors to Redis via memoryHandle), and (in 4b–4d) calls observe() and returns the punch. All I/O stays in session; all computation is pure in decision.

Flow inside decide, per awake agent (decideFunc is already injected as LiveScheduler.decideFunc):

tf          := agent's selected timeframe (from the configured list; degenerate: configured[0])
for each instrument in input.market.candles:          // DS loops all; the demo acts on index 0
    acc  := decision.NewAccessor(input.market, input.tick)
    mem  := input.agentState.perception[instrument]    // this agent's carried memory for THIS instrument
    if decision.ShouldReperceive(traits, acc, instrument, tf, mem.refs):
        bundle, refs := decision.Perceive(traits, acc, instrument, tf, input.agentRNG)
        mem.lastBundle, mem.refs = bundle, refs
        _ = input.agentState.memoryHandle.SetPerceptionRefs(instrument, refs)   // write-through mirror
    // else coast: reuse mem.lastBundle
→ bundles handed to 4b (next stage)

decision.Perceive is pure; the coast branch and the Redis mirror live in session.decide.

2. Seam additions (things 4a needs that are not on the seam — Tier 4 introduces them)

(a) Per-agent seeded RNG. Skill-noise is a fresh draw per read; decideInput carries none. Add agentRNG *rand.Rand to workingAgent, seeded once in buildWorkingSet from ws.sessionRNG while iterating population.All() (stable order → deterministic; not seeded from any identifier, per the CLAUDE reproducibility rule). Each agent then has an independent stream, order-independent of the ws.active map iteration. Reused by 4b's noise-coin and 4c's execution-noise.

(b) Tick/time context. Add tick tickContext{now, sessionStart, sessionEnd time.Time, tickIndex int64} to decideInput, built once per runActionPhase. Feeds session_progress (= clamp((now−start)/(end−start),0,1)) now and entry_tick in 4d.

(c) Per-instrument carried perception memory on workingAgent:

perception map[string]*instrumentMemory       // keyed by instrument
type instrumentMemory struct {
    refs       store.PerceptionRefs
    lastBundle *decision.PerceptionBundle
}

In-memory authoritative; the live loop never re-reads Redis for it. The memoryHandle setter is a crash-recovery mirror only. 4b/4d will add standingOpinion/stance, waitStreak, positionContext to workingAgent the same way.

3. Instrument & timeframe handling (count-agnostic DS, degenerate action)
Data structures assume many; actions assume one. Perception runs per instrument over whatever snapshotView carries; the single-instrument demo shows up only when 4b/4c pick which candidate to punch (index [0]). Nothing hardcodes a count.
Timeframe is configured, dynamic, agent-selected. A startup configured-timeframes list (subset of {1m,5m,30m,60m}) drives history pull, live ingest, snapshot, and the list handed to agents. The agent selects its TF from that list (degenerate now: one entry → configured[0]), and passes it to every candle read. No perception_profile field — TF selection is a runtime concern, deferred like instrument attention.
Snapshot change (required): snapshotView.candles becomes map[string]map[string][]store.Candle (instrument → timeframe → bars); scheduler.snapshot loops the configured timeframes instead of the hardcoded core.TimeframeOneMinute:
  for _, tf := range sched.params.ConfiguredTimeframes {
      view.candles[instrument][tf], _ = sched.marketData.ReadCandles(ctx, ws.exchangeID, instrument, tf, sched.params.MaxLookbackK)
  }
MaxLookbackK sizing: must be ≥ the largest lookback any agent can draw — today chart.ma.slow_period high = 100, plus margin for RSI (period+1) and structure's window. Set MaxLookbackK ≥ ~120. (Candles themselves come from the 2c history pull + live ingest — nothing new to generate; only the dial sized to cover the max param.)
Upstream (session-layer, flagged, inert while one TF): the live WS ingest today appends 1m only; when >1 TF is configured it must ingest all configured timeframes. This leans on Core's WS serving those TF subscriptions (cross-layer — L2 subscribes, Core's business to serve).
4. Per-instrument perception memory — the store reshape

store.PerceptionRefs{LastPrice, LastSpread, LastVolume int64, LastRangePos float64, LastPhase string} keeps its fields but is stored per instrument. SetPerceptionRefs gains an instrument arg and writes one JSON blob per instrument under the agent's runtime hash (RuntimeKey(exchangeID, agentID), field gate_refs:{instrument}), replacing the five flat FieldGateRef* fields. 4a is the first real writer of this memory — it was declared in Tier 1 and has had no caller until now, so reshaping it is free.

5. The accessor (decision/accessor.go) — thin read interface over goroutine memory

Decides nothing; the agent passes what it wants to read, including the timeframe.

NewAccessor(view *snapshotView, tick tickContext) *Accessor      // wraps the whole in-memory snapshot

// candle-based reads — take (instrument, timeframe) because candles exist per timeframe:
Closes(inst, tf string, n int) []float64         // int64 ticks → float64 at this boundary (go-talib wants []float64)
Highs(inst, tf string, n int) []float64
Lows(inst, tf string, n int) []float64
Volumes(inst, tf string, n int) []float64
BarCount(inst, tf string) int

// quote-based reads — take (instrument) only; the live book is one latest state, not per timeframe:
CurrentPrice(inst string) (int64, bool)          // quote.LastPrice if HasLast, else latest candle close of the agent's tf
Spread(inst string) (int64, bool)                // AskPrice−BidPrice if HasBid&&HasAsk
DepthQty(inst string, levels int, side side) int64   // sum top-N Depth.Bids/Asks qty
Imbalance(inst string, levels int) (float64, bool)
Phase(inst string) string                        // quote.Phase

// session context:
SessionProgress() float64
Present(inst string) bool                        // snapshotView.present

Strategy-free primitive guardrail: the accessor serves raw slices and agent-agnostic arithmetic only. All indicator math lives one layer up in the signal functions via go-talib, not in the accessor.

6. Indicator library — pure-Go go-talib

Use github.com/markcheno/go-talib (pure Go, no cgo — keeps go build ./... clean; the C TA-Lib via cgo is explicitly rejected). It provides the standard indicators we consume: talib.Sma, talib.Ema, talib.Rsi, talib.BBands, talib.Min, talib.Max, talib.StdDev. Boundary rules (talib_adapt.go):

Convert int64 tick series → []float64 at the accessor edge; convert any level back to int64 ticks (math.Round) when storing in key_levels / range_bounds.
go-talib fills the warm-up region with 0.0, not NaN — never trust warm-up output; gate on BarCount(inst,tf) ≥ required_period and take the last element only.
Determinism: pure math, fine under the seeded-RNG discipline (talib consumes no RNG).

Everything go-talib does not cover we build (§8): structure swings, cleanliness, maturity, median, confirming, and all of liquidity.

7. The output bundle (decision/bundle.go) — one fixed struct per channel, method invisible to 4b
type Direction int8    // DirFlat 0, DirUp +1, DirDown -1
type VolumeLevel int8  // VolLow, VolNormal, VolHigh

type ChartReading     struct { Direction Direction; Strength, Cleanliness, Maturity float64; KeyLevels []int64; ReadOK bool }
type RangeReading     struct { PositionInRange, Stretch float64; RangeBounds [2]int64; ReadOK bool }
type LiquidityReading struct { Spread, Depth, Imbalance float64; ReadOK bool }
type VolumeReading    struct { Level VolumeLevel; Spike float64; Confirming bool; ReadOK bool }
type TimeReading      struct { Phase string; SessionProgress float64; ReadOK bool }

type PerceptionBundle struct { Chart *ChartReading; Range *RangeReading; Liquidity *LiquidityReading; Volume *VolumeReading; Time *TimeReading }
Diet-off channel → nil pointer (absent ≠ neutral).
Present-but-unreadable (short window / present=false / no quote) → non-nil, ReadOK=false.
Each signals_*.go exposes one perceiveX(block *agent.ChannelBlock, acc, inst, tf, …) XReading that branches on block.Method internally and always returns the same struct — 4b never sees the method or the library.
8. Per-channel signal computation — real keys, field-drawing per method

Every numeric shaping comes from the agent's perception_profile cutoffs/params; the only non-profile constants are σ_perc (skill-noise width) and one MA strength-saturation multiple (§9). All strengths/positions use natural-bounded forms where the indicator provides one.

Chart → ChartReading (signals_chart.go), branch on block.Method
method	direction + strength	library
sma/ema	rawSpread=(fast−slow)/slow over fast_period,slow_period; direction via flat_band; `strength=clamp((	rawSpread
rsi	rsi over period; direction via mid_band on (rsi−50); `strength=clamp((	rsi−50
structure	HH/HL over last confirm_swings pivots (swing_window,min_swing_pct); strength=fraction of swing comparisons agreeing	custom (§8.1)

Shared, method-independent: cleanliness = path-efficiency (§8.2); maturity = run-length (§8.3); key_levels = structure's last swing hi/lo, else talib.Max/talib.Min over the window.

Range → RangeReading (signals_range.go), branch on block.Method
method	position_in_range + stretch + range_bounds	library
bollinger	pos=(price−mid)/(sigma_mult·sd) over period; `stretch=clamp(	pos
channel	hi,lo=Max/Min(window); mid=(hi+lo)/2; pos=(price−mid)/((hi−lo)/2); cutoff extreme_band; range_bounds=[lo,hi]	talib.Max/talib.Min
rsi	pos=(rsi−50)/50 over period; stretch past ob_os_levels; range_bounds=Max/Min fallback (RSI has no price band)	talib.Rsi
Liquidity → LiquidityReading (signals_liquidity.go) — all custom (book math, not price-series)

spread=(AskPrice−BidPrice)/price; depth=Σ top-depth_levels bid+ask qty (absolute — rolling normalization was dropped); imbalance=(bidSum−askSum)/(bidSum+askSum). ReadOK=false if !HasBid||!HasAsk.

Volume → VolumeReading (signals_volume.go)

avgVol = talib.Sma(volumes, avg_window) when block.Method=="mean", custom median when =="median". ratio=currentVol/avgVol. Level via band_width bands around 1.0; Spike= clamp((ratio−1)/(spike_multiple−1),0,1); Confirming = §8.4.

Time → TimeReading (signals_time.go)

Phase=acc.Phase(inst), SessionProgress=acc.SessionProgress(). No params. ReadOK tracks present.

8.1 Structure — fractal pivots (custom)

Bar i is a swing-high if high[i] is the max over [i−swing_window, i+swing_window] (swing-low symmetric on lows) — pivots confirm swing_window bars late (realistic lag). Keep a new pivot only if it differs from the last kept opposite pivot by ≥ min_swing_pct (clean alternation). From the last confirm_swings highs and lows: higher-highs and higher-lows → up; lower-highs and lower-lows → down; else flat. strength = fraction of consecutive swing comparisons agreeing with the direction. Fewer than confirm_swings pivots → ReadOK=false (not a forced flat).

8.2 Cleanliness (custom) — path efficiency

|close_last − close_first| / Σ|close_i − close_{i−1}| over the method's window, ∈[0,1]. 1 = a clean straight move; ~0 = chop. Method-independent, no new constant.

8.3 Maturity (custom) — run length

Consecutive bars the direction relation has held, clamp(runLength / window, 0, 1).

8.4 Confirming (custom) — standalone volume condition

priceΔ = close−open of the just-closed bar; confirming = (|priceΔ|>0) && (ratio≥1). A real move on at-or-above-normal volume is confirmed; a move on thin volume is not (the fake-breakout dodge). Direction-agnostic and independent of the chart channel (channels never read each other); one-bar window, no new per-agent param.

9. Bias + skill-noise (bias_noise.go)

Applied to magnitude/score fields only, in order raw → +bias → +skill-noise → clamp:

bias = traits.Perception.Bias[fieldKey] — the generated φ·tilt map, keys chart.direction, chart.strength, range.position_in_range, range.stretch. Fixed per agent.
skill-noise = N(0, σ_perc·(1−traits.Skill)) drawn from input.agentRNG, fresh per field.
categoricals (direction): bias+noise perturb the continuous pre-cutoff value (rawSpread / rsi−50), then the cutoff runs — so a low-skill agent flips category near the boundary.
levels & facts (key_levels, range_bounds, phase, session_progress): never perturbed.
10. The gate (decision/gate.go)

ShouldReperceive(traits, acc, inst, tf, refs) bool — the cheap glance. For each ON diet channel, a change metric vs the generated traits.Perception.RereadSensitivity[channel] (keys chart/range/volume/liquidity; time has none):

chart: |price−refs.LastPrice|/refs.LastPrice
liquidity: |spread−refs.LastSpread|/refs.LastSpread
volume: |vol−refs.LastVolume|/refs.LastVolume
range: price move vs Max/Min window span (cheap, not a full re-read)
time: acc.Phase(inst) != refs.LastPhase (discrete)

Decision A: ANY on-channel exceeds → recompute the whole bundle, refresh refs+lastBundle, write-through. Else coast on lastBundle. Empty refs (first act for this instrument) → always recompute. (Per-channel partial re-read = Decision B, deferred.)

11. Edge cases
present=false or CurrentPrice not ok → every channel ReadOK=false.
BarCount(inst,tf) < required period (early session / thin history) → that channel ReadOK=false.
Diet-off channel → nil pointer (absent ≠ neutral).
Noise-heavy agent with thin diet → few/no channels computed → mostly nil/unread; its impulse lives in 4b.
12. Codebase changes 4a requires (summary)
New decision package (pure 4a logic + files in §1).
session.decide becomes the orchestrator (coast branch + Redis mirror); seam gains agentRNG on workingAgent, tickContext on decideInput, perception map[string]*instrumentMemory on workingAgent (§2).
snapshotView.candles → map[string]map[string][]store.Candle; scheduler.snapshot loops a ConfiguredTimeframes dial instead of hardcoded core.TimeframeOneMinute (§3); add ConfiguredTimeframes + size MaxLookbackK ≥ ~120 in session.Params.
store.PerceptionRefs stored per instrument; SetPerceptionRefs gains an instrument arg; replace the flat FieldGateRef* fields with a per-instrument blob (§4).
go.mod: add github.com/markcheno/go-talib.
Upstream (flagged, inert at one TF): WS ingest reads all ConfiguredTimeframes, not just 1m.
New global calibration (composition root, decision.PerceptionParams): σ_perc (skill-noise width at skill 0; global, mirrors 4c's σ_exec — per-agent variation already lives in skill) and the single MA strength-saturation multiple m (§8 chart). band_width is already a per-agent generated param, not global.
13. What 4a hands to 4b

A per-instrument PerceptionBundle (some channels nil = absent, some ReadOK=false = unread). 4b mixes the directional channels (chart/range) via disposition and gates on the condition channels (liquidity/volume/time), and must treat absent ≠ neutral. key_levels/range_bounds (int64 ticks) ride through for 4c/4d.

14. Deferred / open (carried, not blocking 4a)
Timeframe-selection mechanic (and whether it ever becomes a trait) — degenerate now (single configured TF); seam only.
Instrument attention — degenerate now (instruments[0]); DS already multi.
Depth normalization — dropped; depth is absolute (no rolling baseline). See design doc §12.
Gate Decision B (partial per-channel re-read) — deferred; Decision A now.
Multi-TF live ingest — needed only when >1 TF configured.

End of 4a (Perception) implementation design — LOCKED. Alternatives in LAYER_2_TIER4_IMPL_ALTERNATIVES.md.