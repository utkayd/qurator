//go:build depspin

// Package depspin pins the research-verified dependencies in go.mod before the packages
// that use them exist, so parallel foundation work never contends over go.mod.
// It is never compiled: the build tag is never set. Remove entries as real imports appear.
package depspin

import (
	_ "github.com/golang-jwt/jwt/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/knadh/koanf/parsers/yaml"
	_ "github.com/knadh/koanf/providers/confmap"
	_ "github.com/knadh/koanf/providers/env"
	_ "github.com/knadh/koanf/providers/file"
	_ "github.com/knadh/koanf/providers/posflag"
	_ "github.com/knadh/koanf/v2"
	_ "github.com/makiuchi-d/gozxing"
	_ "github.com/makiuchi-d/gozxing/qrcode"
	_ "github.com/maypok86/otter/v2"
	_ "github.com/medama-io/go-useragent"
	_ "github.com/minio/minio-go/v7"
	_ "github.com/piglig/go-qr"
	_ "github.com/pressly/goose/v3"
	_ "github.com/prometheus/client_golang/prometheus"
	_ "github.com/prometheus/client_golang/prometheus/promhttp"
	_ "github.com/spf13/pflag"
	_ "github.com/srwiley/oksvg"
	_ "github.com/srwiley/rasterx"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/image/vector"
	_ "golang.org/x/net/html"
	_ "golang.org/x/tools/go/packages"
	_ "gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)
