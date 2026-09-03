package store

import "errors"

// Sentinel errors. Drivers translate their native errors into these and never let a
// backend error type escape (Constitution Principle II). Callers use errors.Is.
var (
	ErrNotFound      = errors.New("store: not found")
	ErrConflict      = errors.New("store: conflict")
	ErrAliasTaken    = errors.New("store: alias taken")
	ErrUnknownDriver = errors.New("store: unknown driver")
)
