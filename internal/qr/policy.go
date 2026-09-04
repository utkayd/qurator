package qr

import (
	"context"
	"errors"
	"image/color"
	"math"
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

// Contrast thresholds (research.md §1). The floor is not configurable: below 3:1 the
// binarisers scanners use cannot separate modules from background reliably.
const (
	ContrastFloor      = 3.0
	DefaultMinContrast = 4.5
)

// ContrastRatio returns the WCAG 2.x contrast ratio between two colours, in [1, 21].
// gozxing's binarisers threshold on luminance, so this measures what scanners see.
func ContrastRatio(a, b color.Color) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// relativeLuminance implements the sRGB → linear conversion from WCAG 2.x.
func relativeLuminance(c color.Color) float64 {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(n.R) + 0.7152*lin(n.G) + 0.0722*lin(n.B)
}

// checkContrast applies the gate.
func checkContrast(fg, bg color.Color, minimum float64) error {
	if ratio := ContrastRatio(fg, bg); ratio < minimum {
		return &ContrastTooLowError{Ratio: math.Round(ratio*100) / 100, Minimum: minimum}
	}
	return nil
}
