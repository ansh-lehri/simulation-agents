package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"simulation-agents/agent"
)

// Querier is the subset of pgx methods store operations rely on. Both
// *pgxpool.Pool and pgx.Tx satisfy it, which lets tests pass a transaction
// (and roll it back for isolation) while production hands over the pool.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// AgentStore is the narrow contract over the durable agents table.
// Four operations, no more — the hot reads are LiveMembers (served by
// the partial reuse index) and LoadMany (the on-demand lease hand-off).
type AgentStore interface {
	// InsertAgent writes one durable row. Idempotent by primary key — a
	// second insert of the same agent_id is a no-op, which is what the
	// WAL recovery path and any mint retries depend on.
	InsertAgent(ctx context.Context, agentRecord agent.Agent) error

	// LiveMembers returns every live row (retired_at IS NULL) for the
	// given country + population — the reuse-index query.
	LiveMembers(ctx context.Context, country, population string) ([]agent.Agent, error)

	// LoadMany returns the rows for the given ids. Order is not
	// guaranteed; the caller should map by ID.
	LoadMany(ctx context.Context, agentIDs []agent.AgentID) ([]agent.Agent, error)

	// Retire sets retired_at on the matching row. Subsequent LiveMembers
	// calls exclude it. If no row matches, this is a silent no-op.
	Retire(ctx context.Context, agentID agent.AgentID, retiredAt time.Time) error
}

// PostgresAgentStore is the Postgres-backed AgentStore.
type PostgresAgentStore struct {
	querier Querier
}

// NewPostgresAgentStore wraps any Querier. Pass the pool in production;
// pass a transaction in tests to get rollback-isolated runs.
func NewPostgresAgentStore(querier Querier) *PostgresAgentStore {
	return &PostgresAgentStore{querier: querier}
}

// agentColumns names the full column list in the order every select /
// scan here uses, so the SELECT and scanAgents stay in lockstep.
const agentColumns = `agent_id, population, country, capital_handle, trading_capital,
	traits, traits_version, traits_updated_at, origin, retired_at`

func (agentStore *PostgresAgentStore) InsertAgent(ctx context.Context, agentRecord agent.Agent) error {
	traitsJSON, err := json.Marshal(agentRecord.Traits)
	if err != nil {
		return fmt.Errorf("agent_store: marshal traits for %s: %w", agentRecord.ID, err)
	}

	// Canonical rows persist session money (evolves across sessions).
	// On-demand rows store NULL — session money is drawn fresh per
	// session and lives only in Redis. Deciding here from population
	// (not from the caller's TradingCapital field) means an accidental
	// non-zero value on an on-demand record cannot leak into Postgres.
	var tradingCapital *int64
	if agentRecord.Population == agent.PopulationCanonical {
		persisted := agentRecord.TradingCapital
		tradingCapital = &persisted
	}

	const insertSQL = `
		INSERT INTO agents (
			agent_id, population, country, capital_handle, trading_capital,
			traits, traits_version, traits_updated_at, origin, retired_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (agent_id) DO NOTHING`

	_, err = agentStore.querier.Exec(ctx, insertSQL,
		string(agentRecord.ID),
		agentRecord.Population,
		agentRecord.Country,
		agentRecord.CapitalHandle,
		tradingCapital,
		string(traitsJSON),
		agentRecord.TraitsVersion,
		agentRecord.TraitsUpdatedAt,
		agentRecord.Origin,
		agentRecord.RetiredAt,
	)
	if err != nil {
		return fmt.Errorf("agent_store: insert %s: %w", agentRecord.ID, err)
	}
	return nil
}

func (agentStore *PostgresAgentStore) LiveMembers(ctx context.Context, country, population string) ([]agent.Agent, error) {
	const query = `
		SELECT ` + agentColumns + `
		FROM agents
		WHERE country = $1 AND population = $2 AND retired_at IS NULL`

	rows, err := agentStore.querier.Query(ctx, query, country, population)
	if err != nil {
		return nil, fmt.Errorf("agent_store: query live members (%s/%s): %w", country, population, err)
	}
	defer rows.Close()

	return scanAgents(rows)
}

func (agentStore *PostgresAgentStore) LoadMany(ctx context.Context, agentIDs []agent.AgentID) ([]agent.Agent, error) {
	if len(agentIDs) == 0 {
		return nil, nil
	}
	// Pass the slice as []string so pgx binds it as text[] via its
	// default type map. AgentID has an underlying string type but
	// converting explicitly makes the intent and the Postgres type
	// side visible at the call site.
	stringIDs := make([]string, len(agentIDs))
	for index, agentID := range agentIDs {
		stringIDs[index] = string(agentID)
	}

	const query = `
		SELECT ` + agentColumns + `
		FROM agents
		WHERE agent_id = ANY($1)`

	rows, err := agentStore.querier.Query(ctx, query, stringIDs)
	if err != nil {
		return nil, fmt.Errorf("agent_store: query load many (%d ids): %w", len(agentIDs), err)
	}
	defer rows.Close()

	return scanAgents(rows)
}

func (agentStore *PostgresAgentStore) Retire(ctx context.Context, agentID agent.AgentID, retiredAt time.Time) error {
	const retireSQL = `UPDATE agents SET retired_at = $2 WHERE agent_id = $1`
	if _, err := agentStore.querier.Exec(ctx, retireSQL, string(agentID), retiredAt); err != nil {
		return fmt.Errorf("agent_store: retire %s: %w", agentID, err)
	}
	return nil
}

// scanAgents materializes every row from the cursor. Trading capital and
// retired_at are nullable and scanned via pointers; traits is a JSONB
// column scanned as raw bytes and unmarshaled.
func scanAgents(rows pgx.Rows) ([]agent.Agent, error) {
	var out []agent.Agent
	for rows.Next() {
		var (
			agentIDStr     string
			tradingCapital *int64
			traitsRaw      []byte
			retiredAt      *time.Time
			agentRow       agent.Agent
		)
		err := rows.Scan(
			&agentIDStr,
			&agentRow.Population,
			&agentRow.Country,
			&agentRow.CapitalHandle,
			&tradingCapital,
			&traitsRaw,
			&agentRow.TraitsVersion,
			&agentRow.TraitsUpdatedAt,
			&agentRow.Origin,
			&retiredAt,
		)
		if err != nil {
			return nil, fmt.Errorf("agent_store: scan row: %w", err)
		}
		agentRow.ID = agent.AgentID(agentIDStr)
		if tradingCapital != nil {
			agentRow.TradingCapital = *tradingCapital
		}
		agentRow.RetiredAt = retiredAt
		if err := json.Unmarshal(traitsRaw, &agentRow.Traits); err != nil {
			return nil, fmt.Errorf("agent_store: unmarshal traits for %s: %w", agentRow.ID, err)
		}
		out = append(out, agentRow)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agent_store: iterate rows: %w", err)
	}
	return out, nil
}
