package session

import "time"

// Params carries the Runner's calibration and operational dials. These
// are composition-root concerns; this struct is the carrier, not a
// policy decision point. The composition root supplies a tuned value
// per field and the Runner reads them as given.
type Params struct {
	// EntryCurve shapes the per-agent entry-fraction draw inside
	// Run. See EntryCurveParams below for the per-field semantics.
	EntryCurve EntryCurveParams

	// HistoryLookback is the width of the candle window the
	// pre-open history pull fetches, measured backward from the
	// session's start wall-clock. Composition-root concern — a
	// tuning run with a longer lookback simply moves this dial.
	HistoryLookback time.Duration

	// HistoryTimeframes is the non-empty set of candle timeframes
	// the pre-open history pull populates in Redis. Each entry
	// must be one of the four Core-accepted timeframes: "1m",
	// "5m", "30m", or "60m". The history pull issues one paged
	// GetCandles sequence per (instrument × timeframe) and writes
	// the result under the matching candles:{tf} key, so an agent
	// reading a higher timeframe at tick time hits a pre-warmed
	// cache rather than a broker round-trip. Serving a timeframe
	// that this list does NOT include stays a broker-side concern
	// (L2 does no local rollup).
	//
	// The live WebSocket stream carries 1m finalized bars only;
	// the higher-timeframe caches stop being extended at
	// session_start. Agents that need the latest bar at a higher
	// timeframe either accept that staleness or ask the broker
	// directly.
	HistoryTimeframes []string

	// LoadSessionsConcurrency bounds the fan-out of the per-agent
	// session-install calls. The first-agent install runs
	// synchronously to produce the live-feed session id; the
	// remaining installs fan out up to this many at a time so a
	// large population does not open hundreds of concurrent sockets
	// to Core.
	LoadSessionsConcurrency int

	// OnDemandSessionDuration is the length the Runner passes to
	// the on-demand session install call for each agent. Core
	// clamps this to its own accepted window; the composition root
	// picks a value compatible with that window and with the
	// bring-up request's own start/end span.
	OnDemandSessionDuration time.Duration

	// AckReadTimeout bounds how long the Runner waits for Core's
	// per-sub ack frames after writing the subscribe batch. A
	// too-slow ack is a sign the live feed is in a bad state and
	// should fail the pre-open rather than block the join
	// indefinitely.
	AckReadTimeout time.Duration
}

// EntryCurveParams carries the dials for the inhomogeneous-Poisson
// entry curve. The baseline math is an exponential base rate
// multiplied by the exponential of a Gaussian-process wiggle — the
// base concentrates arrivals near the open, the Gaussian process
// adds per-session local peaks so no two sessions look identical.
type EntryCurveParams struct {
	// HeightA is the amplitude of the exponential base rate at the
	// session's open (normalized time t = 0). The per-agent entry
	// fraction under thinning is independent of this value, but it
	// is kept as an explicit dial so the curve equation reads as
	// written and so tuning the overall rate (if used elsewhere)
	// remains straightforward.
	HeightA float64

	// TauDecay sets how sharply the base rate decays through the
	// session. A smaller value concentrates arrivals near the
	// open; a larger value flattens them across the session.
	TauDecay float64

	// LengthScale is the RBF-kernel bandwidth of the Gaussian
	// process that wiggles the base rate. It controls the typical
	// width — and therefore the typical count — of session-specific
	// local peaks.
	LengthScale float64

	// VarianceSigmaSquared is the RBF-kernel amplitude of that
	// Gaussian process. It controls how tall the local peaks run
	// relative to the base trend.
	VarianceSigmaSquared float64

	// GridPoints is the resolution at which the Gaussian process
	// is sampled on the normalized interval before interpolation.
	// Higher values smooth the drawn wiggle at the cost of more
	// RNG consumption per draw.
	GridPoints int
}

// DefaultEntryCurveParams returns the baseline tuned values. The
// composition root may override these; keeping them as a function
// rather than exported vars avoids a shared mutable surface and
// makes the baseline obvious in a diff.
func DefaultEntryCurveParams() EntryCurveParams {
	return EntryCurveParams{
		HeightA:              1.0,
		TauDecay:             0.35,
		LengthScale:          0.15,
		VarianceSigmaSquared: 0.25,
		GridPoints:           64,
	}
}
