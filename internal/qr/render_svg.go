package qr

import (
	"context"
	"encoding/base64"
	"image/color"
	"strconv"
	"strings"
)

// renderSVG emits the prepared geometry in module units. The viewBox is in modules so
// every coordinate is an integer or a half — no float formatting drift, no ids, no
// timestamps: the same input always yields the same bytes (FR-004).
//
// Plain runs (all of the square shape, finder patterns of every shape) go into one
// <path>; dots are <circle> elements; rounded runs are <path> elements with arcs; a
// logo is an <image> with a data URI carrying the caller's bytes verbatim.
func renderSVG(ctx context.Context, p *Prepared) ([]byte, error) {
	g := p.geo
	var b strings.Builder
	b.Grow(len(g.Runs)*16 + len(g.Dots)*40 + 256)
	writeSVGHead(&b, p.opts.SizePx, g.Total, p.bg)
	fg := hexString(p.fg)

	var plain, rounded []Run
	for _, r := range g.Runs {
		if r.Corners == 0 {
			plain = append(plain, r)
		} else {
			rounded = append(rounded, r)
		}
	}
	if len(plain) > 0 {
		b.WriteString(`<path fill="`)
		b.WriteString(fg)
		b.WriteString(`" d="`)
		for i, r := range plain {
			if i%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			// M x y h w v 1 h -w z
			b.WriteByte('M')
			b.WriteString(strconv.Itoa(r.X))
			b.WriteByte(' ')
			b.WriteString(strconv.Itoa(r.Y))
			b.WriteByte('h')
			b.WriteString(strconv.Itoa(r.W))
			b.WriteString("v1h-")
			b.WriteString(strconv.Itoa(r.W))
			b.WriteByte('z')
		}
		b.WriteString(`"/>`)
	}
	if len(rounded) > 0 {
		b.WriteString(`<path fill="`)
		b.WriteString(fg)
		b.WriteString(`" d="`)
		for i, r := range rounded {
			if i%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			writeRoundedRun(&b, r)
		}
		b.WriteString(`"/>`)
	}
	if len(g.Dots) > 0 {
		b.WriteString(`<g fill="`)
		b.WriteString(fg)
		b.WriteString(`">`)
		for i, d := range g.Dots {
			if i%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			b.WriteString(`<circle cx="`)
			b.WriteString(half(d.X))
			b.WriteString(`" cy="`)
			b.WriteString(half(d.Y))
			b.WriteString(`" r="0.45"/>`)
		}
		b.WriteString(`</g>`)
	}
	if p.logo != nil {
		h := g.Hole
		b.WriteString(`<image x="`)
		b.WriteString(strconv.Itoa(h.Min.X + 1))
		b.WriteString(`" y="`)
		b.WriteString(strconv.Itoa(h.Min.Y + 1))
		b.WriteString(`" width="`)
		b.WriteString(strconv.Itoa(h.Dx() - 2))
		b.WriteString(`" height="`)
		b.WriteString(strconv.Itoa(h.Dy() - 2))
		b.WriteString(`" preserveAspectRatio="xMidYMid meet" href="data:`)
		b.WriteString(p.logo.mime)
		b.WriteString(`;base64,`)
		b.WriteString(base64.StdEncoding.EncodeToString(p.logo.raw))
		b.WriteString(`"/>`)
	}
	b.WriteString("</svg>\n")
	return []byte(b.String()), nil
}

// half formats n + 0.5.
func half(n int) string { return strconv.Itoa(n) + ".5" }

// writeRoundedRun emits one run as a closed path with circular arcs (radius 0.5) on
// the corners its flags request. It walks the outline clockwise from the top-left.
func writeRoundedRun(b *strings.Builder, r Run) {
	x0, x1 := float64(r.X), float64(r.X+r.W)
	y0, y1 := float64(r.Y), float64(r.Y+1)
	rad := func(flag uint8) float64 {
		if r.Corners&flag != 0 {
			return roundRadius
		}
		return 0
	}
	tl, tr, br, bl := rad(cornerTL), rad(cornerTR), rad(cornerBR), rad(cornerBL)
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	arc := func(x, y float64) {
		b.WriteString("A0.5 0.5 0 0 1 ")
		b.WriteString(f(x))
		b.WriteByte(' ')
		b.WriteString(f(y))
	}
	b.WriteByte('M')
	b.WriteString(f(x0 + tl))
	b.WriteByte(' ')
	b.WriteString(f(y0))
	b.WriteByte('L')
	b.WriteString(f(x1 - tr))
	b.WriteByte(' ')
	b.WriteString(f(y0))
	if tr > 0 {
		arc(x1, y0+tr)
	}
	b.WriteByte('L')
	b.WriteString(f(x1))
	b.WriteByte(' ')
	b.WriteString(f(y1 - br))
	if br > 0 {
		arc(x1-br, y1)
	}
	b.WriteByte('L')
	b.WriteString(f(x0 + bl))
	b.WriteByte(' ')
	b.WriteString(f(y1))
	if bl > 0 {
		arc(x0, y1-bl)
	}
	b.WriteByte('L')
	b.WriteString(f(x0))
	b.WriteByte(' ')
	b.WriteString(f(y0 + tl))
	if tl > 0 {
		arc(x0+tl, y0)
	}
	b.WriteByte('z')
}

// writeSVGHead writes the root element and the background rectangle.
func writeSVGHead(b *strings.Builder, sizePx, total int, bg color.NRGBA) {
	px := strconv.Itoa(sizePx)
	t := strconv.Itoa(total)
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="`)
	b.WriteString(px)
	b.WriteString(`" height="`)
	b.WriteString(px)
	b.WriteString(`" viewBox="0 0 `)
	b.WriteString(t)
	b.WriteByte(' ')
	b.WriteString(t)
	b.WriteString(`">`)
	b.WriteString(`<rect width="`)
	b.WriteString(t)
	b.WriteString(`" height="`)
	b.WriteString(t)
	b.WriteString(`" fill="`)
	b.WriteString(hexString(bg))
	b.WriteString(`"/>`)
}
