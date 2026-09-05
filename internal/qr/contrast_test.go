package qr

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/utkayd/qurator/internal/domain"
)

func TestContrastRatioKnownPairs(t *testing.T) {
	// Ratios from the WCAG 2.x relative-luminance formula.
	cases := []struct {
		fg, bg string
		want   float64
	}{
		{"#000000", "#FFFFFF", 21.0},
		{"#FFFFFF", "#000000", 21.0},
		{"#FFFFFF", "#FFFFFF", 1.0},
		{"#FEFEFE", "#FFFFFF", 1.01},
		{"#777777", "#FFFFFF", 4.48},
		{"#FF0000", "#FFFFFF", 4.0},
		{"#0000FF", "#FFFFFF", 8.59},
		{"#101828", "#FFFFFF", 17.75},
	}
	for _, tc := range cases {
		fg, _ := ParseHexColor(tc.fg)
		bg, _ := ParseHexColor(tc.bg)
		got := ContrastRatio(fg, bg)
		if math.Abs(got-tc.want) > 0.01 {
			t.Errorf("ContrastRatio(%s, %s) = %.3f, want %.2f", tc.fg, tc.bg, got, tc.want)
		}
	}
}

func TestContrastGate(t *testing.T) {
	r := testRenderer() // default gate 4.5:1
	o := baseOptions([]byte("contrast"), domain.ECMedium, 256)
	o.FgColor, o.BgColor = "#FEFEFE", "#FFFFFF"
	_, err := r.Render(context.Background(), o)
	var ce *ContrastTooLowError
	if !errors.Is(err, ErrContrastTooLow) || !errors.As(err, &ce) {
		t.Fatalf("err = %v, want ContrastTooLowError", err)
	}
	if math.Abs(ce.Ratio-1.01) > 0.01 || ce.Minimum != 4.5 {
		t.Errorf("details = %+v, want ratio ≈1.01 minimum 4.5", ce)
	}

	o.FgColor = "#101828"
	if _, err := r.Render(context.Background(), o); err != nil {
		t.Errorf("#101828 on #FFFFFF must pass: %v", err)
	}

	// #777777 on white is 4.48:1 — under the default gate, above the hard floor.
	o.FgColor = "#777777"
	if _, err := r.Render(context.Background(), o); !errors.Is(err, ErrContrastTooLow) {
		t.Errorf("4.48:1 must fail the 4.5:1 default gate: %v", err)
	}
	lenient := NewRenderer(Bounds{MaxPx: 4096}, WithMinContrast(3))
	if _, err := lenient.Render(context.Background(), o); err != nil {
		t.Errorf("4.48:1 must pass a 3:1 gate: %v", err)
	}
	// The floor cannot be configured away.
	floor := NewRenderer(Bounds{MaxPx: 4096}, WithMinContrast(1))
	o.FgColor = "#AAAAAA" // 2.32:1
	_, err = floor.Render(context.Background(), o)
	if !errors.As(err, &ce) || ce.Minimum != ContrastFloor {
		t.Errorf("gate below the 3:1 floor must clamp to it: err = %v", err)
	}
}
