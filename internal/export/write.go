package export

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
)

// entityNames are the fixed, well-known file names inside the archive. Order matters
// only for readability of a manually inspected tar — Read matches by name, not position.
const (
	fileManifest     = "manifest.json"
	fileUsers        = "users.jsonl"
	fileTokens       = "api_tokens.jsonl"
	fileCodes        = "codes.jsonl"
	fileReservations = "alias_reservations.jsonl"
	fileRollups      = "scan_rollups.jsonl"
)

// spooledEntity accumulates one entity's JSONL rows in a temp file so its final size is
// known before the tar header for it is written (archive/tar requires Size up front).
// This keeps qurator's own resident memory at O(1) in row count — the temp file, not a
// buffer, grows with the table — while never materialising the whole table as Go values
// at once either: each row is encoded and flushed to disk as it is produced.
type spooledEntity struct {
	name string
	f    *os.File
	enc  *json.Encoder
	n    int64
}

func newSpool(name string) (*spooledEntity, error) {
	f, err := os.CreateTemp("", "qurator-export-*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("export: spool %s: %w", name, err)
	}
	return &spooledEntity{name: name, f: f, enc: json.NewEncoder(f)}, nil
}

func (s *spooledEntity) put(v any) error {
	if err := s.enc.Encode(v); err != nil {
		return fmt.Errorf("export: encode %s row: %w", s.name, err)
	}
	s.n++
	return nil
}

func (s *spooledEntity) close() error {
	err := s.f.Close()
	os.Remove(s.f.Name())
	return err
}

// Write streams a full export of st to w as a tar archive: manifest.json plus one
// <entity>.jsonl file per entity actually available (see the package doc for when an
// entity is omitted).
func Write(ctx context.Context, st store.Store, w io.Writer) error {
	manifest := Manifest{
		Version:  FormatVersion,
		Entities: map[string]int64{},
		Omitted:  map[string]string{},
	}

	var spools []*spooledEntity
	defer func() {
		for _, s := range spools {
			s.close()
		}
	}()

	ex, ok := st.(Exporter)
	if !ok {
		manifest.Omitted["users"] = "store does not implement export.Exporter: no way to enumerate users"
		manifest.Omitted["api_tokens"] = "requires users to enumerate per-user tokens"
		manifest.Omitted["codes"] = "store.ListCodes requires a UserID filter and no user IDs could be discovered"
		manifest.Omitted["alias_reservations"] = "store does not implement export.Exporter"
		manifest.Omitted["scan_rollups"] = "store does not implement export.Exporter"
		return writeManifestOnly(w, manifest)
	}

	usersSp, err := newSpool(fileUsers)
	if err != nil {
		return err
	}
	spools = append(spools, usersSp)
	tokensSp, err := newSpool(fileTokens)
	if err != nil {
		return err
	}
	spools = append(spools, tokensSp)
	codesSp, err := newSpool(fileCodes)
	if err != nil {
		return err
	}
	spools = append(spools, codesSp)

	if err := ex.ExportUsers(ctx, func(u *domain.User) error {
		rec := userRecord{
			ID: u.ID, Email: u.Email, IsAdmin: u.IsAdmin, TokenVersion: u.TokenVersion,
			Source: string(u.Source), CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt,
		}
		if err := usersSp.put(rec); err != nil {
			return err
		}

		toks, err := st.ListTokens(ctx, u.ID)
		if err != nil {
			return fmt.Errorf("export: list tokens for user %q: %w", u.ID, err)
		}
		for _, t := range toks {
			if err := tokensSp.put(tokenRecord{
				ID: t.ID, UserID: t.UserID, Name: t.Name, CreatedAt: t.CreatedAt,
				LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt, ExpiresAt: t.ExpiresAt,
			}); err != nil {
				return err
			}
		}

		cursor := ""
		for {
			codes, next, err := st.ListCodes(ctx, domain.CodeFilter{UserID: u.ID, Limit: 500, Cursor: cursor})
			if err != nil {
				return fmt.Errorf("export: list codes for user %q: %w", u.ID, err)
			}
			for _, c := range codes {
				if err := codesSp.put(c); err != nil {
					return err
				}
			}
			if next == "" {
				break
			}
			cursor = next
		}
		return nil
	}); err != nil {
		return fmt.Errorf("export: users: %w", err)
	}
	manifest.Entities["users"] = usersSp.n
	manifest.Entities["api_tokens"] = tokensSp.n
	manifest.Entities["codes"] = codesSp.n

	resSp, err := newSpool(fileReservations)
	if err != nil {
		return err
	}
	spools = append(spools, resSp)
	if err := ex.ExportReservations(ctx, func(r ReservationRecord) error {
		return resSp.put(r)
	}); err != nil {
		return fmt.Errorf("export: alias reservations: %w", err)
	}
	manifest.Entities["alias_reservations"] = resSp.n

	rollupsSp, err := newSpool(fileRollups)
	if err != nil {
		return err
	}
	spools = append(spools, rollupsSp)
	if err := ex.ExportRollups(ctx, func(r domain.RollupDelta) error {
		return rollupsSp.put(r)
	}); err != nil {
		return fmt.Errorf("export: scan rollups: %w", err)
	}
	manifest.Entities["scan_rollups"] = rollupsSp.n

	manifest.ExportedAt = time.Now().UTC()

	tw := tar.NewWriter(w)
	if err := writeManifestEntry(tw, manifest); err != nil {
		return err
	}
	for _, s := range spools {
		if err := spoolToTar(tw, s); err != nil {
			return err
		}
	}
	return tw.Close()
}

func writeManifestOnly(w io.Writer, m Manifest) error {
	m.ExportedAt = time.Now().UTC()
	tw := tar.NewWriter(w)
	if err := writeManifestEntry(tw, m); err != nil {
		return err
	}
	return tw.Close()
}

func writeManifestEntry(tw *tar.Writer, m Manifest) error {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("export: marshal manifest: %w", err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: fileManifest, Mode: 0o644, Size: int64(len(body)), ModTime: m.ExportedAt,
	}); err != nil {
		return fmt.Errorf("export: write manifest header: %w", err)
	}
	_, err = tw.Write(body)
	return err
}

func spoolToTar(tw *tar.Writer, s *spooledEntity) error {
	info, err := s.f.Stat()
	if err != nil {
		return fmt.Errorf("export: stat spool %s: %w", s.name, err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: s.name, Mode: 0o644, Size: info.Size(), ModTime: info.ModTime(),
	}); err != nil {
		return fmt.Errorf("export: write %s header: %w", s.name, err)
	}
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("export: rewind spool %s: %w", s.name, err)
	}
	if _, err := io.Copy(tw, s.f); err != nil {
		return fmt.Errorf("export: copy %s into archive: %w", s.name, err)
	}
	return nil
}
