// Package decode wraps the INDEPENDENT decoder (makiuchi-d/gozxing) used to verify
// qurator's QR output. It is shared by the qrdecode CLI, the internal/qr tests and the
// contract suite so every verification path reads bytes the same way.
//
// Binary payloads are read from ResultMetadataType_BYTE_SEGMENTS, never GetText():
// GetText() re-interprets non-UTF-8 bytes and would let a binary round-trip pass on
// ASCII while silently failing on raw bytes (research.md §1).
//
// SVG output is rasterised with srwiley/oksvg + rasterx (pure Go) and then decoded, so
// vector output is verified as scannable rather than assumed.
package decode

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// Result is what the decoder recovered from an image.
type Result struct {
	// Bytes is the exact payload, reassembled from every byte segment in order.
	Bytes []byte
	// ECLevel is the error-correction level read from the symbol's format information:
	// "L", "M", "Q" or "H".
	ECLevel string
}

// PNG decodes a PNG-encoded QR image.
func PNG(data []byte) (*Result, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode png: %w", err)
	}
	return Image(img)
}

// SVG rasterises an SVG document to sidePx × sidePx and decodes it. sidePx <= 0 uses
// the document's own width/height.
func SVG(data []byte, sidePx int) (*Result, error) {
	img, err := RasterizeSVG(data, sidePx)
	if err != nil {
		return nil, err
	}
	return Image(img)
}

// RasterizeSVG renders an SVG document to an RGBA image with oksvg/rasterx.
func RasterizeSVG(data []byte, sidePx int) (*image.RGBA, error) {
	// IgnoreErrorMode: oksvg has no <image> support, and qurator embeds a logo as one.
	// Skipping it leaves the logo's background-coloured hole in place, which is exactly
	// what a scanner must cope with. Any malformed module geometry still fails loudly,
	// because the symbol then does not decode.
	icon, err := oksvg.ReadIconStream(bytes.NewReader(data), oksvg.IgnoreErrorMode)
	if err != nil {
		return nil, fmt.Errorf("parse svg: %w", err)
	}
	w, h := sidePx, sidePx
	if sidePx <= 0 {
		w, h = int(icon.ViewBox.W), int(icon.ViewBox.H)
	}
	if w <= 0 || h <= 0 {
		return nil, errors.New("svg has no usable dimensions")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// oksvg draws only what the document paints; qurator always paints a background
	// rect, so no pre-fill is needed and none is done — a missing background would show
	// up as a decode failure rather than be hidden.
	icon.SetTarget(0, 0, float64(w), float64(h))
	r := rasterx.NewDasher(w, h, rasterx.NewScannerGV(w, h, img, img.Bounds()))
	icon.Draw(r, 1.0)
	return img, nil
}

// Image decodes a QR symbol from any image.Image.
func Image(img image.Image) (*Result, error) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return nil, fmt.Errorf("binarize: %w", err)
	}
	hints := map[gozxing.DecodeHintType]interface{}{
		gozxing.DecodeHintType_TRY_HARDER: true,
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, hints)
	if err != nil {
		return nil, fmt.Errorf("gozxing: %w", err)
	}
	out := &Result{}
	meta := res.GetResultMetadata()
	if segs, ok := meta[gozxing.ResultMetadataType_BYTE_SEGMENTS].([][]byte); ok {
		for _, s := range segs {
			out.Bytes = append(out.Bytes, s...)
		}
	} else {
		// Non-byte-mode symbols (numeric/alphanumeric) carry no byte segments; the text
		// is then a faithful representation. qurator always encodes in byte mode, so this
		// branch only serves foreign symbols fed to the CLI.
		out.Bytes = []byte(res.GetText())
	}
	if ec, ok := meta[gozxing.ResultMetadataType_ERROR_CORRECTION_LEVEL].(string); ok {
		out.ECLevel = ec
	} else if ec, ok := meta[gozxing.ResultMetadataType_ERROR_CORRECTION_LEVEL].(fmt.Stringer); ok {
		out.ECLevel = ec.String()
	}
	return out, nil
}
