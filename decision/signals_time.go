package decision

import "simulation-agents/agent"

// perceiveTime reads the exchange phase and session clock for one instrument.
// No per-agent parameters — the time channel is globally shared.
func perceiveTime(acc *Accessor, inst string) TimeReading {
	if !acc.Present(inst) {
		return TimeReading{}
	}
	return TimeReading{
		Phase:           acc.Phase(inst),
		SessionProgress: acc.SessionProgress(),
		ReadOK:          true,
	}
}

// perceiveTimeDiet is the diet-gated entry point: returns a non-nil pointer
// only when the time-of-session channel is on.
func perceiveTimeDiet(diet *agent.Diet, acc *Accessor, inst string) *TimeReading {
	if diet == nil || !diet.TimeOfSession {
		return nil
	}
	reading := perceiveTime(acc, inst)
	return &reading
}
