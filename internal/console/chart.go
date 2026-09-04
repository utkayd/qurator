package console

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/utkayd/qurator/internal/domain"
)

// chartWidth and chartHeight are the SVG viewBox dimensions. The element itself scales
// to its container via CSS (width: 100%; height: auto), so these only fix the aspect
// ratio and the coordinate space the path is drawn in.
const (
	chartWidth  = 640
	chartHeight = 180
	chartPadX   = 24
	chartPadY   = 16
)

// renderTrendChart renders a scan-count time series as an inline SVG line chart. It is
// server-rendered in Go rather than pulled from a charting library (research.md §5):
// a single trend line does not justify a 25-120KB dependency, and every such dependency
// would have to be vendored to satisfy the no-external-origin rule anyway.
//
// The xmlns attribute is the one http:// URL this package legitimately emits — it is an
// XML namespace identifier, not a network reference, and offline_test.go allowlists it.
func renderTrendChart(series []domain.SeriesPoint) template.HTML {
	if len(series) == 0 {
		return template.HTML(`<p class="help">No scan data for this range yet.</p>`)
	}

	var maxCount int64 = 1
	for _, p := range series {
		if p.Count > maxCount {
			maxCount = p.Count
		}
	}

	innerW := float64(chartWidth - 2*chartPadX)
	innerH := float64(chartHeight - 2*chartPadY)

	points := make([]string, len(series))
	dots := make([]string, len(series))
	n := len(series)
	for i, p := range series {
		var x float64
		if n == 1 {
			x = float64(chartPadX) + innerW/2
		} else {
			x = float64(chartPadX) + innerW*float64(i)/float64(n-1)
		}
		y := float64(chartPadY) + innerH*(1-float64(p.Count)/float64(maxCount))
		points[i] = fmt.Sprintf("%.2f,%.2f", x, y)
		dots[i] = fmt.Sprintf(
			`<circle class="point" cx="%.2f" cy="%.2f" r="2.5"><title>%s: %d</title></circle>`,
			x, y, template.HTMLEscapeString(p.Start.Format("2006-01-02 15:04")), p.Count,
		)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Scan trend chart">`, chartWidth, chartHeight)
	fmt.Fprintf(&b, `<line class="axis" x1="%d" y1="%d" x2="%d" y2="%d"/>`, chartPadX, chartHeight-chartPadY, chartWidth-chartPadX, chartHeight-chartPadY)
	fmt.Fprintf(&b, `<line class="axis" x1="%d" y1="%d" x2="%d" y2="%d"/>`, chartPadX, chartPadY, chartPadX, chartHeight-chartPadY)
	fmt.Fprintf(&b, `<polyline class="line" points="%s"/>`, strings.Join(points, " "))
	for _, d := range dots {
		b.WriteString(d)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
