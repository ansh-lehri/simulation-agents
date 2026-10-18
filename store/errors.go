package store

import "errors"

// ErrNotImplementedThisPhase marks a store method whose execution body
// lives in a later phase (crash-recovery / teardown wiring). The method
// is still part of the interface so the rest of the system compiles
// against the contract today; callers that invoke it get a loud,
// grep-friendly error instead of a silent no-op.
var ErrNotImplementedThisPhase = errors.New("store: not implemented in this phase")
