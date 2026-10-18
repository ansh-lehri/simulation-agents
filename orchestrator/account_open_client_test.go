package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"simulation-agents/agent"
	"simulation-agents/core"
)

// spyCoreAccountClient satisfies CoreAccountClient and records every
// call in order so the account-open tests can assert the per-agent
// loop structure and the (opens-first, then-cash-reads) ordering.
type spyCoreAccountClient struct {
	mutex sync.Mutex

	openByAgentUserID    map[string]string
	openErrByAgentUserID map[string]error
	openCalls            []string

	cashByAccountID    map[string]core.AccountCash
	cashErrByAccountID map[string]error
	cashCalls          []string
}

func newSpyCoreAccountClient() *spyCoreAccountClient {
	return &spyCoreAccountClient{
		openByAgentUserID:    map[string]string{},
		openErrByAgentUserID: map[string]error{},
		cashByAccountID:      map[string]core.AccountCash{},
		cashErrByAccountID:   map[string]error{},
	}
}

func (spy *spyCoreAccountClient) OpenAccount(ctx context.Context, agentUserID string) (string, error) {
	_ = ctx
	spy.mutex.Lock()
	defer spy.mutex.Unlock()
	spy.openCalls = append(spy.openCalls, agentUserID)
	if err, present := spy.openErrByAgentUserID[agentUserID]; present {
		return "", err
	}
	handle, present := spy.openByAgentUserID[agentUserID]
	if !present {
		// Default: a predictable handle derived from the id.
		handle = "handle-" + agentUserID
	}
	return handle, nil
}

func (spy *spyCoreAccountClient) GetAccountCash(ctx context.Context, accountID string) (core.AccountCash, error) {
	_ = ctx
	spy.mutex.Lock()
	defer spy.mutex.Unlock()
	spy.cashCalls = append(spy.cashCalls, accountID)
	if err, present := spy.cashErrByAccountID[accountID]; present {
		return core.AccountCash{}, err
	}
	cash, present := spy.cashByAccountID[accountID]
	if !present {
		cash = core.AccountCash{TradingCash: 50_000, BankCash: 0}
	}
	return cash, nil
}

func mustBuildAccountOpenClient(t *testing.T, spy *spyCoreAccountClient) *AccountOpenClient {
	t.Helper()
	client, err := NewAccountOpenClient(spy, discardLogger())
	if err != nil {
		t.Fatalf("NewAccountOpenClient: %v", err)
	}
	return client
}

func newTestAgents(count int) []agent.Agent {
	agents := make([]agent.Agent, count)
	for index := 0; index < count; index++ {
		agents[index] = agent.Agent{
			ID:         agent.AgentID(fmt.Sprintf("new-%03d", index+1)),
			Population: agent.PopulationCanonical,
			Country:    "IN",
			Origin:     agent.OriginMint,
		}
	}
	return agents
}

// ---- NewAccountOpenClient -----------------------------------------

func TestNewAccountOpenClientRejectsNilDependencies(t *testing.T) {
	if _, err := NewAccountOpenClient(nil, discardLogger()); err == nil {
		t.Fatalf("expected nil coreClient error, got nil")
	}
	if _, err := NewAccountOpenClient(newSpyCoreAccountClient(), nil); err == nil {
		t.Fatalf("expected nil logger error, got nil")
	}
}

// ---- Open (happy path) --------------------------------------------

func TestOpenEmptyNewSliceIsNoCoreCall(t *testing.T) {
	spy := newSpyCoreAccountClient()
	client := mustBuildAccountOpenClient(t, spy)

	if err := client.Open(context.Background(), nil); err != nil {
		t.Fatalf("Open(nil): %v", err)
	}
	if err := client.Open(context.Background(), []agent.Agent{}); err != nil {
		t.Fatalf("Open(empty): %v", err)
	}
	if len(spy.openCalls) != 0 || len(spy.cashCalls) != 0 {
		t.Fatalf("empty new slice triggered Core calls: opens=%v cash=%v",
			spy.openCalls, spy.cashCalls)
	}
}

func TestOpenCallsOpenAccountThenGetAccountCashInOrder(t *testing.T) {
	spy := newSpyCoreAccountClient()
	client := mustBuildAccountOpenClient(t, spy)
	newAgents := newTestAgents(3)

	if err := client.Open(context.Background(), newAgents); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Opens happen once per agent, in input order.
	wantOpenCalls := []string{"new-001", "new-002", "new-003"}
	if !reflectStringSliceEqual(spy.openCalls, wantOpenCalls) {
		t.Fatalf("open calls = %v, want %v", spy.openCalls, wantOpenCalls)
	}
	// Cash reads happen once per handle, in input order, AFTER opens.
	wantCashCalls := []string{"handle-new-001", "handle-new-002", "handle-new-003"}
	if !reflectStringSliceEqual(spy.cashCalls, wantCashCalls) {
		t.Fatalf("cash calls = %v, want %v", spy.cashCalls, wantCashCalls)
	}
}

func TestOpenPopulatesHandleAndFundedCashPerAgent(t *testing.T) {
	spy := newSpyCoreAccountClient()
	spy.openByAgentUserID = map[string]string{
		"new-001": "handle-alpha",
		"new-002": "handle-beta",
	}
	spy.cashByAccountID = map[string]core.AccountCash{
		"handle-alpha": {TradingCash: 100_000},
		"handle-beta":  {TradingCash: 250_000},
	}
	client := mustBuildAccountOpenClient(t, spy)
	newAgents := newTestAgents(2)

	if err := client.Open(context.Background(), newAgents); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if newAgents[0].CapitalHandle != "handle-alpha" || newAgents[0].FundedCash != 100_000 {
		t.Fatalf("agent 0 misfilled: handle=%q funded=%d", newAgents[0].CapitalHandle, newAgents[0].FundedCash)
	}
	if newAgents[1].CapitalHandle != "handle-beta" || newAgents[1].FundedCash != 250_000 {
		t.Fatalf("agent 1 misfilled: handle=%q funded=%d", newAgents[1].CapitalHandle, newAgents[1].FundedCash)
	}
}

// ---- Open (failure paths) -----------------------------------------

func TestOpenSurfacesOpenAccountError(t *testing.T) {
	spy := newSpyCoreAccountClient()
	openErr := errors.New("core 503")
	spy.openErrByAgentUserID = map[string]error{"new-002": openErr}
	client := mustBuildAccountOpenClient(t, spy)
	newAgents := newTestAgents(3)

	err := client.Open(context.Background(), newAgents)
	if err == nil || !errors.Is(err, openErr) {
		t.Fatalf("expected wrapped OpenAccount error, got %v", err)
	}
	// The pass stopped at the failing agent — no further opens, no
	// cash reads at all.
	if len(spy.openCalls) != 2 {
		t.Fatalf("expected 2 open attempts before failure, got %d: %v", len(spy.openCalls), spy.openCalls)
	}
	if len(spy.cashCalls) != 0 {
		t.Fatalf("expected zero cash reads on open failure, got %v", spy.cashCalls)
	}
	// Agent 0 still has its handle set (the open before the failing
	// one succeeded); agent 1 and 2 do not.
	if newAgents[0].CapitalHandle == "" {
		t.Fatalf("agent 0 missing handle after a later open failure")
	}
	if newAgents[1].CapitalHandle != "" || newAgents[2].CapitalHandle != "" {
		t.Fatalf("agents past the failing index carry a handle: %q %q",
			newAgents[1].CapitalHandle, newAgents[2].CapitalHandle)
	}
}

func TestOpenSurfacesGetAccountCashError(t *testing.T) {
	spy := newSpyCoreAccountClient()
	cashErr := errors.New("core 504")
	spy.cashErrByAccountID = map[string]error{"handle-new-002": cashErr}
	client := mustBuildAccountOpenClient(t, spy)
	newAgents := newTestAgents(3)

	err := client.Open(context.Background(), newAgents)
	if err == nil || !errors.Is(err, cashErr) {
		t.Fatalf("expected wrapped GetAccountCash error, got %v", err)
	}
	// All three opens succeeded before the cash reads began.
	if len(spy.openCalls) != 3 {
		t.Fatalf("expected 3 opens before cash reads, got %d", len(spy.openCalls))
	}
	// Cash loop stopped at the failing index: calls for 1 and 2, not 3.
	if len(spy.cashCalls) != 2 {
		t.Fatalf("expected 2 cash reads before failure, got %d: %v", len(spy.cashCalls), spy.cashCalls)
	}
	// Agents past the failing cash read keep handle but no FundedCash.
	if newAgents[2].CapitalHandle == "" {
		t.Fatalf("agent 2 missing handle after a cash read failure")
	}
	if newAgents[2].FundedCash != 0 {
		t.Fatalf("agent 2 carries FundedCash after a cash read failure: %d", newAgents[2].FundedCash)
	}
}

// ---- Idempotency contract -----------------------------------------

func TestOpenReSendRetriesOpenAccountSafely(t *testing.T) {
	// Core's OpenAccount is get-or-create keyed on the agent id, so the
	// same id resolved twice returns the same handle. This test models
	// a retried bring-up: the second Open over the same agents must
	// end in the same state as the first — no duplicate account
	// confusion visible from this side.
	spy := newSpyCoreAccountClient()
	client := mustBuildAccountOpenClient(t, spy)
	newAgents := newTestAgents(2)

	if err := client.Open(context.Background(), newAgents); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	firstHandles := []string{newAgents[0].CapitalHandle, newAgents[1].CapitalHandle}

	// Reset FundedCash/CapitalHandle to zero to simulate a fresh retry
	// where the in-memory state was discarded.
	for index := range newAgents {
		newAgents[index].CapitalHandle = ""
		newAgents[index].FundedCash = 0
	}
	if err := client.Open(context.Background(), newAgents); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	for index := range newAgents {
		if newAgents[index].CapitalHandle != firstHandles[index] {
			t.Fatalf("retry produced different handle for %s: first=%q second=%q",
				newAgents[index].ID, firstHandles[index], newAgents[index].CapitalHandle)
		}
	}
}

// ---- helpers ------------------------------------------------------

func reflectStringSliceEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// Compile-time assertion that *core.Client satisfies the orchestrator's
// narrow interface. If a future Core refactor changes OpenAccount or
// GetAccountCash signatures, this fails at build time rather than at
// first bring-up.
var _ CoreAccountClient = (*core.Client)(nil)

// Keep strings and sync usage non-trivial so vet doesn't flag them when
// a future edit removes a test row — currently used above.
var _ = strings.TrimSpace
var _ sync.Mutex
