package qr

import (
	"image"
	"sync/atomic"

	"github.com/utkayd/qurator/internal/domain"
)

// dotRadius is the dot-shape radius in modules (0.9 module diameter).
const dotRadius = 0.45

// roundRadius is the rounded-shape corner radius in modules: half a module, so an
// isolated module is a circle and a run is a pill.
const roundRadius = 0.5

// Corner flags for a Run.
const (
	cornerTL uint8 = 1 << iota
	cornerTR
	cornerBR
	cornerBL
)

// Run is a horizontal run of W dark modules starting at module (X, Y), in geometry
// coordinates (the quiet zone included). Corners marks which of its four corners are
// rounded; zero means a plain rectangle.
type Run struct {
	X, Y, W int
	Corners uint8
}

// Dot is one dark module drawn as a circle centred on the module.
type Dot struct {
	X, Y int
}

// Geometry is the shape-resolved description of a symbol, computed once and consumed
// by both the PNG rasteriser and the SVG builder so the formats cannot drift apart
// (research.md §1). Coordinates are in modules and include the quiet zone.
type Geometry struct {
	Total int   // side length in modules, quiet zone included
	Runs  []Run // rectangles and rounded runs
	Dots  []Dot // circles (dot shape only)
	// Hole is the module rectangle cleared for a logo, or empty when there is none.
	// It includes the 1-module background-coloured pad.
	Hole image.Rectangle
}

// geometryComputations counts computeGeometry calls; the tests use it to prove one
// geometry serves both formats.
var geometryComputations atomic.Int64

// computeGeometry resolves sym into shapes. hole, when non-nil, is a module rectangle
// in symbol coordinates whose modules are treated as light. Finder patterns are always
// square regardless of shape: scanners locate the symbol by their 1:1:3:1:1 ratio and
// rounding them measurably hurts detection.
func computeGeometry(sym *Symbol, shape ModuleShape, margin int, hole *image.Rectangle) *Geometry {
	geometryComputations.Add(1)
	n := sym.Size()
	g := &Geometry{Total: n + 2*margin}
	if hole != nil {
		g.Hole = hole.Add(image.Pt(margin, margin))
	}
	dark := func(x, y int) bool {
		if hole != nil && image.Pt(x, y).In(*hole) {
			return false
		}
		return sym.Module(x, y)
	}
	inFinder := func(x, y int) bool {
		return (x < 7 && y < 7) || (x >= n-7 && y < 7) || (x < 7 && y >= n-7)
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; {
			if !dark(x, y) {
				x++
				continue
			}
			if shape == domain.ShapeDot && !inFinder(x, y) {
				g.Dots = append(g.Dots, Dot{X: x + margin, Y: y + margin})
				x++
				continue
			}
			// Extend the run; a finder run never crosses into data because the
			// separator around each finder is always light.
			w := 1
			for x+w < n && dark(x+w, y) && (shape == domain.ShapeSquare || inFinder(x+w, y) == inFinder(x, y)) {
				w++
			}
			run := Run{X: x + margin, Y: y + margin, W: w}
			if shape == domain.ShapeRounded && !inFinder(x, y) {
				if !dark(x, y-1) {
					run.Corners |= cornerTL
				}
				if !dark(x+w-1, y-1) {
					run.Corners |= cornerTR
				}
				if !dark(x+w-1, y+1) {
					run.Corners |= cornerBR
				}
				if !dark(x, y+1) {
					run.Corners |= cornerBL
				}
			}
			g.Runs = append(g.Runs, run)
			x += w
		}
	}
	return g
}

// hasSquareAt reports whether module (x, y) (geometry coordinates) is covered by a
// plain rectangular run.
func (g *Geometry) hasSquareAt(x, y int) bool {
	for _, r := range g.Runs {
		if r.Corners == 0 && r.Y == y && x >= r.X && x < r.X+r.W {
			return true
		}
	}
	return false
}
