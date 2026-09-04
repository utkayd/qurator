package qr

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"

	"github.com/utkayd/qurator/internal/domain"
)

// Logo is a centre overlay (FR-027). Scale is the fraction of the symbol's module area
// the overlay occupies, INCLUDING its 1-module background-coloured pad — the pad
// occludes modules too, so counting it keeps the budget honest.
type Logo struct {
	Image     []byte  // PNG or JPEG bytes
	Scale     float64 // (0, 0.25]
	AutoRaise bool    // raise the EC level when Scale exceeds the requested level's budget
}

// Logo limits.
const (
	MaxLogoBytes  = 2 << 20 // encoded size
	MaxLogoPixels = 2048    // per side, decoded
	MaxLogoScale  = 0.25
)

// logoBudget is the largest Scale each level can absorb (research.md §1: below the
// ISO recovery capacity to leave margin for print and camera loss).
var logoBudget = map[ECLevel]float64{
	domain.ECLow:      0.05,
	domain.ECMedium:   0.12,
	domain.ECQuartile: 0.20,
	domain.ECHigh:     0.25,
}

// LogoBudget returns the maximum logo scale for ec.
func LogoBudget(ec ECLevel) float64 { return logoBudget[ec] }

// resolveLogoLevel picks the effective EC level for a logo of scale at requested.
func resolveLogoLevel(requested ECLevel, scale float64, autoRaise bool) (ECLevel, error) {
	if scale <= logoBudget[requested] {
		return requested, nil
	}
	if !autoRaise {
		return "", &LogoTooLargeError{Scale: scale, MaxScale: logoBudget[requested], Level: requested}
	}
	for _, l := range levelOrder[levelRank(requested)+1:] {
		if scale <= logoBudget[l] {
			return l, nil
		}
	}
	return "", &LogoTooLargeError{Scale: scale, MaxScale: logoBudget[domain.ECHigh], Level: domain.ECHigh}
}

// decodedLogo is a validated, decoded logo ready for composition.
type decodedLogo struct {
	img  image.Image
	raw  []byte // original bytes, embedded verbatim in SVG
	mime string // image/png or image/jpeg
}

// decodeLogo validates and decodes PNG or JPEG bytes. Dimensions are checked from the
// header before any pixel is allocated, so a decompression bomb is rejected cheaply.
func decodeLogo(data []byte) (*decodedLogo, error) {
	if len(data) == 0 {
		return nil, &InvalidOptionError{Field: "logo", Reason: "image is empty"}
	}
	if len(data) > MaxLogoBytes {
		return nil, &InvalidOptionError{Field: "logo", Reason: fmt.Sprintf("image exceeds %d bytes", MaxLogoBytes)}
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, &InvalidOptionError{Field: "logo", Reason: "image is not a valid PNG or JPEG"}
	}
	var mime string
	switch format {
	case "png":
		mime = "image/png"
	case "jpeg":
		mime = "image/jpeg"
	default:
		return nil, &InvalidOptionError{Field: "logo", Reason: "image must be PNG or JPEG"}
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxLogoPixels || cfg.Height > MaxLogoPixels {
		return nil, &InvalidOptionError{Field: "logo", Reason: fmt.Sprintf("image must be at most %d×%d pixels", MaxLogoPixels, MaxLogoPixels)}
	}
	var img image.Image
	if mime == "image/png" {
		img, err = png.Decode(bytes.NewReader(data))
	} else {
		img, err = jpeg.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, &InvalidOptionError{Field: "logo", Reason: "image could not be decoded"}
	}
	return &decodedLogo{img: img, raw: data, mime: mime}, nil
}

// rawDataModules is the number of modules available to codewords (data plus error
// correction) in a symbol of version v: the total minus finder, separator, timing,
// alignment, format and version patterns (ISO/IEC 18004 Table 1).
func rawDataModules(v int) int {
	result := (16*v+128)*v + 64
	if v >= 2 {
		numAlign := v/7 + 2
		result -= (25*numAlign-10)*numAlign - 55
		if v >= 7 {
			result -= 36
		}
	}
	return result
}

// logoHole computes the module square (symbol coordinates, pad included) a logo of
// scale occupies on a symbol of n modules. Scale is a fraction of the codeword area,
// not the whole symbol: error correction recovers codewords, and function patterns
// take a sizeable share of small symbols. The side is odd, like n, so the square is
// centred on module boundaries; it is at least 3 so one module of image remains
// inside the pad.
func logoHole(n int, scale float64) image.Rectangle {
	side := int(math.Floor(math.Sqrt(scale * float64(rawDataModules((n-17)/4)))))
	if side%2 == 0 {
		side--
	}
	if side < 3 {
		side = 3
	}
	if side > n-16 { // never touch the finder patterns
		side = n - 16
		if side%2 == 0 {
			side--
		}
	}
	start := (n - side) / 2
	return image.Rect(start, start, start+side, start+side)
}

// fitRect returns the largest rectangle with src's aspect ratio that fits centred in
// box.
func fitRect(box image.Rectangle, src image.Rectangle) image.Rectangle {
	bw, bh := float64(box.Dx()), float64(box.Dy())
	sw, sh := float64(src.Dx()), float64(src.Dy())
	scale := math.Min(bw/sw, bh/sh)
	w, h := int(math.Round(sw*scale)), int(math.Round(sh*scale))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	x := box.Min.X + (box.Dx()-w)/2
	y := box.Min.Y + (box.Dy()-h)/2
	return image.Rect(x, y, x+w, y+h)
}
