package console

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
)

// embedFS holds every byte the console ever serves: vendored JS, our own CSS/JS, and
// html/template sources. Nothing the console needs is fetched at runtime — everything is
// baked into the binary (FR-043).
//
//go:embed assets templates
var embedFS embed.FS

// asset is one servable static file: its fingerprinted URL path, content type, raw
// bytes, and a pre-computed gzip encoding.
type asset struct {
	urlPath     string
	contentType string
	body        []byte
	gzipBody    []byte // nil if gzip did not help or type is already compressed
}

// assetRegistry maps a logical name (e.g. "app.js", "htmx-2.0.4.min.js") to its
// fingerprinted asset, plus a reverse index by URL path for serving.
type assetRegistry struct {
	byName map[string]*asset
	byPath map[string]*asset
}

// staticSources lists every file under assets/ that is served over HTTP, with the
// logical name templates use to reference it. Anything in assets/ not listed here is
// still part of the embedded FS (for offline_test.go's sweep) but is not exposed as a
// route.
var staticSources = []struct {
	name string
	file string
}{
	{"app.css", "assets/app.css"},
	{"app.js", "assets/app.js"},
	{"htmx.min.js", "assets/vendor/htmx-2.0.4.min.js"},
}

func buildAssetRegistry() (*assetRegistry, error) {
	reg := &assetRegistry{byName: map[string]*asset{}, byPath: map[string]*asset{}}
	for _, src := range staticSources {
		raw, err := embedFS.ReadFile(src.file)
		if err != nil {
			return nil, fmt.Errorf("console: reading embedded asset %s: %w", src.file, err)
		}
		sum := sha256.Sum256(raw)
		hash := hex.EncodeToString(sum[:])[:12]
		ext := path.Ext(src.name)
		base := strings.TrimSuffix(path.Base(src.name), ext)
		urlPath := "/ui/assets/" + base + "-" + hash + ext

		ct := mime.TypeByExtension(ext)
		if ct == "" {
			ct = "application/octet-stream"
		}
		if strings.HasSuffix(ext, ".js") {
			ct = "text/javascript; charset=utf-8"
		}
		if strings.HasSuffix(ext, ".css") {
			ct = "text/css; charset=utf-8"
		}

		a := &asset{urlPath: urlPath, contentType: ct, body: raw}
		if gz, ok := gzipIfSmaller(raw); ok {
			a.gzipBody = gz
		}
		reg.byName[src.name] = a
		reg.byPath[urlPath] = a
	}
	return reg, nil
}

func gzipIfSmaller(raw []byte) ([]byte, bool) {
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := w.Write(raw); err != nil {
		return nil, false
	}
	if err := w.Close(); err != nil {
		return nil, false
	}
	if buf.Len() >= len(raw) {
		return nil, false
	}
	return buf.Bytes(), true
}

// URL returns the fingerprinted path for a logical asset name, for use from templates.
// It panics on an unknown name — that is a programmer error in a template, not a
// runtime condition.
func (reg *assetRegistry) URL(name string) string {
	a, ok := reg.byName[name]
	if !ok {
		panic("console: unknown asset " + name)
	}
	return a.urlPath
}

// ServeHTTP serves a fingerprinted static asset with a far-future immutable cache
// lifetime (the fingerprint changes whenever the content does) and a pre-gzipped body
// when the client accepts it.
func (reg *assetRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a, ok := reg.byPath[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", a.contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Vary", "Accept-Encoding")
	if a.gzipBody != nil && acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(a.gzipBody)
		return
	}
	_, _ = w.Write(a.body)
}

func acceptsGzip(r *http.Request) bool {
	for _, enc := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.TrimSpace(enc) == "gzip" {
			return true
		}
	}
	return false
}

// walkEmbedded calls fn with the path and contents of every regular file under the
// embedded FS. Used by offline_test.go to audit every shipped asset and template for
// references to a non-self origin.
func walkEmbedded(fn func(path string, data []byte) error) error {
	var paths []string
	err := fs.WalkDir(embedFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, p := range paths {
		data, err := embedFS.ReadFile(p)
		if err != nil {
			return err
		}
		if err := fn(p, data); err != nil {
			return err
		}
	}
	return nil
}
