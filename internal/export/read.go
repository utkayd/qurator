package export

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
)

// ErrNotEmpty is returned by Read when st already has users and force is false.
var ErrNotEmpty = errors.New("export: store already has users; pass force to import anyway")

// Read replays a tar archive produced by Write into st. api_tokens rows are never
// recreated (see manifest.go: an export never carries a usable secret hash, so there is
// nothing safe to restore). alias_reservations rows are not inserted as such: every
// unreleased reservation belongs to an exported code (deleted ones included), and
// CreateCode re-reserves the short code as it recreates that code. A released
// reservation is replayed by releasing the recreated (deleted) code's alias again, so
// a short code that was free before the export is free after the import.
func Read(ctx context.Context, st store.Store, r io.Reader, force bool) error {
	if !force {
		n, err := st.CountUsers(ctx)
		if err != nil {
			return fmt.Errorf("export: count existing users: %w", err)
		}
		if n > 0 {
			return ErrNotEmpty
		}
	}

	tr := tar.NewReader(r)
	var manifest *Manifest
	codeIDs := map[string]bool{} // populated as codes are created, for rollup validation

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("export: read archive: %w", err)
		}
		switch hdr.Name {
		case fileManifest:
			var m Manifest
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				return fmt.Errorf("export: decode manifest: %w", err)
			}
			if m.Version != FormatVersion {
				return fmt.Errorf("export: manifest version %q, this binary supports %q", m.Version, FormatVersion)
			}
			manifest = &m
		case fileUsers:
			if err := importUsers(ctx, st, tr); err != nil {
				return err
			}
		case fileTokens:
			// Intentionally not recreated; see the doc comment above.
			if err := drainJSONL(tr, func([]byte) error { return nil }); err != nil {
				return fmt.Errorf("export: skip %s: %w", fileTokens, err)
			}
		case fileCodes:
			if err := importCodes(ctx, st, tr, codeIDs); err != nil {
				return err
			}
		case fileReservations:
			if err := importReservations(ctx, st, tr, codeIDs); err != nil {
				return err
			}
		case fileRollups:
			if err := importRollups(ctx, st, tr, codeIDs); err != nil {
				return err
			}
		}
	}
	if manifest == nil {
		return errors.New("export: archive has no manifest.json")
	}
	return nil
}

func drainJSONL(r io.Reader, fn func(line []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return sc.Err()
}

func importUsers(ctx context.Context, st store.Store, r io.Reader) error {
	return drainJSONL(r, func(line []byte) error {
		var rec userRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("export: decode user row: %w", err)
		}
		u := &domain.User{
			ID: rec.ID, Email: rec.Email, IsAdmin: rec.IsAdmin, TokenVersion: rec.TokenVersion,
			Source: domain.UserSource(rec.Source), CreatedAt: rec.CreatedAt, LastLoginAt: rec.LastLoginAt,
			// PasswordHash is deliberately left empty: it was never exported. A local
			// user restored this way cannot sign in with a password until one is reset.
		}
		if err := st.CreateUser(ctx, u); err != nil {
			return fmt.Errorf("export: create user %q: %w", rec.Email, err)
		}
		return nil
	})
}

// importCodes recreates every code. A deleted code is recreated then soft-deleted
// again, so its short code ends up reserved-but-not-live exactly as it was (FR-018);
// its DeletedAt becomes the import time, since CreateCode never accepts one.
func importCodes(ctx context.Context, st store.Store, r io.Reader, codeIDs map[string]bool) error {
	return drainJSONL(r, func(line []byte) error {
		var c domain.Code
		if err := json.Unmarshal(line, &c); err != nil {
			return fmt.Errorf("export: decode code row: %w", err)
		}
		deleted := c.State == domain.CodeDeleted
		if deleted {
			c.State = domain.CodeDisabled
		}
		if err := st.CreateCode(ctx, &c); err != nil {
			return fmt.Errorf("export: create code %q: %w", c.ShortCode, err)
		}
		if deleted {
			if err := st.DeleteCode(ctx, c.ID, c.UserID); err != nil {
				return fmt.Errorf("export: re-delete code %q: %w", c.ShortCode, err)
			}
		}
		codeIDs[c.ID] = true
		return nil
	})
}

// importReservations re-releases every reservation the export recorded as released.
// Unreleased ones were already re-created by importCodes (Write puts codes before
// reservations in the archive, so the codes exist by now).
func importReservations(ctx context.Context, st store.Store, r io.Reader, codeIDs map[string]bool) error {
	return drainJSONL(r, func(line []byte) error {
		var rec reservationRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("export: decode reservation row: %w", err)
		}
		if rec.ReleasedAt == nil || !codeIDs[rec.CodeID] {
			return nil
		}
		if err := st.ReleaseAlias(ctx, rec.ShortCode); err != nil {
			return fmt.Errorf("export: release alias %q: %w", rec.ShortCode, err)
		}
		return nil
	})
}

func importRollups(ctx context.Context, st store.Store, r io.Reader, codeIDs map[string]bool) error {
	const flushEvery = 500
	batch := make([]domain.RollupDelta, 0, flushEvery)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := st.InsertScanBatch(ctx, domain.ScanBatch{Rollups: batch}); err != nil {
			return fmt.Errorf("export: insert rollup batch: %w", err)
		}
		batch = batch[:0]
		return nil
	}
	err := drainJSONL(r, func(line []byte) error {
		var rd domain.RollupDelta
		if err := json.Unmarshal(line, &rd); err != nil {
			return fmt.Errorf("export: decode rollup row: %w", err)
		}
		if !codeIDs[rd.CodeID] {
			// The owning code is not in this archive (a hand-edited or truncated
			// dump). Skip rather than fail the whole import.
			return nil
		}
		batch = append(batch, rd)
		if len(batch) >= flushEvery {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	return flush()
}
