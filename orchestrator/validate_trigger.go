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

// validateScenarioName rejects any non-empty scenario name that was
// not registered at startup. The empty name is always accepted (it
// signals a baseline on-demand bring-up); any other name must be in
// validScenarios or the bring-up fails at intake.
//
// This is the one place a bad scenario name is surfaced before the
// spine runs. The silent fallback-to-baseline behavior is deliberately
// absent — an unknown name is an operator mistake and must be visible,
// not quietly ignored.
func validateScenarioName(scenarioName string, validScenarios map[string]struct{}) error {
	if scenarioName == "" {
		return nil
	}
	if _, present := validScenarios[scenarioName]; !present {
		return fmt.Errorf(
			"orchestrator: unknown scenario %q — not registered at startup",
			scenarioName,
		)
	}
	return nil
}
