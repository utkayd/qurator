package migrations

import (
	"context"
	"database/sql"
)

// up0001 creates every table in data-model.md.
//
// Dialect branches (the only two irreducible divergences, research.md §3):
//   - identity column on scan_events
//   - case-insensitive unique indexes on users.email, codes.short_code,
//     alias_reservations.short_code
//
// Everything else is written once in portable SQL. Timestamps are TEXT (RFC3339, UTC)
// on SQLite and TIMESTAMPTZ on Postgres via the ts() helper; booleans are INTEGER 0/1 on
// SQLite and BOOLEAN on Postgres via bool().
func up0001(ctx context.Context, tx *sql.Tx, d Dialect) error {
	ts, boolean := "TEXT", "INTEGER"
	if d == Postgres {
		ts, boolean = "TIMESTAMPTZ", "BOOLEAN"
	}

	stmts := []string{
		`CREATE TABLE users (
			id            TEXT PRIMARY KEY,
			email         TEXT NOT NULL,
			password_hash TEXT NULL,
			is_admin      ` + boolean + ` NOT NULL DEFAULT ` + falseLit(d) + `,
			token_version INTEGER NOT NULL DEFAULT 0,
			source        TEXT NOT NULL,
			created_at    ` + ts + ` NOT NULL,
			last_login_at ` + ts + ` NULL
		)`,
		`CREATE TABLE api_tokens (
			id           TEXT PRIMARY KEY,
			user_id      TEXT NOT NULL REFERENCES users(id),
			name         TEXT NOT NULL,
			secret_hash  ` + blob(d) + ` NOT NULL,
			created_at   ` + ts + ` NOT NULL,
			last_used_at ` + ts + ` NULL,
			revoked_at   ` + ts + ` NULL,
			expires_at   ` + ts + ` NULL
		)`,
		`CREATE INDEX api_tokens_user ON api_tokens(user_id)`,
		`CREATE TABLE sessions (
			id         TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL REFERENCES users(id),
			issued_at  ` + ts + ` NOT NULL,
			expires_at ` + ts + ` NOT NULL,
			revoked_at ` + ts + ` NULL
		)`,
		`CREATE TABLE styling_profiles (
			id                 TEXT PRIMARY KEY,
			fg_color           TEXT NOT NULL,
			bg_color           TEXT NOT NULL,
			module_shape       TEXT NOT NULL,
			margin_modules     INTEGER NOT NULL,
			size_px            INTEGER NOT NULL,
			ec_level           TEXT NOT NULL,
			ec_level_effective TEXT NOT NULL,
			logo_blob_key      TEXT NULL,
			logo_scale         REAL NULL
		)`,
		`CREATE TABLE codes (
			id          TEXT PRIMARY KEY,
			short_code  TEXT NOT NULL,
			is_alias    ` + boolean + ` NOT NULL,
			user_id     TEXT NOT NULL REFERENCES users(id),
			destination TEXT NOT NULL,
			state       TEXT NOT NULL,
			styling_id  TEXT NOT NULL REFERENCES styling_profiles(id),
			blob_key    TEXT NOT NULL,
			blob_etag   TEXT NOT NULL,
			version     INTEGER NOT NULL DEFAULT 1,
			created_at  ` + ts + ` NOT NULL,
			updated_at  ` + ts + ` NOT NULL,
			deleted_at  ` + ts + ` NULL
		)`,
		`CREATE INDEX codes_user_created ON codes(user_id, created_at DESC, id)`,
		`CREATE INDEX codes_user_dest ON codes(user_id, destination)`,
		`CREATE TABLE alias_reservations (
			short_code  TEXT PRIMARY KEY,
			code_id     TEXT NULL REFERENCES codes(id),
			reserved_at ` + ts + ` NOT NULL,
			released_at ` + ts + ` NULL
		)`,
		// scan_events: deliberately NO address column and NO geography column (FR-022, FR-025).
		`CREATE TABLE scan_events (
			id              ` + identity(d) + `,
			code_id         TEXT NOT NULL REFERENCES codes(id),
			occurred_at     ` + ts + ` NOT NULL,
			ua_family       TEXT NOT NULL,
			device_category TEXT NOT NULL,
			referrer_host   TEXT NOT NULL,
			is_bot          ` + boolean + ` NOT NULL
		)`,
		`CREATE INDEX scan_events_code_time ON scan_events(code_id, occurred_at)`,
		`CREATE TABLE scan_rollups (
			code_id     TEXT NOT NULL REFERENCES codes(id),
			hour_bucket ` + ts + ` NOT NULL,
			dimension   TEXT NOT NULL,
			value       TEXT NOT NULL,
			count       INTEGER NOT NULL,
			PRIMARY KEY (code_id, hour_bucket, dimension, value)
		)`,
	}

	// Case-insensitive uniqueness — the second irreducible divergence.
	switch d {
	case SQLite:
		stmts = append(stmts,
			`CREATE UNIQUE INDEX users_email_ci ON users(email COLLATE NOCASE)`,
			`CREATE UNIQUE INDEX codes_short_code_ci ON codes(short_code COLLATE NOCASE)`,
		)
	case Postgres:
		stmts = append(stmts,
			`CREATE UNIQUE INDEX users_email_ci ON users(lower(email))`,
			`CREATE UNIQUE INDEX codes_short_code_ci ON codes(lower(short_code))`,
		)
	}
	return exec(ctx, tx, stmts...)
}

func identity(d Dialect) string {
	if d == Postgres {
		return "BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY"
	}
	return "INTEGER PRIMARY KEY AUTOINCREMENT"
}

func blob(d Dialect) string {
	if d == Postgres {
		return "BYTEA"
	}
	return "BLOB"
}

func falseLit(d Dialect) string {
	if d == Postgres {
		return "FALSE"
	}
	return "0"
}
