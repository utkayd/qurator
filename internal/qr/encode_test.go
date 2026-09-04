package qr

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/tools/qrdecode/decode"
)

// testRenderer is a renderer with generous bounds, so the round-trip tests exercise the
// encoder rather than the policy layer.
func testRenderer() *Renderer {
	return NewRenderer(Bounds{MaxPx: 4096, MaxDuration: 0, MaxPayload: 2953})
}

func baseOptions(content []byte, ec ECLevel, size int) Options {
	return Options{
		Content: content,
		FgColor: "#000000",
		BgColor: "#FFFFFF",
		Shape:   domain.ShapeSquare,
		Margin:  4,
		SizePx:  size,
		ECLevel: ec,
	}
}

// renderBoth renders PNG and SVG for opts and returns both byte slices.
func renderBoth(t *testing.T, r *Renderer, opts Options) (pngBytes, svgBytes []byte) {
	t.Helper()
	opts.Format = FormatPNG
	p, err := r.Render(context.Background(), opts)
	if err != nil {
		t.Fatalf("render png: %v", err)
	}
	if p.ContentType != "image/png" {
		t.Fatalf("png content type = %q", p.ContentType)
	}
	opts.Format = FormatSVG
	s, err := r.Render(context.Background(), opts)
	if err != nil {
		t.Fatalf("render svg: %v", err)
	}
	if s.ContentType != "image/svg+xml" {
		t.Fatalf("svg content type = %q", s.ContentType)
	}
	return p.Bytes, s.Bytes
}

// decodeBoth decodes a PNG and an SVG (rasterised at sidePx) with the independent
// decoder and asserts both carry exactly want.
func decodeBoth(t *testing.T, pngBytes, svgBytes []byte, sidePx int, want []byte) (pngRes, svgRes *decode.Result) {
	t.Helper()
	pr, err := decode.PNG(pngBytes)
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	if !bytes.Equal(pr.Bytes, want) {
		t.Fatalf("png round-trip mismatch:\n got %q\nwant %q", pr.Bytes, want)
	}
	sr, err := decode.SVG(svgBytes, sidePx)
	if err != nil {
		t.Fatalf("decode svg: %v", err)
	}
	if !bytes.Equal(sr.Bytes, want) {
		t.Fatalf("svg round-trip mismatch:\n got %q\nwant %q", sr.Bytes, want)
	}
	return pr, sr
}

func TestRoundTrip(t *testing.T) {
	r := testRenderer()
	// svgPx is the side at which the vector output is rasterised for decoding. The two
	// version-40 symbols (177 modules + 8 quiet) use 1480 = 185 × 8 so the anti-aliased
	// rasterisation lands on whole pixels per module; gozxing misreads the format
	// information of a version-40 symbol sampled at ~5.5 px/module. PNG output snaps
	// modules to whole pixels itself (see layout in render_png.go) and decodes at 1024.
	cases := []struct {
		name    string
		content []byte
		ec      ECLevel
		size    int
		svgPx   int
	}{
		{"ascii", []byte("https://example.com/hello?x=1&y=2"), domain.ECMedium, 512, 512},
		{"emoji", []byte("🚀 hello, 世界 ✨"), domain.ECMedium, 512, 512},
		{"rtl_arabic", []byte("مرحبا بالعالم — qurator"), domain.ECMedium, 512, 512},
		{"max_payload_L_2953", bytes.Repeat([]byte("q"), 2953), domain.ECLow, 1024, 1480},
		{"max_payload_H_1273", bytes.Repeat([]byte("h"), 1273), domain.ECHigh, 1024, 1480},
		{"raw_bytes", []byte{0x00, 0xFF, 0x80}, domain.ECMedium, 512, 512},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, s := renderBoth(t, r, baseOptions(tc.content, tc.ec, tc.size))
			decodeBoth(t, p, s, tc.svgPx, tc.content)
		})
	}
}

func TestDeterministic(t *testing.T) {
	r := testRenderer()
	opts := baseOptions([]byte("determinism matters for caching"), domain.ECQuartile, 400)
	p1, s1 := renderBoth(t, r, opts)
	p2, s2 := renderBoth(t, r, opts)
	if !bytes.Equal(p1, p2) {
		t.Error("two PNG renders of identical input differ")
	}
	if !bytes.Equal(s1, s2) {
		t.Error("two SVG renders of identical input differ")
	}
	if strings.Contains(string(s1), "id=") {
		t.Error("svg contains an id attribute; ids risk non-determinism and are unnecessary")
	}
}

func TestOverCapacity(t *testing.T) {
	cases := []struct {
		ec    ECLevel
		limit int
	}{
		{domain.ECLow, 2953},
		{domain.ECMedium, 2331},
		{domain.ECQuartile, 1663},
		{domain.ECHigh, 1273},
	}
	for _, tc := range cases {
		t.Run(string(tc.ec), func(t *testing.T) {
			if got := Capacity(tc.ec); got != tc.limit {
				t.Fatalf("Capacity(%s) = %d, want %d", tc.ec, got, tc.limit)
			}
			// Exactly at the limit encodes.
			if _, err := Encode(bytes.Repeat([]byte("x"), tc.limit), tc.ec); err != nil {
				t.Fatalf("payload of %d bytes at %s should fit: %v", tc.limit, tc.ec, err)
			}
			// One byte over does not, and the error names the limit.
			_, err := Encode(bytes.Repeat([]byte("x"), tc.limit+1), tc.ec)
			if !errors.Is(err, ErrContentTooLarge) {
				t.Fatalf("payload of %d bytes at %s: err = %v, want ErrContentTooLarge", tc.limit+1, tc.ec, err)
			}
			var ctl *ContentTooLargeError
			if !errors.As(err, &ctl) {
				t.Fatalf("error %T does not carry ContentTooLargeError", err)
			}
			if ctl.Limit != tc.limit || ctl.Actual != tc.limit+1 || ctl.Level != tc.ec {
				t.Errorf("ContentTooLargeError = %+v, want limit=%d actual=%d level=%s", ctl, tc.limit, tc.limit+1, tc.ec)
			}
		})
	}
}

// TestECLevelHonoured guards the boostEcl trap (research.md §1): piglig's convenience
// encoders silently promote EC-L to a higher level when the data would fit. A request
// for L must yield a symbol whose format information says L.
func TestECLevelHonoured(t *testing.T) {
	r := testRenderer()
	for _, ec := range []ECLevel{domain.ECLow, domain.ECMedium, domain.ECQuartile, domain.ECHigh} {
		t.Run(string(ec), func(t *testing.T) {
			// Short content: every level fits in version 1, which is precisely the case
			// where boostEcl would promote L → H.
			content := []byte("hi")
			sym, err := Encode(content, ec)
			if err != nil {
				t.Fatal(err)
			}
			if sym.ECLevel() != ec {
				t.Errorf("Symbol.ECLevel() = %s, want %s", sym.ECLevel(), ec)
			}
			p, s := renderBoth(t, r, baseOptions(content, ec, 256))
			pr, sr := decodeBoth(t, p, s, 256, content)
			if pr.ECLevel != string(ec) {
				t.Errorf("png: decoder reports EC %q, want %q", pr.ECLevel, ec)
			}
			if sr.ECLevel != string(ec) {
				t.Errorf("svg: decoder reports EC %q, want %q", sr.ECLevel, ec)
			}
		})
	}
}
