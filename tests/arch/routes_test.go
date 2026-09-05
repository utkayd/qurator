package arch

import (
	"strings"
	"testing"

	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/shortcode"
)

// TestEveryRouteRootIsReserved: an alias equal to a route's first path segment would
// shadow that route at /r/{alias} in links and in people's heads. The reserved list rots
// silently whenever someone adds a route, so this test is the list's only maintenance.
func TestEveryRouteRootIsReserved(t *testing.T) {
	for _, rt := range httpapi.Routes {
		_, path, ok := strings.Cut(rt.Pattern, " ")
		if !ok {
			path = rt.Pattern
		}
		seg := strings.Split(strings.TrimPrefix(path, "/"), "/")[0]
		if seg == "" {
			continue
		}
		if !shortcode.IsReserved(seg) {
			t.Errorf("route %q: first segment %q is not in shortcode's reserved list", rt.Pattern, seg)
		}
	}
}
