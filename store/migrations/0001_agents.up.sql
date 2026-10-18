-- Durable agent identity table + the partial reuse index.
--
-- One table serves both populations ("canonical" and "ondemand"), discriminated
-- by the population column. The traits JSONB holds the full frozen trait vector
-- and is read as a whole on load — never filtered on an individual trait value.
-- The agents.agent_id is reused as the economy account's owner_id so the two
-- systems link via one opaque id with no join tables.

CREATE TABLE agents (
    agent_id          TEXT        PRIMARY KEY,
    population        TEXT        NOT NULL CHECK (population IN ('canonical','ondemand')),
    country           TEXT        NOT NULL,
    capital_handle    TEXT        NOT NULL,                        -- economy account_id, opaque to this system
    trading_capital   BIGINT,                                      -- canonical: set at mint + evolves with realized P&L; ondemand: NULL (drawn fresh in Redis)
    traits            JSONB       NOT NULL,                        -- frozen trait vector, written once at mint
    traits_version    INT         NOT NULL DEFAULT 1,              -- bumped only by a future adaptation pass, never the hot path
    traits_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    origin            TEXT        NOT NULL CHECK (origin IN ('boot','mint')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    retired_at        TIMESTAMPTZ                                  -- NULL = live member; set = retired for good, never revived
);

-- The reuse-index query — "live members for this country+population" — is the
-- only hot read against this table. A partial index on retired_at IS NULL keeps
-- it narrow (retired rows accumulate but never land in the index).
CREATE INDEX idx_agents_reuse ON agents (country, population) WHERE retired_at IS NULL;
