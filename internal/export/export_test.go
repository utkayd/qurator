package export_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/export"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

func seedUser(t *testing.T, st store.Store, email string, admin bool) *domain.User {
	t.Helper()
	u := &domain.User{ID: "u-" + email, Email: email, IsAdmin: admin, Source: domain.UserSourceLocal}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func seedCode(t *testing.T, st store.Store, userID, shortCode, dest string) *domain.Code {
	t.Helper()
	c := &domain.Code{
		ID: shortCode + "-id", ShortCode: shortCode, UserID: userID, Destination: dest,
		Styling: domain.Styling{FgColor: "#000000", BgColor: "#ffffff", ModuleShape: domain.ShapeSquare, SizePx: 512, ECLevel: domain.ECMedium, ECLevelEffective: domain.ECMedium},
	}
	if err := st.CreateCode(context.Background(), c); err != nil {
		t.Fatalf("CreateCode: %v", err)
	}
	return c
}

func TestWriteRead_RoundTrip(t *testing.T) {
	ctx := context.Background()
	src := storetest.NewMemStore()

	u := seedUser(t, src, "admin@example.com", true)
	tok := &domain.APIToken{ID: "tok-1", UserID: u.ID, Name: "ci", SecretHash: []byte("should-not-export")}
	if err := src.CreateToken(ctx, tok); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	var codes []*domain.Code
	for i := 0; i < 5; i++ {
		codes = append(codes, seedCode(t, src, u.ID, fmt.Sprintf("code%d", i), fmt.Sprintf("https://example.com/%d", i)))
	}

	hour := time.Now().UTC().Truncate(time.Hour)
	if err := src.InsertScanBatch(ctx, domain.ScanBatch{Rollups: []domain.RollupDelta{
		{CodeID: codes[0].ID, HourBucket: hour, Dimension: domain.DimTotal, Value: "", Count: 7},
	}}); err != nil {
		t.Fatalf("InsertScanBatch: %v", err)
	}

	var buf bytes.Buffer
	if err := export.Write(ctx, src, &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// The archive must contain exactly one JSONL file per entity plus the manifest, and
	// no api_tokens.jsonl row may carry the secret hash.
	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	names := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		names[hdr.Name] = true
		if hdr.Name == "api_tokens.jsonl" {
			body, _ := io.ReadAll(tr)
			if bytes.Contains(body, []byte("should-not-export")) {
				t.Fatalf("api_tokens.jsonl leaked the secret hash: %s", body)
			}
			if bytes.Contains(body, []byte("secret_hash")) {
				t.Fatalf("api_tokens.jsonl carries a secret_hash field at all: %s", body)
			}
		}
		if hdr.Name == "users.jsonl" {
			body, _ := io.ReadAll(tr)
			if bytes.Contains(body, []byte("password")) {
				t.Fatalf("users.jsonl leaked a password field: %s", body)
			}
		}
	}
	for _, want := range []string{"manifest.json", "users.jsonl", "api_tokens.jsonl", "codes.jsonl", "alias_reservations.jsonl", "scan_rollups.jsonl"} {
		if !names[want] {
			t.Errorf("archive missing %s", want)
		}
	}

	dst := storetest.NewMemStore()
	if err := export.Read(ctx, dst, bytes.NewReader(buf.Bytes()), false); err != nil {
		t.Fatalf("Read: %v", err)
	}

	n, err := dst.CountUsers(ctx)
	if err != nil || n != 1 {
		t.Fatalf("CountUsers = %d, %v; want 1", n, err)
	}
	got, _, err := dst.ListCodes(ctx, domain.CodeFilter{UserID: u.ID, Limit: 100})
	if err != nil {
		t.Fatalf("ListCodes: %v", err)
	}
	if len(got) != len(codes) {
		t.Fatalf("ListCodes returned %d codes, want %d", len(got), len(codes))
	}
	for _, c := range got {
		if c.Destination == "" || c.ShortCode == "" {
			t.Errorf("imported code %+v missing fields", c)
		}
	}

	res, err := dst.QueryAnalytics(ctx, domain.AnalyticsQuery{CodeID: codes[0].ID, From: hour.Add(-time.Hour), To: hour.Add(time.Hour), Bucket: domain.BucketHour})
	if err != nil {
		t.Fatalf("QueryAnalytics: %v", err)
	}
	if res.Total != 7 {
		t.Errorf("imported rollup total = %d, want 7", res.Total)
	}
}

func TestRead_RefusesNonEmptyStoreWithoutForce(t *testing.T) {
	ctx := context.Background()
	src := storetest.NewMemStore()
	seedUser(t, src, "a@example.com", true)
	var buf bytes.Buffer
	if err := export.Write(ctx, src, &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	dst := storetest.NewMemStore()
	seedUser(t, dst, "already-here@example.com", true)

	if err := export.Read(ctx, dst, bytes.NewReader(buf.Bytes()), false); err != export.ErrNotEmpty {
		t.Fatalf("Read without force = %v, want ErrNotEmpty", err)
	}
	// force bypasses the guard; it will still fail here because the destination email
	// collides, which is a legitimate downstream conflict, not the guard under test.
}

// TestWrite_FullExportThroughBaseInterface pins that any store.Store is fully
// exportable with nothing optional: every entity file is present, the manifest counts
// match what was created, deleted codes and released reservations are included, and a
// round trip restores the deleted code as deleted with its short code still reserved.
func TestWrite_FullExportThroughBaseInterface(t *testing.T) {
	ctx := context.Background()
	st := storetest.NewMemStore()
	u := seedUser(t, st, "admin@example.com", true)
	other := seedUser(t, st, "other@example.com", false)
	seedCode(t, st, u.ID, "live", "https://example.com/live")
	gone := seedCode(t, st, other.ID, "gone", "https://example.com/gone")
	if err := st.DeleteCode(ctx, gone.ID, other.ID); err != nil {
		t.Fatalf("DeleteCode: %v", err)
	}
	freed := seedCode(t, st, u.ID, "freed", "https://example.com/freed")
	if err := st.DeleteCode(ctx, freed.ID, u.ID); err != nil {
		t.Fatalf("DeleteCode: %v", err)
	}
	if err := st.ReleaseAlias(ctx, "freed"); err != nil {
		t.Fatalf("ReleaseAlias: %v", err)
	}
	hour := time.Now().UTC().Truncate(time.Hour)
	if err := st.InsertScanBatch(ctx, domain.ScanBatch{Rollups: []domain.RollupDelta{
		{CodeID: gone.ID, HourBucket: hour, Dimension: domain.DimTotal, Count: 3},
		{CodeID: gone.ID, HourBucket: hour, Dimension: domain.DimIsBot, Value: "false", Count: 3},
	}}); err != nil {
		t.Fatalf("InsertScanBatch: %v", err)
	}

	var buf bytes.Buffer
	if err := export.Write(ctx, st, &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next: %v", err)
	}
	if hdr.Name != "manifest.json" {
		t.Fatalf("first entry = %s, want manifest.json", hdr.Name)
	}
	var m export.Manifest
	if err := json.NewDecoder(tr).Decode(&m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	want := map[string]int64{"users": 2, "api_tokens": 0, "codes": 3, "alias_reservations": 3, "scan_rollups": 2}
	for k, n := range want {
		if m.Entities[k] != n {
			t.Errorf("manifest.entities[%s] = %d, want %d", k, m.Entities[k], n)
		}
	}
	lines := map[string]int{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		body, _ := io.ReadAll(tr)
		lines[hdr.Name] = bytes.Count(body, []byte("\n"))
		if hdr.Name == "alias_reservations.jsonl" && !bytes.Contains(body, []byte(`"released_at"`)) {
			t.Errorf("released reservation not exported: %s", body)
		}
	}
	for name, n := range map[string]int{"users.jsonl": 2, "api_tokens.jsonl": 0, "codes.jsonl": 3, "alias_reservations.jsonl": 3, "scan_rollups.jsonl": 2} {
		if lines[name] != n {
			t.Errorf("%s has %d rows, want %d", name, lines[name], n)
		}
	}

	dst := storetest.NewMemStore()
	if err := export.Read(ctx, dst, bytes.NewReader(buf.Bytes()), false); err != nil {
		t.Fatalf("Read: %v", err)
	}
	got, err := dst.GetCodeByID(ctx, gone.ID, other.ID)
	if err != nil {
		t.Fatalf("GetCodeByID(gone) after import: %v", err)
	}
	if got.State != domain.CodeDeleted || got.DeletedAt == nil {
		t.Errorf("imported deleted code: state=%q deletedAt=%v, want deleted", got.State, got.DeletedAt)
	}
	if ok, _ := dst.IsAliasAvailable(ctx, "gone"); ok {
		t.Errorf("deleted code's short code is available after import; want reserved (FR-018)")
	}
	if ok, _ := dst.IsAliasAvailable(ctx, "freed"); !ok {
		t.Errorf("released short code is unavailable after import; want free")
	}
	res, err := dst.QueryAnalytics(ctx, domain.AnalyticsQuery{CodeID: gone.ID, From: hour, To: hour.Add(time.Hour), Bucket: domain.BucketHour})
	if err != nil || res.Total != 3 {
		t.Errorf("rollups for deleted code: total=%d err=%v, want 3", res.Total, err)
	}
}

// TestWrite_AllocationsDoNotBlowUpWithRowCount is a proxy for "Write does not hold the
// whole table in memory": if it did, growing the store 100x would grow retained buffers
// (and therefore allocations attributable to buffer growth, not just the unavoidable
// one-encode-per-row cost) faster than linearly. Comparing allocs-per-row between a small
// and a 100x-larger store catches an accidental switch to slice-of-everything.
func TestWrite_AllocationsDoNotBlowUpWithRowCount(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ctx := context.Background()

	build := func(n int) store.Store {
		st := storetest.NewMemStore()
		u := seedUser(t, st, "bulk@example.com", true)
		for i := 0; i < n; i++ {
			seedCode(t, st, u.ID, fmt.Sprintf("bulk-%06d", i), "https://example.com/x")
		}
		return st
	}

	const small, large = 200, 20000
	smallStore := build(small)
	largeStore := build(large)

	allocsSmall := testing.AllocsPerRun(1, func() {
		if err := export.Write(ctx, smallStore, io.Discard); err != nil {
			t.Fatalf("Write(small): %v", err)
		}
	})
	allocsLarge := testing.AllocsPerRun(1, func() {
		if err := export.Write(ctx, largeStore, io.Discard); err != nil {
			t.Fatalf("Write(large): %v", err)
		}
	})

	perRowSmall := allocsSmall / small
	perRowLarge := allocsLarge / large
	t.Logf("allocs/row: small=%v large=%v", perRowSmall, perRowLarge)
	if perRowLarge > perRowSmall*4+4 {
		t.Fatalf("allocations per row grew from %v to %v across a 100x row-count increase; Write likely buffers the whole table", perRowSmall, perRowLarge)
	}
}
