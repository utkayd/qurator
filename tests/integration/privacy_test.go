package integration

// T106 — privacy assertion (SC-012, FR-022, FR-025, Constitution Principle IV).
//
// After 1,000 scans that each carry a distinct client address (RemoteAddr and
// X-Forwarded-For, v4 and v6), a distinct User-Agent, and a Referer whose query string
// holds a secret, every table of the real database is dumped and inspected:
//
//  1. no column NAME suggests an address or a location (ip / addr / geo / country /
//     city / lat / lon), matched on underscore-separated words so `description` and
//     `zip` cannot false-positive;
//  2. no TEXT/BLOB VALUE in any row looks like an IPv4 or IPv6 literal, and none carries
//     the referrer's query string (`SECRET…`, `token=`);
//  3. scan_events.referrer_host holds bare hosts only — no scheme, path, port or query.
//
// The SQLite run is unconditional. The PostgreSQL run needs QURATOR_TEST_PG_DSN
// (standing rule 10: `go test ./...` stays Docker-free) and skips otherwise.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/analytics"
	"github.com/utkayd/qurator/internal/codes"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/httpapi"
	"github.com/utkayd/qurator/internal/httpapi/public"
	"github.com/utkayd/qurator/internal/store"

	// Driver registration, exactly as cmd/qurator does it. Importing these also
	// registers the "sqlite" (modernc) and "pgx" database/sql drivers the raw dumps use.
	_ "github.com/utkayd/qurator/internal/store/postgres"
	_ "github.com/utkayd/qurator/internal/store/sqlite"
)

const pvScans = 1000

var (
	// pvIPv4 matches a dotted quad anywhere in a value.
	pvIPv4 = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	// pvIPv6 matches either a `::`-compressed literal or four-plus hex groups. Two-colon
	// forms are deliberately excluded so stored timestamps (`15:04:05`) do not match;
	// TestPrivacy_RegexSanity proves the pattern still catches the addresses we send.
	pvIPv6 = regexp.MustCompile(`(?i)(?:(?:[0-9a-f]{1,4}:){3,7}[0-9a-f]{1,4}|[0-9a-f]{0,4}::[0-9a-f]{0,4})`)
)

// pvForbiddenWords are column-name words that would indicate an address or a location.
// Matching is on whole `_`-separated words so `description`, `zip` and `is_admin` pass.
var pvForbiddenWords = map[string]bool{
	"ip": true, "ipaddr": true, "ipaddress": true, "addr": true, "address": true,
	"geo": true, "geoip": true, "country": true, "city": true, "region": true,
	"lat": true, "latitude": true, "lon": true, "lng": true, "longitude": true,
	"remote": true, "client": true, "xff": true, "forwarded": true,
}

// pvForbiddenSubstrings are unambiguous fragments checked anywhere in a column name.
var pvForbiddenSubstrings = []string{"ipaddr", "addr", "geo", "country"}

func pvColumnLooksPrivate(col string) bool {
	lc := strings.ToLower(col)
	for _, w := range strings.Split(lc, "_") {
		if pvForbiddenWords[w] {
			return true
		}
	}
	for _, s := range pvForbiddenSubstrings {
		if strings.Contains(lc, s) {
			return true
		}
	}
	return false
}

// pvRemoteAddr / pvXFF / pvUA / pvReferer generate the per-scan inputs whose absence
// from the database the test asserts. Every one is distinct per n so a leak of even a
// single event is visible.
func pvRemoteAddr(n int) string {
	if n%3 == 0 {
		return fmt.Sprintf("[2001:db8::%x]:1234", n+1)
	}
	return fmt.Sprintf("203.0.113.%d:1234", n%256)
}

func pvXFF(n int) string {
	return fmt.Sprintf("198.51.100.%d, 2001:db8:1::%x, 192.0.2.%d", n%256, n+1, (n+7)%256)
}

func pvUA(n int) string {
	switch n % 4 {
	case 0:
		return fmt.Sprintf("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1 SECRET%d", n)
	case 1:
		return fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 SECRET%d", n)
	case 2:
		return fmt.Sprintf("Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36 SECRET%d", n)
	default:
		return fmt.Sprintf("curl/8.4.0 SECRET%d", n)
	}
}

func pvReferer(n int) string {
	return fmt.Sprintf("https://ref%d.example.org:8443/private/path/%d?token=SECRET%d&session=SECRET%d", n%17, n, n, n+1)
}

// pvRunScans opens driver/dsn, migrates, seeds one user and one active code, wires the
// analytics recorder and public handler exactly as cmd/qurator does, and fires pvScans
// redirects through the real router with varied client identifiers. It closes the
// recorder and the store before returning so the on-disk state is final.
func pvRunScans(t *testing.T, driver, dsn string) (shortCode string) {
	t.Helper()
	ctx := context.Background()

	st, err := store.Open(ctx, driver, dsn)
	if err != nil {
		t.Fatalf("store.Open(%s): %v", driver, err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	user := &domain.User{ID: domain.NewID("usr"), Email: fmt.Sprintf("privacy-%s@example.test", domain.RandomCrockford(8)), Source: domain.UserSourceLocal}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	shortCode = "pv" + strings.ToLower(domain.RandomCrockford(10))
	now := time.Now().UTC().Truncate(time.Microsecond)
	code := &domain.Code{
		ID: domain.NewCodeID(), ShortCode: shortCode, UserID: user.ID,
		Destination: "https://destination.example.net/landing", State: domain.CodeActive,
		Styling: domain.Styling{
			ID: domain.NewID("sty"), FgColor: "#000000", BgColor: "#ffffff", ModuleShape: domain.ShapeSquare,
			MarginModules: 4, SizePx: 256, ECLevel: domain.ECMedium, ECLevelEffective: domain.ECMedium,
		},
		BlobKey: "codes/" + shortCode + ".png", BlobETag: `"` + shortCode + `"`,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateCode(ctx, code); err != nil {
		t.Fatalf("CreateCode: %v", err)
	}

	recorder := analytics.NewRecorder(st, analytics.Options{
		BufferSize: 4 * pvScans, BatchSize: 200, FlushInterval: 20 * time.Millisecond,
	}, &stCounter{})
	classifier := analytics.NewClassifier()
	codeSvc := codes.NewService(st, nil, nil, codes.NewCache(), codes.Config{BaseURL: "http://qurator.test"})
	handler := public.NewPublicHandler(public.Options{
		Resolver: codeSvc,
		Recorder: recorder,
		Classify: func(ua string) (string, domain.DeviceCategory, bool) {
			c := classifier.Classify(ua)
			return c.UAFamily, c.DeviceCategory, c.IsBot
		},
	})
	router := httpapi.NewRouter(httpapi.Handlers{Public: handler}, httpapi.Options{})

	for n := 0; n < pvScans; n++ {
		req := httptest.NewRequest(http.MethodGet, "/r/"+shortCode, nil)
		req.RemoteAddr = pvRemoteAddr(n)
		req.Header.Set("X-Forwarded-For", pvXFF(n))
		req.Header.Set("X-Real-IP", pvRemoteAddr(n))
		req.Header.Set("User-Agent", pvUA(n))
		req.Header.Set("Referer", pvReferer(n))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("scan %d: status %d, want 302", n, rec.Code)
		}
	}

	closeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := recorder.Close(closeCtx); err != nil {
		t.Fatalf("recorder.Close: %v", err)
	}
	if u := recorder.Unflushed(); u != 0 {
		t.Fatalf("recorder.Unflushed() = %d after clean Close, want 0", u)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}
	return shortCode
}

// pvTable is one table and its column names, dialect-independent.
type pvTable struct {
	name    string
	columns []string
}

func pvListSQLite(t *testing.T, db *sql.DB) []pvTable {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	var out []pvTable
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, pvTable{name: name})
	}
	for i := range out {
		crows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, out[i].name))
		if err != nil {
			t.Fatalf("table_info(%s): %v", out[i].name, err)
		}
		for crows.Next() {
			var cid int
			var name, typ string
			var notnull int
			var dflt sql.NullString
			var pk int
			if err := crows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			out[i].columns = append(out[i].columns, name)
		}
		crows.Close()
	}
	return out
}

func pvListPostgres(t *testing.T, db *sql.DB) []pvTable {
	t.Helper()
	rows, err := db.Query(`SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema() ORDER BY table_name, ordinal_position`)
	if err != nil {
		t.Fatalf("information_schema.columns: %v", err)
	}
	defer rows.Close()
	var out []pvTable
	for rows.Next() {
		var tbl, col string
		if err := rows.Scan(&tbl, &col); err != nil {
			t.Fatal(err)
		}
		if len(out) == 0 || out[len(out)-1].name != tbl {
			out = append(out, pvTable{name: tbl})
		}
		out[len(out)-1].columns = append(out[len(out)-1].columns, col)
	}
	return out
}

// pvAssertDatabaseClean runs the three assertions against every table db exposes.
func pvAssertDatabaseClean(t *testing.T, db *sql.DB, tables []pvTable, quote func(string) string) {
	t.Helper()
	if len(tables) == 0 {
		t.Fatal("no tables found; the dump would be vacuous")
	}
	var textValues, scanRows int
	for _, tbl := range tables {
		if len(tbl.columns) == 0 {
			t.Fatalf("table %s reports no columns", tbl.name)
		}
		for _, col := range tbl.columns {
			if pvColumnLooksPrivate(col) {
				t.Errorf("table %s has column %q whose name suggests an address or location (FR-022/FR-025)", tbl.name, col)
			}
		}

		rows, err := db.Query(`SELECT * FROM ` + quote(tbl.name))
		if err != nil {
			t.Fatalf("dump %s: %v", tbl.name, err)
		}
		cols, _ := rows.Columns()
		refIdx := -1
		for i, c := range cols {
			if tbl.name == "scan_events" && c == "referrer_host" {
				refIdx = i
			}
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan %s: %v", tbl.name, err)
			}
			if tbl.name == "scan_events" {
				scanRows++
			}
			for i, v := range vals {
				var s string
				switch x := v.(type) {
				case string:
					s = x
				case []byte:
					s = string(x)
				default:
					continue // numbers, bools, times, NULL
				}
				textValues++
				where := fmt.Sprintf("%s.%s", tbl.name, cols[i])
				if pvIPv4.MatchString(s) {
					t.Errorf("%s contains an IPv4 literal: %q", where, s)
				}
				if pvIPv6.MatchString(s) {
					t.Errorf("%s contains an IPv6 literal: %q", where, s)
				}
				if strings.Contains(s, "SECRET") {
					t.Errorf("%s contains a referrer/UA secret: %q", where, s)
				}
				if strings.Contains(s, "token=") {
					t.Errorf("%s contains a query string: %q", where, s)
				}
				if i == refIdx && strings.ContainsAny(s, "/?:#@ ") {
					t.Errorf("scan_events.referrer_host is not a bare host: %q", s)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate %s: %v", tbl.name, err)
		}
		rows.Close()
	}
	if textValues == 0 {
		t.Fatal("no text values inspected; the dump would be vacuous")
	}
	if scanRows < pvScans {
		t.Fatalf("scan_events holds %d rows, want at least %d (events were not persisted, so the dump proves nothing)", scanRows, pvScans)
	}
	t.Logf("inspected %d tables, %d text values, %d scan_events rows", len(tables), textValues, scanRows)
}

// TestPrivacy_RegexSanity proves the detectors would catch what we send, so a passing
// dump means the data is absent rather than the regex being blind.
func TestPrivacy_RegexSanity(t *testing.T) {
	for n := 0; n < 10; n++ {
		host, _, _ := strings.Cut(strings.TrimPrefix(pvRemoteAddr(n), "["), "]:")
		host = strings.TrimSuffix(host, ":1234")
		if !pvIPv4.MatchString(host) && !pvIPv6.MatchString(host) {
			t.Errorf("neither regex matches remote address %q", host)
		}
		if !pvIPv4.MatchString(pvXFF(n)) || !pvIPv6.MatchString(pvXFF(n)) {
			t.Errorf("regexes do not both match X-Forwarded-For %q", pvXFF(n))
		}
	}
	for _, v6 := range []string{"2001:db8::1", "::1", "fe80::a:b:c:d", "2001:0db8:85a3:0000:0000:8a2e:0370:7334"} {
		if !pvIPv6.MatchString(v6) {
			t.Errorf("IPv6 regex misses %q", v6)
		}
	}
	// Stored timestamps and URLs must NOT trip the detectors.
	for _, benign := range []string{"2026-09-04T15:04:05.000000Z", "https://destination.example.net/landing", "codes/ab/cd/cod_x.png", "$argon2id$v=19$m=65536,t=3,p=4$abc$def"} {
		if pvIPv4.MatchString(benign) || pvIPv6.MatchString(benign) {
			t.Errorf("regex false-positives on benign value %q", benign)
		}
	}
	for _, col := range []string{"description", "zip", "is_admin", "styling_id", "referrer_host", "device_category"} {
		if pvColumnLooksPrivate(col) {
			t.Errorf("column check false-positives on %q", col)
		}
	}
	for _, col := range []string{"ip", "client_ip", "ip_address", "remote_addr", "geo_country", "country_code", "city", "latitude"} {
		if !pvColumnLooksPrivate(col) {
			t.Errorf("column check misses %q", col)
		}
	}
}

func TestPrivacy_NoAddressPersisted_SQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	pvRunScans(t, "sqlite", path)

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer db.Close()
	tables := pvListSQLite(t, db)
	pvAssertDatabaseClean(t, db, tables, func(s string) string { return fmt.Sprintf("%q", s) })
}

func TestPrivacy_NoAddressPersisted_Postgres(t *testing.T) {
	dsn := os.Getenv("QURATOR_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QURATOR_TEST_PG_DSN not set; skipping PostgreSQL privacy dump")
	}
	pvRunScans(t, "postgres", dsn)

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open raw postgres: %v", err)
	}
	defer db.Close()
	tables := pvListPostgres(t, db)
	pvAssertDatabaseClean(t, db, tables, func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` })
}
