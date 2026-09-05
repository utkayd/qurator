package qr

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/utkayd/qurator/internal/domain"
)

// solidLogo is a 64×64 opaque PNG in one colour, distinct from fg and bg.
func solidLogo(t *testing.T, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 0xFF
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var logoColor = color.NRGBA{R: 0xE0, G: 0x40, B: 0x20, A: 0xFF}

func logoOptions(t *testing.T, ec ECLevel, scale float64, autoRaise bool) Options {
	t.Helper()
	o := baseOptions([]byte("https://example.com/with-a-logo/and/some/path"), ec, 512)
	o.Logo = &Logo{Image: solidLogo(t, logoColor), Scale: scale, AutoRaise: autoRaise}
	return o
}

func TestLogoWithinBudgetDecodes(t *testing.T) {
	r := testRenderer()
	o := logoOptions(t, domain.ECLow, 0.04, false)
	p, s := renderBoth(t, r, o)
	decodeBoth(t, p, s, 512, o.Content)
	res, err := r.Render(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.ECLevelEffective != domain.ECLow || res.ECLevel != domain.ECLow {
		t.Errorf("levels = %s/%s, want L/L", res.ECLevel, res.ECLevelEffective)
	}
}

func TestLogoOverBudgetRejected(t *testing.T) {
	r := testRenderer()
	_, err := r.Render(context.Background(), logoOptions(t, domain.ECLow, 0.06, false))
	var le *LogoTooLargeError
	if !errors.Is(err, ErrLogoTooLarge) || !errors.As(err, &le) {
		t.Fatalf("err = %v, want LogoTooLargeError", err)
	}
	if le.Scale != 0.06 || le.MaxScale != 0.05 || le.Level != domain.ECLow {
		t.Errorf("details = %+v, want scale 0.06 max_scale 0.05 level L", le)
	}
}

func TestLogoAutoRaise(t *testing.T) {
	r := testRenderer()
	// 0.22 exceeds L (5%), M (12%) and Q (20%) and fits H (25%).
	o := logoOptions(t, domain.ECLow, 0.22, true)
	res, err := r.Render(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.ECLevel != domain.ECLow {
		t.Errorf("requested level must be preserved: %s", res.ECLevel)
	}
	if res.ECLevelEffective != domain.ECHigh {
		t.Errorf("effective level = %s, want H", res.ECLevelEffective)
	}
	p, s := renderBoth(t, r, o)
	pr, sr := decodeBoth(t, p, s, 512, o.Content)
	if pr.ECLevel != "H" || sr.ECLevel != "H" {
		t.Errorf("decoder reports EC %s / %s, want H", pr.ECLevel, sr.ECLevel)
	}

	// Beyond every budget, auto-raise cannot help and the error names H. (0.26 is
	// above the schema maximum, so Render rejects it as an invalid option first; the
	// resolution rule is checked directly.)
	_, err = resolveLogoLevel(domain.ECLow, 0.26, true)
	var le *LogoTooLargeError
	if !errors.As(err, &le) || le.Level != domain.ECHigh || le.MaxScale != 0.25 {
		t.Errorf("err = %v, want LogoTooLargeError at H with max 0.25", err)
	}
	if _, err := r.Render(context.Background(), logoOptions(t, domain.ECLow, 0.26, true)); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("scale above %.2f: err = %v, want ErrInvalidOption", MaxLogoScale, err)
	}
}

func TestLogoPresentInOutput(t *testing.T) {
	r := testRenderer()
	o := logoOptions(t, domain.ECHigh, 0.2, false)
	p, s := renderBoth(t, r, o)
	img, err := png.Decode(bytes.NewReader(p))
	if err != nil {
		t.Fatal(err)
	}
	c := color.NRGBAModel.Convert(img.At(256, 256)).(color.NRGBA)
	if c != logoColor {
		t.Errorf("centre pixel = %v, want logo colour %v", c, logoColor)
	}
	if !strings.Contains(string(s), "<image") || !strings.Contains(string(s), "data:image/png;base64,") {
		t.Error("svg must embed the logo as a data URI <image>")
	}
	decodeBoth(t, p, s, 512, o.Content)
}

func TestLogoInvalid(t *testing.T) {
	r := testRenderer()
	o := baseOptions([]byte("x"), domain.ECHigh, 256)
	o.Logo = &Logo{Image: []byte("not an image"), Scale: 0.1}
	if _, err := r.Render(context.Background(), o); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("err = %v, want ErrInvalidOption", err)
	}
	o.Logo = &Logo{Image: solidLogo(t, logoColor), Scale: 0}
	if _, err := r.Render(context.Background(), o); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("zero scale: err = %v, want ErrInvalidOption", err)
	}
}
