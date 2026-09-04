package export

import "time"

// FormatVersion is bumped whenever the JSONL row shapes below change incompatibly.
const FormatVersion = "1"

// Manifest is manifest.json, the first entry in every export archive. It is written
// last (after every entity's row count is known) but always read first, so Read can
// validate the format version and know which entity files to expect before it opens any
// of them.
type Manifest struct {
	Version    string           `json:"version"`
	ExportedAt time.Time        `json:"exported_at"`
	Entities   map[string]int64 `json:"entities"`
}

// userRecord is the users.jsonl row shape. It deliberately excludes PasswordHash: an
// export must never carry credentials (see package doc and the Hard rules in
// tasks.md T097). A user re-created from an export therefore has no usable local
// password — an operator must either run forward-auth for it or issue a fresh password.
type userRecord struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	IsAdmin      bool       `json:"is_admin"`
	TokenVersion int64      `json:"token_version"`
	Source       string     `json:"source"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

// tokenRecord is the api_tokens.jsonl row shape. It deliberately excludes SecretHash:
// even the hash is a credential-adjacent secret an export must not carry. A token
// re-created from an export is therefore a record of *that a token once existed*, not a
// usable credential — Read does not recreate api_tokens rows at all (see read.go) since
// a token with no secret hash can never authenticate and a placeholder hash would be
// misleading. The count is still reported in Entities for operator visibility.
type tokenRecord struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// reservationRecord is the alias_reservations.jsonl row shape, mirroring
// domain.AliasReservation: a reservation outlives the code that made it (FR-018) and is
// kept, with released_at set, after an admin releases it.
type reservationRecord struct {
	ShortCode  string     `json:"short_code"`
	CodeID     string     `json:"code_id,omitempty"`
	ReservedAt time.Time  `json:"reserved_at"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
}
