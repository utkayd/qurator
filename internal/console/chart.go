package console

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/utkayd/qurator/internal/domain"
)

// chartWidth and chartHeight are the SVG viewBox dimensions. The element itself scales
// to its container via CSS (width: 100%; height: auto), so these only fix the aspect
// ratio and the coordinate space the path is drawn in. The left/bottom paddings leave
// room for the axis labels.
const (
	chartWidth     = 640
	chartHeight    = 200
	chartPadTop    = 12
	chartPadRight  = 16
	chartPadBottom = 28
	chartPadLeft   = 40
	chartGridRows  = 4
)

// renderTrendChart renders a scan-count time series as an inline SVG line chart. It is
// server-rendered in Go rather than pulled from a charting library (research.md §5):
// a single trend line does not justify a 25-120KB dependency, and every such dependency
// would have to be vendored to satisfy the no-external-origin rule anyway.
//
// Every colour comes from app.css via the class names below (currentColor and CSS
// custom properties), so the chart follows the light/dark theme with no inline styles.
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
	// Round the axis ceiling up to a friendly number so the gridline labels are integers.
	ceiling := niceCeiling(maxCount, chartGridRows)

	innerW := float64(chartWidth - chartPadLeft - chartPadRight)
	innerH := float64(chartHeight - chartPadTop - chartPadBottom)
	baseY := float64(chartPadTop) + innerH

	n := len(series)
	xAt := func(i int) float64 {
		if n == 1 {
			return float64(chartPadLeft) + innerW/2
		}
		return float64(chartPadLeft) + innerW*float64(i)/float64(n-1)
	}
	yAt := func(c int64) float64 {
		return float64(chartPadTop) + innerH*(1-float64(c)/float64(ceiling))
	}

	points := make([]string, n)
	dots := make([]string, n)
	for i, p := range series {
		x, y := xAt(i), yAt(p.Count)
		points[i] = fmt.Sprintf("%.2f,%.2f", x, y)
		dots[i] = fmt.Sprintf(
			`<circle class="point" cx="%.2f" cy="%.2f" r="3"><title>%s: %d</title></circle>`,
			x, y, template.HTMLEscapeString(p.Start.Format("2006-01-02 15:04")), p.Count,
		)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Scan trend chart">`, chartWidth, chartHeight)

	// Horizontal gridlines with their y-axis labels.
	for row := 0; row <= chartGridRows; row++ {
		v := ceiling * int64(row) / int64(chartGridRows)
		y := yAt(v)
		fmt.Fprintf(&b, `<line class="grid" x1="%d" y1="%.2f" x2="%d" y2="%.2f"/>`, chartPadLeft, y, chartWidth-chartPadRight, y)
		fmt.Fprintf(&b, `<text class="label" x="%d" y="%.2f" text-anchor="end" dominant-baseline="middle">%d</text>`, chartPadLeft-8, y, v)
	}

	// Area fill under the line, then the line itself, then the points.
	fmt.Fprintf(&b, `<polygon class="area" points="%.2f,%.2f %s %.2f,%.2f"/>`,
		xAt(0), baseY, strings.Join(points, " "), xAt(n-1), baseY)
	fmt.Fprintf(&b, `<polyline class="line" points="%s"/>`, strings.Join(points, " "))
	for _, d := range dots {
		b.WriteString(d)
	}

	// X-axis: first, middle and last bucket labels.
	labelIdx := []int{0}
	if n > 2 {
		labelIdx = append(labelIdx, n/2)
	}
	if n > 1 {
		labelIdx = append(labelIdx, n-1)
	}
	for k, i := range labelIdx {
		anchor := "middle"
		switch {
		case k == 0 && n > 1:
			anchor = "start"
		case k == len(labelIdx)-1 && n > 1:
			anchor = "end"
		}
		fmt.Fprintf(&b, `<text class="label" x="%.2f" y="%d" text-anchor="%s">%s</text>`,
			xAt(i), chartHeight-8, anchor, template.HTMLEscapeString(series[i].Start.Format("2 Jan")))
	}

	b.WriteString(`</svg>`)
	return template.HTML(b.String()) //nolint:gosec // SVG built from numeric series and escaped labels only
}

// niceCeiling returns the smallest multiple of rows that is >= max and, above small
// values, rounds up to a 1/2/5-style step so the gridline labels read cleanly.
func niceCeiling(max int64, rows int) int64 {
	if max < int64(rows) {
		return int64(rows)
	}
	step := (max + int64(rows) - 1) / int64(rows)
	mag := int64(1)
	for step >= 10*mag {
		mag *= 10
	}
	switch {
	case step <= mag:
		step = mag
	case step <= 2*mag:
		step = 2 * mag
	case step <= 5*mag:
		step = 5 * mag
	default:
		step = 10 * mag
	}
	return step * int64(rows)
}
