package orchestrator

import (
	"context"
	"fmt"

	"simulation-agents/agent"
	"simulation-agents/orchestrator/generator"
	"simulation-agents/session"
	"simulation-agents/store"
	"simulation-agents/transport"
)

// continueBringUp is the asynchronous tail of a bring-up. It picks
// up where the synchronous spine left off (account-open done, a
// Population of agents already carrying handles and session capital)
// and runs the four writer-side steps the design calls P2 through
// P5 before handing off to the per-exchange run goroutine:
//
//	P2 — persist the newly minted agents' durable rows and clear each
//	     agent's pending_mint WAL entry.
//	P3 — write the initial Redis snapshot per agent (DORMANT +
//	     trading_capital), existing and new alike.
//	P4 — on-demand only: enlist the newly minted agents into the
//	     per-country lease and pick one agent as the on-demand
//	     runtime tipper.
//	P5 — build the lifecycle channel, register the session params +
//	     scenario curve_eff under sessions:active, and spawn the
//	     per-exchange run goroutine (session.Runner.Run) as the
//	     final handoff.
//
// The tail is a strict sequence: any step's failure routes through
// FailBringUp (which this phase ships as a logger-only surface; the
// resilience layer wires abort-cleanup into it later), and no
// subsequent step runs. The runner is NOT spawned on failure, so a
// mid-tail abort does not leak a goroutine. A background ctx is
// handed to the spawned runner so its lifetime is independent of
// the caller's request ctx — the runner must survive the inbound
// http handler returning.
func (orchestrator *Orchestrator) continueBringUp(
	ctx context.Context,
	request transport.BringUpRequest,
	population Population,
) {
	orchestrator.logger.Info(
		"continueBringUp tail start",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
		"existing_count", len(population.Existing),
		"new_count", len(population.New),
	)

	// P2
	if err := orchestrator.persistNewAgents(ctx, population.New); err != nil {
		orchestrator.failureReporter.FailBringUp(request, err)
		return
	}

	// P3
	if err := orchestrator.writeInitialRuntimeSnapshots(ctx, request.ExchangeID, population); err != nil {
		orchestrator.failureReporter.FailBringUp(request, err)
		return
	}

	// P4 — on-demand only
	if request.SessionType == SessionTypeOnDemand {
		if err := orchestrator.enlistOnDemandAgents(ctx, request, population.New); err != nil {
			orchestrator.failureReporter.FailBringUp(request, err)
			return
		}
		if err := orchestrator.setOnDemandRuntimeTipper(ctx, request, population); err != nil {
			orchestrator.failureReporter.FailBringUp(request, err)
			return
		}
	}

	// P5 — resolve the scenario curve, register sessions:active,
	// and spawn the runner. The curve is resolved once here so both
	// the session-store record and the runner receive the same value
	// without a second provider call.
	curveEffective := orchestrator.resolveCurveEffective(request)
	lifecycleChannel := make(chan session.LifecycleDecision, orchestrator.config.LifecycleBuffer)
	if err := orchestrator.registerActiveSession(ctx, request, curveEffective); err != nil {
		orchestrator.failureReporter.FailBringUp(request, err)
		return
	}

	orchestrator.logger.Info(
		"continueBringUp handing off to session runner",
		"exchange_id", request.ExchangeID,
		"session_type", request.SessionType,
	)
	// The session gets its own cancellable context, detached from the
	// request ctx (the inbound handler may have long since returned,
	// but the session is only just going live) and independent of
	// every other session's context — WindDown cancels exactly this
	// one session without touching any other in-flight work.
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	// Registered BEFORE the spawn below: a WindDown racing in right
	// after the goroutine starts must always find a handle to cancel,
	// never an empty registry.
	orchestrator.registerHandle(request.ExchangeID, &sessionHandle{
		sessionType: request.SessionType,
		country:     request.Country,
		population:  population,
		cancel:      cancelSession,
		lifecycleCh: lifecycleChannel,
	})
	go func() {
		// This defer is the exit barrier WindDown's drain waits on —
		// it must fire only once Run has fully returned, so the
		// orchestrator never starts draining while the goroutine might
		// still be sending on lifecycleChannel.
		defer close(lifecycleChannel)
		orchestrator.sessionRunner.Run(sessionCtx, request, population, curveEffective, lifecycleChannel)
	}()
}

// persistNewAgents inserts each freshly minted agent's durable row
// and then clears its pending_mint WAL entry. The InsertAgent call
// is idempotent by primary key, so a retried persist for the same
// agent id is a no-op; the WAL.Clear is also tolerant of a missing
// entry. Clearing AFTER the durable row is written is the invariant
// that keeps the crash-recovery drain correct: a crash between
// InsertAgent and WAL.Clear leaves the WAL entry present, and the
// drain path re-runs the chain idempotently.
func (orchestrator *Orchestrator) persistNewAgents(
	ctx context.Context,
	newAgents []agent.Agent,
) error {
	for _, agentRecord := range newAgents {
		if err := orchestrator.agentStore.InsertAgent(ctx, agentRecord); err != nil {
			return fmt.Errorf("orchestrator: InsertAgent for %s: %w", agentRecord.ID, err)
		}
		if err := orchestrator.pendingMintWAL.Clear(ctx, agentRecord.ID); err != nil {
			return fmt.Errorf("orchestrator: WAL.Clear for %s: %w", agentRecord.ID, err)
		}
	}
	return nil
}

// writeInitialRuntimeSnapshots writes the thin DORMANT snapshot per
// agent — presence=DORMANT plus the drawn trading_capital. Written
// for every agent (existing + new), so the live tick loop finds a
// consistent runtime map regardless of whether an agent was revived
// or freshly minted. entry_time is deliberately NOT written here —
// the per-exchange runner stamps it in its own pre-open phase so a
// restart of the agent process does not re-draw and change the
// entry pattern.
func (orchestrator *Orchestrator) writeInitialRuntimeSnapshots(
	ctx context.Context,
	exchangeID string,
	population Population,
) error {
	for _, agentRecord := range population.All() {
		if err := orchestrator.runtimeStore.WriteInitialSnapshot(ctx, exchangeID, agentRecord); err != nil {
			return fmt.Errorf(
				"orchestrator: WriteInitialSnapshot for %s on %s: %w",
				agentRecord.ID, exchangeID, err,
			)
		}
	}
	return nil
}

// enlistOnDemandAgents marks each freshly minted on-demand agent as
// leased to this bring-up's exchange. The leased agents from Stage
// A's lease-select were already in the leased hash; this step adds
// the mint shortfall so a concurrent on-demand bring-up for the
// same country does not race onto one of our new ids.
func (orchestrator *Orchestrator) enlistOnDemandAgents(
	ctx context.Context,
	request transport.BringUpRequest,
	newAgents []agent.Agent,
) error {
	for _, agentRecord := range newAgents {
		if err := orchestrator.leaseAllocator.Enlist(
			ctx, request.Country, agentRecord.ID, request.ExchangeID,
		); err != nil {
			return fmt.Errorf(
				"orchestrator: lease.Enlist for %s on country %s: %w",
				agentRecord.ID, request.Country, err,
			)
		}
	}
	return nil
}

// setOnDemandRuntimeTipper picks exactly one agent in the population
// and sets its runtime is_tipper flag. Canonical sessions carry the
// tipper flag as a static Postgres trait on exactly one endogenous
// member and skip this step; on-demand sessions redraw traits every
// session, so the tipper is a runtime assignment per bring-up.
// The pick is deterministic under the orchestrator's RNG source
// (live = fresh entropy, tuning runs = fixed seed), so a tuning
// replay picks the same tipper on each run.
func (orchestrator *Orchestrator) setOnDemandRuntimeTipper(
	ctx context.Context,
	request transport.BringUpRequest,
	population Population,
) error {
	allAgents := population.All()
	if len(allAgents) == 0 {
		return fmt.Errorf("orchestrator: setOnDemandRuntimeTipper received an empty population")
	}
	pickRNG := orchestrator.rngSource.Fresh()
	pickedIndex := pickRNG.IntN(len(allAgents))
	pickedAgent := allAgents[pickedIndex]
	if err := orchestrator.runtimeStore.SetTipper(ctx, request.ExchangeID, pickedAgent.ID); err != nil {
		return fmt.Errorf(
			"orchestrator: SetTipper for %s on %s: %w",
			pickedAgent.ID, request.ExchangeID, err,
		)
	}
	orchestrator.logger.Info(
		"orchestrator: on-demand runtime tipper picked",
		"exchange_id", request.ExchangeID,
		"tipper_agent_id", pickedAgent.ID,
		"population_size", len(allAgents),
	)
	return nil
}

// resolveCurveEffective asks the scenario provider for the lifecycle
// curve override for this bring-up. Returns nil for canonical sessions
// and for on-demand sessions with no scenario — both map to baseline
// lifecycle curves in the scheduler.
func (orchestrator *Orchestrator) resolveCurveEffective(
	request transport.BringUpRequest,
) *generator.CurveEffective {
	if request.SessionType != SessionTypeOnDemand || request.Scenario == "" {
		return nil
	}
	_, curveEffective := orchestrator.scenarioProvider.Effective(request.Country, request.Scenario)
	return curveEffective
}

// registerActiveSession writes the per-exchange session params under
// the sessions:active HASH so a restarted process can rebuild the
// session goroutines without a new bring-up request. The caller
// resolves curveEffective before calling here so the same value is
// also threaded into the runner without a second provider call.
func (orchestrator *Orchestrator) registerActiveSession(
	ctx context.Context,
	request transport.BringUpRequest,
	curveEffective *generator.CurveEffective,
) error {
	var curveEffectiveSnapshot *store.CurveEffectiveSnapshot
	if curveEffective != nil {
		curveEffectiveSnapshot = &store.CurveEffectiveSnapshot{
			EntryRate:   curveEffective.EntryRate,
			StepOutRate: curveEffective.StepOutRate,
			DoneRate:    curveEffective.DoneRate,
			ReentryRate: curveEffective.ReentryRate,
		}
	}
	record := store.ActiveSessionRecord{
		Country:     request.Country,
		SessionType: request.SessionType,
		Instruments: request.Instruments,
		Start:       request.SessionStart,
		End:         request.SessionEnd,
		BrokerID:    request.BrokerID,
		CurveEff:    curveEffectiveSnapshot,
	}
	if err := orchestrator.activeSessionStore.Register(ctx, request.ExchangeID, record); err != nil {
		return fmt.Errorf("orchestrator: Register active session for %s: %w", request.ExchangeID, err)
	}
	return nil
}
