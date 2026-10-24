package session

import (
	"context"
	"math/rand/v2"
	"time"

	"simulation-agents/store"
)

// runPresencePhase is the 3b presence sweep. It runs Clock 1 (activate
// DORMANT agents whose entry threshold has been reached) and then Clock 2
// (per-agent lifecycle rolls for any ACTIVE or IDLE agent whose recurring
// check interval has elapsed). The action phase always runs before this
// call on the same goroutine, so all presence changes here take effect
// on future action ticks only — the ACTIVE subset is race-free.
func (sched *LiveScheduler) runPresencePhase(
	ctx context.Context,
	ws *workingSet,
	curves *lifecycleCurves,
	now time.Time,
	lifecycleChannel chan<- LifecycleDecision,
) {
	nowNorm := normalizedSessionTime(now, ws)
	sched.activateDormantAgents(ctx, ws, now, nowNorm)
	sched.runLifecycleChecks(ctx, ws, curves, now.Unix(), nowNorm, lifecycleChannel)
}

// activateDormantAgents scans every DORMANT agent and flips those whose
// normalized entry threshold has been reached to ACTIVE. The ceiling hook
// is a no-op in the prototype (ceiling = N, never prevents an activation).
// Both the in-memory ACTIVE subset and the Redis runtime snapshot receive
// the write; Redis is the write-on-change mirror for restart recovery.
func (sched *LiveScheduler) activateDormantAgents(
	ctx context.Context,
	ws *workingSet,
	now time.Time,
	nowNorm float64,
) {
	nowUnix := now.Unix()
	activatedCount := 0
	for agentID, agentRecord := range ws.agents {
		if agentRecord.presence != store.PresenceDormant {
			continue
		}
		if nowNorm < agentRecord.entryTime {
			continue
		}
		// Ceiling hook: no-op in prototype (ceiling = N, never blocks activation).
		agentRecord.presence = store.PresenceActive
		agentRecord.nextCheckTime = nowUnix + randCheckInterval(ws.sessionRNG, sched.params.CheckIntervalMin, sched.params.CheckIntervalMax)
		agentRecord.lastCheckTime = nowUnix
		agentRecord.idleSince = 0
		ws.active[agentID] = agentRecord
		_ = sched.runtime.SetPresence(ctx, ws.exchangeID, agentID, store.PresenceActive)
		_ = sched.runtime.SetTimers(ctx, ws.exchangeID, agentID, agentRecord.nextCheckTime, agentRecord.idleSince)
		activatedCount++
	}
	if activatedCount > 0 {
		sched.logger.Info("session: agents activated",
			"exchange_id", ws.exchangeID,
			"activated_count", activatedCount,
			"active_total", len(ws.active),
		)
	}
}

// runLifecycleChecks fires Clock 2: ACTIVE agent checks run first so
// that a freshly-stepped-out agent cannot re-enter in the same sweep.
// The lifecycleChannel parameter is the RETIRE routing seam (Tier 6);
// it is threaded through here but not yet written to.
func (sched *LiveScheduler) runLifecycleChecks(
	ctx context.Context,
	ws *workingSet,
	curves *lifecycleCurves,
	nowUnix int64,
	nowNorm float64,
	lifecycleChannel chan<- LifecycleDecision,
) {
	sched.checkActiveAgents(ctx, ws, curves, nowUnix, nowNorm)
	sched.checkIdleAgents(ctx, ws, curves, nowUnix, lifecycleChannel)
}

// checkActiveAgents runs the per-agent lifecycle roll for each ACTIVE
// agent whose check interval has elapsed. Done supersedes step-out: the
// done roll runs first so an agent near session close goes DONE rather
// than a temporary IDLE step-out. All trait factors and fatigue are 1/0
// in the prototype (f_i=1, c_fat=0), so rate × Δ is the raw per-check
// probability before clamping to [0,1].
func (sched *LiveScheduler) checkActiveAgents(
	ctx context.Context,
	ws *workingSet,
	curves *lifecycleCurves,
	nowUnix int64,
	nowNorm float64,
) {
	doneCount, stepOutCount := 0, 0
	for agentID, agentRecord := range ws.active {
		if nowUnix < agentRecord.nextCheckTime {
			continue
		}
		delta := float64(nowUnix - agentRecord.lastCheckTime)
		pDone := clamp01(curves.doneRate(nowNorm) * delta)
		pStepOut := clamp01(curves.stepOutRate(nowNorm) * delta)

		if ws.sessionRNG.Float64() < pDone {
			agentRecord.presence = store.PresenceDone
			delete(ws.active, agentID)
			_ = sched.runtime.SetPresence(ctx, ws.exchangeID, agentID, store.PresenceDone)
			doneCount++
			continue
		}
		if ws.sessionRNG.Float64() < pStepOut {
			agentRecord.presence = store.PresenceIdle
			agentRecord.idleSince = nowUnix
			agentRecord.nextCheckTime = nowUnix + randCheckInterval(ws.sessionRNG, sched.params.CheckIntervalMin, sched.params.CheckIntervalMax)
			agentRecord.lastCheckTime = nowUnix
			delete(ws.active, agentID)
			_ = sched.runtime.SetPresence(ctx, ws.exchangeID, agentID, store.PresenceIdle)
			_ = sched.runtime.SetTimers(ctx, ws.exchangeID, agentID, agentRecord.nextCheckTime, agentRecord.idleSince)
			stepOutCount++
			continue
		}
		// Stay ACTIVE: re-arm for next check interval.
		agentRecord.nextCheckTime = nowUnix + randCheckInterval(ws.sessionRNG, sched.params.CheckIntervalMin, sched.params.CheckIntervalMax)
		agentRecord.lastCheckTime = nowUnix
		_ = sched.runtime.SetTimers(ctx, ws.exchangeID, agentID, agentRecord.nextCheckTime, agentRecord.idleSince)
	}
	if doneCount > 0 || stepOutCount > 0 {
		sched.logger.Info("session: active agent checks",
			"exchange_id", ws.exchangeID,
			"done_count", doneCount,
			"step_out_count", stepOutCount,
			"active_total", len(ws.active),
		)
	}
}

// checkIdleAgents runs the per-agent lifecycle roll for each IDLE agent
// whose check interval has elapsed. Re-entry runs before give-up: the
// two mechanisms are complementary and re-entry takes priority. Neither
// roll uses a Δ factor — idle-duration probabilities are per-check
// values, not intensities that need an interval conversion.
// The lifecycleChannel is the RETIRE routing seam; writes to it
// (ACTIVE/IDLE → RETIRED at end-of-session) are Tier 6 and not built here.
func (sched *LiveScheduler) checkIdleAgents(
	ctx context.Context,
	ws *workingSet,
	curves *lifecycleCurves,
	nowUnix int64,
	lifecycleChannel chan<- LifecycleDecision,
) {
	_ = lifecycleChannel // RETIRE path is Tier 6; not built in this stage
	reentryCount, giveUpCount := 0, 0
	for agentID, agentRecord := range ws.agents {
		if agentRecord.presence != store.PresenceIdle {
			continue
		}
		if nowUnix < agentRecord.nextCheckTime {
			continue
		}
		idleDuration := float64(nowUnix - agentRecord.idleSince)
		pReentry := clamp01(curves.reentryProb(idleDuration))
		pGiveUp := clamp01(curves.giveUpProb(idleDuration))

		if ws.sessionRNG.Float64() < pReentry {
			agentRecord.presence = store.PresenceActive
			agentRecord.nextCheckTime = nowUnix + randCheckInterval(ws.sessionRNG, sched.params.CheckIntervalMin, sched.params.CheckIntervalMax)
			agentRecord.lastCheckTime = nowUnix
			agentRecord.idleSince = 0
			ws.active[agentID] = agentRecord
			_ = sched.runtime.SetPresence(ctx, ws.exchangeID, agentID, store.PresenceActive)
			_ = sched.runtime.SetTimers(ctx, ws.exchangeID, agentID, agentRecord.nextCheckTime, agentRecord.idleSince)
			reentryCount++
			continue
		}
		if ws.sessionRNG.Float64() < pGiveUp {
			agentRecord.presence = store.PresenceDone
			_ = sched.runtime.SetPresence(ctx, ws.exchangeID, agentID, store.PresenceDone)
			giveUpCount++
			continue
		}
		// Stay IDLE: re-arm for next check interval.
		agentRecord.nextCheckTime = nowUnix + randCheckInterval(ws.sessionRNG, sched.params.CheckIntervalMin, sched.params.CheckIntervalMax)
		agentRecord.lastCheckTime = nowUnix
		_ = sched.runtime.SetTimers(ctx, ws.exchangeID, agentID, agentRecord.nextCheckTime, agentRecord.idleSince)
	}
	if reentryCount > 0 || giveUpCount > 0 {
		sched.logger.Info("session: idle agent checks",
			"exchange_id", ws.exchangeID,
			"reentry_count", reentryCount,
			"give_up_count", giveUpCount,
			"active_total", len(ws.active),
		)
	}
}

// normalizedSessionTime returns the session's progress as a value in
// [0, 1] — 0 at session open, 1 at close. Times outside the session
// span are clamped so out-of-window calls don't produce unexpected values.
func normalizedSessionTime(now time.Time, ws *workingSet) float64 {
	total := ws.sessionEnd.Sub(ws.sessionStart).Seconds()
	if total <= 0 {
		return 0.0
	}
	elapsed := now.Sub(ws.sessionStart).Seconds()
	return clamp01(elapsed / total)
}

// randCheckInterval draws a check interval uniformly from [min, max]
// and returns it in seconds. Zero or negative bounds fall back to the
// 20–40 min range from the lifecycle design.
func randCheckInterval(rng *rand.Rand, minInterval, maxInterval time.Duration) int64 {
	if minInterval <= 0 {
		minInterval = 20 * time.Minute
	}
	if maxInterval <= minInterval {
		maxInterval = minInterval + 20*time.Minute
	}
	minSecs := int64(minInterval.Seconds())
	rangeSecs := int64(maxInterval.Seconds()) - minSecs
	return minSecs + int64(rng.IntN(int(rangeSecs)+1))
}

// clamp01 clamps a value to [0, 1]. Used to convert a rate × interval
// product into a valid per-check probability; the product can slightly
// exceed 1 at high rates near session close.
func clamp01(v float64) float64 {
	if v <= 0 {
		return 0.0
	}
	if v >= 1 {
		return 1.0
	}
	return v
}
