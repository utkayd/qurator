package qr

import (
	"context"
	"errors"
	"time"
)

// Bounds caps the resources a single render may consume (FR-029). Zero values mean
// "no bound" for MaxDuration; MaxPx and MaxPayload fall back to DefaultBounds.
type Bounds struct {
	MaxPx       int           // maximum output side length in pixels
	MaxDuration time.Duration // maximum wall-clock time for encode + render
	MaxPayload  int           // maximum payload bytes, further capped by Capacity(ec)
}

// DefaultBounds mirrors the configuration defaults (render.*).
var DefaultBounds = Bounds{MaxPx: 4096, MaxDuration: 2 * time.Second, MaxPayload: 2953}

func (b Bounds) normalised() Bounds {
	if b.MaxPx <= 0 {
		b.MaxPx = DefaultBounds.MaxPx
	}
	if b.MaxPayload <= 0 {
		b.MaxPayload = DefaultBounds.MaxPayload
	}
	return b
}

func (b Bounds) checkSize(px int) error {
	if px > b.MaxPx {
		return &DimensionsExceededError{Requested: px, Maximum: b.MaxPx}
	}
	return nil
}

// payloadLimit is the effective byte limit for a level: the configured maximum or the
// symbol capacity, whichever is smaller.
func (b Bounds) payloadLimit(ec ECLevel) int {
	limit := Capacity(ec)
	if b.MaxPayload < limit {
		limit = b.MaxPayload
	}
	return limit
}

func (b Bounds) checkPayload(n int, ec ECLevel) error {
	if limit := b.payloadLimit(ec); n > limit {
		return &ContentTooLargeError{Limit: limit, Actual: n, Level: ec}
	}
	return nil
}

// withBudget derives a context bounded by MaxDuration. The cancel func must be called.
func (b Bounds) withBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if b.MaxDuration <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, b.MaxDuration)
}

// budgetError translates a context failure into the render error the caller should
// see: a deadline is a RenderTimeoutError; a cancellation (the client went away) is
// passed through unchanged so the HTTP layer can stay silent.
func (b Bounds) budgetError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &RenderTimeoutError{Timeout: b.MaxDuration}
	}
	return err
}
