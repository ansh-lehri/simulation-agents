# decision — 4a Perception (Tier 4, first block of `decide()`)
<!-- Per-package CLAUDE.md. READ ORDER:
       1) CLAUDE.md                            — root constitution
       2) session/CLAUDE.md                    — the goroutine that calls into this package (Tier 3)
       3) THIS file                             — specific to decision/4a
       4) the spec docs below                   — the authority
     Does not repeat the above. -->

## Spec (the authority)
- `docs/agents/LAYER_2_TIER4_4A_PERCEPTION_IMPL.md` (LOCKED) — the HOW-in-code, §1–14. This is
  the primary spec; implement exactly what it says, in its own order.
- `docs/agents/LAYER_2_TIER4_4A_PERCEPTION.md` — the WHAT/WHY design behind it.
- `docs/agents/LAYER_2_TIER4_PERCEPTION_PROFILE_GENERATION.md` — how `perception_profile` is
  drawn (LOCKED, already implemented in orchestrator/generator — read-only context, do not touch).
- `docs/agents/LAYER_2_TIER4_FRAMING.md` — cross-cutting Tier-4 principles (observations-not-
  opinions, reproducibility, scenario-neutrality) that bound every decision below.
- `docs/agents/LAYER_2_TIER4_IMPL_ALTERNATIVES.md` — alternatives weighed and rejected; read
  before proposing a different shape for anything this doc already settled.

## What 4a IS
The first block inside `session.decide()`: per awake agent, per instrument, turn the per-tick
market snapshot into disposition-agnostic **readings** (one fixed struct per channel) through
that agent's frozen `perception_profile`, gated by a cheap re-perceive-or-coast check. Pure
compute — no I/O, no Redis, no Core calls. 4a hands 4b a `PerceptionBundle`; it decides nothing
about direction-as-opinion (§3.2 of the framing doc: perception produces observations, not
opinions).

## Owns / public surface
New package `decision`, files per the impl doc §1: `accessor.go`, `bundle.go`, `gate.go`,
`perceive.go`, `signals_chart.go`, `signals_range.go`, `signals_liquidity.go`,
`signals_volume.go`, `signals_time.go`, `bias_noise.go`, `talib_adapt.go`.

- **`Accessor`** (`accessor.go`) — thin read interface over the shared `snapshotView` +
  `tickContext`. `NewAccessor`, `Closes/Highs/Lows/Volumes/BarCount(inst, tf, n)`,
  `CurrentPrice/Spread/DepthQty/Imbalance/Phase(inst)`, `SessionProgress()`, `Present(inst)`.
  Decides nothing; serves raw slices and agent-agnostic arithmetic only (§5 guardrail).
- **`PerceptionBundle`** + the five reading structs (`bundle.go`, §7, types reproduced verbatim
  from the impl doc — do not rename fields).
- **`ShouldReperceive(traits, acc, inst, tf, refs) bool`** (`gate.go`, §10) — Decision A only
  (whole-bundle recompute vs coast); Decision B (partial per-channel re-read) is OUT OF SCOPE.
- **`Perceive(traits, acc, inst, tf, agentRNG) (*PerceptionBundle, store.PerceptionRefs)`**
  (`perceive.go`) — the pure orchestrator: branches per on-diet channel to the matching
  `signals_*.go` function, applies bias+skill-noise (§9), returns the bundle + refreshed refs.
- **`signals_chart.go` / `signals_range.go` / `signals_liquidity.go` / `signals_volume.go` /
  `signals_time.go`** — one `perceiveX(block *agent.ChannelBlock, acc, inst, tf, ...) XReading`
  per channel; each branches on `block.Method` internally, always returns the same struct shape
  (§7 "method invisible to 4b").
- **`bias_noise.go`** — `applyBiasNoise` helper: raw → +bias → +skill-noise → clamp, on
  magnitude/score fields only (§9). Levels & facts (key_levels, range_bounds, phase,
  session_progress) are NEVER perturbed.
- **`talib_adapt.go`** — int64-tick ↔ []float64 boundary conversions; the warm-up guard
  (`BarCount(inst,tf) >= required_period`, never trust go-talib's 0.0-filled warm-up region).

## Where 4a sits in the seam (read-only context, not this package's code to write)
- `session.decide(decideInput)` is the I/O orchestrator (already seamed, per the impl doc):
  builds the `Accessor`, reads `workingAgent.perception[instrument]` carried memory, calls
  `decision.ShouldReperceive` / `decision.Perceive`, writes carried state back, mirrors to Redis
  via `memoryHandle.SetPerceptionRefs`. **Do not implement or modify `session.decide` itself in
  this stage** — only call into it / consume its seam exactly as the impl doc §1–2 describes. If
  a signature here doesn't carry what 4a needs, that is a note back to `session`, not a silent
  workaround (root CLAUDE.md "signatures bend to the tier's needs, not the reverse" — but that
  bend happens in `session`, not by reaching into it from `decision`).
- Seam pieces 4a's logic depends on (own by reference, do not redefine): `agentRNG *rand.Rand`
  on `workingAgent` (§2a); `tickContext{now, sessionStart, sessionEnd, tickIndex}` on
  `decideInput` (§2b); `perception map[string]*instrumentMemory` on `workingAgent` (§2c);
  `snapshotView.candles map[string]map[string][]store.Candle` (§3); `store.PerceptionRefs`
  reshaped per-instrument + `SetPerceptionRefs(instrument, refs)` gaining an instrument arg (§4).

## Invariants (LOCKED)
- **Pure package.** No I/O, no Redis, no Core calls, no `context.Context` parameter anywhere in
  `decision`. Everything here is a deterministic function of its arguments.
- **Disposition-agnostic.** A channel's reading never depends on `traits.Disposition` —
  perception is the same whether the agent is trend or mean-reversion; only 4b's mixing differs
  (§3.2 framing doc). Do not let disposition leak into any `signals_*.go` function.
- **Absent ≠ unreadable.** Diet-off channel → `nil` pointer in the bundle. Present-but-unreadable
  (short window / `present=false` / no quote) → non-nil struct with `ReadOK=false`. Never
  collapse these two states.
- **Strategy-free accessor.** All indicator math (`talib.Sma/Ema/Rsi/BBands/Min/Max/StdDev`,
  structure, cleanliness, maturity, confirming) lives in the `signals_*.go` files, one layer
  above the accessor — never inside `accessor.go`.
- **go-talib warm-up guard.** go-talib fills warm-up with `0.0`, not `NaN`. Every signal function
  gates on `BarCount(inst,tf) >= required_period` before trusting the last element; never consume
  warm-up output.
- **int64 ticks at the boundary only.** Candle series convert to `[]float64` at the accessor edge
  (`talib_adapt.go`); any derived level (`key_levels`, `range_bounds`) converts back to `int64`
  via `math.Round` before leaving `decision`. No floats cross back into money/tick fields.
- **Bias + skill-noise order is fixed:** raw → `+bias` → `+skill-noise` → clamp (§9). Skill-noise
  is drawn fresh per field, per call, from `agentRNG` passed in — never a package-level or
  locally-seeded RNG (root CLAUDE.md reproducibility rule: no seed from an identifier, live =
  fresh, tuning = fixed seed — the seed itself is `agentRNG`'s business, not this package's).
- **Categoricals perturb pre-cutoff.** Direction fields: bias+noise perturb the continuous
  pre-cutoff value (`rawSpread` / `rsi−50`), THEN the cutoff runs. Never perturb the already-
  categorized direction.
- **Levels & facts never perturbed:** `key_levels`, `range_bounds`, `phase`, `session_progress`
  are raw, no bias, no noise, ever.
- **Gate is whole-bundle (Decision A).** Any on-channel exceeding its `RereadSensitivity` →
  recompute the ENTIRE bundle, not just that channel. Empty refs (first act for this instrument)
  → always recompute. Partial per-channel re-read (Decision B) is explicitly deferred — do not
  build it "while you're in there."
- **Method invisible to 4b.** Every `signals_*.go` function returns the same fixed struct
  regardless of `block.Method`; 4b must never see which method/library produced a reading.
- **The only non-profile constants** are `σ_perc` (skill-noise width) and the one MA strength-
  saturation multiple `m` — both live in a new `decision.PerceptionParams` calibration struct at
  the composition root, never hardcoded inline, never per-agent (per-agent variation already
  lives in `traits.Skill` / the profile). `band_width` is already a per-agent generated param —
  do not re-derive it here.
- **go-talib only**, pure Go (`github.com/markcheno/go-talib`) — cgo TA-Lib is explicitly
  rejected (keeps `go build ./...` clean). Everything go-talib doesn't cover (structure swings,
  cleanliness, maturity, confirming, all of liquidity) is custom, per impl doc §8.

## Do NOT
- Do NOT implement `session.decide`, the coast-branch wiring, or the Redis write-through mirror
  — those are `session`'s job; `decision` is called BY them, never calls them.
- Do NOT touch the `orchestrator/generator` perception-profile draw — it is LOCKED and already
  implemented; 4a only reads `agent.PerceptionProfile` / `agent.ChannelBlock`.
- Do NOT build Gate Decision B (partial per-channel re-read) — deferred, Decision A only.
- Do NOT build timeframe-selection mechanics, instrument-attention, or depth normalization —
  all explicitly deferred/dropped (§14). Timeframe is a parameter passed in, degenerate to
  `configured[0]` upstream; 4a just takes `tf` as given.
- Do NOT add disposition-aware branching anywhere in `decision` — that is 4b's exclusive
  concern (observations vs opinions, §3.2).
- Do NOT invent new per-agent calibration knobs beyond what the impl doc names; if a shaping
  constant looks missing, it is either already a profile field (check `agent.ChannelBlock`'s
  `ReadingParams`/`ReadingCutoffs` first) or belongs in `PerceptionParams` — stop and ask rather
  than hardcoding a magic number.
- Do NOT use `float64` for anything that leaves this package as a tick or money value —
  convert back to `int64` at the boundary (`talib_adapt.go`).
- Do NOT reach outside `decision` except to read `agent.TraitVector` / `agent.ChannelBlock` /
  `store.Candle` / `store.PerceptionRefs` types and the seam structs named above.

## Tests (ship with the code)
- **Accessor:** each read method against a fixed `snapshotView` fixture — correct window
  truncation, `int64→float64` conversion, `CurrentPrice` fallback (quote `HasLast` → else latest
  candle close), `Spread`/`DepthQty`/`Imbalance` gated on `HasBid`/`HasAsk`, `Present` passthrough.
- **Warm-up guard:** `BarCount < required_period` → `ReadOK=false`, never consumes go-talib's
  `0.0` warm-up fill (table test with a deliberately short series per indicator).
- **Per-channel signal functions:** one table test per method family (sma/ema, rsi, structure
  for chart; bollinger/channel/rsi for range) — direction + strength/stretch/position against
  hand-computed fixtures; cleanliness/maturity/confirming as their own table tests (§8.1–8.4).
- **Bundle shape:** diet-off → `nil` pointer; diet-on + unreadable → non-nil + `ReadOK=false`;
  diet-on + readable → non-nil + `ReadOK=true`, asserted per channel.
- **Bias + skill-noise:** order is raw→bias→noise→clamp (golden fixture with a stubbed/fixed
  `agentRNG`); categoricals flip category near the boundary at low skill, high skill stays
  stable; `key_levels`/`range_bounds`/`phase`/`session_progress` byte-identical with and without
  bias/noise applied (never perturbed).
- **Gate:** each channel's change metric against `RereadSensitivity`; empty refs → always
  recompute; any one on-channel over threshold → whole-bundle recompute signal; all under →
  coast signal. `time` has no sensitivity entry — assert it never gates via the generic path.
- **Determinism:** fixed-seed `agentRNG` → `Perceive` run twice → byte-identical bundle (root
  CLAUDE.md standing rule, applies here as everywhere seeded).
- **Edge cases (§11):** `present=false` / no `CurrentPrice` → every channel `ReadOK=false`;
  thin-diet agent (few/no on-channels) → bundle is mostly `nil`, no panic.
- `go test ./decision/... -race` green; `go vet` + `golangci-lint` clean.