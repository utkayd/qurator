package qr

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
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

// renderPNG rasterises square modules. The symbol plus quiet zone fills SizePx exactly;
// module edges snap to whole pixels so every module is crisp.
func renderPNG(ctx context.Context, sym *Symbol, opts Options, fg, bg color.NRGBA) ([]byte, error) {
	size := opts.SizePx
	total := sym.Size() + 2*opts.Margin
	lay := newLayout(size, total)
	edge := lay.edge

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	fillNRGBA(img, bg)
	n := sym.Size()
	for y := 0; y < n; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		py0, py1 := edge(opts.Margin+y), edge(opts.Margin+y+1)
		for x := 0; x < n; x++ {
			if !sym.Module(x, y) {
				continue
			}
			px0, px1 := edge(opts.Margin+x), edge(opts.Margin+x+1)
			for py := py0; py < py1; py++ {
				row := img.Pix[py*img.Stride+px0*4 : py*img.Stride+px1*4]
				for i := 0; i < len(row); i += 4 {
					row[i], row[i+1], row[i+2], row[i+3] = fg.R, fg.G, fg.B, 0xFF
				}
			}
		}
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

// fillNRGBA paints the whole image with c.
func fillNRGBA(img *image.NRGBA, c color.NRGBA) {
	w := img.Rect.Dx()
	row := img.Pix[:w*4]
	for i := 0; i < len(row); i += 4 {
		row[i], row[i+1], row[i+2], row[i+3] = c.R, c.G, c.B, 0xFF
	}
	for y := 1; y < img.Rect.Dy(); y++ {
		copy(img.Pix[y*img.Stride:y*img.Stride+w*4], row)
	}
}
