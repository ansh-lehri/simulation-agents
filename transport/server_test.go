package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"simulation-agents/transport"
)

// windDownCapture records one wind-down invocation so tests can assert both
// of its arguments reached the orchestrator intact.
type windDownCapture struct {
	ExchangeID   string
	TeardownMode string
}

// spyOrchestrator implements transport.AgentOrchestrator by recording every
// inbound call. It supports configurable responses so tests can simulate
// orchestrator success, orchestrator failure, and slow in-flight work.
type spyOrchestrator struct {
	mu              sync.Mutex
	bringUpCalls    []transport.BringUpRequest
	windDownCalls   []windDownCapture
	bringUpResponse transport.BringUpAck
	bringUpError    error
	windDownError   error
	bringUpDelay    time.Duration
}

func (spy *spyOrchestrator) BringUp(ctx context.Context, request transport.BringUpRequest) (transport.BringUpAck, error) {
	spy.mu.Lock()
	spy.bringUpCalls = append(spy.bringUpCalls, request)
	delay := spy.bringUpDelay
	response := spy.bringUpResponse
	responseErr := spy.bringUpError
	spy.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return transport.BringUpAck{}, ctx.Err()
		}
	}
	return response, responseErr
}

func (spy *spyOrchestrator) WindDown(_ context.Context, exchangeID string, teardownMode string) error {
	spy.mu.Lock()
	spy.windDownCalls = append(spy.windDownCalls, windDownCapture{ExchangeID: exchangeID, TeardownMode: teardownMode})
	responseErr := spy.windDownError
	spy.mu.Unlock()
	return responseErr
}

func (spy *spyOrchestrator) recordedBringUps() []transport.BringUpRequest {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	copied := make([]transport.BringUpRequest, len(spy.bringUpCalls))
	copy(copied, spy.bringUpCalls)
	return copied
}

func (spy *spyOrchestrator) recordedWindDowns() []windDownCapture {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	copied := make([]windDownCapture, len(spy.windDownCalls))
	copy(copied, spy.windDownCalls)
	return copied
}

// newRoutedTestServer wires a transport.Server's handler into an httptest
// server so route-level tests can hit real HTTP without owning a listener.
func newRoutedTestServer(t *testing.T, orchestrator transport.AgentOrchestrator) *httptest.Server {
	t.Helper()
	logger, _ := newCapturingLogger()
	serverInstance := transport.New("127.0.0.1:0", orchestrator, logger)
	routedServer := httptest.NewServer(serverInstance.Handler())
	t.Cleanup(routedServer.Close)
	return routedServer
}

func TestServer_Healthz_ReturnsOK(t *testing.T) {
	routedServer := newRoutedTestServer(t, &spyOrchestrator{})
	response, err := http.Get(routedServer.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want %d", response.StatusCode, http.StatusOK)
	}
}

func TestServer_BringUp_ForwardsRequestAndReturnsAck(t *testing.T) {
	orchestrator := &spyOrchestrator{
		bringUpResponse: transport.BringUpAck{
			ExchangeID: "exchange-us-equities-001",
			Status:     transport.StatusAccountsReady,
		},
	}
	routedServer := newRoutedTestServer(t, orchestrator)

	requestBody := `{
		"exchange_id": "exchange-us-equities-001",
		"session_type": "canonical",
		"country": "US",
		"session_start": "2026-10-17T09:30:00Z",
		"session_end":   "2026-10-17T16:00:00Z",
		"instruments":   ["ACME","FICT"],
		"broker_id":     "broker-primary",
		"scenario":      ""
	}`
	response, err := http.Post(routedServer.URL+"/bringup", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("POST /bringup: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("POST /bringup status = %d, want 200, body=%s", response.StatusCode, body)
	}

	var ack transport.BringUpAck
	if err := json.NewDecoder(response.Body).Decode(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.ExchangeID != "exchange-us-equities-001" {
		t.Errorf("ack.ExchangeID = %q, want %q", ack.ExchangeID, "exchange-us-equities-001")
	}
	if ack.Status != transport.StatusAccountsReady {
		t.Errorf("ack.Status = %q, want %q", ack.Status, transport.StatusAccountsReady)
	}

	recorded := orchestrator.recordedBringUps()
	if len(recorded) != 1 {
		t.Fatalf("orchestrator saw %d bring-up calls, want 1", len(recorded))
	}
	received := recorded[0]
	expectedStart := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)
	expectedEnd := time.Date(2026, 10, 17, 16, 0, 0, 0, time.UTC)
	if received.ExchangeID != "exchange-us-equities-001" ||
		received.SessionType != "canonical" ||
		received.Country != "US" ||
		received.BrokerID != "broker-primary" ||
		!received.SessionStart.Equal(expectedStart) ||
		!received.SessionEnd.Equal(expectedEnd) ||
		len(received.Instruments) != 2 ||
		received.Instruments[0] != "ACME" || received.Instruments[1] != "FICT" {
		t.Errorf("orchestrator received unexpected request: %+v", received)
	}
}

func TestServer_BringUp_RejectsMalformedJSON(t *testing.T) {
	orchestrator := &spyOrchestrator{}
	routedServer := newRoutedTestServer(t, orchestrator)

	response, err := http.Post(routedServer.URL+"/bringup", "application/json", strings.NewReader(`{not-json`))
	if err != nil {
		t.Fatalf("POST /bringup: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	if len(orchestrator.recordedBringUps()) != 0 {
		t.Errorf("orchestrator saw a bring-up call despite malformed JSON")
	}
}

func TestServer_BringUp_RejectsUnknownFields(t *testing.T) {
	orchestrator := &spyOrchestrator{}
	routedServer := newRoutedTestServer(t, orchestrator)

	body := `{"exchange_id":"ex","session_type":"canonical","country":"US","instruments":[],"broker_id":"b","session_start":"2026-10-17T09:30:00Z","session_end":"2026-10-17T16:00:00Z","unexpected_field":"typo"}`
	response, err := http.Post(routedServer.URL+"/bringup", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /bringup: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (strict decoder must reject unknown fields)", response.StatusCode, http.StatusBadRequest)
	}
	if len(orchestrator.recordedBringUps()) != 0 {
		t.Errorf("orchestrator saw a bring-up call despite unknown field")
	}
}

func TestServer_BringUp_SurfacesOrchestratorError(t *testing.T) {
	orchestrator := &spyOrchestrator{
		bringUpError: errorString("validateTrigger: country missing"),
	}
	routedServer := newRoutedTestServer(t, orchestrator)

	body := minimalBringUpJSON()
	response, err := http.Post(routedServer.URL+"/bringup", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /bringup: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusInternalServerError)
	}

	var envelope struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if envelope.Error == "" {
		t.Error("error envelope has empty error text")
	}
}

func TestServer_WindDown_ForwardsFieldsAndReturnsNoContent(t *testing.T) {
	orchestrator := &spyOrchestrator{}
	routedServer := newRoutedTestServer(t, orchestrator)

	body := `{"exchange_id":"exchange-us-equities-001","teardown_mode":"graceful"}`
	response, err := http.Post(routedServer.URL+"/winddown", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /winddown: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}

	recorded := orchestrator.recordedWindDowns()
	if len(recorded) != 1 {
		t.Fatalf("orchestrator saw %d wind-down calls, want 1", len(recorded))
	}
	if recorded[0].ExchangeID != "exchange-us-equities-001" || recorded[0].TeardownMode != "graceful" {
		t.Errorf("orchestrator received unexpected wind-down call: %+v", recorded[0])
	}
}

func TestServer_BringUp_RejectsWrongMethod(t *testing.T) {
	routedServer := newRoutedTestServer(t, &spyOrchestrator{})
	response, err := http.Get(routedServer.URL + "/bringup")
	if err != nil {
		t.Fatalf("GET /bringup: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d (method matching must reject GET on POST route)", response.StatusCode, http.StatusMethodNotAllowed)
	}
}

func TestServer_ShutdownWaitsForInFlightRequest(t *testing.T) {
	orchestrator := &spyOrchestrator{
		bringUpResponse: transport.BringUpAck{ExchangeID: "ex", Status: transport.StatusAccountsReady},
		bringUpDelay:    150 * time.Millisecond,
	}
	logger, _ := newCapturingLogger()
	serverInstance := transport.New("127.0.0.1:0", orchestrator, logger)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- serverInstance.Serve(listener) }()

	// Fire a slow in-flight request against the live listener.
	type requestOutcome struct {
		status int
		body   []byte
		err    error
	}
	outcome := make(chan requestOutcome, 1)
	go func() {
		postClient := &http.Client{Timeout: 2 * time.Second}
		response, requestErr := postClient.Post(
			"http://"+listener.Addr().String()+"/bringup",
			"application/json",
			strings.NewReader(minimalBringUpJSON()),
		)
		if requestErr != nil {
			outcome <- requestOutcome{err: requestErr}
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		outcome <- requestOutcome{status: response.StatusCode, body: body}
	}()

	// Give the client time to reach the handler so Shutdown has an in-flight
	// request to wait on. The handler then sleeps for bringUpDelay.
	time.Sleep(40 * time.Millisecond)

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := serverInstance.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	result := <-outcome
	if result.err != nil {
		t.Fatalf("in-flight request errored: %v", result.err)
	}
	if result.status != http.StatusOK {
		t.Errorf("in-flight request status = %d, want 200 (shutdown must wait for in-flight)", result.status)
	}

	if err := <-serveErr; err != nil {
		t.Errorf("Serve returned error after shutdown: %v", err)
	}
}

func TestNew_PanicsOnNilOrchestrator(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("New with nil orchestrator did not panic")
		}
	}()
	logger, _ := newCapturingLogger()
	_ = transport.New("127.0.0.1:0", nil, logger)
}

func TestNew_PanicsOnNilLogger(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("New with nil logger did not panic")
		}
	}()
	_ = transport.New("127.0.0.1:0", &spyOrchestrator{}, nil)
}

// errorString is a tiny error type used to feed deterministic error values
// into the spy orchestrator without pulling in fmt.
type errorString string

func (text errorString) Error() string { return string(text) }

// minimalBringUpJSON returns a JSON body with every required BringUpRequest
// field populated, so tests that only care about non-request behavior
// (orchestrator failure, graceful shutdown) can share one input.
func minimalBringUpJSON() string {
	var buffer bytes.Buffer
	buffer.WriteString(`{"exchange_id":"ex","session_type":"canonical","country":"US","instruments":[],`)
	buffer.WriteString(`"broker_id":"b","session_start":"2026-10-17T09:30:00Z","session_end":"2026-10-17T16:00:00Z"}`)
	return buffer.String()
}
