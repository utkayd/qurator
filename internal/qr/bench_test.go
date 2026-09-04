package qr

import (
	"bytes"
	"context"
	"testing"

	"github.com/utkayd/qurator/internal/domain"
)

// The CI benchmark gate (Principle III, bench.yml) runs these: a 200-byte payload at
// 512px, the ephemeral hot path.
func benchOptions(f Format) Options {
	return Options{
		Content: bytes.Repeat([]byte("q"), 200),
		Format:  f,
		Shape:   domain.ShapeSquare,
		Margin:  4,
		SizePx:  512,
		ECLevel: domain.ECMedium,
	}
}

func BenchmarkRenderPNG(b *testing.B) {
	r := NewRenderer(DefaultBounds)
	opts := benchOptions(FormatPNG)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := r.Render(context.Background(), opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderSVG(b *testing.B) {
	r := NewRenderer(DefaultBounds)
	opts := benchOptions(FormatSVG)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := r.Render(context.Background(), opts); err != nil {
			b.Fatal(err)
		}
	}
}
