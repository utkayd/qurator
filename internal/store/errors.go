package store

import "errors"

// Sentinel errors. Drivers translate their native errors into these and never let a
// backend error type escape (Constitution Principle II). Callers use errors.Is.
var (
	ErrNotFound   = errors.New("store: not found")
	ErrConflict   = errors.New("store: conflict")
	ErrAliasTaken = errors.New("store: alias taken")
	// ErrClientRefTaken: another code of the same user already carries this client_ref
	// (spec 003, FR-206). Drivers cannot say WHICH row of a CreateCodes batch collided,
	// so the codes service pre-checks every ref and treats this as a lost race.
	ErrClientRefTaken = errors.New("store: client_ref taken")
	ErrUnknownDriver  = errors.New("store: unknown driver")
)
