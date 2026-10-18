package orchestrator

import (
	"fmt"

	"simulation-agents/transport"
)

// validateTrigger checks the subset of the inbound bring-up request the
// orchestrator dispatches on. The three fields it validates are those
// the spine cannot start without:
//
//   - SessionType: must be one of the two known session types; otherwise
//     the spine does not know which generation path to run.
//   - Country: keys the generation distributions (and the lease pool for
//     on-demand).
//   - ExchangeID: tags the session's Redis/market-data namespace and the
//     write-ahead-log entries each mint stages.
//
// Every other request field — SessionStart / SessionEnd / Instruments /
// BrokerID / Scenario — is carried through unvalidated. Session times
// are consumed by the lifecycle writer and the pre-open join; instruments
// by the market-data subscriber; broker id is reserved for a multi-broker
// routing concern; scenario is honored only on on-demand sessions and
// resolved by the scenario provider (an empty scenario is baseline).
//
// Pure function: no logger side effects, no injected dependencies.
// Warnings about carried-through fields (for example a non-empty
// scenario on a canonical session) are the caller's responsibility and
// land in BringUp.
func validateTrigger(request transport.BringUpRequest) error {
	switch request.SessionType {
	case SessionTypeCanonical, SessionTypeOnDemand:
		// recognized
	case "":
		return fmt.Errorf("orchestrator: BringUpRequest.SessionType is required")
	default:
		return fmt.Errorf("orchestrator: BringUpRequest.SessionType %q is not one of {%q, %q}",
			request.SessionType, SessionTypeCanonical, SessionTypeOnDemand)
	}

	if request.Country == "" {
		return fmt.Errorf("orchestrator: BringUpRequest.Country is required")
	}
	if request.ExchangeID == "" {
		return fmt.Errorf("orchestrator: BringUpRequest.ExchangeID is required")
	}
	return nil
}
