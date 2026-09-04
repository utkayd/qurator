package qr

import (
	"context"
	"fmt"
	"image/color"

	"github.com/utkayd/qurator/internal/domain"
)

// Format is an output image format.
type Format string

// Supported formats and their content types (FR-002).
const (
	FormatPNG Format = "png"
	FormatSVG Format = "svg"
)

// ContentType returns the MIME type for f.
func (f Format) ContentType() string {
	switch f {
	case FormatSVG:
		return "image/svg+xml"
	default:
		return "image/png"
	}
}

// ModuleShape aliases the domain type.
type ModuleShape = domain.ModuleShape

// Options is one render request. Zero values take the defaults documented on each
// field, which are the OpenAPI defaults.
type Options struct {
	Content []byte
	Format  Format      // default png
	FgColor string      // #RRGGBB, default #000000
	BgColor string      // #RRGGBB, default #FFFFFF
	Shape   ModuleShape // default square
	Margin  int         // quiet zone in modules, default 4
	SizePx  int         // side length in pixels, default 512
	ECLevel ECLevel     // default M
}

// Result is a rendered image.
type Result struct {
	Bytes       []byte
	ContentType string
	// ECLevel is the level requested; ECLevelEffective the one encoded. They differ only
	// when a logo forced an automatic raise (FR-027).
	ECLevel          ECLevel
	ECLevelEffective ECLevel
	// Version is the QR version of the encoded symbol.
	Version int
}

// Renderer turns Options into image bytes under a fixed policy. It is safe for
// concurrent use and holds no mutable state.
type Renderer struct {
	bounds Bounds
}

// NewRenderer builds a renderer with the given resource bounds.
func NewRenderer(b Bounds) *Renderer {
	return &Renderer{bounds: b.normalised()}
}

// Bounds returns the renderer's policy.
func (r *Renderer) Bounds() Bounds { return r.bounds }

// normalise applies defaults and validates every field.
func (o Options) normalise() (Options, color.NRGBA, color.NRGBA, error) {
	if o.Format == "" {
		o.Format = FormatPNG
	}
	if o.Format != FormatPNG && o.Format != FormatSVG {
		return o, color.NRGBA{}, color.NRGBA{}, &InvalidOptionError{Field: "format", Reason: fmt.Sprintf("unsupported format %q", string(o.Format))}
	}
	if o.FgColor == "" {
		o.FgColor = "#000000"
	}
	if o.BgColor == "" {
		o.BgColor = "#FFFFFF"
	}
	fg, err := ParseHexColor(o.FgColor)
	if err != nil {
		return o, color.NRGBA{}, color.NRGBA{}, &InvalidOptionError{Field: "fg_color", Reason: err.Error()}
	}
	bg, err := ParseHexColor(o.BgColor)
	if err != nil {
		return o, color.NRGBA{}, color.NRGBA{}, &InvalidOptionError{Field: "bg_color", Reason: err.Error()}
	}
	if o.Shape == "" {
		o.Shape = domain.ShapeSquare
	}
	switch o.Shape {
	case domain.ShapeSquare, domain.ShapeDot, domain.ShapeRounded:
	default:
		return o, fg, bg, &InvalidOptionError{Field: "module_shape", Reason: fmt.Sprintf("unknown shape %q", string(o.Shape))}
	}
	if o.Margin < 0 || o.Margin > 64 {
		return o, fg, bg, &InvalidOptionError{Field: "margin_modules", Reason: "must be between 0 and 64"}
	}
	if o.SizePx == 0 {
		o.SizePx = 512
	}
	if o.SizePx < 1 {
		return o, fg, bg, &InvalidOptionError{Field: "size_px", Reason: "must be positive"}
	}
	if o.ECLevel == "" {
		o.ECLevel = domain.ECMedium
	}
	if levelRank(o.ECLevel) < 0 {
		return o, fg, bg, &InvalidOptionError{Field: "ec_level", Reason: fmt.Sprintf("unknown level %q", string(o.ECLevel))}
	}
	return o, fg, bg, nil
}

// Render encodes and renders under the renderer's bounds. Errors are typed (see
// errors.go); a cancelled parent context is returned as-is.
func (r *Renderer) Render(ctx context.Context, opts Options) (*Result, error) {
	opts, fg, bg, err := opts.normalise()
	if err != nil {
		return nil, err
	}
	if err := r.bounds.checkSize(opts.SizePx); err != nil {
		return nil, err
	}
	if err := r.bounds.checkPayload(len(opts.Content), opts.ECLevel); err != nil {
		return nil, err
	}

	ctx, cancel := r.bounds.withBudget(ctx)
	defer cancel()

	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.render(ctx, opts, fg, bg)
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		if o.err != nil {
			return nil, r.bounds.budgetError(o.err)
		}
		return o.res, nil
	case <-ctx.Done():
		// The worker checks ctx between rows and exits shortly after.
		return nil, r.bounds.budgetError(ctx.Err())
	}
}

// render is the budgeted body of Render; it runs on its own goroutine and must
// check ctx regularly.
func (r *Renderer) render(ctx context.Context, opts Options, fg, bg color.NRGBA) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sym, err := Encode(opts.Content, opts.ECLevel)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []byte
	switch opts.Format {
	case FormatSVG:
		out, err = renderSVG(ctx, sym, opts, fg, bg)
	default:
		out, err = renderPNG(ctx, sym, opts, fg, bg)
	}
	if err != nil {
		return nil, err
	}
	return &Result{
		Bytes:            out,
		ContentType:      opts.Format.ContentType(),
		ECLevel:          opts.ECLevel,
		ECLevelEffective: sym.ECLevel(),
		Version:          sym.Version(),
	}, nil
}

// ParseHexColor parses #RRGGBB (case-insensitive) into an opaque colour.
func ParseHexColor(s string) (color.NRGBA, error) {
	if len(s) != 7 || s[0] != '#' {
		return color.NRGBA{}, fmt.Errorf("%q is not of the form #RRGGBB", s)
	}
	var v [3]uint8
	for i := 0; i < 3; i++ {
		hi, ok1 := hexNibble(s[1+2*i])
		lo, ok2 := hexNibble(s[2+2*i])
		if !ok1 || !ok2 {
			return color.NRGBA{}, fmt.Errorf("%q is not of the form #RRGGBB", s)
		}
		v[i] = hi<<4 | lo
	}
	return color.NRGBA{R: v[0], G: v[1], B: v[2], A: 0xFF}, nil
}

func hexNibble(c byte) (uint8, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// hexString formats a colour as lowercase #rrggbb for SVG output.
func hexString(c color.NRGBA) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}
