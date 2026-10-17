package transport

import (
	"encoding/json"
	"errors"
	"net/http"
)

// errorEnvelope is the response body shape used on any non-success status.
// A single free-text error key — the HTTP status code is the machine-readable
// signal and the error text is human-readable context for operators.
type errorEnvelope struct {
	Error string `json:"error"`
}

// windDownWireRequest is the JSON shape accepted by the wind-down route.
// It is unpacked into the AgentOrchestrator.WindDown call rather than carried
// through as a struct, so the orchestrator interface stays independent of
// the HTTP wire format.
type windDownWireRequest struct {
	ExchangeID   string `json:"exchange_id"`
	TeardownMode string `json:"teardown_mode"`
}

// handleHealthz is the liveness probe. The server is considered live as
// soon as this handler is wired, so a 200 here means the process is up and
// the inbound surface is serving. No body — the status code is the signal.
func (server *Server) handleHealthz(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusOK)
}

// handleBringUp decodes the inbound BringUpRequest, forwards it to the
// orchestrator, and writes the ack back. Decode failures surface as 400;
// orchestrator failures surface as 500. Boundary logging is handled by the
// orchestrator itself, so the happy path produces no additional log line.
func (server *Server) handleBringUp(writer http.ResponseWriter, httpRequest *http.Request) {
	var bringUpRequest BringUpRequest
	if err := decodeStrictJSON(httpRequest, &bringUpRequest); err != nil {
		server.writeError(writer, http.StatusBadRequest, "invalid bring-up request", err)
		return
	}
	ack, err := server.orchestrator.BringUp(httpRequest.Context(), bringUpRequest)
	if err != nil {
		server.writeError(writer, http.StatusInternalServerError, "bring-up failed", err)
		return
	}
	server.writeJSON(writer, http.StatusOK, ack)
}

// handleWindDown decodes the inbound wind-down payload, forwards the two
// fields to the orchestrator, and returns 204 on success. Decode failures
// surface as 400; orchestrator failures surface as 500.
func (server *Server) handleWindDown(writer http.ResponseWriter, httpRequest *http.Request) {
	var wire windDownWireRequest
	if err := decodeStrictJSON(httpRequest, &wire); err != nil {
		server.writeError(writer, http.StatusBadRequest, "invalid wind-down request", err)
		return
	}
	if err := server.orchestrator.WindDown(httpRequest.Context(), wire.ExchangeID, wire.TeardownMode); err != nil {
		server.writeError(writer, http.StatusInternalServerError, "wind-down failed", err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// decodeStrictJSON decodes the request body into target. It rejects unknown
// fields so client-side typos (e.g. "exchange_id" vs "exchangeid") surface
// as a 400 instead of being silently dropped, and it rejects trailing
// content so a doubled payload cannot be partially honored.
func decodeStrictJSON(httpRequest *http.Request, target any) error {
	if httpRequest.Body == nil {
		return errors.New("empty request body")
	}
	decoder := json.NewDecoder(httpRequest.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("unexpected trailing content after JSON body")
	}
	return nil
}

// writeJSON writes the given value as a JSON response body with the given
// status. If encoding fails after the status line has been written there is
// no way to recover the response, so the failure is logged and swallowed.
func (server *Server) writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(body); err != nil {
		server.logger.Warn("failed to encode response body", "error", err)
	}
}

// writeError writes an errorEnvelope body with the given status and logs
// the rejection at the HTTP layer. The message is the user-facing text
// placed in the envelope; the cause is only logged, never sent back —
// clients branch on HTTP status, not on the error text.
func (server *Server) writeError(writer http.ResponseWriter, status int, message string, cause error) {
	server.logger.Warn("http handler rejected request",
		"status", status,
		"message", message,
		"cause", cause.Error(),
	)
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(errorEnvelope{Error: message}); err != nil {
		server.logger.Warn("failed to encode error envelope", "error", err)
	}
}
