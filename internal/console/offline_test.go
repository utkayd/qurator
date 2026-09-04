package console

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/domain"
)

// urlRe finds any http:// or https:// URL in embedded text.
var urlRe = regexp.MustCompile(`https?://[^\s"'<>)]+`)

// allowedOrigins are the only non-self origins any embedded asset or template may ever
// reference. The sole entry is the SVG XML namespace identifier, which is not a network
// reference — it is a fixed string XML processors compare byte-for-byte, never
// dereferenced — emitted by chart.go's inline SVG (FR-043; research.md §5: NO external
// origins in the console).
var allowedOrigins = []string{
	"http://www.w3.org/2000/svg",
}

func TestNoExternalOriginsInEmbeddedAssets(t *testing.T) {
	err := walkEmbedded(func(path string, data []byte) error {
		text := string(data)
		for _, m := range urlRe.FindAllString(text, -1) {
			if isAllowed(m) {
				continue
			}
			t.Errorf("%s: references a non-self origin: %q", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking embedded FS: %v", err)
	}
}

func isAllowed(url string) bool {
	for _, a := range allowedOrigins {
		if strings.HasPrefix(url, a) {
			return true
		}
	}
	return false
}

// TestChartEmitsOnlyTheAllowedNamespaceOrigin is a focused regression test: the one
// legitimate http:// string the chart renderer emits must be exactly the SVG namespace,
// and nothing else in its output may look like a URL to a non-self origin.
func TestChartEmitsOnlyTheAllowedNamespaceOrigin(t *testing.T) {
	now := time.Now()
	svg := string(renderTrendChart([]domain.SeriesPoint{
		{Start: now, Count: 1},
		{Start: now.Add(24 * time.Hour), Count: 5},
	}))
	for _, m := range urlRe.FindAllString(svg, -1) {
		if !isAllowed(m) {
			t.Errorf("chart SVG references a non-self origin: %q", m)
		}
	}
	if !strings.Contains(svg, allowedOrigins[0]) {
		t.Fatalf("chart SVG missing the expected xmlns namespace")
	}
}
