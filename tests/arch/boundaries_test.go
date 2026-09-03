// Package arch enforces the import boundaries the constitution requires. These tests are
// the difference between "we agreed not to" and "the build refuses".
package arch

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const module = "github.com/utkayd/qurator"

func load(t *testing.T) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedImports | packages.NeedDeps, Dir: "../..", Tests: false}
	pkgs, err := packages.Load(cfg, module+"/...")
	if err != nil {
		t.Fatal(err)
	}
	return pkgs
}

// transitiveImports returns every package path reachable from p.
func transitiveImports(p *packages.Package) map[string]bool {
	seen := map[string]bool{}
	var walk func(*packages.Package)
	walk = func(q *packages.Package) {
		for path, imp := range q.Imports {
			if seen[path] {
				continue
			}
			seen[path] = true
			walk(imp)
		}
	}
	walk(p)
	return seen
}

type rule struct {
	name    string
	subject func(path string) bool // packages the rule applies to
	deny    func(path string) bool // imports they must not reach, even transitively
}

func TestImportBoundaries(t *testing.T) {
	rules := []rule{
		{
			name:    "Principle III: internal/qr never reaches a store",
			subject: func(p string) bool { return p == module+"/internal/qr" || strings.HasPrefix(p, module+"/internal/qr/") },
			deny: func(p string) bool {
				return strings.HasPrefix(p, module+"/internal/store") || strings.HasPrefix(p, module+"/internal/blob")
			},
		},
		{
			name:    "domain is pure: no net/http, no database/sql",
			subject: func(p string) bool { return p == module+"/internal/domain" },
			deny:    func(p string) bool { return p == "net/http" || p == "database/sql" },
		},
		{
			name: "Principle II: SQL drivers only inside their store driver package",
			subject: func(p string) bool {
				return strings.HasPrefix(p, module+"/") &&
					!strings.HasPrefix(p, module+"/internal/store/sqlite") &&
					!strings.HasPrefix(p, module+"/internal/store/postgres") &&
					!strings.HasPrefix(p, module+"/internal/store/migrations") &&
					!strings.HasPrefix(p, module+"/cmd/") &&
					!strings.HasPrefix(p, module+"/tools/")
			},
			deny: func(p string) bool {
				return strings.HasPrefix(p, "modernc.org/sqlite") || strings.HasPrefix(p, "github.com/jackc/pgx")
			},
		},
		{
			name: "Principle II: S3 client only inside blob/s3blob",
			subject: func(p string) bool {
				return strings.HasPrefix(p, module+"/") &&
					!strings.HasPrefix(p, module+"/internal/blob/s3blob") &&
					!strings.HasPrefix(p, module+"/cmd/")
			},
			deny: func(p string) bool { return strings.HasPrefix(p, "github.com/minio/minio-go") },
		},
	}
	pkgs := load(t)
	for _, r := range rules {
		t.Run(r.name, func(t *testing.T) {
			for _, p := range pkgs {
				if !r.subject(p.PkgPath) {
					continue
				}
				for imp := range transitiveImports(p) {
					if r.deny(imp) {
						t.Errorf("%s reaches forbidden import %s", p.PkgPath, imp)
					}
				}
			}
		})
	}
}
