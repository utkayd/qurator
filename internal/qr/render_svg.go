package qr

import (
	"context"
	"image/color"
	"strconv"
	"strings"
)

// renderSVG emits square modules as one <path> in module units. The viewBox is in
// modules so every coordinate is a small integer: no float formatting, no ids, no
// timestamps — the same input always yields the same bytes (FR-004).
func renderSVG(ctx context.Context, sym *Symbol, opts Options, fg, bg color.NRGBA) ([]byte, error) {
	n := sym.Size()
	total := n + 2*opts.Margin
	var b strings.Builder
	b.Grow(n * n / 2)
	writeSVGHead(&b, opts.SizePx, total, bg)
	b.WriteString(`<path fill="`)
	b.WriteString(hexString(fg))
	b.WriteString(`" d="`)
	for y := 0; y < n; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < n; {
			if !sym.Module(x, y) {
				x++
				continue
			}
			run := 1
			for x+run < n && sym.Module(x+run, y) {
				run++
			}
			// M x y h run v 1 h -run z
			b.WriteByte('M')
			b.WriteString(strconv.Itoa(x + opts.Margin))
			b.WriteByte(' ')
			b.WriteString(strconv.Itoa(y + opts.Margin))
			b.WriteByte('h')
			b.WriteString(strconv.Itoa(run))
			b.WriteString("v1h-")
			b.WriteString(strconv.Itoa(run))
			b.WriteByte('z')
			x += run
		}
	}
	b.WriteString(`"/></svg>` + "\n")
	return []byte(b.String()), nil
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
	b.WriteString(`" shape-rendering="crispEdges">`)
	b.WriteString(`<rect width="`)
	b.WriteString(t)
	b.WriteString(`" height="`)
	b.WriteString(t)
	b.WriteString(`" fill="`)
	b.WriteString(hexString(bg))
	b.WriteString(`"/>`)
}
