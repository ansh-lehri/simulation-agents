package session

import (
	"context"
	"time"
)

// RealClock is the production Clock implementation. It consults the
// host wall-clock and sleeps against a time.Timer for WaitUntil. The
// composition root wires one shared RealClock across the whole
// process; tests inject a fake clock instead so they never sleep.
type RealClock struct{}

// NewRealClock returns the zero-value RealClock. Kept as a
// constructor rather than exporting the struct literal so a future
// extension (configured monotonic clock, test seam, etc.) does not
// break callers.
func NewRealClock() RealClock {
	return RealClock{}
}

// Now returns the current wall-clock time via the standard library.
func (RealClock) Now() time.Time {
	return time.Now()
}

// WaitUntil blocks until the deadline is reached or ctx is cancelled.
// A deadline that is already in the past returns nil immediately —
// the caller's intent "do not proceed before this instant" is already
// satisfied, and sleeping for a negative duration would panic the
// underlying timer.
//
// On ctx cancellation WaitUntil returns ctx.Err() so the caller can
// distinguish a cancelled wait from a reached deadline. The timer is
// stopped on both exit paths to release its goroutine promptly.
func (RealClock) WaitUntil(ctx context.Context, deadline time.Time) error {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
