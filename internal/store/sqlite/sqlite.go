package sqlite

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/migrations"
)

func init() { store.Register("sqlite", Open) }

// pragmas are applied on every connection. WAL lets readers proceed while the single
// writer commits; busy_timeout absorbs the rare checkpoint contention instead of
// surfacing SQLITE_BUSY; foreign_keys makes InsertScanBatch atomic-by-constraint;
// synchronous=NORMAL is durable across process crashes under WAL (research.md §3).
const pragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"

// tsLayout is a fixed-width UTC layout. RFC3339Nano trims trailing zeros, which breaks
// lexical ordering of TEXT columns; this layout keeps every value the same width so
// `created_at < ?` on TEXT orders chronologically. Microsecond precision is the floor
// shared with TIMESTAMPTZ, so both dialects round-trip identically (Req09).
const tsLayout = "2006-01-02T15:04:05.000000Z07:00"

const defaultListLimit = 50

// Store is the SQLite driver. Writes go through w, a pool capped at ONE connection so
// SQLite's single-writer rule is enforced in Go (queued, never SQLITE_BUSY) rather than
// discovered at runtime; reads use r, a normal pool, so the scan path never waits on a
// write (research.md §3).
type Store struct {
	w *sql.DB
	r *sql.DB
}

var _ store.Store = (*Store)(nil)

// Open opens (creating if necessary) the database at dsn. dsn may be a bare path or a
// full `file:` URI; pragmas are appended unless the caller supplied their own.
func Open(_ context.Context, dsn string) (store.Store, error) {
	full, err := buildDSN(dsn)
	if err != nil {
		return nil, err
	}
	w, err := sql.Open("sqlite", full)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)

	r, err := sql.Open("sqlite", full)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("sqlite: open reader: %w", err)
	}
	r.SetMaxOpenConns(16)
	r.SetMaxIdleConns(4)
	r.SetConnMaxLifetime(0)
	return &Store{w: w, r: r}, nil
}

func buildDSN(dsn string) (string, error) {
	if dsn == "" {
		return "", errors.New("sqlite: empty DSN")
	}
	if !strings.HasPrefix(dsn, "file:") {
		if dsn != ":memory:" {
			if dir := filepath.Dir(dsn); dir != "." {
				if err := os.MkdirAll(dir, 0o750); err != nil {
					return "", fmt.Errorf("sqlite: create data directory: %w", err)
				}
			}
		}
		dsn = "file:" + dsn
	}
	if strings.Contains(dsn, "_pragma=") {
		return dsn, nil
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + pragmas, nil
}

// ---- error translation ----------------------------------------------------------------

// translate maps native errors to the store sentinels (contracts/store.md). Extended code
// 2067 (UNIQUE) and 1555 (PRIMARY KEY) on the short-code namespace mean the alias is
// taken; any other uniqueness failure is a conflict; a foreign-key failure means a
// referenced row does not exist.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("sqlite: %w", store.ErrNotFound)
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			msg := se.Error()
			if strings.Contains(msg, "codes.short_code") || strings.Contains(msg, "alias_reservations.short_code") {
				return fmt.Errorf("sqlite: %w: %v", store.ErrAliasTaken, err)
			}
			return fmt.Errorf("sqlite: %w: %v", store.ErrConflict, err)
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return fmt.Errorf("sqlite: %w: %v", store.ErrNotFound, err)
		}
	}
	return err
}

func notFound(what string) error { return fmt.Errorf("sqlite: %s: %w", what, store.ErrNotFound) }
func conflict(what string) error { return fmt.Errorf("sqlite: %s: %w", what, store.ErrConflict) }

// ---- value helpers -------------------------------------------------------------------

func fmtTime(t time.Time) string { return t.UTC().Truncate(time.Microsecond).Format(tsLayout) }

func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("sqlite: bad timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}

func parseTimePtr(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// withTx runs fn in a transaction on the writer pool.
func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return translate(fmt.Errorf("sqlite: commit: %w", err))
	}
	return nil
}

// ---- users ---------------------------------------------------------------------------

func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	if u.ID == "" || u.Email == "" {
		return errors.New("sqlite: user id and email are required")
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now()
	}
	_, err := s.w.ExecContext(ctx, `INSERT INTO users (id, email, password_hash, is_admin, token_version, source, created_at, last_login_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Email, nullStr(u.PasswordHash), b2i(u.IsAdmin), u.TokenVersion, string(u.Source), fmtTime(u.CreatedAt), fmtTimePtr(u.LastLoginAt))
	return translate(err)
}

const userCols = `id, email, COALESCE(password_hash, ''), is_admin, token_version, source, created_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (*domain.User, error) {
	var (
		u         domain.User
		isAdmin   int
		source    string
		createdAt string
		lastLogin sql.NullString
	)
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &isAdmin, &u.TokenVersion, &source, &createdAt, &lastLogin); err != nil {
		return nil, translate(err)
	}
	var err error
	u.IsAdmin = isAdmin != 0
	u.Source = domain.UserSource(source)
	if u.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if u.LastLoginAt, err = parseTimePtr(lastLogin); err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*domain.User, error) {
	return scanUser(s.r.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	return scanUser(s.r.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ? COLLATE NOCASE`, email))
}

func (s *Store) BumpTokenVersion(ctx context.Context, userID string) (int64, error) {
	var v int64
	err := s.w.QueryRowContext(ctx, `UPDATE users SET token_version = token_version + 1 WHERE id = ? RETURNING token_version`, userID).Scan(&v)
	if err != nil {
		return 0, translate(err)
	}
	return v, nil
}

func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	if err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, translate(err)
	}
	return n, nil
}

// ---- tokens --------------------------------------------------------------------------

func (s *Store) CreateToken(ctx context.Context, t *domain.APIToken) error {
	if t.ID == "" || t.UserID == "" {
		return errors.New("sqlite: token id and user id are required")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now()
	}
	_, err := s.w.ExecContext(ctx, `INSERT INTO api_tokens (id, user_id, name, secret_hash, created_at, last_used_at, revoked_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Name, t.SecretHash, fmtTime(t.CreatedAt), fmtTimePtr(t.LastUsedAt), fmtTimePtr(t.RevokedAt), fmtTimePtr(t.ExpiresAt))
	return translate(err)
}

const tokenCols = `id, user_id, name, secret_hash, created_at, last_used_at, revoked_at, expires_at` //nolint:gosec // column name, not a credential

func scanToken(row interface{ Scan(...any) error }) (*domain.APIToken, error) {
	var (
		t                          domain.APIToken
		createdAt                  string
		lastUsed, revoked, expires sql.NullString
	)
	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.SecretHash, &createdAt, &lastUsed, &revoked, &expires); err != nil {
		return nil, translate(err)
	}
	var err error
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if t.LastUsedAt, err = parseTimePtr(lastUsed); err != nil {
		return nil, err
	}
	if t.RevokedAt, err = parseTimePtr(revoked); err != nil {
		return nil, err
	}
	if t.ExpiresAt, err = parseTimePtr(expires); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) GetTokenByID(ctx context.Context, id string) (*domain.APIToken, error) {
	return scanToken(s.r.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE id = ?`, id))
}

func (s *Store) ListTokens(ctx context.Context, userID string) ([]*domain.APIToken, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, translate(err)
	}
	defer func() { _ = rows.Close() }()
	out := []*domain.APIToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, translate(rows.Err())
}

func (s *Store) RevokeToken(ctx context.Context, id, userID string) error {
	res, err := s.w.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ? AND user_id = ?`, fmtTime(now()), id, userID)
	if err != nil {
		return translate(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("token " + id)
	}
	return nil
}

func (s *Store) TouchTokenLastUsed(ctx context.Context, id string, at time.Time) error {
	res, err := s.w.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, fmtTime(at), id)
	if err != nil {
		return translate(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("token " + id)
	}
	return nil
}

// ---- codes ---------------------------------------------------------------------------

// CreateCode inserts the styling profile, the code, and its alias reservation in one
// transaction. Two things guard the namespace, both surfacing as ErrAliasTaken: the
// partial case-insensitive unique index on live codes (migration 0002), and the
// alias_reservations row, which survives DeleteCode (FR-018) and is only ever marked
// released, never removed. Re-registering a released short code re-arms that row via the
// upsert below; an upsert that touches zero rows means the row is still unreleased.
func (s *Store) CreateCode(ctx context.Context, c *domain.Code) error {
	if c.ID == "" || c.ShortCode == "" || c.UserID == "" {
		return errors.New("sqlite: code id, short code and user id are required")
	}
	shortCode := strings.ToLower(c.ShortCode)
	created := c.CreatedAt
	if created.IsZero() {
		created = now()
	}
	updated := c.UpdatedAt
	if updated.IsZero() {
		updated = created
	}
	state := c.State
	if state == "" {
		state = domain.CodeActive
	}
	styling := c.Styling
	if styling.ID == "" {
		styling.ID = domain.NewID("sty")
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO styling_profiles (id, fg_color, bg_color, module_shape, margin_modules, size_px, ec_level, ec_level_effective, logo_blob_key, logo_scale)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			styling.ID, styling.FgColor, styling.BgColor, string(styling.ModuleShape), styling.MarginModules, styling.SizePx,
			string(styling.ECLevel), string(styling.ECLevelEffective), nullStr(styling.LogoBlobKey), nullFloat(styling.LogoScale)); err != nil {
			return translate(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO codes (id, short_code, is_alias, user_id, destination, state, styling_id, blob_key, blob_etag, version, created_at, updated_at, deleted_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, NULL)`,
			c.ID, shortCode, b2i(c.IsAlias), c.UserID, c.Destination, string(state), styling.ID, c.BlobKey, c.BlobETag,
			fmtTime(created), fmtTime(updated)); err != nil {
			return translate(err)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO alias_reservations (short_code, code_id, reserved_at, released_at) VALUES (?, ?, ?, NULL)
			ON CONFLICT (short_code) DO UPDATE SET code_id = excluded.code_id, reserved_at = excluded.reserved_at, released_at = NULL
			WHERE alias_reservations.released_at IS NOT NULL`,
			shortCode, c.ID, fmtTime(now()))
		if err != nil {
			return translate(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("sqlite: short code %q reserved: %w", shortCode, store.ErrAliasTaken)
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.ShortCode = shortCode
	c.Version = 1
	c.State = state
	c.Styling = styling
	c.CreatedAt = created.UTC().Truncate(time.Microsecond)
	c.UpdatedAt = updated.UTC().Truncate(time.Microsecond)
	c.DeletedAt = nil
	return nil
}

const codeCols = `c.id, c.short_code, c.is_alias, c.user_id, c.destination, c.state, c.blob_key, c.blob_etag, c.version, c.created_at, c.updated_at, c.deleted_at,
	sp.id, sp.fg_color, sp.bg_color, sp.module_shape, sp.margin_modules, sp.size_px, sp.ec_level, sp.ec_level_effective, COALESCE(sp.logo_blob_key, ''), COALESCE(sp.logo_scale, 0)`

const codeFrom = ` FROM codes c JOIN styling_profiles sp ON sp.id = c.styling_id `

func scanCode(row interface{ Scan(...any) error }) (*domain.Code, error) {
	var (
		c                domain.Code
		isAlias          int
		state, shape, ec string
		ecEff            string
		created, updated string
		deleted          sql.NullString
		marginModules    int
		sizePx           int
		logoKey          string
		logoScale        float64
	)
	if err := row.Scan(&c.ID, &c.ShortCode, &isAlias, &c.UserID, &c.Destination, &state, &c.BlobKey, &c.BlobETag, &c.Version, &created, &updated, &deleted,
		&c.Styling.ID, &c.Styling.FgColor, &c.Styling.BgColor, &shape, &marginModules, &sizePx, &ec, &ecEff, &logoKey, &logoScale); err != nil {
		return nil, translate(err)
	}
	var err error
	c.IsAlias = isAlias != 0
	c.State = domain.CodeState(state)
	c.Styling.ModuleShape = domain.ModuleShape(shape)
	c.Styling.MarginModules = marginModules
	c.Styling.SizePx = sizePx
	c.Styling.ECLevel = domain.ECLevel(ec)
	c.Styling.ECLevelEffective = domain.ECLevel(ecEff)
	c.Styling.LogoBlobKey = logoKey
	c.Styling.LogoScale = logoScale
	if c.CreatedAt, err = parseTime(created); err != nil {
		return nil, err
	}
	if c.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, err
	}
	if c.DeletedAt, err = parseTimePtr(deleted); err != nil {
		return nil, err
	}
	return &c, nil
}

// GetCodeByShortCode resolves the live code for a short code, or — so the redirect path
// can serve the fallback — the deleted code that still holds its reservation. A deleted
// row whose alias has been released (or re-registered by another code) no longer
// resolves, which is also why deleted rows may share a short code with a live one.
func (s *Store) GetCodeByShortCode(ctx context.Context, shortCode string) (*domain.Code, error) {
	return scanCode(s.r.QueryRowContext(ctx, `SELECT `+codeCols+codeFrom+`WHERE c.short_code = ? COLLATE NOCASE
		AND (c.state != 'deleted' OR c.id = (SELECT code_id FROM alias_reservations WHERE short_code = lower(?) AND released_at IS NULL))
		ORDER BY (c.state = 'deleted'), c.created_at DESC LIMIT 1`, shortCode, shortCode))
}

func (s *Store) GetCodeByID(ctx context.Context, id, userID string) (*domain.Code, error) {
	return scanCode(s.r.QueryRowContext(ctx, `SELECT `+codeCols+codeFrom+`WHERE c.id = ? AND c.user_id = ?`, id, userID))
}

type cursorPos struct {
	createdAt time.Time
	id        string
}

func encodeCursor(p cursorPos) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmtTime(p.createdAt) + "\x00" + p.id))
}

func decodeCursor(s string) (cursorPos, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursorPos{}, fmt.Errorf("sqlite: invalid cursor: %w", err)
	}
	ts, id, ok := strings.Cut(string(raw), "\x00")
	if !ok || id == "" {
		return cursorPos{}, errors.New("sqlite: invalid cursor")
	}
	at, err := parseTime(ts)
	if err != nil {
		return cursorPos{}, fmt.Errorf("sqlite: invalid cursor: %w", err)
	}
	return cursorPos{createdAt: at, id: id}, nil
}

// ListCodes pages newest-first by (created_at DESC, id DESC) using a keyset cursor, so
// rows inserted mid-listing are neither skipped nor duplicated (Req12).
func (s *Store) ListCodes(ctx context.Context, f domain.CodeFilter) ([]*domain.Code, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	var (
		where = []string{"c.user_id = ?", "c.state != 'deleted'"}
		args  = []any{f.UserID}
	)
	if f.CreatedAfter != nil {
		where = append(where, "c.created_at > ?")
		args = append(args, fmtTime(*f.CreatedAfter))
	}
	if f.CreatedBefore != nil {
		where = append(where, "c.created_at < ?")
		args = append(args, fmtTime(*f.CreatedBefore))
	}
	if f.Destination != "" {
		where = append(where, "instr(lower(c.destination), lower(?)) > 0")
		args = append(args, f.Destination)
	}
	if f.Cursor != "" {
		p, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		where = append(where, "(c.created_at < ? OR (c.created_at = ? AND c.id < ?))")
		ts := fmtTime(p.createdAt)
		args = append(args, ts, ts, p.id)
	}
	args = append(args, limit+1)
	// The concatenated fragments are all compile-time constants (codeCols, codeFrom) or
	// literal "?" placeholders appended to where/args together — no user-controlled
	// data ever reaches the query string itself.
	q := `SELECT ` + codeCols + codeFrom + `WHERE ` + strings.Join(where, " AND ") + ` ORDER BY c.created_at DESC, c.id DESC LIMIT ?` //nolint:gosec // fragments are compile-time constants; values are bound via ? placeholders
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", translate(err)
	}
	defer func() { _ = rows.Close() }()
	var out []*domain.Code
	for rows.Next() {
		c, err := scanCode(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", translate(err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(cursorPos{createdAt: last.CreatedAt, id: last.ID})
	}
	if out == nil {
		out = []*domain.Code{}
	}
	return out, next, nil
}

// ownedExists distinguishes "not yours / missing" (ErrNotFound) from a genuine state
// or version conflict after a guarded UPDATE touched zero rows.
func ownedExists(ctx context.Context, tx *sql.Tx, id, userID string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM codes WHERE id = ? AND user_id = ?`, id, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, translate(err)
}

// UpdateDestination is a compare-and-increment on the integer version, never on a
// timestamp (Req06). Deleted codes are terminal and report ErrConflict.
func (s *Store) UpdateDestination(ctx context.Context, id, userID, dest string, expectedVersion int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE codes SET destination = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND user_id = ? AND version = ? AND state != 'deleted'`,
			dest, fmtTime(now()), id, userID, expectedVersion)
		if err != nil {
			return translate(err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return nil
		}
		exists, err := ownedExists(ctx, tx, id, userID)
		if err != nil {
			return err
		}
		if !exists {
			return notFound("code " + id)
		}
		return conflict("code " + id + " version mismatch or deleted")
	})
}

func (s *Store) SetCodeState(ctx context.Context, id, userID string, state domain.CodeState) error {
	switch state {
	case domain.CodeActive, domain.CodeDisabled:
	case domain.CodeDeleted:
		return errors.New("sqlite: use DeleteCode to delete")
	default:
		return fmt.Errorf("sqlite: unknown state %q", state)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE codes SET state = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND user_id = ? AND state != 'deleted'`,
			string(state), fmtTime(now()), id, userID)
		if err != nil {
			return translate(err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return nil
		}
		exists, err := ownedExists(ctx, tx, id, userID)
		if err != nil {
			return err
		}
		if !exists {
			return notFound("code " + id)
		}
		return conflict("code " + id + " is deleted")
	})
}

// DeleteCode soft-deletes. The alias_reservations row is deliberately untouched (FR-018).
func (s *Store) DeleteCode(ctx context.Context, id, userID string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		ts := fmtTime(now())
		res, err := tx.ExecContext(ctx, `UPDATE codes SET state = 'deleted', deleted_at = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND user_id = ? AND state != 'deleted'`, ts, ts, id, userID)
		if err != nil {
			return translate(err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return nil
		}
		exists, err := ownedExists(ctx, tx, id, userID)
		if err != nil {
			return err
		}
		if !exists {
			return notFound("code " + id)
		}
		return nil // already deleted: idempotent
	})
}

// ---- aliases -------------------------------------------------------------------------

func (s *Store) IsAliasAvailable(ctx context.Context, shortCode string) (bool, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM codes WHERE short_code = ? COLLATE NOCASE AND state != 'deleted') +
		(SELECT COUNT(*) FROM alias_reservations WHERE short_code = lower(?) AND released_at IS NULL)`, shortCode, shortCode).Scan(&n)
	if err != nil {
		return false, translate(err)
	}
	return n == 0, nil
}

// ReleaseAlias marks the reservation released (released_at set; the row and the deleted
// code's short_code are left intact for history and export). The partial unique index
// ignores deleted rows, so nothing else blocks re-registration. Not idempotent by
// contract: a second call finds no unreleased reservation and is ErrNotFound.
func (s *Store) ReleaseAlias(ctx context.Context, shortCode string) error {
	key := strings.ToLower(shortCode)
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM alias_reservations WHERE short_code = ? AND released_at IS NULL`, key).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return notFound("alias " + key + " not reserved")
			}
			return translate(err)
		}
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codes WHERE short_code = ? COLLATE NOCASE AND state != 'deleted'`, key).Scan(&live); err != nil {
			return translate(err)
		}
		if live > 0 {
			return conflict("alias " + key + " owned by live code")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alias_reservations SET released_at = ? WHERE short_code = ? AND released_at IS NULL`, fmtTime(now()), key); err != nil {
			return translate(err)
		}
		return nil
	})
}

// ---- analytics -----------------------------------------------------------------------

// InsertScanBatch writes raw events and rollup deltas in ONE transaction. A bad row
// (unknown code, via the foreign key) aborts the whole batch (Req07).
func (s *Store) InsertScanBatch(ctx context.Context, b domain.ScanBatch) error {
	if len(b.Events) == 0 && len(b.Rollups) == 0 {
		return nil
	}
	for i, ev := range b.Events {
		if ev.CodeID == "" || ev.OccurredAt.IsZero() {
			return fmt.Errorf("sqlite: event %d is malformed", i)
		}
	}
	for i, r := range b.Rollups {
		if r.CodeID == "" || r.Dimension == "" || r.HourBucket.IsZero() {
			return fmt.Errorf("sqlite: rollup %d is malformed", i)
		}
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if len(b.Events) > 0 {
			ins, err := tx.PrepareContext(ctx, `INSERT INTO scan_events (code_id, occurred_at, ua_family, device_category, referrer_host, is_bot) VALUES (?, ?, ?, ?, ?, ?)`)
			if err != nil {
				return translate(err)
			}
			defer func() { _ = ins.Close() }()
			for _, ev := range b.Events {
				if _, err := ins.ExecContext(ctx, ev.CodeID, fmtTime(ev.OccurredAt), ev.UAFamily, string(ev.DeviceCategory), ev.ReferrerHost, b2i(ev.IsBot)); err != nil {
					return translate(err)
				}
			}
		}
		if len(b.Rollups) > 0 {
			up, err := tx.PrepareContext(ctx, `INSERT INTO scan_rollups (code_id, hour_bucket, dimension, value, count) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (code_id, hour_bucket, dimension, value) DO UPDATE SET count = scan_rollups.count + excluded.count`)
			if err != nil {
				return translate(err)
			}
			defer func() { _ = up.Close() }()
			for _, r := range b.Rollups {
				hour := r.HourBucket.UTC().Truncate(time.Hour)
				if _, err := up.ExecContext(ctx, r.CodeID, fmtTime(hour), string(r.Dimension), r.Value, r.Count); err != nil {
					return translate(err)
				}
			}
		}
		return nil
	})
}

// QueryAnalytics reads rollups only, over hour buckets in [From, To), and aggregates
// into the requested bucket in Go so both dialects share one definition of "week".
func (s *Store) QueryAnalytics(ctx context.Context, q domain.AnalyticsQuery) (*domain.AnalyticsResult, error) {
	bucket := q.Bucket
	if bucket == "" {
		bucket = domain.BucketDay
	}
	rows, err := s.r.QueryContext(ctx, `SELECT hour_bucket, dimension, value, count FROM scan_rollups
		WHERE code_id = ? AND hour_bucket >= ? AND hour_bucket < ?`, q.CodeID, fmtTime(q.From), fmtTime(q.To))
	if err != nil {
		return nil, translate(err)
	}
	defer func() { _ = rows.Close() }()
	agg := newAggregator(bucket)
	for rows.Next() {
		var (
			hourS, dim, value string
			n                 int64
		)
		if err := rows.Scan(&hourS, &dim, &value, &n); err != nil {
			return nil, translate(err)
		}
		hour, err := parseTime(hourS)
		if err != nil {
			return nil, err
		}
		agg.add(hour, domain.Dimension(dim), value, n)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	return agg.result(), nil
}

type aggregator struct {
	bucket    domain.Bucket
	total     int64
	series    map[time.Time]int64
	breakdown map[domain.Dimension]map[string]int64
}

func newAggregator(b domain.Bucket) *aggregator {
	return &aggregator{bucket: b, series: map[time.Time]int64{}, breakdown: map[domain.Dimension]map[string]int64{}}
}

func (a *aggregator) add(hour time.Time, dim domain.Dimension, value string, n int64) {
	if dim == domain.DimTotal {
		a.total += n
		a.series[bucketStart(hour, a.bucket)] += n
		return
	}
	if a.breakdown[dim] == nil {
		a.breakdown[dim] = map[string]int64{}
	}
	a.breakdown[dim][value] += n
}

func (a *aggregator) result() *domain.AnalyticsResult {
	res := &domain.AnalyticsResult{
		Total: a.total,
		Breakdowns: map[domain.Dimension][]domain.BreakdownValue{
			domain.DimUAFamily:       {},
			domain.DimDeviceCategory: {},
			domain.DimReferrerHost:   {},
			domain.DimIsBot:          {},
		},
	}
	res.Series = make([]domain.SeriesPoint, 0, len(a.series))
	for start, n := range a.series {
		res.Series = append(res.Series, domain.SeriesPoint{Start: start, Count: n})
	}
	sort.Slice(res.Series, func(i, j int) bool { return res.Series[i].Start.Before(res.Series[j].Start) })
	for dim, vals := range a.breakdown {
		out := make([]domain.BreakdownValue, 0, len(vals))
		for v, n := range vals {
			out = append(out, domain.BreakdownValue{Value: v, Count: n})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Value < out[j].Value
		})
		res.Breakdowns[dim] = out
	}
	return res
}

// bucketStart truncates t (UTC) to the start of its bucket. Weeks start on Monday.
func bucketStart(t time.Time, b domain.Bucket) time.Time {
	t = t.UTC()
	switch b {
	case domain.BucketHour:
		return t.Truncate(time.Hour)
	case domain.BucketWeek:
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		offset := (int(d.Weekday()) + 6) % 7
		return d.AddDate(0, 0, -offset)
	default:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
}

// PruneScanEvents deletes at most limit raw events older than before, oldest first.
// SQLite has no DELETE ... LIMIT without a compile flag; the subquery form is portable
// (research.md §4).
func (s *Store) PruneScanEvents(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	res, err := s.w.ExecContext(ctx, `DELETE FROM scan_events WHERE id IN (
		SELECT id FROM scan_events WHERE occurred_at < ? ORDER BY occurred_at, id LIMIT ?)`, fmtTime(before), limit)
	if err != nil {
		return 0, translate(err)
	}
	n, err := res.RowsAffected()
	return n, translate(err)
}

// ---- bulk iteration ------------------------------------------------------------------
//
// Every walker streams through a rows cursor on the reader pool and hands each row to fn
// as it is scanned; nothing is buffered. fn may call back into the store (the reader pool
// has spare connections and WAL never blocks a reader), and its first error ends the walk
// and is returned unwrapped.

func (s *Store) ForEachUser(ctx context.Context, fn func(*domain.User) error) error {
	rows, err := s.r.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return translate(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return err
		}
		if err := fn(u); err != nil {
			return err
		}
	}
	return translate(rows.Err())
}

func (s *Store) ForEachCode(ctx context.Context, fn func(*domain.Code) error) error {
	rows, err := s.r.QueryContext(ctx, `SELECT `+codeCols+codeFrom+`ORDER BY c.created_at, c.id`)
	if err != nil {
		return translate(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		c, err := scanCode(rows)
		if err != nil {
			return err
		}
		if err := fn(c); err != nil {
			return err
		}
	}
	return translate(rows.Err())
}

func (s *Store) ForEachRollup(ctx context.Context, fn func(domain.RollupDelta) error) error {
	rows, err := s.r.QueryContext(ctx, `SELECT code_id, hour_bucket, dimension, value, count FROM scan_rollups ORDER BY code_id, hour_bucket, dimension, value`)
	if err != nil {
		return translate(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			r     domain.RollupDelta
			hourS string
			dim   string
		)
		if err := rows.Scan(&r.CodeID, &hourS, &dim, &r.Value, &r.Count); err != nil {
			return translate(err)
		}
		if r.HourBucket, err = parseTime(hourS); err != nil {
			return err
		}
		r.Dimension = domain.Dimension(dim)
		if err := fn(r); err != nil {
			return err
		}
	}
	return translate(rows.Err())
}

func (s *Store) ForEachReservation(ctx context.Context, fn func(domain.AliasReservation) error) error {
	rows, err := s.r.QueryContext(ctx, `SELECT short_code, COALESCE(code_id, ''), reserved_at, released_at FROM alias_reservations ORDER BY short_code`)
	if err != nil {
		return translate(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			r         domain.AliasReservation
			reservedS string
			released  sql.NullString
		)
		if err := rows.Scan(&r.ShortCode, &r.CodeID, &reservedS, &released); err != nil {
			return translate(err)
		}
		if r.ReservedAt, err = parseTime(reservedS); err != nil {
			return err
		}
		if r.ReleasedAt, err = parseTimePtr(released); err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return translate(rows.Err())
}

// ---- lifecycle -----------------------------------------------------------------------

func (s *Store) Migrate(ctx context.Context) error {
	return migrations.Apply(ctx, s.w, migrations.SQLite)
}

func (s *Store) Ping(ctx context.Context) error {
	if err := s.w.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlite: writer: %w", err)
	}
	if err := s.r.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlite: reader: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.w.Close())
}
