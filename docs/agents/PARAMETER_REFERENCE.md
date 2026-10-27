# Parameter Reference — Market Simulator + Simulation Agents

All calibration dials across both repos in one place: what they are, how they interact, and which ones would benefit from being dynamic.

---

## 1. Market-Simulator (Core) Parameters

Live in `market-simulator/configs/markets/IN.json` and the conductor. They define the physical rules of the exchange and the market schedule.

### Instrument

| Parameter | Current Value | What it is | Effect |
| --- | --- | --- | --- |
| `tick_size` | 5 | Minimum price increment (integer ticks). All prices snap to multiples. | Smaller → tighter spreads possible; finer limit-price placement. Agents' `KPlace` placement formula uses the bid–ask spread in ticks, so a large `tick_size` makes a wide minimum spread. |
| `band_bps` | 1000 | Circuit-breaker band in basis points (±10%). Orders outside this range around the reference price are rejected by the exchange. | Prevents runaway prices; can trap agents with stale price anchors if price drifts hard. |
| `seed_price` | 10000 | Initial reference price in ticks at market birth. Determines price scale all agents' capital sizing must work against. | Too high relative to agent capital → agents can't afford even 1 share. Root cause of the capital-sizing overhaul (`CapitalSizeLogNormal.Mu` raised from 10 → 13, `MaxFrac` from 0.2 → 0.35). |
| `ohlcv_interval` | `"1m"` | Candle bar granularity stored to Postgres and relayed on the WS tape. | History pull (`HistoryLookback`) fetches these 1m bars. Higher timeframe caches (5m, 30m, 60m) are built from them at pre-open. |

### Market Structure

| Parameter | Current Value | What it is | Effect |
| --- | --- | --- | --- |
| `num_shards` | 1 | Number of book shards per instrument. | More shards = more concurrency for high-volume markets. |
| `preopen_lead` | `"15m"` | How far before `session.at[continuous]` the conductor wakes the market into pre-open. | Agents can observe the order book but not trade. The agent seeder does run during pre-open for on-demand warmup. |
| Session schedule | 09:15 continuous, 15:30 closing_auction, 15:40 closed | Phase transitions the exchange enforces. | Controls which order types are accepted. Closing auction aggregates residual supply/demand at day end. |
| `preopen_lead` + session schedule | — | Together define the `SessionStart` wall-clock that `pullHistory` uses as the right edge of its lookback window. | If `HistoryLookback` is too narrow and the last real session was > N days ago, `pullHistory` returns empty → seeder has no price anchor → bootstrap deadlock. |

### On-Demand

| Parameter | Current Value | What it is | Effect |
| --- | --- | --- | --- |
| `ondemand.duration` | `"60m"` | How long an on-demand instance runs. | Sets the window agents have to trade. Also sets `ContinuousFraction × 60m` as the seeder's continuous phase. |
| `ondemand.warmup` | `"2m"` | Warm-up period at the start of an on-demand session during which the exchange is in pre-open phase. | Gives the seeder time to build a two-sided book before agents can place orders. |

### Conductor-Level Initial Liquidity (per instrument)

These configure the very first book the conductor seeds when an exchange instance comes live, before any agent seeder runs.

| Parameter | Current Value | What it is |
| --- | --- | --- |
| `liquidity.levels` | 5 | Number of price levels on each side of the initial book. |
| `liquidity.spacing_pct` | 0.1 | Percentage spacing between price levels (0.1 = 10 bps). |
| `liquidity.near_qty` | 100 | Quantity at the best bid/ask level. |
| `liquidity.taper` | 0.5 | Quantity multiplier per level away from best (0.5 = each outer level has half the qty). |

---

## 2. Agent Population Generation

Live in `simulation-agents/cmd/agentsd/calibration.go` (`defaultGenParams`, `defaultOrchestratorConfig`). Every agent is frozen at mint — traits never mutate mid-session.

### Population Size

| Parameter | Value | What it is |
| --- | --- | --- |
| `PopulationSize` | 300 | How many agents are spawned per bring-up. All trade the same instrument. |
| `HerdProbability` | 0 | Fraction of agents drawn as "herd" (tip-follower) kind. Currently 0 — herd decision path not yet built. |

### Universal Scalar Traits (Beta distributions, drawn for every agent)

A Beta(α, β) distribution sits on [0, 1]. Symmetric (α=β=2) = bell-shaped, center near 0.5. Skewed toward 0 when α<β.

| Trait | Distribution | Meaning | Downstream use |
| --- | --- | --- | --- |
| `Skill` | Beta(2, 2) | How "good" the agent is. 0 = noise-heavy, 1 = precise. | Scales execution noise (`SigmaExec × (1−skill)`); higher-skill agents perceive less noise. |
| `Aggressiveness` | Beta(2, 2) | How willing to take vs. provide liquidity. 0 = very patient, 1 = very aggressive. | Warped by conviction + `Gamma` → compared to `ThetaMarket`. Also drives method selection and `ThetaAct` placement. |
| `PnlReactivity` | Beta(2, 2) | How strongly P&L outcomes shift behavior. | Used in position-management stages (not yet live). |
| `PAct` | Beta(2, 5) | Per-tick probability the agent fires at all. Skewed low (more agents skip more ticks). | At each action tick, agent only enters DECIDE path if `u < PAct`. Keeps CPU load realistic. |
| `ActProb` | Beta(2, 2) | Herd-only tip credulity. | Unused (HerdProbability = 0). |

### Capital

| Parameter | Value | What it is | Effect |
| --- | --- | --- | --- |
| `CapitalSizeLogNormal` | Mu=13, Sigma=1 | Log-normal shape of the frozen `capital_size` scale trait. Median ≈ e^13 ≈ 442k. | The whale/minnow distribution. Must be workable against `seed_price`. At Mu=10 (median ~22k) agents couldn't afford 1 share at SYNTH_DEF's ~49k price scale. |
| `CapitalParams.Floor` | 1,000 | Minimum session trading capital after the draw. | Ensures even the most undercapitalized agent can participate. |
| `CapitalParams.JitterSpread` | 0.1 | ±10% multiplicative jitter on the raw capital draw. | Gives two agents with the same `capital_size` trait slightly different session capital. |
| `CapitalParams.DefaultFundedCash` | 500,000 | Fallback ceiling when `GetAccountCash` returns 0 or for on-demand existing agents. | Raised from 100k alongside the Mu=13 bump; stays in the same order of magnitude as Core's observed ~1M funded_cash. |

### Disposition & Diet

| Parameter | Value | What it is |
| --- | --- | --- |
| `DispositionDirichletAlphas` | Trend=1, MeanRev=1, Noise=1 | Symmetric Dirichlet over three trading styles. Equal alphas = uniform distribution over the simplex; each agent draws a random mix. |
| `DietOptionalFlagProbability` | 0.5 | Coin flip for each optional diet channel (channels not forced by the dominant disposition). |

### Perception Profile Generation

| Parameter | Value | What it is |
| --- | --- | --- |
| `ReactivityBeta` | Beta(2, 2) | Latent ρ ∈ [0, 1]: drives most perception window widths and cutoffs. ρ→0 = deliberate (wide windows, slow signals); ρ→1 = jumpy (narrow windows, fast signals). |
| `OptimismBeta` | Beta(2, 2) → stretched to [−1, +1] | Latent φ: tilts directional perception fields (chart direction/strength, range position/stretch) by `φ × BiasTiltStrength`. |
| `SigmaIdio` | 0.05 | Per-field Gaussian jitter (5% of range width) around the ρ-placed center. Prevents same-ρ agents collapsing to one point. |
| `MethodAnchorsAndDemands` | 6 methods | Each method has a `ReactivityAnchor` (which ρ it attracts) and `SkillDemand` (minimum skill to use it). chart.ma=0.2, chart.structure=0.85, etc. |
| `MethodProximityTau` | 0.25 | How sharply a method's reactivity anchor attracts matching agents. Small = anchor dominates. |
| `PerceptionRanges` | Various | Per-method parameter boxes, e.g. RSI period [7, 21], MA fast period [5, 20]. |

### Commit Gate Traits

| Parameter | Value | What it is |
| --- | --- | --- |
| `ThetaActRange` | [0.3, 0.8] | Range of the per-agent commit bar. Aggressive agents land near 0.3 (fire easily); cautious near 0.8 (high bar). |
| `ConfirmsNeededRange` | [1, 2] | How many consecutive same-direction ticks a "waiter" agent needs before committing. |

### Exit & Hold Traits

| Parameter | Value | What it is |
| --- | --- | --- |
| `ExitModeAnchors` | distance=0.2, hybrid=0.55, level=0.85 | Exit mode selection by skill. Low-skill agents use distance-based stops; high-skill use level-based. |
| `ExitStopPctRange` | [0.5%, 3%] | Stop-loss percentage range. |
| `ExitRewardRiskRange` | [1×, 3×] | Target as a multiple of stop distance. |
| `MaxHoldRange` | [10, 600] ticks | How long an agent will hold a position before forced exit. ρ-placed: jumpy agents hold short, deliberate agents hold long. |

---

## 3. Agent Decision Pipeline

Live in `simulation-agents/cmd/agentsd/calibration.go` (`defaultDecisionParams`) and `simulation-agents/decision/`.

Pipeline: **Perceive → Mix → Interpret → Gate (Commit) → Execute**.

### Perception

| Parameter | Value | What it is |
| --- | --- | --- |
| `SigmaPerc` | 0.05 | Noise width applied to each agent's perception signal, scaled by `(1 − skill)`. A perfectly-skilled agent sees the market cleanly; skill=0 agent sees ±5% noise on every reading. |
| `MASaturationMultiple` | 2.0 | A moving-average spread at 2× the flat-band threshold maps to maximum chart strength (1.0). Controls how quickly the trend signal saturates. |

### Mixer

| Parameter | Value | What it is |
| --- | --- | --- |
| `SecondarySensitivity` | 0.5 | How strongly a secondary trait's disagreement damps the dominant trait's conviction. 0 = ignore secondaries; large = strongly-disagreeing secondary pulls conviction toward zero (never flips sign). |
| `NoiseImpulseConviction` | 0.5 | Fixed conviction assigned to a noise-disposition impulse (noise reads no channel so has no magnitude of its own). |

### Interpretation Multipliers

| Parameter | Value | What it is |
| --- | --- | --- |
| `TrendCleanlinessBoost` | 0.5 | Multiplier bonus on trend conviction when the chart move is "clean" (low chop). `multiplier = 1 + 0.5 × cleanliness`. |
| `TrendVolumeConfirmBoost` | 0.2 | Bonus when volume confirms the move (at-or-above-baseline). |
| `TrendVolumeLowDamp` | 0.3 | Penalty when volume is thin — a price move on thin volume is weaker trend evidence. |
| `MeanRevVolumeLowExtremeBoost` | 0.2 | Bonus for mean-reversion when volume is thin AND the range is stretched to an extreme (classic exhaustion setup). |

### Commit Gate (Shaves)

Shaves are multipliers on conviction, each clamped to a floor so a single bad condition can't silence the agent entirely.

**TimeShave** — makes agents more cautious near open and close:

| Parameter | Value | What it is |
| --- | --- | --- |
| `OpenDamp` | 0.3 | Depth of the open-side caution dip (U-shape bottom at session open). |
| `CloseDamp` | 0.4 | Depth of the close-side caution dip (U-shape bottom at session close). |
| `OpenRampExponent` | 2 | How sharply the open-side dip fades as the session progresses (quadratic). |
| `CloseRampExponent` | 2 | How sharply the close-side dip grows as the session nears end. |
| `TimeShave.Floor` | 0.3 | Multiplier never shaves below 0.3, however early or late. |

**SpreadShave** — reduces conviction when the spread is wide (only for agents with `OrderBook` diet):

| Parameter | Value | What it is |
| --- | --- | --- |
| `SpreadSensitivity` | 1.0 | How strongly a wide spread shaves conviction. `mult = 1 − sensitivity × (spread / normalSpread)`. |
| `NormalSpread` | 0.001 | The "normal" spread baseline (as a fraction of price). Current spread is compared to this. |
| `SpreadShave.Floor` | 0.3 | Multiplier never shaves below 0.3. |

**CooldownShave** — reduces conviction right after a recent commit:

| Parameter | Value | What it is |
| --- | --- | --- |
| `CooldownShave.Damp` | 0.5 | Depth of the cooldown dip immediately after a commit. |
| `CooldownShave.Tau` | 10 | Tick half-life of the cooldown. After ~10 ticks the dip has mostly decayed. |
| `CooldownShave.Floor` | 0.4 | Multiplier never shaves below 0.4. |

**Gate logic:**

| Parameter | Value | What it is |
| --- | --- | --- |
| `WaitReassessThreshold` | 5 | Max ticks a "waiter" agent coasts without a perception change before being forced to re-decide. Prevents a quiet consolidation from silencing the agent forever. |
| `ThetaAddMargin` | 0.1 | Added to `ThetaAct` to derive `ThetaAdd` — the higher bar to add to an existing position. Ensures adding is never easier than opening. |

### Execution

The most market-impact-heavy parameters. They control order type, size, and price placement.

| Parameter | Value | What it is | Market effect |
| --- | --- | --- | --- |
| `MaxFrac` | 0.35 | Maximum fraction of free capital deployed per full-conviction entry. `baseNotional = freeCapital × MaxFrac × conviction`. | Higher → larger orders per agent → more price impact per tick. |
| `Gamma` | 0.5 | How strongly conviction warps aggressiveness upward toward market. `effectiveAggr = aggr^(1 − γ × conviction)`. | Higher → even mildly convinced agents send market orders. |
| `ThetaMarket` | 0.95 | Aggressiveness cutoff above which an agent uses a market IOC instead of a limit GTC. | 0.95 → top 5% of effective aggressiveness take the market. With Gamma=0.5, highly convicted limit-personality agents can still cross the threshold. |
| `KPlace` | 1.0 | How far into the spread a limit order is placed, as a fraction of spread width. `limitBuy = bestBid + KPlace × effectiveAggr × spread`. | **1.0 → an aggressive limit buy is placed exactly at bestAsk → immediately fills like a market order.** 0.5 → placed at mid-spread, rests. With ThetaMarket=0.95, only the top 5% become true market orders but KPlace=1.0 makes the next ~90% of limit buys also consume the ask immediately. Primary cause of the ask-side depletion issue. |
| `SigmaExec` | 0.15 | Execution noise width (std dev). Scaled by `(1 − skill)`. Applied to both quantity (multiplicative) and limit price (additive, in spread units). | Low-skill agents accidentally pay up or size strangely. |

---

## 4. Liquidity Seeder

Live in `simulation-agents/cmd/agentsd/session_wiring.go` (the `LiquiditySeeder` block inside `defaultSessionParams`). The seeder is a dumb operator that places resting GTC orders to bootstrap a two-sided book before real agents start deciding. Without it, `KindQuote` never fires, the WS subscription stays nacked, and no agent can trade.

### Operator Identities

| Parameter | Value | What it is | Effect |
| --- | --- | --- | --- |
| `SellerCount` | 10 | How many dumb seller identities are opened per bring-up. Each independently cancels its prior GTC sell and places a fresh one each round. | More sellers = more resting ask-side orders. The primary lever to counter ask-side depletion when KPlace is high. |
| `BuyerCount` | 5 | How many dumb buyer identities are opened. | Fewer buyers than sellers by design — agents are net buyers in the current calibration, so the seeder doesn't need to reinforce the bid side as aggressively. |

### Order Sizing

| Parameter | Value | What it is |
| --- | --- | --- |
| `OrderSizeMin` | 5 | Minimum per-order quantity (drawn uniformly from [Min, Max] each round). |
| `OrderSizeMax` | 500 | Maximum per-order quantity. Large ceiling allows meaningful liquidity provision at thin books. |

### Price Level Construction

| Parameter | Value | What it is | Effect |
| --- | --- | --- | --- |
| `PivotLevelCount` | 5 | How many R/S floor-trader pivot levels (R1…R5, S1…S5) are computed around the anchor. | Distributes orders at technically meaningful price points. |
| `RandomLevelCount` | 3 | How many additional orders are placed at random prices within `RandomLevelSpreadBps` of the anchor. | Adds randomness so the book doesn't look mechanical. |
| `RandomLevelSpreadBps` | 100 | Width of the random-level band, in basis points of the anchor price (100bps = ±1%). | Wider = orders spread further from center. |
| `GenesisOffsetBps` | 100 | Anchors levels off `seed_price` (±1%) on a true cold start when no prior OHLCV bar exists at all. | Only triggers on the very first canonical bring-up ever. |

### Seeding Schedule

| Parameter | Value | What it is | Effect |
| --- | --- | --- | --- |
| `ContinuousFraction` | 0.25 | Fraction of session duration during which seeders run every tick. After this, drops to sparse top-ups. | During the continuous phase the book is actively maintained. After it, agents must sustain the book themselves. |
| `ReentryCadenceMin` | 5s | Minimum wait between sparse top-ups once the continuous phase ends. | |
| `ReentryCadenceMax` | 15s | Maximum wait between sparse top-ups. A random interval in [5s, 15s] is drawn each time. | |

---

## 5. Session Runtime & Lifecycle

Live in `simulation-agents/cmd/agentsd/session_wiring.go` (`defaultSessionParams`) and `simulation-agents/session/params.go`.

### Tick Loop

| Parameter | Value | What it is | Effect |
| --- | --- | --- | --- |
| `TAction` | 1s | Floor duration between action ticks. If a tick takes longer, the next one chains immediately. | Lower → more orders per wall-clock second → heavier load on Core. With 300 agents and PAct≈0.3, ~90 agents may fire per tick → ~90 HTTP calls/s. |
| `TPresence` | 30s | Cadence of the presence-sweep phase (lifecycle rolls). Significantly coarser than TAction. | Controls how frequently agents can change presence state (active/idle/done). |
| `MaxLookbackK` | 120 | Maximum candles read per instrument per action tick. | Bounds how far back signal functions look. 120 × 1m bars = last 2 hours visible per tick. |

### History Pull & WS Subscription

| Parameter | Value | What it is | Critical dependency |
| --- | --- | --- | --- |
| `HistoryLookback` | 20 days | Width of the candle window fetched from Postgres at pre-open, measured backward from `SessionStart`. | **Must be wide enough to find candles from the last real session.** Reducing below the gap since the last session → empty Redis → seeder has no price anchor → no orders → KindQuote never fires → WS subscription stuck on "unknown instrument" forever. Root cause of the bootstrap deadlock fixed in commit a48ac64. |
| `SubscribeMaxRetries` | 36 | Times the ingest goroutine resends the subscribe frame for a nacked "unknown instrument" subscription. 36 × 5s = 180s window. | Must cover the time from bring-up start to the first seeder order landing. Raised from 12 (60s) to 36 (180s). |
| `HistoryTimeframes` | `["1m"]` | Which candle timeframes `pullHistory` populates in Redis. | Agents signal on these timeframes. Only 1m is live-extended by the WS tape. |

### Dispatcher & Concurrency

| Parameter | Value | What it is | Effect |
| --- | --- | --- | --- |
| `PunchBuffer` | 2000 | Buffer depth of the order dispatcher channel. | Sized to absorb one full action tick's punches (300 agents × max fire rate) without blocking the action loop. |
| `DispatcherWorkers` | 50 | Goroutines draining the punch channel and sending HTTP orders to Core. | One worker ≈ 20 orders/s. 50 workers ≈ 1000 orders/s — enough for 300 agents at TAction=1s. |
| `LoadSessionsConcurrency` | 50 | Max concurrent Core calls when installing agent sessions at pre-open. | Caps the burst of HTTP connections opened simultaneously. |

### Entry Curve

Controls how agent entry fractions are distributed across the session (Poisson base + Gaussian-process wiggle).

| Parameter | Value | What it is |
| --- | --- | --- |
| `HeightA` | 1.0 | Amplitude of the exponential base rate at session open (t=0). |
| `TauDecay` | 0.35 | How sharply the base rate decays through the session. 0.35 = moderate front-loading of arrivals. |
| `LengthScale` | 0.15 | RBF-kernel bandwidth of the GP wiggle. Controls width of session-specific local peaks. |
| `VarianceSigmaSquared` | 0.25 | GP amplitude. Controls how tall local peaks run relative to the base trend. |

### Lifecycle Curves (Presence Sweep)

Three rates are scenario-overridable; shape params are fixed per session.

| Parameter | Value | What it is |
| --- | --- | --- |
| `StepOutRate` | 0.05 | Plateau height of the step-out intensity curve. Low → most agents stay active per check. |
| `DoneRate` | 0.04 | Peak height of the done-early curve. Spikes near session close. |
| `ReentryRate` | 0.70 | Peak probability of re-entry at `μ_break`. An idle agent near 20 min has a 70% chance of returning. |
| `StepOutTauRamp` | 0.20 | How quickly step-out ramps up (through ~20% of session). |
| `DoneTauRamp` | 0.25 | How quickly done-early spikes (final ~25% of session). |
| `StepOutGPVariance` | 0.25 | GP amplitude riding the step-out curve (session-to-session variability). |
| `ReentryMeanBreak` | 1200s (20 min) | Idle duration at which re-entry probability peaks. |
| `ReentrySigmaBreak` | 480s (8 min) | Spread of the re-entry peak. |
| `GiveUpMax` | 0.80 | Ceiling probability of permanent give-up for a very long idle agent. |
| `GiveUpTau` | 1800s (30 min) | Idle time-scale of give-up rise past `μ_break`. |
| `CheckIntervalMin/Max` | 20–40 min | Random interval between consecutive lifecycle rolls. |
| `ExpectedQuitsPerSession` | 0.3 | Expected agents that permanently quit at canonical session end. Per-agent quit probability = 0.3 / PopulationSize. |

---

## 6. Parameter Relationships

Parameters rarely act alone. The interactions below are the ones that have caused real issues or require coordinated tuning.

### The Price-Scale Triplet: `seed_price` × `CapitalSizeLogNormal` × `MaxFrac`

These three must be calibrated together. An agent can place an order only if `floor(freeCapital × MaxFrac × conviction / currentPrice) ≥ 1`.

- SYNTH_DEF `seed_price = 10000` ticks × `tick_size = 5` = a price around ₹50,000 per share.
- With `Mu=10` (median capital ~22k) and `MaxFrac=0.2`, essentially no agent could afford even 1 share. Fix: raise Mu to 13 (median ~442k) and MaxFrac to 0.35. Both dials move together.
- If `seed_price` changes (e.g. new instrument), revisit both `CapitalSizeLogNormal.Mu` and `MaxFrac` before running.

### The Order Type Triplet: `ThetaMarket` × `KPlace` × `Aggressiveness`

These three determine whether a given agent sends a market IOC or a resting limit GTC, and exactly where the limit lands.

1. `effectiveAggr = aggressiveness^(1 − Gamma × conviction)` — conviction can only pull aggressiveness up.
2. If `effectiveAggr ≥ ThetaMarket` → **market IOC** (consumes liquidity immediately).
3. Else: `limitBuy = bestBid + effectiveAggr × KPlace × spread`.

**The KPlace=1.0 trap:** With `ThetaMarket=0.95`, only the top 5% of effective aggressiveness sends true market orders. But `KPlace=1.0` means an agent with `effectiveAggr=0.9` places its limit buy at `bestBid + 0.9 × spread = bestBid + 0.9 × (bestAsk − bestBid)`, which is just inside bestAsk. That order **fills immediately**, consuming ask-side liquidity exactly like a market order. Result: ~90% of buys drain the ask book continuously.

**The fix:** `KPlace=0.5` places limit buys at mid-spread. An agent with `effectiveAggr=0.9` lands at `bestBid + 0.45 × spread` — inside the book but not at the touch. Orders rest unless price moves.

### Bootstrap Chain: `HistoryLookback` → `pullHistory` → seeder → `KindQuote` → `SubscribeMaxRetries`

This is the startup dependency chain that broke when `HistoryLookback` was reduced from 20 days to 4 days:

1. `pullHistory` fetches candles from Postgres in the window `[SessionStart − HistoryLookback, SessionStart]`.
2. If no candles are found (gap > HistoryLookback since last session), Redis stays empty.
3. Seeder's `computeSeedLevelsForInstrument` returns nil (no price anchor) → no orders placed.
4. No orders → `KindQuote` never fires → market-data layer never registers the instrument.
5. WS subscription gets nacked with "unknown instrument" on every retry until `SubscribeMaxRetries` is exhausted.
6. Session fails with punch_count=0.

`HistoryLookback=20 days` and `SubscribeMaxRetries=36` (180s) are paired: the lookback ensures the seeder always has data; the retries give the seeder time to place its first orders across the ~58-page history pull.

### Seeder Size vs Agent Demand: `SellerCount` / `BuyerCount` vs `KPlace` / `ThetaMarket` / `PopulationSize`

The seeder must replenish ask-side liquidity at least as fast as agents consume it.

- With `KPlace=1.0` and `ThetaMarket=0.95`: effectively all agent buys consume the ask immediately.
- `SellerCount=10` cancels+replaces up to 10 ask-side orders per seeder round. If `TAction=1s` and 90+ buys/s arrive, the seeder can't keep up.
- Even raising `PopulationSize` from 300 to 1000 didn't fix the imbalance because **more agents = more buy pressure**, not more supply.
- Correct fix: lower `KPlace` (make buys rest instead of consuming) OR raise `SellerCount` significantly.

### Throughput: `TAction` × `DispatcherWorkers` × `PunchBuffer` × `PAct`

At each action tick, expected concurrent orders = `PopulationSize × PAct × (fraction that commit)`.

- `PAct` ~ Beta(2,5) median ≈0.28. With 300 agents: ~84 agents fire per tick at median PAct.
- `TAction=1s`, 84 HTTP punch calls, `DispatcherWorkers=50`: workers can handle ~1000 orders/s so 84/s is well within limit.
- `PunchBuffer=2000`: sized to absorb one heavy tick (300 agents all firing) without blocking the action loop.
- Raising `PopulationSize` to 13k would require proportionally higher `DispatcherWorkers`.

### Lifecycle Rates vs Session Duration

`StepOutRate`, `DoneRate`, `ReentryRate`, and `CheckIntervalMin/Max` interact with how long the session runs.

- `CheckIntervalMin=20min`, `CheckIntervalMax=40min`: in a 6.25h canonical session (09:15–15:30), each agent gets ~9–19 lifecycle checks.
- `ReentryMeanBreak=20min`: an agent that stepped out at the 1-hour mark will likely re-enter around the 1h20m mark.
- `GiveUpTau=30min` past `μ_break=20min`: an agent idle for 50+ minutes has `GiveUpMax=80%` give-up probability.
- In an on-demand session of 60 min, most agents never complete even one check cycle (CheckIntervalMin=20min = 1/3 of the session). Lifecycle curve parameters are less relevant for on-demand; the bootstrap and seeder params matter more.

---

## 7. Dynamic Candidates

Parameters currently static (set once at bring-up). Making them adaptive would improve market realism or robustness.

| Parameter | Current | What makes it a dynamic candidate | Suggested signal |
| --- | --- | --- | --- |
| `KPlace` | 1.0 (static) | **High priority.** With KPlace=1.0, limit-personality agents behave like market-order agents and continuously drain the ask. Decaying from 1.0 near open (thin book, eager agents) toward 0.5 mid-session (established book) would reduce early ask-side blowout. | Session progress (t/T) or bid–ask spread relative to `NormalSpread`. |
| `SellerCount` / `BuyerCount` | 10 / 5 (static) | The seeder should reinforce whichever side is thin. If the ask book repeatedly empties while the bid side is deep, the seeder should add more sellers. | Running ratio of bid depth / ask depth over the last N ticks. |
| `ThetaMarket` | 0.95 (static) | Controls the fraction of orders that are market vs. limit. Lower = more market orders, helpful in thin books. Higher = more limit orders, healthier in deep books. | Order-book depth or spread width. |
| `ContinuousFraction` | 0.25 (static) | If the book goes thin mid-session, the seeder should resume continuous operation temporarily rather than only top-up every 5–15s. | Order-book one-sidedness or spread spike. |
| `TAction` | 1s (static) | During low-volatility periods, a slower tick wastes nothing. During high-volatility periods, a faster tick keeps perception fresh. | Rolling price volatility (std of last K close prices). |
| `MaxFrac` | 0.35 (static) | An agent already holding a large position should size new entries smaller. Partially handled by `freeCapital` netting out open positions, but MaxFrac is still applied to the reduced pool. | Agent's current position concentration (cost basis / total capital). |
| `StepOutRate` / `DoneRate` / `ReentryRate` | Per-session baseline | Already scenario-overridable via `CurveEffective`. The scenario engine (L2 Phase 4) is designed to shift these during a session in response to events (e.g. a news shock raises `StepOutRate`). Infrastructure is built; the scenario-event triggers are the next step. | Scenario engine output (already wired). |
| `HistoryLookback` | 20 days (static) | Could be narrowed once the canonical market has been running continuously, auto-widening only when a large gap is detected. | Days since last recorded session in Postgres. |
| `PopulationSize` | 300 (static) | A larger session might warrant a bigger crowd; an on-demand session for a single user could run leaner. | Session type (canonical vs on-demand) or aggregate funded_cash. |
