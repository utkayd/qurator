package qr

import (
	"context"
	"fmt"
	"image"
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
	Logo    *Logo       // optional centre overlay
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
	bounds      Bounds
	minContrast float64
}

// RendererOption tunes a Renderer.
type RendererOption func(*Renderer)

// WithMinContrast sets the contrast gate (FR-028). Values below ContrastFloor are
// clamped to it: the floor is where scanners stop working, not a preference.
func WithMinContrast(ratio float64) RendererOption {
	return func(r *Renderer) {
		if ratio < ContrastFloor {
			ratio = ContrastFloor
		}
		r.minContrast = ratio
	}
}

// NewRenderer builds a renderer with the given resource bounds.
func NewRenderer(b Bounds, opts ...RendererOption) *Renderer {
	r := &Renderer{bounds: b.normalised(), minContrast: DefaultMinContrast}
	for _, o := range opts {
		o(r)
	}
	return r
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
	if o.Logo != nil && (o.Logo.Scale <= 0 || o.Logo.Scale > MaxLogoScale) {
		return o, fg, bg, &InvalidOptionError{Field: "logo.scale", Reason: fmt.Sprintf("must be between 0.01 and %.2f", MaxLogoScale)}
	}
	return o, fg, bg, nil
}

// Prepared is an encoded symbol with its geometry resolved, ready to be emitted in
// either format. Both PNG and SVG read the same Geometry.
type Prepared struct {
	opts      Options
	fg, bg    color.NRGBA
	sym       *Symbol
	geo       *Geometry
	logo      *decodedLogo
	effective ECLevel
}

// Prepare validates, applies policy, encodes and computes geometry. It does not
// consult the time budget; Render does. Errors are typed (see errors.go).
func (r *Renderer) Prepare(ctx context.Context, opts Options) (*Prepared, error) {
	opts, fg, bg, err := opts.normalise()
	if err != nil {
		return nil, err
	}
	if err := r.bounds.checkSize(opts.SizePx); err != nil {
		return nil, err
	}
	if err := checkContrast(fg, bg, r.minContrast); err != nil {
		return nil, err
	}
	effective := opts.ECLevel
	var logo *decodedLogo
	if opts.Logo != nil {
		if effective, err = resolveLogoLevel(opts.ECLevel, opts.Logo.Scale, opts.Logo.AutoRaise); err != nil {
			return nil, err
		}
		if logo, err = decodeLogo(opts.Logo.Image); err != nil {
			return nil, err
		}
	}
	// The payload cap is that of the level actually encoded.
	if err := r.bounds.checkPayload(len(opts.Content), effective); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sym, err := Encode(opts.Content, effective)
	if err != nil {
		return nil, err
	}
	var hole *image.Rectangle
	if logo != nil {
		h := logoHole(sym.Size(), opts.Logo.Scale)
		hole = &h
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Prepared{
		opts:      opts,
		fg:        fg,
		bg:        bg,
		sym:       sym,
		geo:       computeGeometry(sym, opts.Shape, opts.Margin, hole),
		logo:      logo,
		effective: effective,
	}, nil
}

// PNG rasterises the prepared symbol.
func (p *Prepared) PNG(ctx context.Context) ([]byte, error) { return renderPNG(ctx, p) }

// SVG builds the vector document for the prepared symbol.
func (p *Prepared) SVG(ctx context.Context) ([]byte, error) { return renderSVG(ctx, p) }

// ECLevelEffective is the level encoded after any automatic raise.
func (p *Prepared) ECLevelEffective() ECLevel { return p.effective }

// Render prepares and emits opts.Format under the renderer's bounds. A cancelled
// parent context is returned as-is; an exhausted budget is a RenderTimeoutError.
func (r *Renderer) Render(ctx context.Context, opts Options) (*Result, error) {
	ctx, cancel := r.bounds.withBudget(ctx)
	defer cancel()

	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.render(ctx, opts)
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

// render is the budgeted body of Render; it runs on its own goroutine.
func (r *Renderer) render(ctx context.Context, opts Options) (*Result, error) {
	p, err := r.Prepare(ctx, opts)
	if err != nil {
		return nil, err
	}
	var out []byte
	switch p.opts.Format {
	case FormatSVG:
		out, err = p.SVG(ctx)
	default:
		out, err = p.PNG(ctx)
	}
	if err != nil {
		return nil, err
	}
	return &Result{
		Bytes:            out,
		ContentType:      p.opts.Format.ContentType(),
		ECLevel:          p.opts.ECLevel,
		ECLevelEffective: p.effective,
		Version:          p.sym.Version(),
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
