package qr

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/utkayd/qurator/internal/domain"
)

var allShapes = []ModuleShape{domain.ShapeSquare, domain.ShapeDot, domain.ShapeRounded}
var allLevels = []ECLevel{domain.ECLow, domain.ECMedium, domain.ECQuartile, domain.ECHigh}

// TestShapesDecode renders every shape × size × EC level to PNG and SVG and decodes
// both with the independent decoder (T079).
func TestShapesDecode(t *testing.T) {
	r := testRenderer()
	content := []byte("https://example.com/shape-test?ref=qurator")
	for _, shape := range allShapes {
		for _, size := range []int{160, 320, 640} {
			for _, ec := range allLevels {
				t.Run(fmt.Sprintf("%s/%d/%s", shape, size, ec), func(t *testing.T) {
					t.Parallel()
					o := baseOptions(content, ec, size)
					o.Shape = shape
					p, s := renderBoth(t, r, o)
					decodeBoth(t, p, s, size, content)
				})
			}
		}
	}
}

// TestShapesSVGElements pins the element kinds each shape emits, so a change in the
// vector output is a deliberate one.
func TestShapesSVGElements(t *testing.T) {
	r := testRenderer()
	content := []byte("shape elements")
	for _, shape := range allShapes {
		o := baseOptions(content, domain.ECMedium, 256)
		o.Shape = shape
		o.Format = FormatSVG
		res, err := r.Render(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		svg := string(res.Bytes)
		switch shape {
		case domain.ShapeDot:
			if !strings.Contains(svg, "<circle") {
				t.Errorf("dot: no <circle> element in svg")
			}
		case domain.ShapeRounded:
			// Rounded runs are paths with arcs; square finder patterns stay rects.
			if !strings.Contains(svg, "<path") || !strings.Contains(svg, " a") && !strings.Contains(svg, "A") {
				t.Errorf("rounded: expected a <path> with arc commands")
			}
		}
		// Finder patterns are always square: the top-left finder's outer ring starts at
		// module (margin, margin) and the top-left module must be a plain rect.
		if !strings.Contains(svg, "<rect") && shape != domain.ShapeSquare {
			t.Errorf("%s: finder patterns must remain square rects", shape)
		}
	}
}

// TestGeometryComputedOnce asserts PNG and SVG derive from one Geometry: a prepared
// render computes geometry exactly once and both formats consume it.
func TestGeometryComputedOnce(t *testing.T) {
	r := testRenderer()
	for _, shape := range allShapes {
		o := baseOptions([]byte("one geometry"), domain.ECQuartile, 300)
		o.Shape = shape
		before := geometryComputations.Load()
		p, err := r.Prepare(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.PNG(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := p.SVG(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := geometryComputations.Load() - before; got != 1 {
			t.Errorf("%s: geometry computed %d times for one PNG+SVG pair, want 1", shape, got)
		}
	}
}

// TestFinderPatternsSquare checks the geometry itself: every module of the three
// finder patterns is emitted as a square regardless of shape.
func TestFinderPatternsSquare(t *testing.T) {
	sym, err := Encode([]byte("finder"), domain.ECMedium)
	if err != nil {
		t.Fatal(err)
	}
	for _, shape := range allShapes {
		g := computeGeometry(sym, shape, 4, nil)
		n := sym.Size()
		finders := [][2]int{{0, 0}, {n - 7, 0}, {0, n - 7}}
		for _, f := range finders {
			for dy := 0; dy < 7; dy++ {
				for dx := 0; dx < 7; dx++ {
					x, y := f[0]+dx, f[1]+dy
					if !sym.Module(x, y) {
						continue
					}
					if !g.hasSquareAt(x+4, y+4) {
						t.Fatalf("%s: finder module (%d,%d) is not a square", shape, x, y)
					}
				}
			}
		}
	}
}
