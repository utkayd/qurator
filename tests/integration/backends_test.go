package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	// Registers the "pgx" database/sql driver the Postgres schema setup uses.
	_ "github.com/utkayd/qurator/internal/store/postgres"
)

// T105 / quickstart Scenario 7 / SC-010: the same scripted lifecycle, driven over HTTP
// against the real binary, once per storage backend. The observable (status, error
// code) sequence must be byte-for-byte identical across backends — any divergence is a
// Principle II violation.
//
// sqlite+fs always runs. postgres+s3 runs only when both QURATOR_TEST_PG_DSN and
// QURATOR_TEST_S3_ENDPOINT are set; otherwise it skips visibly.

// itStep is one observed step of the lifecycle: the request that was made, the status
// it returned, and the error code from the envelope (empty on success). Detail holds
// any extra observation that must also agree across backends.
type itStep struct {
	Name   string
	Status int
	Code   string
	Detail string
}

func (s itStep) String() string {
	return fmt.Sprintf("%-22s %d %-14q %s", s.Name, s.Status, s.Code, s.Detail)
}

// itBackend describes one storage backend of the matrix.
type itBackend struct {
	name string
	// env returns the driver env for a fresh instance rooted at dir, or skips.
	env func(t *testing.T, dir string) map[string]string
}

var itBackends = []itBackend{
	{name: "sqlite+fs", env: func(t *testing.T, dir string) map[string]string {
		// Defaults: ./data/qurator.db and ./data/blobs under the work dir.
		return map[string]string{}
	}},
	{name: "postgres+s3", env: itPostgresS3Env},
}

// itPostgresS3Env provisions an isolated Postgres schema and a fresh S3 bucket for one
// instance, both removed in t.Cleanup, and returns the env selecting them.
func itPostgresS3Env(t *testing.T, _ string) map[string]string {
	t.Helper()
	dsn := os.Getenv("QURATOR_TEST_PG_DSN")
	endpoint := os.Getenv("QURATOR_TEST_S3_ENDPOINT")
	if dsn == "" || endpoint == "" {
		t.Skip("QURATOR_TEST_PG_DSN and QURATOR_TEST_S3_ENDPOINT both required; skipping postgres+s3")
	}
	access := itEnvOr("QURATOR_TEST_S3_ACCESS_KEY", "minioadmin")
	secret := itEnvOr("QURATOR_TEST_S3_SECRET_KEY", "minioadmin")
	useSSL, _ := strconv.ParseBool(itEnvOr("QURATOR_TEST_S3_USE_SSL", "false"))

	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(b[:])

	// Postgres: one throwaway schema per instance so concurrent tests never collide.
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin postgres: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := "it_" + suffix
	if _, err := admin.ExecContext(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	})

	// S3: one throwaway bucket per instance, emptied and removed afterwards.
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(access, secret, ""),
		Secure:       useSSL,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}
	bucket := "qurator-it-" + suffix
	if err := mc.MakeBucket(context.Background(), bucket, minio.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket %s: %v", bucket, err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for obj := range mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
			if obj.Err == nil {
				_ = mc.RemoveObject(ctx, bucket, obj.Key, minio.RemoveObjectOptions{})
			}
		}
		_ = mc.RemoveBucket(ctx, bucket)
	})

	return map[string]string{
		"QURATOR_DB_DRIVER":          "postgres",
		"QURATOR_DB_DSN":             itWithSearchPath(dsn, schema),
		"QURATOR_BLOB_DRIVER":        "s3",
		"QURATOR_BLOB_S3_ENDPOINT":   endpoint,
		"QURATOR_BLOB_S3_BUCKET":     bucket,
		"QURATOR_BLOB_S3_ACCESS_KEY": access,
		"QURATOR_BLOB_S3_SECRET_KEY": secret,
		"QURATOR_BLOB_S3_USE_SSL":    strconv.FormatBool(useSSL),
		"QURATOR_BLOB_S3_PATH_STYLE": "true",
	}
}

func itEnvOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// itWithSearchPath appends a search_path parameter to either DSN form pgx accepts.
func itWithSearchPath(dsn, schema string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "search_path=" + schema
	}
	return dsn + " search_path=" + schema
}

// itLifecycle runs the scripted lifecycle against a running instance and returns the
// observed step sequence. It only Fatals on transport failures; every HTTP outcome,
// expected or not, is recorded so the cross-backend comparison sees the same thing a
// user would.
func itLifecycle(t *testing.T, p *itProc) []itStep {
	t.Helper()
	const (
		alias = "it-matrix"
		dest1 = "https://example.com/first"
		dest2 = "https://example.com/second"
	)
	var steps []itStep
	rec := func(name string, r itResp, detail string) itResp {
		steps = append(steps, itStep{Name: name, Status: r.Status, Code: r.ErrCode, Detail: detail})
		return r
	}
	str := func(m map[string]any, k string) string {
		v, _ := m[k].(string)
		return v
	}

	s, signin := itSignin(t, p.Base, itAdminEmail, itAdminPassword)
	rec("signin", signin, "")

	created := rec("create alias", s.itDo(http.MethodPost, "/v1/codes", map[string]any{"destination": dest1, "alias": alias}, nil), "")
	steps[len(steps)-1].Detail = fmt.Sprintf("short_code=%s version=%v", str(created.JSON, "short_code"), created.JSON["version"])
	id := str(created.JSON, "id")
	if id == "" {
		t.Fatalf("create alias: no id in %d %s", created.Status, created.Body)
	}

	scan := func(name string) {
		r := s.itDo(http.MethodGet, "/r/"+alias, nil, nil)
		rec(name, r, fmt.Sprintf("Location=%s Cache-Control=%q", r.Header.Get("Location"), r.Header.Get("Cache-Control")))
	}
	scan("scan #1")

	patched := rec("patch If-Match", s.itDo(http.MethodPatch, "/v1/codes/"+id, map[string]any{"destination": dest2}, map[string]string{"If-Match": `"1"`}), "")
	steps[len(steps)-1].Detail = fmt.Sprintf("destination=%s version=%v", str(patched.JSON, "destination"), patched.JSON["version"])

	scan("scan #2")

	an := itWaitAnalyticsTotal(s, id, 2, 15*time.Second)
	rec("analytics", an, fmt.Sprintf("total=%d", itAnalyticsTotal(an)))

	tok := rec("create token", s.itDo(http.MethodPost, "/v1/tokens", map[string]any{"name": "it-matrix"}, nil), "")
	tokID := str(tok.JSON, "id")
	steps[len(steps)-1].Detail = fmt.Sprintf("has_secret=%t", str(tok.JSON, "secret") != "")
	rec("revoke token", s.itDo(http.MethodDelete, "/v1/tokens/"+tokID, nil, nil), "")

	rec("delete code", s.itDo(http.MethodDelete, "/v1/codes/"+id, nil, nil), "")
	gone := rec("get deleted", s.itDo(http.MethodGet, "/v1/codes/"+id, nil, nil), "")
	steps[len(steps)-1].Detail = "state=" + str(gone.JSON, "state")
	rec("scan deleted", s.itDo(http.MethodGet, "/r/"+alias, nil, nil), "")

	// FR-018: a deleted code's alias stays reserved.
	retaken := rec("recreate alias", s.itDo(http.MethodPost, "/v1/codes", map[string]any{"destination": dest1, "alias": alias}, nil), "")
	if d, ok := retaken.JSON["error"].(map[string]any); ok {
		if det, ok := d["details"].(map[string]any); ok {
			steps[len(steps)-1].Detail = "alias=" + str(det, "alias")
		}
	}

	// Spec 002 / SC-105: a direct code persists its mode, offers no scan address, and
	// refuses a destination change with the stable code — identically on every backend.
	direct := rec("create direct", s.itDo(http.MethodPost, "/v1/codes", map[string]any{"destination": dest1, "alias": alias + "-direct", "mode": "direct"}, nil), "")
	_, hasScanURL := direct.JSON["scan_url"]
	steps[len(steps)-1].Detail = fmt.Sprintf("mode=%s has_scan_url=%t", str(direct.JSON, "mode"), hasScanURL)
	directID := str(direct.JSON, "id")
	immutable := rec("patch direct", s.itDo(http.MethodPatch, "/v1/codes/"+directID, map[string]any{"destination": dest2}, nil), "")
	if d, ok := immutable.JSON["error"].(map[string]any); ok {
		if det, ok := d["details"].(map[string]any); ok {
			steps[len(steps)-1].Detail = "mode=" + str(det, "mode")
		}
	}
	return steps
}

// itWantLifecycle is the sequence every backend must produce. It is asserted directly
// for each backend (so sqlite alone still checks behaviour) AND backends are compared
// with each other (so a shared regression cannot hide behind a stale expectation).
var itWantLifecycle = []itStep{
	{"signin", 200, "", ""},
	{"create alias", 201, "", "short_code=it-matrix version=1"},
	{"scan #1", 302, "", `Location=https://example.com/first Cache-Control="` + itWantCacheControl + `"`},
	{"patch If-Match", 200, "", "destination=https://example.com/second version=2"},
	{"scan #2", 302, "", `Location=https://example.com/second Cache-Control="` + itWantCacheControl + `"`},
	{"analytics", 200, "", "total=2"},
	{"create token", 201, "", "has_secret=true"},
	{"revoke token", 204, "", ""},
	{"delete code", 204, "", ""},
	{"get deleted", 200, "", "state=deleted"},
	{"scan deleted", 200, "", ""},
	{"recreate alias", 409, "alias_taken", "alias=it-matrix"},
	{"create direct", 201, "", "mode=direct has_scan_url=false"},
	{"patch direct", 409, "direct_code_immutable", "mode=direct"},
}

func itDiffSteps(a, b []itStep) string {
	var sb strings.Builder
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var x, y string
		if i < len(a) {
			x = a[i].String()
		}
		if i < len(b) {
			y = b[i].String()
		}
		mark := "  "
		if x != y {
			mark = "!!"
		}
		fmt.Fprintf(&sb, "%s %-70s | %s\n", mark, x, y)
	}
	return sb.String()
}

// TestBackends_IdenticalLifecycle runs the matrix and asserts SC-010.
func TestBackends_IdenticalLifecycle(t *testing.T) {
	observed := map[string][]itStep{}
	for _, be := range itBackends {
		be := be
		t.Run(be.name, func(t *testing.T) {
			dir := t.TempDir()
			env := itDevAdminEnv()
			for k, v := range be.env(t, dir) {
				env[k] = v
			}
			p := itStartWithBase(t, dir, env)
			steps := itLifecycle(t, p)
			observed[be.name] = steps
			if diff := itDiffSteps(itWantLifecycle, steps); !itStepsEqual(itWantLifecycle, steps) {
				t.Errorf("%s lifecycle differs from expectation (want | got):\n%s\nstderr:\n%s", be.name, diff, p.Stderr.String())
			}
		})
	}

	ref, ok := observed[itBackends[0].name]
	if !ok {
		t.Fatalf("reference backend %s did not run", itBackends[0].name)
	}
	compared := 0
	for name, steps := range observed {
		if name == itBackends[0].name {
			continue
		}
		compared++
		if !itStepsEqual(ref, steps) {
			t.Errorf("SC-010 violated: %s and %s observed different sequences (%s | %s):\n%s",
				itBackends[0].name, name, itBackends[0].name, name, itDiffSteps(ref, steps))
		}
	}
	t.Logf("backends compared against %s: %d", itBackends[0].name, compared)
}

func itStepsEqual(a, b []itStep) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
