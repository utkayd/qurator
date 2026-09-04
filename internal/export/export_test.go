package export_test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/export"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/storetest"
)

// fakeStore wraps the shared memstore fixture and additionally implements
// export.Exporter, standing in for a real driver from Stage 3 that has closed the
// interface gap described in doc.go. storetest.NewMemStore itself deliberately does NOT
// implement Exporter (it is owned by another stream) — that is exercised separately in
// TestWrite_NoExporter_ManifestOnly below.
type fakeStore struct {
	store.Store
	mu           sync.Mutex
	users        []*domain.User
	reservations []export.ReservationRecord
	rollups      []domain.RollupDelta
}

func newFakeStore() *fakeStore {
	return &fakeStore{Store: storetest.NewMemStore()}
}

func (f *fakeStore) CreateUser(ctx context.Context, u *domain.User) error {
	if err := f.Store.CreateUser(ctx, u); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *u
	f.users = append(f.users, &cp)
	return nil
}

func (f *fakeStore) CreateCode(ctx context.Context, c *domain.Code) error {
	if err := f.Store.CreateCode(ctx, c); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reservations = append(f.reservations, export.ReservationRecord{
		ShortCode: c.ShortCode, CodeID: c.ID, ReservedAt: c.CreatedAt,
	})
	return nil
}

func (f *fakeStore) InsertScanBatch(ctx context.Context, b domain.ScanBatch) error {
	if err := f.Store.InsertScanBatch(ctx, b); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollups = append(f.rollups, b.Rollups...)
	return nil
}

func (f *fakeStore) ExportUsers(ctx context.Context, fn func(*domain.User) error) error {
	f.mu.Lock()
	users := append([]*domain.User(nil), f.users...)
	f.mu.Unlock()
	for _, u := range users {
		if err := fn(u); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (f *fakeStore) ExportReservations(ctx context.Context, fn func(export.ReservationRecord) error) error {
	f.mu.Lock()
	rs := append([]export.ReservationRecord(nil), f.reservations...)
	f.mu.Unlock()
	for _, r := range rs {
		if err := fn(r); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (f *fakeStore) ExportRollups(ctx context.Context, fn func(domain.RollupDelta) error) error {
	f.mu.Lock()
	rs := append([]domain.RollupDelta(nil), f.rollups...)
	f.mu.Unlock()
	for _, r := range rs {
		if err := fn(r); err != nil {
			return err
		}
	}
	return ctx.Err()
}

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
	src := newFakeStore()

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
	src := newFakeStore()
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

// TestWrite_NoExporter_ManifestOnly documents the real degradation: the shared memstore
// fixture used across every other package's tests does NOT implement export.Exporter, so
// exporting against it (as any Stage 2 stream that has not adopted Exporter would)
// produces a manifest recording every entity as omitted, never a partial or misleading
// dump.
func TestWrite_NoExporter_ManifestOnly(t *testing.T) {
	ctx := context.Background()
	st := storetest.NewMemStore()
	seedUser(t, st, "admin@example.com", true)

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
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("expected manifest.json to be the only entry, got another: %v", err)
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

	build := func(n int) *fakeStore {
		st := newFakeStore()
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
