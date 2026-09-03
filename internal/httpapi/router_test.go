package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// openAPIOperations parses contracts/openapi.yaml and returns "METHOD /path" pairs with
// {param} placeholders normalised (we do not care about the placeholder name).
func openAPIOperations(t *testing.T) map[string]bool {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "specs/001-qr-service-baseline/contracts/openapi.yaml"))
	if err != nil {
		t.Skipf("openapi.yaml not readable: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ops := map[string]bool{}
	for p, methods := range doc.Paths {
		for m := range methods {
			switch m {
			case "get", "post", "put", "patch", "delete", "head", "options":
				ops[strings.ToUpper(m)+" "+normalise(p)] = true
			}
		}
	}
	return ops
}

// normalise rewrites /i/{id}.{ext} style paths and param names to a canonical form so
// the router pattern "/i/{file}" and the OpenAPI path "/i/{id}.{ext}" compare equal.
func normalise(p string) string {
	var b strings.Builder
	in := false
	for _, r := range p {
		switch {
		case r == '{':
			in = true
			b.WriteString("{}")
		case r == '}':
			in = false
		case in:
		default:
			b.WriteRune(r)
		}
	}
	s := b.String()
	s = strings.ReplaceAll(s, "{}.{}", "{}") // /i/{id}.{ext} → /i/{}
	return s
}

func TestEveryOpenAPIOperationIsRouted(t *testing.T) {
	ops := openAPIOperations(t)
	routed := map[string]bool{}
	for _, rt := range Routes {
		if rt.Group == GroupConsole {
			continue
		}
		routed[normalise(rt.Pattern)] = true
	}
	for op := range ops {
		if !routed[op] {
			t.Errorf("OpenAPI operation %q has no route in Routes", op)
		}
	}
	for r := range routed {
		if !ops[r] {
			t.Errorf("route %q is registered but absent from openapi.yaml", r)
		}
	}
}

// marker middlewares let the test see which chain a request passed through.
func marker(name string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("X-Chain", name)
			next.ServeHTTP(w, r)
		})
	}
}

func TestPublicGroupNeverSeesAuth(t *testing.T) {
	h := NewRouter(Handlers{}, Options{
		Common: []Middleware{marker("common")},
		Auth:   marker("auth"),
		CSRF:   marker("csrf"),
	})
	public := []string{"GET /r/abc", "GET /i/cod_x.png", "GET /healthz", "GET /readyz", "POST /v1/auth/signin"}
	for _, p := range public {
		m, path, _ := strings.Cut(p, " ")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, path, nil))
		chain := rec.Header().Values("X-Chain")
		if !reflect.DeepEqual(chain, []string{"common"}) {
			t.Errorf("%s: chain = %v, want [common] only — auth middleware must never be mounted on public routes (Principle IV)", p, chain)
		}
	}
	protected := []string{"GET /v1/codes", "GET /v1/qr", "GET /v1/auth/me", "GET /v1/export"}
	for _, p := range protected {
		m, path, _ := strings.Cut(p, " ")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, path, nil))
		chain := rec.Header().Values("X-Chain")
		if !reflect.DeepEqual(chain, []string{"common", "auth", "csrf"}) {
			t.Errorf("%s: chain = %v, want [common auth csrf]", p, chain)
		}
	}
}

func TestStubsReturn501Envelope(t *testing.T) {
	h := NewRouter(Handlers{}, Options{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/codes", nil))
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), `"not_implemented"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestUnmatchedRedirectsToConsole(t *testing.T) {
	h := NewRouter(Handlers{}, Options{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/ui/" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
