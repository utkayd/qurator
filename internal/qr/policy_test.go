package qr

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/domain"
)

func TestDimensionsExceeded(t *testing.T) {
	r := NewRenderer(Bounds{MaxPx: 256})
	_, err := r.Render(context.Background(), baseOptions([]byte("x"), domain.ECMedium, 257))
	var de *DimensionsExceededError
	if !errors.Is(err, ErrDimensionsExceeded) || !errors.As(err, &de) {
		t.Fatalf("err = %v, want DimensionsExceededError", err)
	}
	if de.Requested != 257 || de.Maximum != 256 {
		t.Errorf("details = %+v", de)
	}
	if _, err := r.Render(context.Background(), baseOptions([]byte("x"), domain.ECMedium, 256)); err != nil {
		t.Errorf("size at the bound must render: %v", err)
	}
}

func TestConfiguredPayloadCap(t *testing.T) {
	r := NewRenderer(Bounds{MaxPx: 512, MaxPayload: 10})
	_, err := r.Render(context.Background(), baseOptions([]byte("0123456789A"), domain.ECLow, 128))
	var ctl *ContentTooLargeError
	if !errors.As(err, &ctl) {
		t.Fatalf("err = %v, want ContentTooLargeError", err)
	}
	if ctl.Limit != 10 || ctl.Actual != 11 {
		t.Errorf("details = %+v, want limit 10 actual 11", ctl)
	}
}

func TestRenderTimeout(t *testing.T) {
	r := NewRenderer(Bounds{MaxPx: 4096, MaxDuration: time.Nanosecond})
	_, err := r.Render(context.Background(), baseOptions([]byte("slow"), domain.ECMedium, 2048))
	var te *RenderTimeoutError
	if !errors.Is(err, ErrRenderTimeout) || !errors.As(err, &te) {
		t.Fatalf("err = %v, want RenderTimeoutError", err)
	}
	if te.Timeout != time.Nanosecond {
		t.Errorf("Timeout = %s", te.Timeout)
	}
}

func TestParentCancellationPassesThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewRenderer(DefaultBounds)
	_, err := r.Render(ctx, baseOptions([]byte("x"), domain.ECMedium, 128))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrRenderTimeout) {
		t.Error("a cancelled client must not be reported as a render timeout")
	}
}

func TestInvalidOptions(t *testing.T) {
	r := NewRenderer(DefaultBounds)
	cases := map[string]Options{
		"format":  {Content: []byte("x"), Format: "gif"},
		"fg":      {Content: []byte("x"), FgColor: "red"},
		"bg":      {Content: []byte("x"), BgColor: "#12345"},
		"shape":   {Content: []byte("x"), Shape: "hex"},
		"margin":  {Content: []byte("x"), Margin: 65},
		"size":    {Content: []byte("x"), SizePx: -1},
		"ec":      {Content: []byte("x"), ECLevel: "Z"},
		"content": {Content: nil},
	}
	for name, o := range cases {
		if _, err := r.Render(context.Background(), o); !errors.Is(err, ErrInvalidOption) {
			t.Errorf("%s: err = %v, want ErrInvalidOption", name, err)
		}
	}
}
