Layer 2 — Tier 1: Identity & Persistence (Implementation Design)

What this doc is. The build specification for Tier 1 — the concrete data structures, store interfaces, key schemes, and pure helpers that hold L2's identity and per-session state. It is the building blocks; the bring-up flow that calls them is L2_TIER2_IMPLEMENTATION_DESIGN. Each point follows: what it is / expected & why / cross-layer / ingress-egress / the implementation / decisions. Language is Go. Read L2_TIER1_IDENTITY_AND_PERSISTENCE_DESIGN first — this builds exactly that design.

Grounding. Every structure traces to the Tier-1 design doc and the locked decisions (project decision log). Code is illustrative of shape and contract, not final; signatures are fixed where the design locks them. New impl-level choices are tagged [decided here]. Values (distribution params, timeouts, pool sizes) are calibration/ops, never fixed here.

Scope. In: the Agent/TraitVector records, the Postgres agents table + AgentStore interface, the Redis runtime key scheme + RuntimeSnapshot + runtime ops, the market-data cache keys, the LeaseAllocator interface, the pending_mint WAL struct+ops, the drawTradingCapital helper. Out: the orchestration that sequences these (Tier 2 impl), the trait draw internals (Tier 2 impl + POPULATOR/SCHEMA math), release/reconcile execution (Tier 6).

Point 1 — The Agent record (the in-memory carrier)

What it is. The struct that flows through bring-up and is handed to the goroutine; it accretes fields across 2a stages and is the source for both the durable row and the runtime snapshot.

Expected & why. Three strata (Tier 1 §1): identity + frozen traits + durable-mutable live together in the record; per-session runtime is separate (Point 4). Traits are a nested value object serialized to the JSONB column.

Ingress/egress. Built in 2a (mint / load); written to Postgres (Point 2) and Redis (Point 4) in 2b; handed to the goroutine in the Population.

The implementation.

go
type AgentID string

type Agent struct {
    // identity
    ID         AgentID
    Population string      // "canonical" | "ondemand"
    Country    string
    Origin     string      // "boot" | "mint"

    // durable-mutable
    CapitalHandle   string      // economy account_id (opaque)
    TradingCapital  int64       // A; canonical persisted+evolving, on-demand Redis-only (0/NULL in PG)
    TraitsVersion   int
    TraitsUpdatedAt time.Time
    RetiredAt       *time.Time  // nil = live; set = RETIRED

    // frozen trait vector (→ JSONB)
    Traits TraitVector

    // transient during 2a only (not persisted): funded_cash observed at account-open, feeds capital draw
    FundedCash int64
}

Decisions. FundedCash is a transient 2a field (from the account-open response), consumed by drawTradingCapital (Point 7), never stored. [decided here]

Point 2 — Durable store: agents table + AgentStore

What it is. The Postgres table (Tier 1 §2) and the narrow interface L2 uses over it.

Expected & why. One table, both populations, population-discriminated; the only query shapes L2 needs are insert one, reuse-query live members by country+population, load many by id (on-demand lease), and retire (for_good). The reuse index serves the hot path.

Cross-layer. None — L2's own Postgres.

The implementation.

sql
CREATE TABLE agents (
    agent_id          TEXT        PRIMARY KEY,
    population        TEXT        NOT NULL CHECK (population IN ('canonical','ondemand')),
    country           TEXT        NOT NULL,
    capital_handle    TEXT        NOT NULL,
    trading_capital   BIGINT,                           -- canonical: set+evolving; ondemand: NULL
    traits            JSONB       NOT NULL,
    traits_version    INT         NOT NULL DEFAULT 1,
    traits_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    origin            TEXT        NOT NULL CHECK (origin IN ('boot','mint')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at        TIMESTAMPTZ
);
CREATE INDEX idx_agents_reuse ON agents (country, population) WHERE retired_at IS NULL;
go
type AgentStore interface {
    InsertAgent(ctx, Agent) error                 // idempotent by PK agent_id (so recovery re-runs are no-ops)
    LiveMembers(ctx, country string, pop string) ([]Agent, error)  // the reuse-index query, retired_at IS NULL
    LoadMany(ctx, ids []AgentID) ([]Agent, error) // on-demand lease: load leased rows
    Retire(ctx, id AgentID, at time.Time) error   // set retired_at (for_good; Tier-3 3b routes this via orchestrator)
    // canonical P&L→A fold update = Tier 6; not here
}

Decisions. InsertAgent must be idempotent by PK — the WAL recovery path (Point 6) and any 2b retry rely on it. [decided here] trading_capital is a nullable BIGINT (NULL ≡ on-demand). No trade-log table exists (ownership spine).

Point 3 — The frozen TraitVector (JSONB value object)

What it is. The Go shape of the traits JSONB (Tier 1 §3), kind-dependent.

Expected & why. Universal fields always present; endogenous-only and herd-only fields gated by AgentKind. Serialized whole to JSONB; read whole on load. omitempty so absent blocks don't bloat the row.

The implementation.

go
type TraitVector struct {
    AgentKind      string   `json:"agent_kind"` // "endogenous" | "herd"
    // universal
    Skill          float64  `json:"skill"`
    Aggressiveness float64  `json:"aggressiveness"`
    CapitalSize    float64  `json:"capital_size"` // frozen SCALE (session money A is the column, not here)
    PnlReactivity  float64  `json:"pnl_reactivity"`
    PAct           float64  `json:"p_act"`
    // endogenous-only (omitempty)
    Disposition    *Disposition       `json:"disposition,omitempty"`     // {trend,mean_rev,noise}
    Diet           *Diet              `json:"diet,omitempty"`            // 5 bool flags
    Rho            *float64           `json:"rho,omitempty"`             // reactivity latent [0,1]
    Phi            *float64           `json:"phi,omitempty"`             // optimism latent [-1,1]
    Perception     *PerceptionProfile `json:"perception_profile,omitempty"`
    Exit           *ExitProfile       `json:"exit_profile,omitempty"`
    MaxHold        *int               `json:"max_hold,omitempty"`
    // ThetaAdd     *float64          `json:"theta_add,omitempty"`      // RESERVED — not designed (blocked on theta_act)
    // herd-only
    ActProb        *float64           `json:"act_prob,omitempty"`
    // canonical-only flag (one endogenous agent); on-demand sets is_tipper at runtime, not here
    IsTipper       bool               `json:"is_tipper,omitempty"`
}
// PerceptionProfile holds: per-ON-channel blocks (method-forked chart/range carry the CHOSEN method's params;
// fixed-calc liquidity/volume/time carry their params), plus bias, channel_weights, reread_sensitivity,
// decide_sensitivity. Exact sub-shape = PERCEPTION_PROFILE_GENERATION; channel_weights contents await the channel→trait map.

Decisions. Pointer + omitempty for all kind-dependent blocks → herd rows stay lean, OFF channels absent. [decided here] ThetaAdd left commented/reserved — not populated (blocked on theta_act). PerceptionProfile's method-forked sub-structs are built by the generator (Tier 2); the JSONB absorbs the variation with no schema change.

Point 4 — Redis runtime layer: keys, RuntimeSnapshot, ops

What it is. The per-session Redis state (Tier 1 §5): key helpers, the per-agent snapshot struct, and the write ops. No frozen traits, no positions/cash/P&L.

Expected & why. Per-agent runtime is a single hash keyed by (exchange_id, agent_id); write-on-change cadence (3b R5). Kind-gated blocks (4b decision memory for endogenous; tip-state for herd). trading_capital lives here (sole home for on-demand).

The implementation.

go
func runtimeKey(ex string, id AgentID) string { return fmt.Sprintf("runtime:%s:agent:%s", ex, id) }
func tipslotKey(ex string) string             { return fmt.Sprintf("runtime:%s:tipslot", ex) }
func candleKey(ex, inst, tf string) string    { return fmt.Sprintf("marketdata:%s:%s:candles:%s", ex, inst, tf) }
func quoteKey(ex, inst string) string         { return fmt.Sprintf("marketdata:%s:%s:quote", ex, inst) }

type RuntimeSnapshot struct {
    TradingCapital int64   // A (on-demand sole home; canonical convenience copy)
    IsTipper       bool    // ON-DEMAND per-session tipper only
    Presence       string  // DORMANT|ACTIVE|IDLE|DONE|RETIRED
    EntryTime      float64 // [0,1]; stamped in 2c (NOT in 2b)
    NextCheckTime  int64
    IdleSince      int64
    // 4b working memory (endogenous)
    StandingOpinion   float64
    StandingStance    string  // FLAT_IDLE|WAIT|HOLDING
    WaitStreak        int
    LastDecidedReadings map[string]float64
    // 4d working memory (while holding)
    PositionContext *PositionContext // {EntryDir, EntryChannel, EntryTick}
    // tip-following (herd)
    LastActedTipID string
    Tracking       *Tracking // {TrackingTipID, Target, NoticeAt}
    MyExitLevel    int64
    SubmitAt       int64
}

type RuntimeStore interface {
    WriteInitialSnapshot(ex string, a Agent) error          // 2b P3: {presence=DORMANT, trading_capital} only
    SetPresence(ex string, id AgentID, p string) error      // write-on-change
    SetTimers(ex string, id AgentID, next, idleSince int64) error
    StampEntryTime(ex string, id AgentID, t float64) error  // 2c
    SetTipper(ex string, id AgentID) error                  // 2b P4 (on-demand)
    // 4b/4d working-memory writers added by Tier 3/4 consumers
}

Decisions. The initial 2b snapshot writes only {presence=DORMANT, trading_capital} — entry_time/timers/working-memory are written later by the goroutine (thin-by-design, Fork 2). [decided here, from the 2b lock] Positions/cash/P&L never appear in any op. Market-data is written by the 2c ingest path (Tier 2), read by Tier 3/4.

Point 5 — Market-data cache (keys only; writer = Tier 2 ingest)

What it is. The Redis layout for cached market data; L2 is a pure consumer (no rollup).

Expected & why. candles:{tf} (only 1m populated) append/range; quote overwrite. One WS per exchange_id; exchange_id namespace gives canonical/on-demand isolation for free.

The implementation. Keys as in Point 4 (candleKey, quoteKey). Ops:

go
type MarketDataStore interface {
    WriteCandles(ex, inst, tf string, bars []Candle) error // history pull (2c)
    AppendCandle(ex, inst, tf string, bar Candle) error    // live ingest (2c)
    WriteQuote(ex, inst string, q Quote) error             // live ingest, overwrite
}

Decisions. No fold/rollup method exists — serving any TF is the broker's job. On-demand history is written under the on-demand exchange_id from the canonical market's candles (the source selection is a Tier-2 2c concern; the store is TF/instrument-keyed and source-agnostic).

Point 6 — The pending_mint WAL

What it is. The crash-recovery write-ahead log for the mint→open→persist chain (Tier 1 §2 / Tier 2 2a; locked amendment).

Expected & why. Staged at mint before account-open (new agents only); cleared after the durable row is persisted. Guarantees no orphaned Core account. trading_capital deliberately not staged (recovery redraws it).

The implementation.

go
type StageRecord struct { Traits TraitVector; Population, Country, ExchangeID string }

type PendingMintWAL interface {
    Stage(id AgentID, rec StageRecord) error // HSET pending_mint {id} = rec   (2a, before account-open)
    Clear(id AgentID) error                  // HDEL pending_mint {id}          (2b, after durable row written)
    Drain() ([]StageRecord, error)           // HGETALL pending_mint            (startup recovery; EXECUTION = Tier 6)
}

Decisions. Recovery execution (re-open idempotently → re-persist by PK → clear, run at startup before serving new BRING_UPs) is Tier 6; Tier 1 provides the WAL interface + the recovery contract. [from the locked amendment]

Point 7 — Capital helper: drawTradingCapital

What it is. The pure function that produces A from the frozen scale and the funded ceiling (Tier 1 §4). Called by Tier-2 Stage C.

Expected & why. A = clamp(capital_size × jitter, floor, funded_cash), fallback default_funded_cash. Pure, seeded.

The implementation.

go
func drawTradingCapital(capitalSize int64, fundedCash int64, rng *RNG, p GenParams) int64 {
    ceiling := fundedCash
    if ceiling <= 0 { ceiling = p.DefaultFundedCash }      // response omitted funded_cash → config fallback
    a := int64(float64(capitalSize) * drawJitter(rng, p.CapitalJitter))
    return clamp(a, p.CapitalFloor, ceiling)               // ≤ funded_cash by construction
}

Decisions. The three-case dispatch (new / on-demand-existing / canonical-existing) lives in Tier-2 Stage C; this helper is the shared primitive. Jitter/floor are calibration.

Point 8 — The LeaseAllocator interface

What it is. The on-demand free/busy bookkeeping over the two Redis keys (Tier 1 §6). Interface + the two keys; select/enlist are used by Tier 2, release/reconcile by Tier 6.

Expected & why. available is a SET (native "take N"); leased a HASH (reverse-lookup for teardown/reclaim). Select must be atomic (pop + mark) so concurrent on-demand sessions can't double-lease.

The implementation.

go
func availKey(country string) string   { return fmt.Sprintf("lease:%s:available", country) }
func leasedKey(country string) string  { return fmt.Sprintf("lease:%s:leased", country) }

type LeaseAllocator interface {
    SelectAvailable(country string, n int, ex string) ([]AgentID, error) // ATOMIC pop-from-available + HSET leased→ex (2a)
    Enlist(country string, id AgentID, ex string) error                  // HSET leased→ex for newly-minted on-demand (2b)
    Release(country string, ids []AgentID) error                         // HDEL leased + SADD available (Tier 6; after session_discard)
    Reconcile(country string, liveExchanges map[string]bool, members []AgentID) error // restart: orphans→available (Tier 6)
}

Decisions. SelectAvailable atomicity via a Lua step (pop+mark as one op) to close the pop-but-not-yet-marked restart gap. [from the Tier-1 lease lock] Release/Reconcile are Tier 6; interface named here so Tier 2 compiles against it.

What Tier-1 impl guarantees
A single durable table + narrow store interface; idempotent insert; the reuse-index query is the only hot read.
A per-agent Redis runtime hash carrying presence/timers/working-memory/session-A — never traits, never positions/P&L.
Market-data cache keyed for canonical/on-demand isolation; L2 does no rollup.
The WAL + lease interfaces that the bring-up flow (Tier 2) sequences and the resilience tier (Tier 6) completes.
Open / deferred (Tier-1 impl)
ThetaAdd reserved (blocked); PerceptionProfile.channel_weights awaits the channel→trait map.
Release/Reconcile/WAL-Drain execution → Tier 6.
Canonical P&L→A fold update op → later tier.
All distribution/jitter/floor/pool-size values → calibration/ops.

End of Tier 1 implementation design. Next: L2_TIER2_IMPLEMENTATION_DESIGN.