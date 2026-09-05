package postgres

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
	"github.com/utkayd/qurator/internal/store/migrations"
)

func init() { store.Register("postgres", Open) }

const defaultListLimit = 50

// Store is the PostgreSQL driver over database/sql + pgx/stdlib. Case-insensitive
// uniqueness comes from the lower() expression indexes the shared migration creates, so
// every short-code and email lookup is written as lower(col) = lower($n) to use them.
type Store struct {
	db *sql.DB
}

var _ store.Store = (*Store)(nil)

// Open connects using a pgx-compatible DSN (URL or key=value form).
func Open(ctx context.Context, dsn string) (store.Store, error) {
	if dsn == "" {
		return nil, errors.New("postgres: empty DSN")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return &Store{db: db}, nil
}

// ---- error translation ----------------------------------------------------------------

const (
	sqlstateUniqueViolation = "23505"
	sqlstateFKViolation     = "23503"
)

// translate maps native errors to the store sentinels. A unique violation on the
// short-code namespace (codes_short_code_ci or the reservations primary key) is
// ErrAliasTaken; any other unique violation is ErrConflict; an FK violation means a
// referenced row is missing.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("postgres: %w", store.ErrNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlstateUniqueViolation:
			switch pgErr.ConstraintName {
			case "codes_short_code_ci", "alias_reservations_pkey":
				return fmt.Errorf("postgres: %w: %s", store.ErrAliasTaken, pgErr.ConstraintName)
			}
			return fmt.Errorf("postgres: %w: %s", store.ErrConflict, pgErr.ConstraintName)
		case sqlstateFKViolation:
			return fmt.Errorf("postgres: %w: %s", store.ErrNotFound, pgErr.ConstraintName)
		}
	}
	return err
}

func notFound(what string) error { return fmt.Errorf("postgres: %s: %w", what, store.ErrNotFound) }
func conflict(what string) error { return fmt.Errorf("postgres: %s: %w", what, store.ErrConflict) }

// ---- value helpers -------------------------------------------------------------------

func utc(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func utcPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return utc(*t)
}

func fromNull(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
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

func now() time.Time { return utc(time.Now()) }

func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return translate(fmt.Errorf("postgres: commit: %w", err))
	}
	return nil
}

// ---- users ---------------------------------------------------------------------------

func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	if u.ID == "" || u.Email == "" {
		return errors.New("postgres: user id and email are required")
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (id, email, password_hash, is_admin, token_version, source, created_at, last_login_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		u.ID, u.Email, nullStr(u.PasswordHash), u.IsAdmin, u.TokenVersion, string(u.Source), utc(u.CreatedAt), utcPtr(u.LastLoginAt))
	return translate(err)
}

const userCols = `id, email, COALESCE(password_hash, ''), is_admin, token_version, source, created_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (*domain.User, error) {
	var (
		u         domain.User
		source    string
		createdAt time.Time
		lastLogin sql.NullTime
	)
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.TokenVersion, &source, &createdAt, &lastLogin); err != nil {
		return nil, translate(err)
	}
	u.Source = domain.UserSource(source)
	u.CreatedAt = createdAt.UTC()
	u.LastLoginAt = fromNull(lastLogin)
	return &u, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE lower(email) = lower($1)`, email))
}

func (s *Store) BumpTokenVersion(ctx context.Context, userID string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `UPDATE users SET token_version = token_version + 1 WHERE id = $1 RETURNING token_version`, userID).Scan(&v)
	if err != nil {
		return 0, translate(err)
	}
	return v, nil
}

func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, translate(err)
	}
	return n, nil
}

// ---- tokens --------------------------------------------------------------------------

func (s *Store) CreateToken(ctx context.Context, t *domain.APIToken) error {
	if t.ID == "" || t.UserID == "" {
		return errors.New("postgres: token id and user id are required")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens (id, user_id, name, secret_hash, created_at, last_used_at, revoked_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		t.ID, t.UserID, t.Name, t.SecretHash, utc(t.CreatedAt), utcPtr(t.LastUsedAt), utcPtr(t.RevokedAt), utcPtr(t.ExpiresAt))
	return translate(err)
}

const tokenCols = `id, user_id, name, secret_hash, created_at, last_used_at, revoked_at, expires_at` //nolint:gosec // column name, not a credential

func scanToken(row interface{ Scan(...any) error }) (*domain.APIToken, error) {
	var (
		t                          domain.APIToken
		createdAt                  time.Time
		lastUsed, revoked, expires sql.NullTime
	)
	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.SecretHash, &createdAt, &lastUsed, &revoked, &expires); err != nil {
		return nil, translate(err)
	}
	t.CreatedAt = createdAt.UTC()
	t.LastUsedAt = fromNull(lastUsed)
	t.RevokedAt = fromNull(revoked)
	t.ExpiresAt = fromNull(expires)
	return &t, nil
}

func (s *Store) GetTokenByID(ctx context.Context, id string) (*domain.APIToken, error) {
	return scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE id = $1`, id))
}

func (s *Store) ListTokens(ctx context.Context, userID string) ([]*domain.APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE user_id = $1 ORDER BY created_at, id`, userID)
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
	res, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, $1) WHERE id = $2 AND user_id = $3`, now(), id, userID)
	if err != nil {
		return translate(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("token " + id)
	}
	return nil
}

func (s *Store) TouchTokenLastUsed(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = $1 WHERE id = $2`, utc(at), id)
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
// transaction; see the SQLite driver for the reservation model, which is identical.
func (s *Store) CreateCode(ctx context.Context, c *domain.Code) error {
	if c.ID == "" || c.ShortCode == "" || c.UserID == "" {
		return errors.New("postgres: code id, short code and user id are required")
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
	mode := c.Mode
	if mode == "" {
		mode = domain.ModeDynamic
	}
	styling := c.Styling
	if styling.ID == "" {
		styling.ID = domain.NewID("sty")
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO styling_profiles (id, fg_color, bg_color, module_shape, margin_modules, size_px, ec_level, ec_level_effective, logo_blob_key, logo_scale)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			styling.ID, styling.FgColor, styling.BgColor, string(styling.ModuleShape), styling.MarginModules, styling.SizePx,
			string(styling.ECLevel), string(styling.ECLevelEffective), nullStr(styling.LogoBlobKey), nullFloat(styling.LogoScale)); err != nil {
			return translate(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO codes (id, short_code, is_alias, user_id, mode, destination, state, styling_id, blob_key, blob_etag, version, created_at, updated_at, deleted_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 1, $11, $12, NULL)`,
			c.ID, shortCode, c.IsAlias, c.UserID, string(mode), c.Destination, string(state), styling.ID, c.BlobKey, c.BlobETag,
			utc(created), utc(updated)); err != nil {
			return translate(err)
		}
		// Re-arm a released reservation or create one; a still-unreleased row makes the
		// upsert touch zero rows, which is the alias being taken (see the SQLite driver).
		res, err := tx.ExecContext(ctx, `INSERT INTO alias_reservations (short_code, code_id, reserved_at, released_at) VALUES ($1, $2, $3, NULL)
			ON CONFLICT (short_code) DO UPDATE SET code_id = EXCLUDED.code_id, reserved_at = EXCLUDED.reserved_at, released_at = NULL
			WHERE alias_reservations.released_at IS NOT NULL`,
			shortCode, c.ID, now())
		if err != nil {
			return translate(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("postgres: short code %q reserved: %w", shortCode, store.ErrAliasTaken)
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.ShortCode = shortCode
	c.Version = 1
	c.State = state
	c.Mode = mode
	c.Styling = styling
	c.CreatedAt = utc(created)
	c.UpdatedAt = utc(updated)
	c.DeletedAt = nil
	return nil
}

const codeCols = `c.id, c.short_code, c.is_alias, c.user_id, c.mode, c.destination, c.state, c.blob_key, c.blob_etag, c.version, c.created_at, c.updated_at, c.deleted_at,
	sp.id, sp.fg_color, sp.bg_color, sp.module_shape, sp.margin_modules, sp.size_px, sp.ec_level, sp.ec_level_effective, COALESCE(sp.logo_blob_key, ''), COALESCE(sp.logo_scale, 0)`

// codeFrom joins the styling profile. logo_scale is DOUBLE PRECISION since migration
// 0002, so a float64 round-trips exactly on both dialects.
const codeFrom = ` FROM codes c JOIN styling_profiles sp ON sp.id = c.styling_id `

func scanCode(row interface{ Scan(...any) error }) (*domain.Code, error) {
	var (
		c                       domain.Code
		mode                    string
		state, shape, ec, ecEff string
		created, updated        time.Time
		deleted                 sql.NullTime
		logoKey                 string
		logoScale               float64
	)
	if err := row.Scan(&c.ID, &c.ShortCode, &c.IsAlias, &c.UserID, &mode, &c.Destination, &state, &c.BlobKey, &c.BlobETag, &c.Version, &created, &updated, &deleted,
		&c.Styling.ID, &c.Styling.FgColor, &c.Styling.BgColor, &shape, &c.Styling.MarginModules, &c.Styling.SizePx, &ec, &ecEff, &logoKey, &logoScale); err != nil {
		return nil, translate(err)
	}
	c.Mode = domain.CodeMode(mode)
	c.State = domain.CodeState(state)
	c.Styling.ModuleShape = domain.ModuleShape(shape)
	c.Styling.ECLevel = domain.ECLevel(ec)
	c.Styling.ECLevelEffective = domain.ECLevel(ecEff)
	c.Styling.LogoBlobKey = logoKey
	c.Styling.LogoScale = logoScale
	c.CreatedAt = created.UTC()
	c.UpdatedAt = updated.UTC()
	c.DeletedAt = fromNull(deleted)
	return &c, nil
}

// GetCodeByShortCode resolves the live code, or else the deleted code that still holds
// the reservation (so the redirect path can serve the fallback); see the SQLite driver.
func (s *Store) GetCodeByShortCode(ctx context.Context, shortCode string) (*domain.Code, error) {
	return scanCode(s.db.QueryRowContext(ctx, `SELECT `+codeCols+codeFrom+`WHERE lower(c.short_code) = lower($1)
		AND (c.state <> 'deleted' OR c.id = (SELECT code_id FROM alias_reservations WHERE short_code = lower($1) AND released_at IS NULL))
		ORDER BY (c.state = 'deleted'), c.created_at DESC LIMIT 1`, shortCode))
}

func (s *Store) GetCodeByID(ctx context.Context, id, userID string) (*domain.Code, error) {
	return scanCode(s.db.QueryRowContext(ctx, `SELECT `+codeCols+codeFrom+`WHERE c.id = $1 AND c.user_id = $2`, id, userID))
}

type cursorPos struct {
	createdAt time.Time
	id        string
}

func encodeCursor(p cursorPos) string {
	return base64.RawURLEncoding.EncodeToString([]byte(p.createdAt.UTC().Format(time.RFC3339Nano) + "\x00" + p.id))
}

func decodeCursor(s string) (cursorPos, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursorPos{}, fmt.Errorf("postgres: invalid cursor: %w", err)
	}
	ts, id, ok := strings.Cut(string(raw), "\x00")
	if !ok || id == "" {
		return cursorPos{}, errors.New("postgres: invalid cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return cursorPos{}, fmt.Errorf("postgres: invalid cursor: %w", err)
	}
	return cursorPos{createdAt: at.UTC(), id: id}, nil
}

// ListCodes pages newest-first by (created_at DESC, id DESC) with a keyset cursor (Req12).
func (s *Store) ListCodes(ctx context.Context, f domain.CodeFilter) ([]*domain.Code, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	var (
		where = []string{"c.user_id = $1", "c.state <> 'deleted'"}
		args  = []any{f.UserID}
	)
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.CreatedAfter != nil {
		where = append(where, "c.created_at > "+arg(utc(*f.CreatedAfter)))
	}
	if f.CreatedBefore != nil {
		where = append(where, "c.created_at < "+arg(utc(*f.CreatedBefore)))
	}
	if f.Destination != "" {
		where = append(where, "position(lower("+arg(f.Destination)+") IN lower(c.destination)) > 0")
	}
	if f.Cursor != "" {
		p, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		ts := arg(p.createdAt)
		id := arg(p.id)
		where = append(where, "(c.created_at < "+ts+" OR (c.created_at = "+ts+" AND c.id < "+id+"))")
	}
	// The concatenated fragments are all compile-time constants (codeCols, codeFrom) or
	// built by arg(), which appends the value to args and substitutes only a $N
	// placeholder into the SQL text — no user-controlled data ever reaches the query
	// string itself.
	q := `SELECT ` + codeCols + codeFrom + `WHERE ` + strings.Join(where, " AND ") + ` ORDER BY c.created_at DESC, c.id DESC LIMIT ` + arg(limit+1) //nolint:gosec // fragments are compile-time constants; values are bound via arg() placeholders
	rows, err := s.db.QueryContext(ctx, q, args...)
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

func ownedExists(ctx context.Context, tx *sql.Tx, id, userID string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM codes WHERE id = $1 AND user_id = $2`, id, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, translate(err)
}

// UpdateDestination is a compare-and-increment on the integer version (Req06).
func (s *Store) UpdateDestination(ctx context.Context, id, userID, dest string, expectedVersion int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE codes SET destination = $1, version = version + 1, updated_at = $2
			WHERE id = $3 AND user_id = $4 AND version = $5 AND state <> 'deleted'`,
			dest, now(), id, userID, expectedVersion)
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
		return errors.New("postgres: use DeleteCode to delete")
	default:
		return fmt.Errorf("postgres: unknown state %q", state)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE codes SET state = $1, version = version + 1, updated_at = $2
			WHERE id = $3 AND user_id = $4 AND state <> 'deleted'`,
			string(state), now(), id, userID)
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

// DeleteCode soft-deletes; the alias_reservations row is untouched (FR-018).
func (s *Store) DeleteCode(ctx context.Context, id, userID string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		ts := now()
		res, err := tx.ExecContext(ctx, `UPDATE codes SET state = 'deleted', deleted_at = $1, version = version + 1, updated_at = $1
			WHERE id = $2 AND user_id = $3 AND state <> 'deleted'`, ts, id, userID)
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
		return nil
	})
}

// ---- aliases -------------------------------------------------------------------------

func (s *Store) IsAliasAvailable(ctx context.Context, shortCode string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM codes WHERE lower(short_code) = lower($1) AND state <> 'deleted') +
		(SELECT COUNT(*) FROM alias_reservations WHERE short_code = lower($1) AND released_at IS NULL)`, shortCode).Scan(&n)
	if err != nil {
		return false, translate(err)
	}
	return n == 0, nil
}

// ReleaseAlias marks the reservation released; the row stays for history and export.
// See the SQLite driver.
func (s *Store) ReleaseAlias(ctx context.Context, shortCode string) error {
	key := strings.ToLower(shortCode)
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM alias_reservations WHERE short_code = $1 AND released_at IS NULL FOR UPDATE`, key).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return notFound("alias " + key + " not reserved")
			}
			return translate(err)
		}
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codes WHERE lower(short_code) = lower($1) AND state <> 'deleted'`, key).Scan(&live); err != nil {
			return translate(err)
		}
		if live > 0 {
			return conflict("alias " + key + " owned by live code")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alias_reservations SET released_at = $1 WHERE short_code = $2 AND released_at IS NULL`, now(), key); err != nil {
			return translate(err)
		}
		return nil
	})
}

// ---- analytics -----------------------------------------------------------------------

// InsertScanBatch writes events and rollup deltas in ONE transaction; the foreign key
// on code_id makes a bad row abort everything (Req07).
func (s *Store) InsertScanBatch(ctx context.Context, b domain.ScanBatch) error {
	if len(b.Events) == 0 && len(b.Rollups) == 0 {
		return nil
	}
	for i, ev := range b.Events {
		if ev.CodeID == "" || ev.OccurredAt.IsZero() {
			return fmt.Errorf("postgres: event %d is malformed", i)
		}
	}
	for i, r := range b.Rollups {
		if r.CodeID == "" || r.Dimension == "" || r.HourBucket.IsZero() {
			return fmt.Errorf("postgres: rollup %d is malformed", i)
		}
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if len(b.Events) > 0 {
			ins, err := tx.PrepareContext(ctx, `INSERT INTO scan_events (code_id, occurred_at, ua_family, device_category, referrer_host, is_bot) VALUES ($1, $2, $3, $4, $5, $6)`)
			if err != nil {
				return translate(err)
			}
			defer func() { _ = ins.Close() }()
			for _, ev := range b.Events {
				if _, err := ins.ExecContext(ctx, ev.CodeID, utc(ev.OccurredAt), ev.UAFamily, string(ev.DeviceCategory), ev.ReferrerHost, ev.IsBot); err != nil {
					return translate(err)
				}
			}
		}
		if len(b.Rollups) > 0 {
			up, err := tx.PrepareContext(ctx, `INSERT INTO scan_rollups (code_id, hour_bucket, dimension, value, count) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (code_id, hour_bucket, dimension, value) DO UPDATE SET count = scan_rollups.count + excluded.count`)
			if err != nil {
				return translate(err)
			}
			defer func() { _ = up.Close() }()
			for _, r := range b.Rollups {
				if _, err := up.ExecContext(ctx, r.CodeID, r.HourBucket.UTC().Truncate(time.Hour), string(r.Dimension), r.Value, r.Count); err != nil {
					return translate(err)
				}
			}
		}
		return nil
	})
}

// QueryAnalytics reads rollups over [From, To) and buckets in Go (shared week rule).
func (s *Store) QueryAnalytics(ctx context.Context, q domain.AnalyticsQuery) (*domain.AnalyticsResult, error) {
	bucket := q.Bucket
	if bucket == "" {
		bucket = domain.BucketDay
	}
	rows, err := s.db.QueryContext(ctx, `SELECT hour_bucket, dimension, value, count FROM scan_rollups
		WHERE code_id = $1 AND hour_bucket >= $2 AND hour_bucket < $3`, q.CodeID, utc(q.From), utc(q.To))
	if err != nil {
		return nil, translate(err)
	}
	defer func() { _ = rows.Close() }()
	agg := newAggregator(bucket)
	for rows.Next() {
		var (
			hour       time.Time
			dim, value string
			n          int64
		)
		if err := rows.Scan(&hour, &dim, &value, &n); err != nil {
			return nil, translate(err)
		}
		agg.add(hour.UTC(), domain.Dimension(dim), value, n)
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
func (s *Store) PruneScanEvents(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM scan_events WHERE id IN (
		SELECT id FROM scan_events WHERE occurred_at < $1 ORDER BY occurred_at, id LIMIT $2)`, utc(before), limit)
	if err != nil {
		return 0, translate(err)
	}
	n, err := res.RowsAffected()
	return n, translate(err)
}

// ---- bulk iteration ------------------------------------------------------------------
//
// Each walker streams a rows cursor and hands rows to fn as they are scanned; fn may
// call back into the store on another pooled connection, and its first error ends the
// walk and is returned unwrapped.

func (s *Store) ForEachUser(ctx context.Context, fn func(*domain.User) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at, id`)
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+codeCols+codeFrom+`ORDER BY c.created_at, c.id`)
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
	rows, err := s.db.QueryContext(ctx, `SELECT code_id, hour_bucket, dimension, value, count FROM scan_rollups ORDER BY code_id, hour_bucket, dimension, value`)
	if err != nil {
		return translate(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			r    domain.RollupDelta
			hour time.Time
			dim  string
		)
		if err := rows.Scan(&r.CodeID, &hour, &dim, &r.Value, &r.Count); err != nil {
			return translate(err)
		}
		r.HourBucket = hour.UTC()
		r.Dimension = domain.Dimension(dim)
		if err := fn(r); err != nil {
			return err
		}
	}
	return translate(rows.Err())
}

func (s *Store) ForEachReservation(ctx context.Context, fn func(domain.AliasReservation) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT short_code, COALESCE(code_id, ''), reserved_at, released_at FROM alias_reservations ORDER BY short_code`)
	if err != nil {
		return translate(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			r        domain.AliasReservation
			reserved time.Time
			released sql.NullTime
		)
		if err := rows.Scan(&r.ShortCode, &r.CodeID, &reserved, &released); err != nil {
			return translate(err)
		}
		r.ReservedAt = reserved.UTC()
		r.ReleasedAt = fromNull(released)
		if err := fn(r); err != nil {
			return err
		}
	}
	return translate(rows.Err())
}

// ---- lifecycle -----------------------------------------------------------------------

func (s *Store) Migrate(ctx context.Context) error {
	return migrations.Apply(ctx, s.db, migrations.Postgres)
}

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) Close() error { return s.db.Close() }
