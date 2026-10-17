package config

import (
	"errors"
	"fmt"
)

// ErrConfigLoad is the root category sentinel; all load failures wrap it.
// Callers dispatch with errors.Is(err, ErrConfigLoad) to distinguish config
// errors from other error types.
var ErrConfigLoad = errors.New("connectivity/config: load failed")

// Category sentinels — callers use errors.Is to identify the failure class.
var (
	ErrMissingVar  = fmt.Errorf("%w: missing required env var", ErrConfigLoad)
	ErrBadURL      = fmt.Errorf("%w: malformed URL", ErrConfigLoad)
	ErrBadDuration = fmt.Errorf("%w: malformed duration", ErrConfigLoad)
	ErrBadInt      = fmt.Errorf("%w: malformed integer", ErrConfigLoad)
)

// ErrMissingEnvVar names the specific var that was absent. Unwraps to ErrMissingVar.
type ErrMissingEnvVar struct {
	VarName string
}

func (err ErrMissingEnvVar) Error() string {
	return fmt.Sprintf("connectivity/config: required env var %q is not set", err.VarName)
}

func (err ErrMissingEnvVar) Unwrap() error { return ErrMissingVar }
