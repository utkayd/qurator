package qr

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

// pngEncoder has fixed settings so identical images encode to identical bytes
// (FR-004). image/png writes no timestamp or metadata chunks.
var pngEncoder = png.Encoder{CompressionLevel: png.DefaultCompression}

// layout maps module coordinates to pixels. Whenever the image is at least one pixel
// per module, every module gets the same whole number of pixels and the remainder is
// absorbed by the quiet zone (split evenly, so the symbol stays centred): decoders
// estimate the sampling grid from module size, and a mix of 5- and 6-pixel modules at
// version 40 was observed to defeat gozxing's format-information read. Only when the
// image is smaller than the module count does the scale become fractional.
type layout struct {
	origin float64 // pixel offset of module 0 (including the quiet zone)
	module float64 // pixels per module
}

func newLayout(sizePx, totalModules int) layout {
	if sizePx >= totalModules {
		m := sizePx / totalModules
		return layout{origin: float64((sizePx - m*totalModules) / 2), module: float64(m)}
	}
	return layout{origin: 0, module: float64(sizePx) / float64(totalModules)}
}

// edge is the pixel boundary of module index m.
func (l layout) edge(m int) int { return int(l.origin + float64(m)*l.module + 0.5) }

// at is the exact pixel position of module coordinate m (fractional).
func (l layout) at(m float64) float32 { return float32(l.origin + m*l.module) }

// bandRows is the height of one anti-aliasing band. The vector rasteriser keeps a
// float32 accumulation buffer the size of its canvas; banding keeps that bounded
// (~1 MB at 4096 px wide) instead of 64 MB for a full 4096² canvas.
const bandRows = 64

// renderPNG rasterises the prepared geometry. Plain rectangles (every module of the
// square shape, and finder patterns of every shape) are filled with pixel-snapped
// edges so they stay crisp; dots and rounded runs are anti-aliased through
// x/image/vector. The logo, if any, is composited last.
func renderPNG(ctx context.Context, p *Prepared) ([]byte, error) {
	size := p.opts.SizePx
	lay := newLayout(size, p.geo.Total)
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	fillRGBA(img, p.bg)
	fg := color.RGBA{R: p.fg.R, G: p.fg.G, B: p.fg.B, A: 0xFF}

	// Crisp rectangles.
	var curved []Run
	for i, r := range p.geo.Runs {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if r.Corners != 0 {
			curved = append(curved, r)
			continue
		}
		fillRect(img, lay.edge(r.X), lay.edge(r.Y), lay.edge(r.X+r.W), lay.edge(r.Y+1), fg)
	}

	// Anti-aliased shapes, in bands.
	if len(curved) > 0 || len(p.geo.Dots) > 0 {
		if err := rasterCurved(ctx, img, lay, curved, p.geo.Dots, fg); err != nil {
			return nil, err
		}
	}

	if p.logo != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		compositeLogo(img, lay, p.geo.Hole, p.logo.img)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Grow(size * size / 8)
	if err := pngEncoder.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// rasterCurved draws rounded runs and dots through the vector rasteriser, one
// horizontal band at a time.
func rasterCurved(ctx context.Context, img *image.RGBA, lay layout, runs []Run, dots []Dot, fg color.RGBA) error {
	size := img.Rect.Dx()
	src := image.NewUniform(fg)
	z := vector.NewRasterizer(size, bandRows)
	for y0 := 0; y0 < size; y0 += bandRows {
		if err := ctx.Err(); err != nil {
			return err
		}
		y1 := y0 + bandRows
		if y1 > size {
			y1 = size
		}
		z.Reset(size, y1-y0)
		any := false
		off := float32(y0)
		for _, r := range runs {
			top, bottom := lay.at(float64(r.Y)), lay.at(float64(r.Y+1))
			if bottom <= off || top >= float32(y1) {
				continue
			}
			any = true
			roundedRunPath(z, lay, r, off)
		}
		for _, d := range dots {
			top, bottom := lay.at(float64(d.Y)), lay.at(float64(d.Y+1))
			if bottom <= off || top >= float32(y1) {
				continue
			}
			any = true
			cx, cy := lay.at(float64(d.X)+0.5), lay.at(float64(d.Y)+0.5)-off
			circlePath(z, cx, cy, float32(dotRadius*lay.module))
		}
		if any {
			z.Draw(img, image.Rect(0, y0, size, y1), src, image.Point{})
		}
	}
	return nil
}

// kappa is the cubic Bézier control distance approximating a quarter circle.
const kappa = 0.5522847498

// arcTo appends a clockwise (screen coordinates) quarter-circle from the current pen
// position a to b around centre c.
func arcTo(z *vector.Rasterizer, ax, ay, bx, by, cx, cy float32) {
	// Travel tangent at a point P on a clockwise arc is (P-C) rotated +90°.
	tax, tay := -(ay - cy), ax-cx
	tbx, tby := -(by - cy), bx-cx
	z.CubeTo(ax+kappa*tax, ay+kappa*tay, bx-kappa*tbx, by-kappa*tby, bx, by)
}

// roundedRunPath emits a run with the rounded corners its flags request.
func roundedRunPath(z *vector.Rasterizer, lay layout, r Run, yOff float32) {
	x0, x1 := lay.at(float64(r.X)), lay.at(float64(r.X+r.W))
	y0, y1 := lay.at(float64(r.Y))-yOff, lay.at(float64(r.Y+1))-yOff
	rad := float32(roundRadius * lay.module)
	radius := func(flag uint8) float32 {
		if r.Corners&flag != 0 {
			return rad
		}
		return 0
	}
	tl, tr, br, bl := radius(cornerTL), radius(cornerTR), radius(cornerBR), radius(cornerBL)
	z.MoveTo(x0+tl, y0)
	z.LineTo(x1-tr, y0)
	if tr > 0 {
		arcTo(z, x1-tr, y0, x1, y0+tr, x1-tr, y0+tr)
	}
	z.LineTo(x1, y1-br)
	if br > 0 {
		arcTo(z, x1, y1-br, x1-br, y1, x1-br, y1-br)
	}
	z.LineTo(x0+bl, y1)
	if bl > 0 {
		arcTo(z, x0+bl, y1, x0, y1-bl, x0+bl, y1-bl)
	}
	z.LineTo(x0, y0+tl)
	if tl > 0 {
		arcTo(z, x0, y0+tl, x0+tl, y0, x0+tl, y0+tl)
	}
	z.ClosePath()
}

// circlePath emits a circle as four clockwise quarter arcs.
func circlePath(z *vector.Rasterizer, cx, cy, r float32) {
	z.MoveTo(cx, cy-r)
	arcTo(z, cx, cy-r, cx+r, cy, cx, cy)
	arcTo(z, cx+r, cy, cx, cy+r, cx, cy)
	arcTo(z, cx, cy+r, cx-r, cy, cx, cy)
	arcTo(z, cx-r, cy, cx, cy-r, cx, cy)
	z.ClosePath()
}

// compositeLogo scales the logo into the hole's inner square (the hole minus its
// 1-module pad) preserving aspect ratio, and draws it over the background.
func compositeLogo(img *image.RGBA, lay layout, hole image.Rectangle, logo image.Image) {
	inner := image.Rect(lay.edge(hole.Min.X+1), lay.edge(hole.Min.Y+1), lay.edge(hole.Max.X-1), lay.edge(hole.Max.Y-1))
	if inner.Dx() < 1 || inner.Dy() < 1 {
		return
	}
	dst := fitRect(inner, logo.Bounds())
	xdraw.CatmullRom.Scale(img, dst, logo, logo.Bounds(), draw.Over, nil)
}

// fillRect paints an axis-aligned pixel rectangle.
func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for py := y0; py < y1; py++ {
		row := img.Pix[py*img.Stride+x0*4 : py*img.Stride+x1*4]
		for i := 0; i < len(row); i += 4 {
			row[i], row[i+1], row[i+2], row[i+3] = c.R, c.G, c.B, 0xFF
		}
	}
}

// fillRGBA paints the whole image with c.
func fillRGBA(img *image.RGBA, c color.NRGBA) {
	w := img.Rect.Dx()
	row := img.Pix[:w*4]
	for i := 0; i < len(row); i += 4 {
		row[i], row[i+1], row[i+2], row[i+3] = c.R, c.G, c.B, 0xFF
	}
	for y := 1; y < img.Rect.Dy(); y++ {
		copy(img.Pix[y*img.Stride:y*img.Stride+w*4], row)
	}
}
