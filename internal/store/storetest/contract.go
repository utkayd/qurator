package storetest

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
)

// RunStoreContract runs the single contract suite every store.Store driver must pass
// unmodified (Constitution Principle II). newStore must return a fresh, empty, migrated
// store on every call; the suite never shares state between subtests.
//
// The first twelve numbered requirements come from contracts/store.md §Store; Req13 pins
// the bulk-iteration walkers export depends on (FR-055); Req14 pins the code mode
// column (spec 002, FR-101/FR-108); Req15 pins client_ref uniqueness and batch atomicity
// (spec 003, FR-206/FR-207). The remaining
// subtests pin behaviours of the frozen interface that the numbered list leaves implicit
// (users, tokens, alias availability, listing filters).
func RunStoreContract(t *testing.T, newStore func(t *testing.T) store.Store) {
	t.Helper()

	t.Run("Req01_ShortCodeUniquenessIsCaseInsensitive", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		mustCode(t, s, u.ID, "spring-sale", true)

		c := newCode(u.ID, "Spring-Sale", true)
		err := s.CreateCode(ctx(t), c)
		if !errors.Is(err, store.ErrAliasTaken) {
			t.Fatalf("CreateCode(Spring-Sale) after spring-sale: got %v, want ErrAliasTaken", err)
		}
		// A generated (non-alias) code shares the same namespace.
		c = newCode(u.ID, "SPRING-SALE", false)
		if err := s.CreateCode(ctx(t), c); !errors.Is(err, store.ErrAliasTaken) {
			t.Fatalf("CreateCode(SPRING-SALE, generated): got %v, want ErrAliasTaken", err)
		}
	})

	t.Run("Req02_ShortCodeLookupIsCaseInsensitive", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		created := mustCode(t, s, u.ID, "spring-sale", true)

		for _, q := range []string{"spring-sale", "SPRING-SALE", "Spring-Sale"} {
			got, err := s.GetCodeByShortCode(ctx(t), q)
			if err != nil {
				t.Fatalf("GetCodeByShortCode(%q): %v", q, err)
			}
			if got.ID != created.ID {
				t.Fatalf("GetCodeByShortCode(%q): got id %q, want %q", q, got.ID, created.ID)
			}
			if got.ShortCode != "spring-sale" {
				t.Fatalf("GetCodeByShortCode(%q): stored short code %q, want lowercase %q", q, got.ShortCode, "spring-sale")
			}
		}
	})

	t.Run("Req03_DeletedAliasStaysReservedUntilReleased", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		c := mustCode(t, s, u.ID, "spring-sale", true)

		// A live code's alias cannot be released.
		if err := s.ReleaseAlias(ctx(t), "spring-sale"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("ReleaseAlias on live code: got %v, want ErrConflict", err)
		}
		if err := s.DeleteCode(ctx(t), c.ID, u.ID); err != nil {
			t.Fatalf("DeleteCode: %v", err)
		}
		got, err := s.GetCodeByID(ctx(t), c.ID, u.ID)
		if err != nil {
			t.Fatalf("GetCodeByID after delete: %v (soft delete must retain the row)", err)
		}
		if got.State != domain.CodeDeleted || got.DeletedAt == nil {
			t.Fatalf("after delete: state=%q deletedAt=%v, want deleted with DeletedAt set", got.State, got.DeletedAt)
		}
		// The redirect path must see deleted codes to serve the fallback destination.
		if bySC, err := s.GetCodeByShortCode(ctx(t), "spring-sale"); err != nil || bySC.State != domain.CodeDeleted {
			t.Fatalf("GetCodeByShortCode after delete: code=%v err=%v, want deleted row", bySC, err)
		}

		// FR-018: the alias is still taken, in any case.
		for _, sc := range []string{"spring-sale", "SPRING-SALE", "Spring-Sale"} {
			if err := s.CreateCode(ctx(t), newCode(u.ID, sc, true)); !errors.Is(err, store.ErrAliasTaken) {
				t.Fatalf("CreateCode(%q) after delete: got %v, want ErrAliasTaken", sc, err)
			}
			if ok, err := s.IsAliasAvailable(ctx(t), sc); err != nil || ok {
				t.Fatalf("IsAliasAvailable(%q) after delete: ok=%v err=%v, want false", sc, ok, err)
			}
		}

		// Unknown alias: nothing to release.
		if err := s.ReleaseAlias(ctx(t), "never-existed"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("ReleaseAlias(unknown): got %v, want ErrNotFound", err)
		}

		// Admin release makes it registrable again (case-insensitively).
		if err := s.ReleaseAlias(ctx(t), "SPRING-SALE"); err != nil {
			t.Fatalf("ReleaseAlias after delete: %v", err)
		}
		if ok, err := s.IsAliasAvailable(ctx(t), "spring-sale"); err != nil || !ok {
			t.Fatalf("IsAliasAvailable after release: ok=%v err=%v, want true", ok, err)
		}
		// Release is not idempotent: a second release finds nothing reserved.
		if err := s.ReleaseAlias(ctx(t), "spring-sale"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("second ReleaseAlias: got %v, want ErrNotFound", err)
		}
		c2 := newCode(u.ID, "Spring-Sale", true)
		if err := s.CreateCode(ctx(t), c2); err != nil {
			t.Fatalf("CreateCode after release: %v", err)
		}
		got, err = s.GetCodeByShortCode(ctx(t), "spring-sale")
		if err != nil || got.ID != c2.ID {
			t.Fatalf("lookup after re-registration: id=%q err=%v, want %q", got.ID, err, c2.ID)
		}
		// The re-registered alias is reserved again once its new owner is deleted.
		if err := s.DeleteCode(ctx(t), c2.ID, u.ID); err != nil {
			t.Fatalf("DeleteCode(c2): %v", err)
		}
		if err := s.CreateCode(ctx(t), newCode(u.ID, "spring-sale", true)); !errors.Is(err, store.ErrAliasTaken) {
			t.Fatalf("CreateCode after second delete: got %v, want ErrAliasTaken", err)
		}
	})

	t.Run("Req04_MissingRowsReturnErrNotFound", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")

		if got, err := s.GetUserByID(ctx(t), "usr_missing"); !errors.Is(err, store.ErrNotFound) || got != nil {
			t.Fatalf("GetUserByID(missing): (%v, %v), want (nil, ErrNotFound)", got, err)
		}
		if got, err := s.GetUserByEmail(ctx(t), "nobody@example.com"); !errors.Is(err, store.ErrNotFound) || got != nil {
			t.Fatalf("GetUserByEmail(missing): (%v, %v), want (nil, ErrNotFound)", got, err)
		}
		if _, err := s.BumpTokenVersion(ctx(t), "usr_missing"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("BumpTokenVersion(missing): %v, want ErrNotFound", err)
		}
		if got, err := s.GetTokenByID(ctx(t), "tok_missing"); !errors.Is(err, store.ErrNotFound) || got != nil {
			t.Fatalf("GetTokenByID(missing): (%v, %v), want (nil, ErrNotFound)", got, err)
		}
		if err := s.RevokeToken(ctx(t), "tok_missing", u.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("RevokeToken(missing): %v, want ErrNotFound", err)
		}
		if err := s.TouchTokenLastUsed(ctx(t), "tok_missing", time.Now()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("TouchTokenLastUsed(missing): %v, want ErrNotFound", err)
		}
		if got, err := s.GetCodeByShortCode(ctx(t), "nope"); !errors.Is(err, store.ErrNotFound) || got != nil {
			t.Fatalf("GetCodeByShortCode(missing): (%v, %v), want (nil, ErrNotFound)", got, err)
		}
		if got, err := s.GetCodeByID(ctx(t), "cod_missing", u.ID); !errors.Is(err, store.ErrNotFound) || got != nil {
			t.Fatalf("GetCodeByID(missing): (%v, %v), want (nil, ErrNotFound)", got, err)
		}
		if err := s.UpdateDestination(ctx(t), "cod_missing", u.ID, "https://x.example", 1); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("UpdateDestination(missing): %v, want ErrNotFound", err)
		}
		if err := s.SetCodeState(ctx(t), "cod_missing", u.ID, domain.CodeDisabled); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("SetCodeState(missing): %v, want ErrNotFound", err)
		}
		if err := s.DeleteCode(ctx(t), "cod_missing", u.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("DeleteCode(missing): %v, want ErrNotFound", err)
		}
		if err := s.ReleaseAlias(ctx(t), "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("ReleaseAlias(missing): %v, want ErrNotFound", err)
		}
	})

	t.Run("Req05_OwnershipIsolationReturnsErrNotFound", func(t *testing.T) {
		s := newStore(t)
		owner := mustUser(t, s, "owner@example.com")
		other := mustUser(t, s, "other@example.com")
		c := mustCode(t, s, owner.ID, "owned", true)

		if got, err := s.GetCodeByID(ctx(t), c.ID, other.ID); !errors.Is(err, store.ErrNotFound) || got != nil {
			t.Fatalf("GetCodeByID as non-owner: (%v, %v), want (nil, ErrNotFound)", got, err)
		}
		if err := s.UpdateDestination(ctx(t), c.ID, other.ID, "https://evil.example", c.Version); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("UpdateDestination as non-owner: %v, want ErrNotFound", err)
		}
		if err := s.SetCodeState(ctx(t), c.ID, other.ID, domain.CodeDisabled); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("SetCodeState as non-owner: %v, want ErrNotFound", err)
		}
		if err := s.DeleteCode(ctx(t), c.ID, other.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("DeleteCode as non-owner: %v, want ErrNotFound", err)
		}
		// Nothing above may have changed the row.
		got, err := s.GetCodeByID(ctx(t), c.ID, owner.ID)
		if err != nil {
			t.Fatalf("GetCodeByID as owner: %v", err)
		}
		if got.Destination != c.Destination || got.State != domain.CodeActive || got.Version != c.Version {
			t.Fatalf("row mutated by non-owner: %+v", got)
		}
		// Non-owner listing does not see it either.
		items, _, err := s.ListCodes(ctx(t), domain.CodeFilter{UserID: other.ID, Limit: 10})
		if err != nil || len(items) != 0 {
			t.Fatalf("ListCodes as non-owner: %d items, err=%v; want 0", len(items), err)
		}
	})

	t.Run("Req06_OptimisticConcurrencyWithinOneSecond", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		c := mustCode(t, s, u.ID, "race", true)
		expected := c.Version

		// Both calls are issued back-to-back so they fall within the same second: a
		// driver that compares timestamps instead of the version counter will let both
		// through.
		err1 := s.UpdateDestination(ctx(t), c.ID, u.ID, "https://winner.example", expected)
		err2 := s.UpdateDestination(ctx(t), c.ID, u.ID, "https://loser.example", expected)
		if err1 != nil {
			t.Fatalf("first UpdateDestination: %v", err1)
		}
		if !errors.Is(err2, store.ErrConflict) {
			t.Fatalf("second UpdateDestination with stale version: %v, want ErrConflict", err2)
		}
		got, err := s.GetCodeByID(ctx(t), c.ID, u.ID)
		if err != nil {
			t.Fatalf("GetCodeByID: %v", err)
		}
		if got.Version != expected+1 {
			t.Fatalf("version after one successful update: %d, want %d", got.Version, expected+1)
		}
		if got.Destination != "https://winner.example" {
			t.Fatalf("destination: %q, want the winner's", got.Destination)
		}
		if !got.UpdatedAt.After(c.UpdatedAt) && !got.UpdatedAt.Equal(c.UpdatedAt) {
			t.Fatalf("UpdatedAt went backwards: %v < %v", got.UpdatedAt, c.UpdatedAt)
		}
		// The new version is usable for the next edit.
		if err := s.UpdateDestination(ctx(t), c.ID, u.ID, "https://next.example", got.Version); err != nil {
			t.Fatalf("UpdateDestination with fresh version: %v", err)
		}
		got, err = s.GetCodeByID(ctx(t), c.ID, u.ID)
		if err != nil || got.Version != expected+2 {
			t.Fatalf("version after second update: %d err=%v, want %d", got.Version, err, expected+2)
		}
	})

	t.Run("Req07_InsertScanBatchIsAtomic", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		c := mustCode(t, s, u.ID, "atomic", true)
		at := time.Date(2026, 8, 10, 12, 30, 0, 0, time.UTC)

		events := []domain.ScanEvent{
			scan(c.ID, at, "Chrome", domain.DeviceMobile, "example.com", false),
			scan(c.ID, at.Add(time.Minute), "Safari", domain.DeviceDesktop, "", false),
			scan("cod_does_not_exist", at, "Chrome", domain.DeviceMobile, "example.com", false),
		}
		batch := domain.ScanBatch{Events: events, Rollups: BuildRollups(events)}
		if err := s.InsertScanBatch(ctx(t), batch); err == nil {
			t.Fatalf("InsertScanBatch with a bad row: want error, got nil")
		}
		res := mustQuery(t, s, c.ID, at.Add(-time.Hour), at.Add(2*time.Hour), domain.BucketHour)
		if res.Total != 0 {
			t.Fatalf("valid events from a failed batch were persisted: total=%d, want 0", res.Total)
		}
		for dim, vals := range res.Breakdowns {
			if sum(vals) != 0 {
				t.Fatalf("rollups from a failed batch were persisted: %s=%v", dim, vals)
			}
		}

		// An empty batch is a valid no-op.
		if err := s.InsertScanBatch(ctx(t), domain.ScanBatch{}); err != nil {
			t.Fatalf("InsertScanBatch(empty): %v", err)
		}
		// The same valid events succeed on their own, proving the earlier failure was the
		// bad row and not the batch shape.
		good := events[:2]
		if err := s.InsertScanBatch(ctx(t), domain.ScanBatch{Events: good, Rollups: BuildRollups(good)}); err != nil {
			t.Fatalf("InsertScanBatch(valid): %v", err)
		}
		if res := mustQuery(t, s, c.ID, at.Add(-time.Hour), at.Add(2*time.Hour), domain.BucketHour); res.Total != 2 {
			t.Fatalf("total after valid batch: %d, want 2", res.Total)
		}
	})

	t.Run("Req08_RollupsEqualRawCounts", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		c := mustCode(t, s, u.ID, "rollups", true)
		other := mustCode(t, s, u.ID, "other-code", true)
		base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC) // a Monday

		var events []domain.ScanEvent
		families := []string{"Chrome", "Safari", "Firefox", "Googlebot"}
		devices := []domain.DeviceCategory{domain.DeviceMobile, domain.DeviceDesktop, domain.DeviceTablet, domain.DeviceBot}
		refs := []string{"", "example.com", "news.example", "example.com"}
		for i := 0; i < 40; i++ {
			at := base.Add(time.Duration(i) * 97 * time.Minute) // spreads across hours and days
			events = append(events, scan(c.ID, at, families[i%4], devices[i%4], refs[i%4], i%4 == 3))
		}
		// Noise on another code must not leak into this code's numbers.
		events = append(events, scan(other.ID, base.Add(time.Hour), "Chrome", domain.DeviceMobile, "", false))
		// Split into two batches so the upsert path (existing rows) is exercised too.
		for _, part := range [][]domain.ScanEvent{events[:15], events[15:]} {
			if err := s.InsertScanBatch(ctx(t), domain.ScanBatch{Events: part, Rollups: BuildRollups(part)}); err != nil {
				t.Fatalf("InsertScanBatch: %v", err)
			}
		}

		from, to := base, base.Add(14*24*time.Hour)
		for _, bucket := range []domain.Bucket{domain.BucketHour, domain.BucketDay, domain.BucketWeek} {
			res := mustQuery(t, s, c.ID, from, to, bucket)
			if res.Total != 40 {
				t.Fatalf("[%s] total: %d, want 40", bucket, res.Total)
			}
			for _, dim := range []domain.Dimension{domain.DimUAFamily, domain.DimDeviceCategory, domain.DimReferrerHost, domain.DimIsBot} {
				vals, ok := res.Breakdowns[dim]
				if !ok {
					t.Fatalf("[%s] breakdown %q missing", bucket, dim)
				}
				if got := sum(vals); got != res.Total {
					t.Fatalf("[%s] breakdown %q sums to %d, total is %d", bucket, dim, got, res.Total)
				}
			}
			if got := seriesSum(res.Series); got != res.Total {
				t.Fatalf("[%s] series sums to %d, total is %d", bucket, got, res.Total)
			}
			for i, p := range res.Series {
				if p.Start.Before(from) || !p.Start.Before(to) {
					t.Fatalf("[%s] series point %d start %v outside [%v, %v)", bucket, i, p.Start, from, to)
				}
				if i > 0 && !res.Series[i-1].Start.Before(p.Start) {
					t.Fatalf("[%s] series not strictly ascending at %d", bucket, i)
				}
				if aligned := bucketStart(p.Start, bucket); !aligned.Equal(p.Start) {
					t.Fatalf("[%s] series point start %v not aligned (want %v)", bucket, p.Start, aligned)
				}
			}
		}
		// Exact per-value counts: each of the four rotations produced 10 events.
		res := mustQuery(t, s, c.ID, from, to, domain.BucketDay)
		if got := countOf(res.Breakdowns[domain.DimUAFamily], "Chrome"); got != 10 {
			t.Fatalf("ua_family Chrome: %d, want 10", got)
		}
		if got := countOf(res.Breakdowns[domain.DimReferrerHost], "example.com"); got != 20 {
			t.Fatalf("referrer_host example.com: %d, want 20", got)
		}
		if got := countOf(res.Breakdowns[domain.DimIsBot], "true"); got != 10 {
			t.Fatalf("is_bot true: %d, want 10", got)
		}
		// Range filtering: the first two hours hold events 0 (00:00) and 1 (01:37).
		if res := mustQuery(t, s, c.ID, base, base.Add(2*time.Hour), domain.BucketHour); res.Total != 2 {
			t.Fatalf("first-two-hours total: %d, want 2", res.Total)
		}
		// An empty range is a valid empty result, not an error.
		if res := mustQuery(t, s, c.ID, base.Add(-48*time.Hour), base.Add(-24*time.Hour), domain.BucketDay); res.Total != 0 || len(res.Series) != 0 {
			t.Fatalf("empty range: %+v, want zero", res)
		}
	})

	t.Run("Req09_TimestampsRoundTripInUTC", func(t *testing.T) {
		s := newStore(t)
		loc := time.FixedZone("UTC+2", 2*3600)
		// Microsecond precision is the common floor (TIMESTAMPTZ); sub-microsecond
		// digits are deliberately absent so both dialects must round-trip exactly.
		at := time.Date(2026, 7, 4, 13, 45, 6, 123456000, loc)
		last := at.Add(time.Hour)

		u := &domain.User{ID: newID("usr"), Email: "tz@example.com", PasswordHash: "x", Source: domain.UserSourceLocal, CreatedAt: at, LastLoginAt: &last}
		if err := s.CreateUser(ctx(t), u); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		gotU, err := s.GetUserByID(ctx(t), u.ID)
		if err != nil {
			t.Fatalf("GetUserByID: %v", err)
		}
		checkTime(t, "user.CreatedAt", gotU.CreatedAt, at)
		if gotU.LastLoginAt == nil {
			t.Fatalf("user.LastLoginAt nil after round-trip")
		}
		checkTime(t, "user.LastLoginAt", *gotU.LastLoginAt, last)

		c := newCode(u.ID, "tzcode", true)
		c.CreatedAt, c.UpdatedAt = at, at
		if err := s.CreateCode(ctx(t), c); err != nil {
			t.Fatalf("CreateCode: %v", err)
		}
		gotC, err := s.GetCodeByID(ctx(t), c.ID, u.ID)
		if err != nil {
			t.Fatalf("GetCodeByID: %v", err)
		}
		checkTime(t, "code.CreatedAt", gotC.CreatedAt, at)
		checkTime(t, "code.UpdatedAt", gotC.UpdatedAt, at)
		if gotC.DeletedAt != nil {
			t.Fatalf("code.DeletedAt: %v, want nil", gotC.DeletedAt)
		}

		exp := at.Add(48 * time.Hour)
		tok := &domain.APIToken{ID: newID("tok"), UserID: u.ID, Name: "t", SecretHash: []byte{1, 2, 3}, CreatedAt: at, ExpiresAt: &exp}
		if err := s.CreateToken(ctx(t), tok); err != nil {
			t.Fatalf("CreateToken: %v", err)
		}
		if err := s.TouchTokenLastUsed(ctx(t), tok.ID, last); err != nil {
			t.Fatalf("TouchTokenLastUsed: %v", err)
		}
		gotT, err := s.GetTokenByID(ctx(t), tok.ID)
		if err != nil {
			t.Fatalf("GetTokenByID: %v", err)
		}
		checkTime(t, "token.CreatedAt", gotT.CreatedAt, at)
		if gotT.ExpiresAt == nil || gotT.LastUsedAt == nil {
			t.Fatalf("token optional timestamps lost: expires=%v lastUsed=%v", gotT.ExpiresAt, gotT.LastUsedAt)
		}
		checkTime(t, "token.ExpiresAt", *gotT.ExpiresAt, exp)
		checkTime(t, "token.LastUsedAt", *gotT.LastUsedAt, last)
		if gotT.RevokedAt != nil {
			t.Fatalf("token.RevokedAt: %v, want nil", gotT.RevokedAt)
		}

		// Analytics buckets come back in UTC too.
		ev := []domain.ScanEvent{scan(c.ID, at, "Chrome", domain.DeviceMobile, "", false)}
		if err := s.InsertScanBatch(ctx(t), domain.ScanBatch{Events: ev, Rollups: BuildRollups(ev)}); err != nil {
			t.Fatalf("InsertScanBatch: %v", err)
		}
		res := mustQuery(t, s, c.ID, at.Add(-24*time.Hour), at.Add(24*time.Hour), domain.BucketHour)
		if res.Total != 1 || len(res.Series) != 1 {
			t.Fatalf("analytics: %+v, want one point", res)
		}
		checkTime(t, "series.Start", res.Series[0].Start, at.UTC().Truncate(time.Hour))
	})

	t.Run("Req10_PruneRespectsLimitAndKeepsRollups", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		c := mustCode(t, s, u.ID, "prune", true)
		old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		recent := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

		var events []domain.ScanEvent
		for i := 0; i < 10; i++ {
			events = append(events, scan(c.ID, old.Add(time.Duration(i)*time.Hour), "Chrome", domain.DeviceMobile, "", false))
		}
		for i := 0; i < 3; i++ {
			events = append(events, scan(c.ID, recent.Add(time.Duration(i)*time.Hour), "Chrome", domain.DeviceMobile, "", false))
		}
		if err := s.InsertScanBatch(ctx(t), domain.ScanBatch{Events: events, Rollups: BuildRollups(events)}); err != nil {
			t.Fatalf("InsertScanBatch: %v", err)
		}
		from, to := old.Add(-time.Hour), recent.Add(24*time.Hour)
		before := mustQuery(t, s, c.ID, from, to, domain.BucketDay)
		if before.Total != 13 {
			t.Fatalf("total before prune: %d, want 13", before.Total)
		}

		cutoff := old.Add(24 * time.Hour) // strictly after all 10 old events, before the 3 recent
		n, err := s.PruneScanEvents(ctx(t), cutoff, 4)
		if err != nil {
			t.Fatalf("PruneScanEvents(limit=4): %v", err)
		}
		if n != 4 {
			t.Fatalf("PruneScanEvents(limit=4) removed %d, want 4", n)
		}
		n, err = s.PruneScanEvents(ctx(t), cutoff, 100)
		if err != nil || n != 6 {
			t.Fatalf("PruneScanEvents(limit=100) removed %d err=%v, want the remaining 6", n, err)
		}
		n, err = s.PruneScanEvents(ctx(t), cutoff, 100)
		if err != nil || n != 0 {
			t.Fatalf("PruneScanEvents on empty: removed %d err=%v, want 0", n, err)
		}
		// Recent events are untouched by a cutoff in the past; prove it by pruning them
		// with a later cutoff.
		n, err = s.PruneScanEvents(ctx(t), recent.Add(24*time.Hour), 100)
		if err != nil || n != 3 {
			t.Fatalf("PruneScanEvents(recent) removed %d err=%v, want 3", n, err)
		}

		after := mustQuery(t, s, c.ID, from, to, domain.BucketDay)
		if after.Total != before.Total {
			t.Fatalf("rollups changed by prune: total %d -> %d", before.Total, after.Total)
		}
		for dim, vals := range before.Breakdowns {
			if sum(after.Breakdowns[dim]) != sum(vals) {
				t.Fatalf("breakdown %q changed by prune: %v -> %v", dim, vals, after.Breakdowns[dim])
			}
		}
		if seriesSum(after.Series) != seriesSum(before.Series) {
			t.Fatalf("series changed by prune")
		}
	})

	t.Run("Req11_MigrateIsIdempotentAndPreservesData", func(t *testing.T) {
		s := newStore(t) // already migrated by construction
		u := mustUser(t, s, "a@example.com")
		c := mustCode(t, s, u.ID, "persist", true)
		if err := s.Migrate(ctx(t)); err != nil {
			t.Fatalf("second Migrate: %v", err)
		}
		c2 := mustCode(t, s, u.ID, "persist-two", true)
		if err := s.Migrate(ctx(t)); err != nil {
			t.Fatalf("third Migrate: %v", err)
		}
		for _, id := range []string{c.ID, c2.ID} {
			if _, err := s.GetCodeByID(ctx(t), id, u.ID); err != nil {
				t.Fatalf("code %s lost across Migrate: %v", id, err)
			}
		}
		if _, err := s.GetUserByEmail(ctx(t), "a@example.com"); err != nil {
			t.Fatalf("user lost across Migrate: %v", err)
		}
		if err := s.Ping(ctx(t)); err != nil {
			t.Fatalf("Ping: %v", err)
		}
	})

	t.Run("Req12_PaginationIsStableUnderConcurrentInserts", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

		original := map[string]bool{}
		for i := 0; i < 5; i++ {
			c := newCode(u.ID, "page-"+string(rune('a'+i)), true)
			c.CreatedAt = base.Add(time.Duration(i) * time.Second)
			c.UpdatedAt = c.CreatedAt
			if err := s.CreateCode(ctx(t), c); err != nil {
				t.Fatalf("CreateCode: %v", err)
			}
			original[c.ID] = true
		}

		seen := map[string]int{}
		var order []string
		cursor := ""
		page := 0
		for {
			items, next, err := s.ListCodes(ctx(t), domain.CodeFilter{UserID: u.ID, Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatalf("ListCodes page %d: %v", page, err)
			}
			if len(items) > 2 {
				t.Fatalf("page %d returned %d items, limit is 2", page, len(items))
			}
			for _, it := range items {
				seen[it.ID]++
				order = append(order, it.ID)
			}
			if page == 0 {
				// Insert mid-pagination: one newer than everything, one sharing a
				// timestamp with an existing row to exercise the tiebreaker.
				fresh := newCode(u.ID, "page-fresh", true)
				fresh.CreatedAt = base.Add(time.Hour)
				fresh.UpdatedAt = fresh.CreatedAt
				if err := s.CreateCode(ctx(t), fresh); err != nil {
					t.Fatalf("CreateCode(fresh): %v", err)
				}
				tie := newCode(u.ID, "page-tie", true)
				tie.CreatedAt = base.Add(2 * time.Second)
				tie.UpdatedAt = tie.CreatedAt
				if err := s.CreateCode(ctx(t), tie); err != nil {
					t.Fatalf("CreateCode(tie): %v", err)
				}
			}
			if next == "" {
				break
			}
			cursor = next
			page++
			if page > 10 {
				t.Fatalf("pagination did not terminate")
			}
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("code %s returned %d times", id, n)
			}
		}
		for id := range original {
			if seen[id] != 1 {
				t.Fatalf("original code %s skipped", id)
			}
		}
		// Newest first, consistently, across pages.
		var all []*domain.Code
		for _, id := range order {
			c, err := s.GetCodeByID(ctx(t), id, u.ID)
			if err != nil {
				t.Fatalf("GetCodeByID(%s): %v", id, err)
			}
			all = append(all, c)
		}
		for i := 1; i < len(all); i++ {
			if all[i].CreatedAt.After(all[i-1].CreatedAt) {
				t.Fatalf("listing not newest-first at %d: %v after %v", i, all[i].CreatedAt, all[i-1].CreatedAt)
			}
		}
		// A full listing sees all seven, and deleted codes drop out.
		items, next, err := s.ListCodes(ctx(t), domain.CodeFilter{UserID: u.ID, Limit: 100})
		if err != nil || next != "" || len(items) != 7 {
			t.Fatalf("full listing: %d items next=%q err=%v, want 7 and no cursor", len(items), next, err)
		}
		if err := s.DeleteCode(ctx(t), items[0].ID, u.ID); err != nil {
			t.Fatalf("DeleteCode: %v", err)
		}
		items, _, err = s.ListCodes(ctx(t), domain.CodeFilter{UserID: u.ID, Limit: 100})
		if err != nil || len(items) != 6 {
			t.Fatalf("listing after delete: %d items err=%v, want 6", len(items), err)
		}
	})

	t.Run("Req13_ForEachStreamsEverything", func(t *testing.T) {
		s := newStore(t)
		ua := mustUser(t, s, "a@example.com")
		ub := mustUser(t, s, "b@example.com")
		live := mustCode(t, s, ua.ID, "live-one", true)
		gone := mustCode(t, s, ub.ID, "gone-one", false)
		freed := mustCode(t, s, ua.ID, "freed-one", true)
		if err := s.DeleteCode(ctx(t), gone.ID, ub.ID); err != nil {
			t.Fatalf("DeleteCode(gone): %v", err)
		}
		if err := s.DeleteCode(ctx(t), freed.ID, ua.ID); err != nil {
			t.Fatalf("DeleteCode(freed): %v", err)
		}
		if err := s.ReleaseAlias(ctx(t), "freed-one"); err != nil {
			t.Fatalf("ReleaseAlias(freed): %v", err)
		}
		hour := time.Now().UTC().Truncate(time.Hour)
		if err := s.InsertScanBatch(ctx(t), domain.ScanBatch{Rollups: []domain.RollupDelta{
			{CodeID: live.ID, HourBucket: hour, Dimension: domain.DimTotal, Value: "", Count: 2},
			{CodeID: live.ID, HourBucket: hour.Add(30 * time.Minute), Dimension: domain.DimTotal, Value: "", Count: 3}, // same hour: merged
			{CodeID: gone.ID, HourBucket: hour, Dimension: domain.DimIsBot, Value: "true", Count: 1},
		}}); err != nil {
			t.Fatalf("InsertScanBatch: %v", err)
		}

		// Users: every row once, and fn may call back into the store mid-walk.
		users := map[string]string{}
		if err := s.ForEachUser(ctx(t), func(u *domain.User) error {
			if _, dup := users[u.ID]; dup {
				t.Fatalf("ForEachUser visited %q twice", u.ID)
			}
			users[u.ID] = u.Email
			if _, err := s.GetUserByID(ctx(t), u.ID); err != nil {
				return err
			}
			return nil
		}); err != nil {
			t.Fatalf("ForEachUser: %v", err)
		}
		if len(users) != 2 || users[ua.ID] != "a@example.com" || users[ub.ID] != "b@example.com" {
			t.Fatalf("ForEachUser visited %v, want both users", users)
		}

		// Codes: all users, deleted rows included, with styling inlined.
		codes := map[string]*domain.Code{}
		if err := s.ForEachCode(ctx(t), func(c *domain.Code) error {
			if _, dup := codes[c.ID]; dup {
				t.Fatalf("ForEachCode visited %q twice", c.ID)
			}
			codes[c.ID] = c
			return nil
		}); err != nil {
			t.Fatalf("ForEachCode: %v", err)
		}
		if len(codes) != 3 {
			t.Fatalf("ForEachCode visited %d codes, want 3 (deleted included)", len(codes))
		}
		if c := codes[gone.ID]; c == nil || c.State != domain.CodeDeleted || c.DeletedAt == nil || c.UserID != ub.ID {
			t.Fatalf("ForEachCode(gone) = %+v, want the deleted row", c)
		}
		if c := codes[live.ID]; c == nil || c.Styling.ID != live.Styling.ID || c.ShortCode != "live-one" {
			t.Fatalf("ForEachCode(live) = %+v, want styling inlined and short code intact", c)
		}

		// Rollups: one aggregate row per (code, hour, dimension, value).
		type rk struct {
			code  string
			hour  time.Time
			dim   domain.Dimension
			value string
		}
		rollups := map[rk]int64{}
		if err := s.ForEachRollup(ctx(t), func(r domain.RollupDelta) error {
			k := rk{r.CodeID, r.HourBucket.UTC(), r.Dimension, r.Value}
			if _, dup := rollups[k]; dup {
				t.Fatalf("ForEachRollup visited %+v twice", k)
			}
			rollups[k] = r.Count
			return nil
		}); err != nil {
			t.Fatalf("ForEachRollup: %v", err)
		}
		if len(rollups) != 2 {
			t.Fatalf("ForEachRollup visited %d rows, want 2: %v", len(rollups), rollups)
		}
		if n := rollups[rk{live.ID, hour, domain.DimTotal, ""}]; n != 5 {
			t.Fatalf("ForEachRollup(live total) = %d, want 5 (two deltas merged)", n)
		}
		if n := rollups[rk{gone.ID, hour, domain.DimIsBot, "true"}]; n != 1 {
			t.Fatalf("ForEachRollup(gone is_bot) = %d, want 1", n)
		}

		// Reservations: one per short code ever registered, released ones included and
		// still pointing at the code that made them.
		res := map[string]domain.AliasReservation{}
		if err := s.ForEachReservation(ctx(t), func(r domain.AliasReservation) error {
			if _, dup := res[r.ShortCode]; dup {
				t.Fatalf("ForEachReservation visited %q twice", r.ShortCode)
			}
			res[r.ShortCode] = r
			return nil
		}); err != nil {
			t.Fatalf("ForEachReservation: %v", err)
		}
		if len(res) != 3 {
			t.Fatalf("ForEachReservation visited %d rows, want 3: %v", len(res), res)
		}
		for sc, want := range map[string]*domain.Code{"live-one": live, "gone-one": gone} {
			r, ok := res[sc]
			if !ok || r.CodeID != want.ID || r.ReleasedAt != nil || r.ReservedAt.IsZero() {
				t.Fatalf("ForEachReservation(%s) = %+v, want unreleased, code %q", sc, r, want.ID)
			}
		}
		if r := res["freed-one"]; r.CodeID != freed.ID || r.ReleasedAt == nil {
			t.Fatalf("ForEachReservation(freed-one) = %+v, want released, code %q", r, freed.ID)
		}

		// fn's error aborts the walk early and comes back unwrapped.
		sentinel := errors.New("stop here")
		calls := 0
		err := s.ForEachCode(ctx(t), func(*domain.Code) error {
			calls++
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("ForEachCode with failing fn: got %v, want the sentinel", err)
		}
		if calls != 1 {
			t.Fatalf("ForEachCode kept walking after fn failed: %d calls, want 1", calls)
		}
		calls = 0
		if err := s.ForEachUser(ctx(t), func(*domain.User) error { calls++; return sentinel }); !errors.Is(err, sentinel) || calls != 1 {
			t.Fatalf("ForEachUser with failing fn: err=%v calls=%d, want sentinel after 1 call", err, calls)
		}
		calls = 0
		if err := s.ForEachRollup(ctx(t), func(domain.RollupDelta) error { calls++; return sentinel }); !errors.Is(err, sentinel) || calls != 1 {
			t.Fatalf("ForEachRollup with failing fn: err=%v calls=%d, want sentinel after 1 call", err, calls)
		}
		calls = 0
		if err := s.ForEachReservation(ctx(t), func(domain.AliasReservation) error { calls++; return sentinel }); !errors.Is(err, sentinel) || calls != 1 {
			t.Fatalf("ForEachReservation with failing fn: err=%v calls=%d, want sentinel after 1 call", err, calls)
		}
	})

	t.Run("Req14_ModePersistsAndDefaultsDynamic", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")

		// Mode unset on input reads back as dynamic — the v1 behaviour, made explicit.
		unset := mustCode(t, s, u.ID, "mode-unset", true)
		if unset.Mode != domain.ModeDynamic {
			t.Fatalf("Mode unset on CreateCode: read back %q, want %q", unset.Mode, domain.ModeDynamic)
		}
		dyn := newCode(u.ID, "mode-dynamic", true)
		dyn.Mode = domain.ModeDynamic
		if err := s.CreateCode(ctx(t), dyn); err != nil {
			t.Fatalf("CreateCode(dynamic): %v", err)
		}
		dir := newCode(u.ID, "mode-direct", true)
		dir.Mode = domain.ModeDirect
		if err := s.CreateCode(ctx(t), dir); err != nil {
			t.Fatalf("CreateCode(direct): %v", err)
		}
		if dir.Mode != domain.ModeDirect {
			t.Fatalf("CreateCode(direct) reflected Mode %q back to the caller", dir.Mode)
		}
		want := map[string]domain.CodeMode{unset.ID: domain.ModeDynamic, dyn.ID: domain.ModeDynamic, dir.ID: domain.ModeDirect}
		for id, mode := range want {
			got, err := s.GetCodeByID(ctx(t), id, u.ID)
			if err != nil {
				t.Fatalf("GetCodeByID(%s): %v", id, err)
			}
			if got.Mode != mode {
				t.Fatalf("GetCodeByID(%s).Mode = %q, want %q", id, got.Mode, mode)
			}
		}
		if got, err := s.GetCodeByShortCode(ctx(t), "MODE-DIRECT"); err != nil || got.Mode != domain.ModeDirect {
			t.Fatalf("GetCodeByShortCode(direct): mode=%q err=%v", got.Mode, err)
		}
		items, _, err := s.ListCodes(ctx(t), domain.CodeFilter{UserID: u.ID, Limit: 10})
		if err != nil || len(items) != 3 {
			t.Fatalf("ListCodes: %d items err=%v", len(items), err)
		}
		for _, it := range items {
			if it.Mode != want[it.ID] {
				t.Fatalf("ListCodes(%s).Mode = %q, want %q", it.ShortCode, it.Mode, want[it.ID])
			}
		}
		// Export walks through ForEachCode, so it must report mode too (FR-106).
		seen := map[string]domain.CodeMode{}
		if err := s.ForEachCode(ctx(t), func(c *domain.Code) error { seen[c.ID] = c.Mode; return nil }); err != nil {
			t.Fatalf("ForEachCode: %v", err)
		}
		for id, mode := range want {
			if seen[id] != mode {
				t.Fatalf("ForEachCode(%s).Mode = %q, want %q", id, seen[id], mode)
			}
		}
		// The store itself does not police direct immutability (the service does), but
		// mode must survive every other mutation unchanged.
		if err := s.UpdateDestination(ctx(t), dir.ID, u.ID, "https://example.com/moved", dir.Version); err != nil {
			t.Fatalf("UpdateDestination: %v", err)
		}
		if err := s.DeleteCode(ctx(t), dir.ID, u.ID); err != nil {
			t.Fatalf("DeleteCode: %v", err)
		}
		if got, _ := s.GetCodeByID(ctx(t), dir.ID, u.ID); got.Mode != domain.ModeDirect {
			t.Fatalf("Mode changed across mutations: %q", got.Mode)
		}
	})

	t.Run("Req15_ClientRefUniquePerUserAndBatchAtomic", func(t *testing.T) {
		s := newStore(t)
		ua := mustUser(t, s, "a@example.com")
		ub := mustUser(t, s, "b@example.com")

		// Round trip, lookup by ref, and per-user scoping: the same ref is fine for
		// another user.
		a1 := newCode(ua.ID, "ref-a1", true)
		a1.ClientRef = "order-1"
		if err := s.CreateCode(ctx(t), a1); err != nil {
			t.Fatalf("CreateCode(a1): %v", err)
		}
		got, err := s.GetCodeByID(ctx(t), a1.ID, ua.ID)
		if err != nil || got.ClientRef != "order-1" {
			t.Fatalf("GetCodeByID(a1): ClientRef=%q err=%v, want order-1", got.ClientRef, err)
		}
		byRef, err := s.GetCodeByClientRef(ctx(t), ua.ID, "order-1")
		if err != nil || byRef.ID != a1.ID {
			t.Fatalf("GetCodeByClientRef(a, order-1): id=%q err=%v, want %q", byRef.ID, err, a1.ID)
		}
		if _, err := s.GetCodeByClientRef(ctx(t), ub.ID, "order-1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetCodeByClientRef(b, order-1): %v, want ErrNotFound (refs are per user)", err)
		}
		if _, err := s.GetCodeByClientRef(ctx(t), ua.ID, "never"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetCodeByClientRef(a, never): %v, want ErrNotFound", err)
		}
		if _, err := s.GetCodeByClientRef(ctx(t), ua.ID, ""); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetCodeByClientRef(a, empty): %v, want ErrNotFound (empty means none)", err)
		}
		b1 := newCode(ub.ID, "ref-b1", true)
		b1.ClientRef = "order-1"
		if err := s.CreateCode(ctx(t), b1); err != nil {
			t.Fatalf("CreateCode(b1) with another user's ref: %v", err)
		}
		// Same user twice is refused with the dedicated sentinel; case matters (opaque).
		dup := newCode(ua.ID, "ref-a2", true)
		dup.ClientRef = "order-1"
		if err := s.CreateCode(ctx(t), dup); !errors.Is(err, store.ErrClientRefTaken) {
			t.Fatalf("CreateCode(dup ref): %v, want ErrClientRefTaken", err)
		}
		if _, err := s.GetCodeByID(ctx(t), dup.ID, ua.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("refused row was persisted: %v", err)
		}
		// Codes without a ref never collide with each other.
		for _, sc := range []string{"ref-none-1", "ref-none-2"} {
			mustCode(t, s, ua.ID, sc, true)
		}
		// A deleted code keeps its ref (the row survives, so the key does too).
		if err := s.DeleteCode(ctx(t), a1.ID, ua.ID); err != nil {
			t.Fatalf("DeleteCode(a1): %v", err)
		}
		if got, err := s.GetCodeByClientRef(ctx(t), ua.ID, "order-1"); err != nil || got.ID != a1.ID || got.State != domain.CodeDeleted {
			t.Fatalf("GetCodeByClientRef after delete: %+v err=%v, want the deleted row", got, err)
		}

		// CreateCodes: one bad row (a taken client_ref) means nothing is inserted.
		batch := []*domain.Code{newCode(ua.ID, "batch-1", true), newCode(ua.ID, "batch-2", true), newCode(ua.ID, "batch-3", true)}
		batch[0].ClientRef = "b-1"
		batch[1].ClientRef = "order-1" // taken by a1
		batch[2].ClientRef = "b-3"
		if err := s.CreateCodes(ctx(t), batch); !errors.Is(err, store.ErrClientRefTaken) {
			t.Fatalf("CreateCodes with a taken ref: %v, want ErrClientRefTaken", err)
		}
		for _, c := range batch {
			if _, err := s.GetCodeByID(ctx(t), c.ID, ua.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("CreateCodes left %s behind after a failure: %v", c.ShortCode, err)
			}
			if ok, _ := s.IsAliasAvailable(ctx(t), c.ShortCode); !ok {
				t.Fatalf("CreateCodes reserved %q despite failing", c.ShortCode)
			}
		}
		// The same goes for a duplicate WITHIN the batch, and for a taken alias.
		twice := []*domain.Code{newCode(ua.ID, "batch-4", true), newCode(ua.ID, "batch-5", true)}
		twice[0].ClientRef, twice[1].ClientRef = "same", "same"
		if err := s.CreateCodes(ctx(t), twice); !errors.Is(err, store.ErrClientRefTaken) {
			t.Fatalf("CreateCodes with an in-batch duplicate ref: %v, want ErrClientRefTaken", err)
		}
		alias := []*domain.Code{newCode(ua.ID, "batch-6", true), newCode(ua.ID, "ref-b1", true)}
		if err := s.CreateCodes(ctx(t), alias); !errors.Is(err, store.ErrAliasTaken) {
			t.Fatalf("CreateCodes with a taken alias: %v, want ErrAliasTaken", err)
		}
		for _, sc := range []string{"batch-4", "batch-5", "batch-6"} {
			if ok, _ := s.IsAliasAvailable(ctx(t), sc); !ok {
				t.Fatalf("failed CreateCodes reserved %q", sc)
			}
		}
		// An empty batch is a valid no-op; a good batch lands whole with every row
		// reflected back like CreateCode does.
		if err := s.CreateCodes(ctx(t), nil); err != nil {
			t.Fatalf("CreateCodes(empty): %v", err)
		}
		good := []*domain.Code{newCode(ua.ID, "Good-1", true), newCode(ua.ID, "good-2", false), newCode(ua.ID, "good-3", true)}
		good[0].ClientRef = "g-1"
		good[2].ClientRef = "g-3"
		good[2].Mode = domain.ModeDirect
		if err := s.CreateCodes(ctx(t), good); err != nil {
			t.Fatalf("CreateCodes(good): %v", err)
		}
		if good[0].ShortCode != "good-1" || good[0].Version != 1 || good[1].Mode != domain.ModeDynamic || good[0].Styling.ID == "" {
			t.Fatalf("CreateCodes did not reflect persisted values: %+v", good[0])
		}
		for _, c := range good {
			got, err := s.GetCodeByID(ctx(t), c.ID, ua.ID)
			if err != nil {
				t.Fatalf("GetCodeByID(%s) after CreateCodes: %v", c.ShortCode, err)
			}
			if got.ClientRef != c.ClientRef || got.Mode != c.Mode || got.Version != 1 {
				t.Fatalf("CreateCodes(%s) round trip: %+v", c.ShortCode, got)
			}
		}
		items, _, err := s.ListCodes(ctx(t), domain.CodeFilter{UserID: ua.ID, Limit: 100})
		if err != nil {
			t.Fatalf("ListCodes: %v", err)
		}
		refs := map[string]string{}
		for _, it := range items {
			refs[it.ShortCode] = it.ClientRef
		}
		if refs["good-1"] != "g-1" || refs["good-3"] != "g-3" || refs["good-2"] != "" {
			t.Fatalf("ListCodes client refs: %v", refs)
		}
		// Export walks through ForEachCode, so it must carry client_ref too.
		seen := map[string]string{}
		if err := s.ForEachCode(ctx(t), func(c *domain.Code) error { seen[c.ID] = c.ClientRef; return nil }); err != nil {
			t.Fatalf("ForEachCode: %v", err)
		}
		if seen[good[0].ID] != "g-1" || seen[a1.ID] != "order-1" {
			t.Fatalf("ForEachCode client refs: %v", seen)
		}
	})

	t.Run("Users_CreateAndLookupByEmailCaseInsensitive", func(t *testing.T) {
		s := newStore(t)
		if n, err := s.CountUsers(ctx(t)); err != nil || n != 0 {
			t.Fatalf("CountUsers(empty): %d err=%v, want 0", n, err)
		}
		u := mustUser(t, s, "Ada@Example.com")
		for _, q := range []string{"ada@example.com", "ADA@EXAMPLE.COM", "Ada@Example.com"} {
			got, err := s.GetUserByEmail(ctx(t), q)
			if err != nil {
				t.Fatalf("GetUserByEmail(%q): %v", q, err)
			}
			if got.ID != u.ID {
				t.Fatalf("GetUserByEmail(%q): id %q, want %q", q, got.ID, u.ID)
			}
			if !strings.EqualFold(got.Email, u.Email) {
				t.Fatalf("GetUserByEmail(%q): email %q, want %q", q, got.Email, u.Email)
			}
			if got.PasswordHash != u.PasswordHash || got.IsAdmin != u.IsAdmin || got.Source != u.Source || got.TokenVersion != u.TokenVersion {
				t.Fatalf("GetUserByEmail(%q): fields differ: got %+v want %+v", q, got, u)
			}
		}
		got, err := s.GetUserByID(ctx(t), u.ID)
		if err != nil || got.ID != u.ID {
			t.Fatalf("GetUserByID: %+v err=%v", got, err)
		}
		if err := s.CreateUser(ctx(t), &domain.User{ID: newID("usr"), Email: "ADA@example.com", PasswordHash: "y", Source: domain.UserSourceLocal, CreatedAt: time.Now()}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("CreateUser(duplicate email, different case): %v, want ErrConflict", err)
		}
		mustUser(t, s, "b@example.com")
		if n, err := s.CountUsers(ctx(t)); err != nil || n != 2 {
			t.Fatalf("CountUsers: %d err=%v, want 2", n, err)
		}
	})

	t.Run("Users_BumpTokenVersionIncrementsAndReturnsNew", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		if u.TokenVersion != 0 {
			t.Fatalf("initial TokenVersion %d, want 0", u.TokenVersion)
		}
		v, err := s.BumpTokenVersion(ctx(t), u.ID)
		if err != nil || v != 1 {
			t.Fatalf("BumpTokenVersion #1: %d err=%v, want 1", v, err)
		}
		v, err = s.BumpTokenVersion(ctx(t), u.ID)
		if err != nil || v != 2 {
			t.Fatalf("BumpTokenVersion #2: %d err=%v, want 2", v, err)
		}
		got, err := s.GetUserByID(ctx(t), u.ID)
		if err != nil || got.TokenVersion != 2 {
			t.Fatalf("persisted TokenVersion %d err=%v, want 2", got.TokenVersion, err)
		}
	})

	t.Run("Tokens_CreateListRevokeTouch", func(t *testing.T) {
		s := newStore(t)
		owner := mustUser(t, s, "owner@example.com")
		other := mustUser(t, s, "other@example.com")

		t1 := mustToken(t, s, owner.ID, "ci")
		t2 := mustToken(t, s, owner.ID, "laptop")
		t3 := mustToken(t, s, other.ID, "theirs")

		got, err := s.GetTokenByID(ctx(t), t1.ID)
		if err != nil {
			t.Fatalf("GetTokenByID: %v", err)
		}
		if got.UserID != owner.ID || got.Name != "ci" || string(got.SecretHash) != string(t1.SecretHash) || got.Revoked() || got.LastUsedAt != nil {
			t.Fatalf("GetTokenByID: %+v", got)
		}
		// The store must not alias the caller's slice.
		got.SecretHash[0] ^= 0xff
		if again, _ := s.GetTokenByID(ctx(t), t1.ID); string(again.SecretHash) != string(t1.SecretHash) {
			t.Fatalf("SecretHash aliased across calls")
		}

		list, err := s.ListTokens(ctx(t), owner.ID)
		if err != nil {
			t.Fatalf("ListTokens: %v", err)
		}
		if ids := idsOfTokens(list); len(ids) != 2 || !ids[t1.ID] || !ids[t2.ID] || ids[t3.ID] {
			t.Fatalf("ListTokens(owner): %v, want exactly {t1, t2}", ids)
		}
		if list, _ := s.ListTokens(ctx(t), "usr_nobody"); len(list) != 0 {
			t.Fatalf("ListTokens(unknown user): %d items, want 0 and no error", len(list))
		}

		at := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
		if err := s.TouchTokenLastUsed(ctx(t), t1.ID, at); err != nil {
			t.Fatalf("TouchTokenLastUsed: %v", err)
		}
		got, _ = s.GetTokenByID(ctx(t), t1.ID)
		if got.LastUsedAt == nil || !got.LastUsedAt.Equal(at) {
			t.Fatalf("LastUsedAt after touch: %v, want %v", got.LastUsedAt, at)
		}

		// Ownership: a non-owner cannot revoke and learns nothing.
		if err := s.RevokeToken(ctx(t), t1.ID, other.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("RevokeToken as non-owner: %v, want ErrNotFound", err)
		}
		if got, _ := s.GetTokenByID(ctx(t), t1.ID); got.Revoked() {
			t.Fatalf("token revoked by non-owner")
		}
		if err := s.RevokeToken(ctx(t), t1.ID, owner.ID); err != nil {
			t.Fatalf("RevokeToken: %v", err)
		}
		got, err = s.GetTokenByID(ctx(t), t1.ID)
		if err != nil {
			t.Fatalf("GetTokenByID after revoke: %v (revoked tokens must remain readable)", err)
		}
		if !got.Revoked() {
			t.Fatalf("token not marked revoked")
		}
		// Revoked tokens stay listed so the UI can show them.
		list, _ = s.ListTokens(ctx(t), owner.ID)
		if ids := idsOfTokens(list); len(ids) != 2 {
			t.Fatalf("ListTokens after revoke: %v, want both", ids)
		}
	})

	t.Run("Codes_CreateRoundTripsAllFields", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		c := newCode(u.ID, "full-fields", true)
		c.Styling = domain.Styling{
			ID: newID("sty"), FgColor: "#112233", BgColor: "#ffffff", ModuleShape: domain.ShapeRounded,
			MarginModules: 4, SizePx: 512, ECLevel: domain.ECMedium, ECLevelEffective: domain.ECHigh,
			LogoBlobKey: "logos/x.png", LogoScale: 0.2,
		}
		c.BlobKey, c.BlobETag = "codes/full-fields.svg", `"etag-1"`
		if err := s.CreateCode(ctx(t), c); err != nil {
			t.Fatalf("CreateCode: %v", err)
		}
		got, err := s.GetCodeByID(ctx(t), c.ID, u.ID)
		if err != nil {
			t.Fatalf("GetCodeByID: %v", err)
		}
		if got.ID != c.ID || got.ShortCode != c.ShortCode || got.IsAlias != c.IsAlias || got.UserID != c.UserID ||
			got.Destination != c.Destination || got.State != domain.CodeActive || got.BlobKey != c.BlobKey ||
			got.BlobETag != c.BlobETag || got.Version != 1 || got.Styling != c.Styling || got.DeletedAt != nil {
			t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, c)
		}
		// Duplicate ID is a conflict, not silent overwrite.
		dup := newCode(u.ID, "another", true)
		dup.ID = c.ID
		if err := s.CreateCode(ctx(t), dup); err == nil {
			t.Fatalf("CreateCode(duplicate id): want error, got nil")
		}
	})

	t.Run("Codes_SetCodeStateTransitions", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		c := mustCode(t, s, u.ID, "states", true)

		if err := s.SetCodeState(ctx(t), c.ID, u.ID, domain.CodeDisabled); err != nil {
			t.Fatalf("disable: %v", err)
		}
		got, _ := s.GetCodeByID(ctx(t), c.ID, u.ID)
		if got.State != domain.CodeDisabled || got.Version != c.Version+1 {
			t.Fatalf("after disable: state=%q version=%d, want disabled/%d", got.State, got.Version, c.Version+1)
		}
		if bySC, _ := s.GetCodeByShortCode(ctx(t), "states"); bySC.State != domain.CodeDisabled {
			t.Fatalf("GetCodeByShortCode after disable: state=%q", bySC.State)
		}
		if err := s.SetCodeState(ctx(t), c.ID, u.ID, domain.CodeActive); err != nil {
			t.Fatalf("enable: %v", err)
		}
		got, _ = s.GetCodeByID(ctx(t), c.ID, u.ID)
		if got.State != domain.CodeActive || got.Version != c.Version+2 {
			t.Fatalf("after enable: state=%q version=%d", got.State, got.Version)
		}
		if err := s.DeleteCode(ctx(t), c.ID, u.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		// Deleted is terminal.
		if err := s.SetCodeState(ctx(t), c.ID, u.ID, domain.CodeActive); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("SetCodeState on deleted code: %v, want ErrConflict", err)
		}
		got, _ = s.GetCodeByID(ctx(t), c.ID, u.ID)
		if got.State != domain.CodeDeleted {
			t.Fatalf("deleted code resurrected: state=%q", got.State)
		}
	})

	t.Run("Aliases_IsAliasAvailable", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		if ok, err := s.IsAliasAvailable(ctx(t), "free-alias"); err != nil || !ok {
			t.Fatalf("IsAliasAvailable(free): ok=%v err=%v, want true", ok, err)
		}
		live := mustCode(t, s, u.ID, "live-alias", true)
		gen := mustCode(t, s, u.ID, "0123456789ab", false)
		for _, sc := range []string{"live-alias", "LIVE-ALIAS", "0123456789AB"} {
			if ok, err := s.IsAliasAvailable(ctx(t), sc); err != nil || ok {
				t.Fatalf("IsAliasAvailable(%q, taken): ok=%v err=%v, want false", sc, ok, err)
			}
		}
		if err := s.DeleteCode(ctx(t), live.ID, u.ID); err != nil {
			t.Fatalf("DeleteCode: %v", err)
		}
		if err := s.DeleteCode(ctx(t), gen.ID, u.ID); err != nil {
			t.Fatalf("DeleteCode: %v", err)
		}
		// Reserved (deleted) short codes — alias or generated — remain unavailable.
		for _, sc := range []string{"live-alias", "0123456789ab"} {
			if ok, err := s.IsAliasAvailable(ctx(t), sc); err != nil || ok {
				t.Fatalf("IsAliasAvailable(%q, reserved): ok=%v err=%v, want false", sc, ok, err)
			}
		}
		if err := s.ReleaseAlias(ctx(t), "live-alias"); err != nil {
			t.Fatalf("ReleaseAlias: %v", err)
		}
		if ok, err := s.IsAliasAvailable(ctx(t), "Live-Alias"); err != nil || !ok {
			t.Fatalf("IsAliasAvailable after release: ok=%v err=%v, want true", ok, err)
		}
		if ok, _ := s.IsAliasAvailable(ctx(t), "0123456789ab"); ok {
			t.Fatalf("releasing one alias freed another")
		}
	})

	t.Run("Codes_ListFiltersByDestinationAndCreatedRange", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "a@example.com")
		other := mustUser(t, s, "other@example.com")
		base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

		mk := func(uid, sc, dest string, at time.Time) *domain.Code {
			c := newCode(uid, sc, true)
			c.Destination = dest
			c.CreatedAt, c.UpdatedAt = at, at
			if err := s.CreateCode(ctx(t), c); err != nil {
				t.Fatalf("CreateCode(%s): %v", sc, err)
			}
			return c
		}
		c1 := mk(u.ID, "f-one", "https://example.com/spring", base)
		c2 := mk(u.ID, "f-two", "https://Example.com/summer", base.Add(1*time.Hour))
		c3 := mk(u.ID, "f-three", "https://other.example/autumn", base.Add(2*time.Hour))
		c4 := mk(u.ID, "f-four", "https://example.com/winter", base.Add(3*time.Hour))
		mk(other.ID, "f-theirs", "https://example.com/theirs", base.Add(4*time.Hour))

		list := func(f domain.CodeFilter) map[string]bool {
			t.Helper()
			f.UserID = u.ID
			if f.Limit == 0 {
				f.Limit = 100
			}
			items, _, err := s.ListCodes(ctx(t), f)
			if err != nil {
				t.Fatalf("ListCodes(%+v): %v", f, err)
			}
			return idsOfCodes(items)
		}
		expect := func(name string, got map[string]bool, want ...*domain.Code) {
			t.Helper()
			if len(got) != len(want) {
				t.Fatalf("%s: got %d items %v, want %d", name, len(got), got, len(want))
			}
			for _, w := range want {
				if !got[w.ID] {
					t.Fatalf("%s: missing %s (%s)", name, w.ShortCode, w.ID)
				}
			}
		}

		// Destination substring, case-insensitive (OpenAPI destination_contains).
		expect("dest=example.com", list(domain.CodeFilter{Destination: "example.com"}), c1, c2, c4)
		expect("dest=EXAMPLE.COM", list(domain.CodeFilter{Destination: "EXAMPLE.COM"}), c1, c2, c4)
		expect("dest=autumn", list(domain.CodeFilter{Destination: "autumn"}), c3)
		expect("dest=nomatch", list(domain.CodeFilter{Destination: "nomatch"}))

		// Creation range (boundaries chosen strictly between rows so that inclusive vs
		// exclusive endpoints are not pinned).
		after := base.Add(30 * time.Minute)
		before := base.Add(150 * time.Minute)
		expect("after", list(domain.CodeFilter{CreatedAfter: &after}), c2, c3, c4)
		expect("before", list(domain.CodeFilter{CreatedBefore: &before}), c1, c2, c3)
		expect("between", list(domain.CodeFilter{CreatedAfter: &after, CreatedBefore: &before}), c2, c3)
		expect("between+dest", list(domain.CodeFilter{CreatedAfter: &after, CreatedBefore: &before, Destination: "example.com"}), c2)

		// Filters combine with pagination.
		items, next, err := s.ListCodes(ctx(t), domain.CodeFilter{UserID: u.ID, Destination: "example.com", Limit: 2})
		if err != nil || len(items) != 2 || next == "" {
			t.Fatalf("filtered page 1: %d items next=%q err=%v", len(items), next, err)
		}
		rest, next2, err := s.ListCodes(ctx(t), domain.CodeFilter{UserID: u.ID, Destination: "example.com", Limit: 2, Cursor: next})
		if err != nil || len(rest) != 1 || next2 != "" {
			t.Fatalf("filtered page 2: %d items next=%q err=%v", len(rest), next2, err)
		}
		got := idsOfCodes(append(items, rest...))
		expect("filtered pages", got, c1, c2, c4)
	})
}

// BuildRollups computes the per-batch rollup deltas for events: for every event, one
// increment on the total dimension and one per recorded breakdown dimension, keyed by the
// event's UTC hour. The analytics pipeline uses the same function so the rollups it hands
// InsertScanBatch match what the contract suite asserts.
func BuildRollups(events []domain.ScanEvent) []domain.RollupDelta {
	type key struct {
		code string
		hour time.Time
		dim  domain.Dimension
		val  string
	}
	counts := map[key]int64{}
	for _, ev := range events {
		hour := ev.OccurredAt.UTC().Truncate(time.Hour)
		bot := "false"
		if ev.IsBot {
			bot = "true"
		}
		counts[key{ev.CodeID, hour, domain.DimTotal, ""}]++
		counts[key{ev.CodeID, hour, domain.DimUAFamily, ev.UAFamily}]++
		counts[key{ev.CodeID, hour, domain.DimDeviceCategory, string(ev.DeviceCategory)}]++
		counts[key{ev.CodeID, hour, domain.DimReferrerHost, ev.ReferrerHost}]++
		counts[key{ev.CodeID, hour, domain.DimIsBot, bot}]++
	}
	out := make([]domain.RollupDelta, 0, len(counts))
	for k, n := range counts {
		out = append(out, domain.RollupDelta{CodeID: k.code, HourBucket: k.hour, Dimension: k.dim, Value: k.val, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.CodeID != b.CodeID {
			return a.CodeID < b.CodeID
		}
		if !a.HourBucket.Equal(b.HourBucket) {
			return a.HourBucket.Before(b.HourBucket)
		}
		if a.Dimension != b.Dimension {
			return a.Dimension < b.Dimension
		}
		return a.Value < b.Value
	})
	return out
}

// ---- helpers -------------------------------------------------------------------------

func ctx(t *testing.T) context.Context {
	t.Helper()
	return t.Context()
}

var idEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

func newID(prefix string) string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + strings.ToLower(idEnc.EncodeToString(b[:]))
}

func mustUser(t *testing.T, s store.Store, email string) *domain.User {
	t.Helper()
	u := &domain.User{ //nolint:gosec // fixed fake PHC test fixture below, not a real credential
		ID:           newID("usr"),
		Email:        email,
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		Source:       domain.UserSourceLocal,
		CreatedAt:    time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := s.CreateUser(ctx(t), u); err != nil {
		t.Fatalf("CreateUser(%s): %v", email, err)
	}
	return u
}

func mustToken(t *testing.T, s store.Store, userID, name string) *domain.APIToken {
	t.Helper()
	var h [32]byte
	if _, err := rand.Read(h[:]); err != nil {
		t.Fatal(err)
	}
	tok := &domain.APIToken{
		ID:         newID("tok"),
		UserID:     userID,
		Name:       name,
		SecretHash: h[:],
		CreatedAt:  time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := s.CreateToken(ctx(t), tok); err != nil {
		t.Fatalf("CreateToken(%s): %v", name, err)
	}
	return tok
}

func newCode(userID, shortCode string, isAlias bool) *domain.Code {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return &domain.Code{
		ID:          newID("cod"),
		ShortCode:   shortCode,
		IsAlias:     isAlias,
		UserID:      userID,
		Destination: "https://example.com/" + strings.ToLower(shortCode),
		State:       domain.CodeActive,
		Styling: domain.Styling{
			ID: newID("sty"), FgColor: "#000000", BgColor: "#ffffff", ModuleShape: domain.ShapeSquare,
			MarginModules: 4, SizePx: 256, ECLevel: domain.ECMedium, ECLevelEffective: domain.ECMedium,
		},
		BlobKey:   "codes/" + strings.ToLower(shortCode) + ".svg",
		BlobETag:  `"` + strings.ToLower(shortCode) + `"`,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// mustCode creates a code and returns it as the store reports it (so Version and any
// normalised fields reflect persisted values).
func mustCode(t *testing.T, s store.Store, userID, shortCode string, isAlias bool) *domain.Code {
	t.Helper()
	c := newCode(userID, shortCode, isAlias)
	if err := s.CreateCode(ctx(t), c); err != nil {
		t.Fatalf("CreateCode(%s): %v", shortCode, err)
	}
	got, err := s.GetCodeByID(ctx(t), c.ID, userID)
	if err != nil {
		t.Fatalf("GetCodeByID after CreateCode(%s): %v", shortCode, err)
	}
	if got.Version != 1 {
		t.Fatalf("CreateCode(%s): version %d, want 1", shortCode, got.Version)
	}
	return got
}

func scan(codeID string, at time.Time, ua string, dev domain.DeviceCategory, ref string, bot bool) domain.ScanEvent {
	return domain.ScanEvent{CodeID: codeID, OccurredAt: at, UAFamily: ua, DeviceCategory: dev, ReferrerHost: ref, IsBot: bot}
}

func mustQuery(t *testing.T, s store.Store, codeID string, from, to time.Time, b domain.Bucket) *domain.AnalyticsResult {
	t.Helper()
	res, err := s.QueryAnalytics(ctx(t), domain.AnalyticsQuery{CodeID: codeID, From: from, To: to, Bucket: b})
	if err != nil {
		t.Fatalf("QueryAnalytics: %v", err)
	}
	if res == nil {
		t.Fatalf("QueryAnalytics returned nil result with nil error")
	}
	if res.Breakdowns == nil {
		t.Fatalf("QueryAnalytics returned nil Breakdowns map")
	}
	return res
}

func checkTime(t *testing.T, what string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("%s: %v, want %v (instant differs)", what, got, want)
	}
	if got.Location() != time.UTC {
		t.Fatalf("%s: location %v, want UTC", what, got.Location())
	}
}

func sum(vals []domain.BreakdownValue) int64 {
	var n int64
	for _, v := range vals {
		n += v.Count
	}
	return n
}

func seriesSum(pts []domain.SeriesPoint) int64 {
	var n int64
	for _, p := range pts {
		n += p.Count
	}
	return n
}

func countOf(vals []domain.BreakdownValue, value string) int64 {
	var n int64
	for _, v := range vals {
		if v.Value == value {
			n += v.Count
		}
	}
	return n
}

func idsOfTokens(ts []*domain.APIToken) map[string]bool {
	m := map[string]bool{}
	for _, t := range ts {
		m[t.ID] = true
	}
	return m
}

func idsOfCodes(cs []*domain.Code) map[string]bool {
	m := map[string]bool{}
	for _, c := range cs {
		m[c.ID] = true
	}
	return m
}

// bucketStart truncates t (in UTC) to the start of its bucket. Weeks start on Monday.
func bucketStart(t time.Time, b domain.Bucket) time.Time {
	t = t.UTC()
	switch b {
	case domain.BucketHour:
		return t.Truncate(time.Hour)
	case domain.BucketWeek:
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		offset := (int(d.Weekday()) + 6) % 7 // Monday=0
		return d.AddDate(0, 0, -offset)
	default: // day
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
}
