package qr

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors. Callers classify with errors.Is; the typed errors below carry the
// structured detail the HTTP layer returns (contracts/errors.md).
var (
	ErrContentTooLarge    = errors.New("qr: content too large")
	ErrDimensionsExceeded = errors.New("qr: dimensions exceeded")
	ErrRenderTimeout      = errors.New("qr: render timeout")
	ErrInvalidOption      = errors.New("qr: invalid option")
)

// ContentTooLargeError reports a payload over the encodable maximum for a level.
type ContentTooLargeError struct {
	Limit  int
	Actual int
	Level  ECLevel
}

func (e *ContentTooLargeError) Error() string {
	return fmt.Sprintf("qr: content is %d bytes; the maximum at error correction level %s is %d bytes", e.Actual, e.Level, e.Limit)
}

// Unwrap makes errors.Is(err, ErrContentTooLarge) true.
func (e *ContentTooLargeError) Unwrap() error { return ErrContentTooLarge }

// DimensionsExceededError reports a requested output size above the configured bound.
type DimensionsExceededError struct {
	Requested int
	Maximum   int
}

func (e *DimensionsExceededError) Error() string {
	return fmt.Sprintf("qr: requested size %dpx exceeds the configured maximum of %dpx", e.Requested, e.Maximum)
}

// Unwrap makes errors.Is(err, ErrDimensionsExceeded) true.
func (e *DimensionsExceededError) Unwrap() error { return ErrDimensionsExceeded }

// RenderTimeoutError reports a render that exceeded its time budget.
type RenderTimeoutError struct {
	Timeout time.Duration
}

func (e *RenderTimeoutError) Error() string {
	return fmt.Sprintf("qr: rendering exceeded its %s budget", e.Timeout)
}

// Unwrap makes errors.Is(err, ErrRenderTimeout) true.
func (e *RenderTimeoutError) Unwrap() error { return ErrRenderTimeout }

// InvalidOptionError reports an Options field that is out of range or malformed.
type InvalidOptionError struct {
	Field  string
	Reason string
}

func (e *InvalidOptionError) Error() string {
	return fmt.Sprintf("qr: invalid %s: %s", e.Field, e.Reason)
}

// Unwrap makes errors.Is(err, ErrInvalidOption) true.
func (e *InvalidOptionError) Unwrap() error { return ErrInvalidOption }
