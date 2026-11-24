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
// call in a thread-safe way so the account-open tests can assert
// per-agent correctness across concurrent goroutines.
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
	// concurrency=1 gives deterministic serial behaviour in tests that
	// check call counts and per-agent state; tests that need to exercise
	// the parallel fan-out use a higher value explicitly.
	client, err := NewAccountOpenClient(spy, 1, discardLogger())
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
	if _, err := NewAccountOpenClient(nil, 1, discardLogger()); err == nil {
		t.Fatalf("expected nil coreClient error, got nil")
	}
	if _, err := NewAccountOpenClient(newSpyCoreAccountClient(), 1, nil); err == nil {
		t.Fatalf("expected nil logger error, got nil")
	}
}

func TestNewAccountOpenClientRejectsNonPositiveConcurrency(t *testing.T) {
	for _, c := range []int{0, -1, -100} {
		if _, err := NewAccountOpenClient(newSpyCoreAccountClient(), c, discardLogger()); err == nil {
			t.Fatalf("concurrency=%d: expected error, got nil", c)
		}
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

func TestOpenCallsOpenAccountAndGetAccountCashPerAgent(t *testing.T) {
	// concurrency=1 so execution is serial and we can check exact counts.
	spy := newSpyCoreAccountClient()
	client := mustBuildAccountOpenClient(t, spy)
	newAgents := newTestAgents(3)

	if err := client.Open(context.Background(), newAgents); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Every agent must get exactly one open call and one cash call.
	if len(spy.openCalls) != 3 {
		t.Fatalf("open calls = %d, want 3: %v", len(spy.openCalls), spy.openCalls)
	}
	if len(spy.cashCalls) != 3 {
		t.Fatalf("cash calls = %d, want 3: %v", len(spy.cashCalls), spy.cashCalls)
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

// TestOpenParallelAllAgentsProcessed verifies that with concurrency > 1
// every agent still gets its handle and funded cash correctly filled —
// i.e. no goroutine races on the slice write.
func TestOpenParallelAllAgentsProcessed(t *testing.T) {
	const agentCount = 50
	spy := newSpyCoreAccountClient()
	client, err := NewAccountOpenClient(spy, 10, discardLogger())
	if err != nil {
		t.Fatalf("NewAccountOpenClient: %v", err)
	}
	newAgents := newTestAgents(agentCount)

	if err := client.Open(context.Background(), newAgents); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i, a := range newAgents {
		wantHandle := fmt.Sprintf("handle-new-%03d", i+1)
		if a.CapitalHandle != wantHandle {
			t.Fatalf("agent %d: handle=%q want=%q", i, a.CapitalHandle, wantHandle)
		}
		if a.FundedCash != 50_000 {
			t.Fatalf("agent %d: funded_cash=%d want 50000", i, a.FundedCash)
		}
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
}

// ---- Idempotency contract -----------------------------------------

func TestOpenReSendRetriesOpenAccountSafely(t *testing.T) {
	// Core's OpenAccount is get-or-create keyed on the agent id, so the
	// same id resolved twice returns the same handle. A retried bring-up
	// must end in the same state as the first run.
	spy := newSpyCoreAccountClient()
	client := mustBuildAccountOpenClient(t, spy)
	newAgents := newTestAgents(2)

	if err := client.Open(context.Background(), newAgents); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	firstHandles := []string{newAgents[0].CapitalHandle, newAgents[1].CapitalHandle}

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

// Compile-time assertion that *core.Client satisfies the orchestrator's
// narrow interface. If a future Core refactor changes OpenAccount or
// GetAccountCash signatures, this fails at build time rather than at
// first bring-up.
var _ CoreAccountClient = (*core.Client)(nil)

// Keep strings and sync usage non-trivial so vet doesn't flag them.
var _ = strings.TrimSpace
var _ sync.Mutex
