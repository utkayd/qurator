package storetest

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
)

// memStore is an in-memory store.Store for tests of packages above the store layer. It
// is not a driver: it never ships in the binary. It follows every rule the contract suite
// pins so that a package tested against it behaves the same against SQLite or PostgreSQL.
type memStore struct {
	mu sync.Mutex

	users  map[string]*domain.User     // by ID
	emails map[string]string           // lower(email) -> user ID
	tokens map[string]*domain.APIToken // by ID
	codes  map[string]*domain.Code     // by ID
	byCode map[string]string           // lower(short_code) -> code ID (live and deleted rows)
	byRef  map[refKey]string           // (user, client_ref) -> code ID (spec 003)

	// reservations mirrors alias_reservations: lower(short_code) -> reservation. A
	// reservation survives DeleteCode; ReleaseAlias marks it released rather than
	// removing it, and a later CreateCode for the same short code re-arms the same row.
	reservations map[string]*reservation

	events  []scanRow
	nextSeq int64
	rollups map[rollupKey]int64
}

// refKey mirrors the partial unique index codes_user_client_ref.
type refKey struct{ userID, ref string }

type reservation struct {
	codeID     string
	reservedAt time.Time
	releasedAt *time.Time
}

func (r *reservation) taken() bool { return r.releasedAt == nil }

type scanRow struct {
	seq int64
	ev  domain.ScanEvent
}

type rollupKey struct {
	codeID string
	hour   time.Time
	dim    domain.Dimension
	value  string
}

// NewMemStore returns an empty, "migrated" in-memory store.
func NewMemStore() store.Store {
	return &memStore{
		users:        map[string]*domain.User{},
		emails:       map[string]string{},
		tokens:       map[string]*domain.APIToken{},
		codes:        map[string]*domain.Code{},
		byCode:       map[string]string{},
		byRef:        map[refKey]string{},
		reservations: map[string]*reservation{},
		rollups:      map[rollupKey]int64{},
	}
}

const defaultListLimit = 50

// ---- users ---------------------------------------------------------------------------

func (m *memStore) CreateUser(ctx context.Context, u *domain.User) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if u.ID == "" || u.Email == "" {
		return errors.New("memstore: user id and email are required")
	}
	if _, dup := m.users[u.ID]; dup {
		return fmt.Errorf("memstore: user id %q: %w", u.ID, store.ErrConflict)
	}
	key := strings.ToLower(u.Email)
	if _, dup := m.emails[key]; dup {
		return fmt.Errorf("memstore: email %q: %w", u.Email, store.ErrConflict)
	}
	cp := copyUser(u)
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	m.users[cp.ID] = cp
	m.emails[key] = cp.ID
	return nil
}

func (m *memStore) GetUserByID(ctx context.Context, id string) (*domain.User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return nil, fmt.Errorf("memstore: user %q: %w", id, store.ErrNotFound)
	}
	return copyUser(u), nil
}

func (m *memStore) GetUserByEmail(ctx context.Context, email string) (*domain.User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.emails[strings.ToLower(email)]
	if !ok {
		return nil, fmt.Errorf("memstore: email %q: %w", email, store.ErrNotFound)
	}
	return copyUser(m.users[id]), nil
}

func (m *memStore) BumpTokenVersion(ctx context.Context, userID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[userID]
	if !ok {
		return 0, fmt.Errorf("memstore: user %q: %w", userID, store.ErrNotFound)
	}
	u.TokenVersion++
	return u.TokenVersion, nil
}

func (m *memStore) CountUsers(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.users)), nil
}

// ---- tokens --------------------------------------------------------------------------

func (m *memStore) CreateToken(ctx context.Context, t *domain.APIToken) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.ID == "" || t.UserID == "" {
		return errors.New("memstore: token id and user id are required")
	}
	if _, dup := m.tokens[t.ID]; dup {
		return fmt.Errorf("memstore: token id %q: %w", t.ID, store.ErrConflict)
	}
	cp := copyToken(t)
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	m.tokens[cp.ID] = cp
	return nil
}

func (m *memStore) GetTokenByID(ctx context.Context, id string) (*domain.APIToken, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return nil, fmt.Errorf("memstore: token %q: %w", id, store.ErrNotFound)
	}
	return copyToken(t), nil
}

func (m *memStore) ListTokens(ctx context.Context, userID string) ([]*domain.APIToken, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*domain.APIToken{}
	for _, t := range m.tokens {
		if t.UserID == userID {
			out = append(out, copyToken(t))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *memStore) RevokeToken(ctx context.Context, id, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok || t.UserID != userID {
		return fmt.Errorf("memstore: token %q: %w", id, store.ErrNotFound)
	}
	if t.RevokedAt == nil {
		now := time.Now().UTC()
		t.RevokedAt = &now
	}
	return nil
}

func (m *memStore) TouchTokenLastUsed(ctx context.Context, id string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	if !ok {
		return fmt.Errorf("memstore: token %q: %w", id, store.ErrNotFound)
	}
	at = at.UTC()
	t.LastUsedAt = &at
	return nil
}

// ---- codes ---------------------------------------------------------------------------

func (m *memStore) CreateCode(ctx context.Context, c *domain.Code) error {
	return m.CreateCodes(ctx, []*domain.Code{c})
}

// CreateCodes is all-or-nothing (spec 003, FR-207): every row is checked against the
// store AND against the rows before it in the batch, and nothing is written until all
// pass — one lock hold is the in-memory transaction.
func (m *memStore) CreateCodes(ctx context.Context, cs []*domain.Code) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seenID := map[string]bool{}
	seenCode := map[string]bool{}
	seenRef := map[refKey]bool{}
	for _, c := range cs {
		if c.ID == "" || c.ShortCode == "" || c.UserID == "" {
			return errors.New("memstore: code id, short code and user id are required")
		}
		if _, dup := m.codes[c.ID]; dup || seenID[c.ID] {
			return fmt.Errorf("memstore: code id %q: %w", c.ID, store.ErrConflict)
		}
		key := strings.ToLower(c.ShortCode)
		// One namespace: a live or deleted row, or a surviving reservation, all block it.
		if _, taken := m.byCode[key]; taken || seenCode[key] {
			return fmt.Errorf("memstore: short code %q: %w", c.ShortCode, store.ErrAliasTaken)
		}
		if r, reserved := m.reservations[key]; reserved && r.taken() {
			return fmt.Errorf("memstore: short code %q reserved: %w", c.ShortCode, store.ErrAliasTaken)
		}
		if c.ClientRef != "" {
			rk := refKey{c.UserID, c.ClientRef}
			if _, taken := m.byRef[rk]; taken || seenRef[rk] {
				return fmt.Errorf("memstore: client_ref %q: %w", c.ClientRef, store.ErrClientRefTaken)
			}
			seenRef[rk] = true
		}
		seenID[c.ID] = true
		seenCode[key] = true
	}
	now := time.Now().UTC()
	for _, c := range cs {
		key := strings.ToLower(c.ShortCode)
		cp := copyCode(c)
		cp.ShortCode = key
		if cp.CreatedAt.IsZero() {
			cp.CreatedAt = now
		}
		if cp.UpdatedAt.IsZero() {
			cp.UpdatedAt = cp.CreatedAt
		}
		if cp.State == "" {
			cp.State = domain.CodeActive
		}
		if cp.Mode == "" {
			cp.Mode = domain.ModeDynamic
		}
		if cp.Styling.ID == "" {
			cp.Styling.ID = domain.NewID("sty")
		}
		cp.Version = 1
		cp.DeletedAt = nil

		m.codes[cp.ID] = cp
		m.byCode[key] = cp.ID
		if cp.ClientRef != "" {
			m.byRef[refKey{cp.UserID, cp.ClientRef}] = cp.ID
		}
		m.reservations[key] = &reservation{codeID: cp.ID, reservedAt: now}

		// Reflect persisted values back to the caller, as a driver returning the row would.
		c.ShortCode = cp.ShortCode
		c.Version = cp.Version
		c.State = cp.State
		c.Mode = cp.Mode
		c.Styling = cp.Styling
		c.CreatedAt = cp.CreatedAt
		c.UpdatedAt = cp.UpdatedAt
	}
	return nil
}

func (m *memStore) GetCodeByClientRef(ctx context.Context, userID, ref string) (*domain.Code, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byRef[refKey{userID, ref}]
	if !ok || ref == "" {
		return nil, fmt.Errorf("memstore: client_ref %q: %w", ref, store.ErrNotFound)
	}
	return copyCode(m.codes[id]), nil
}

func (m *memStore) GetCodeByShortCode(ctx context.Context, shortCode string) (*domain.Code, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byCode[strings.ToLower(shortCode)]
	if !ok {
		return nil, fmt.Errorf("memstore: short code %q: %w", shortCode, store.ErrNotFound)
	}
	return copyCode(m.codes[id]), nil
}

func (m *memStore) GetCodeByID(ctx context.Context, id, userID string) (*domain.Code, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.owned(id, userID)
	if err != nil {
		return nil, err
	}
	return copyCode(c), nil
}

// owned returns the row only when it belongs to userID; any other outcome is ErrNotFound
// so a caller cannot distinguish "not yours" from "does not exist".
func (m *memStore) owned(id, userID string) (*domain.Code, error) {
	c, ok := m.codes[id]
	if !ok || c.UserID != userID {
		return nil, fmt.Errorf("memstore: code %q: %w", id, store.ErrNotFound)
	}
	return c, nil
}

type cursorPos struct {
	createdAt time.Time
	id        string
}

func encodeCursor(p cursorPos) string {
	raw := p.createdAt.UTC().Format(time.RFC3339Nano) + "\x00" + p.id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (cursorPos, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursorPos{}, fmt.Errorf("memstore: invalid cursor: %w", err)
	}
	ts, id, ok := strings.Cut(string(raw), "\x00")
	if !ok || id == "" {
		return cursorPos{}, errors.New("memstore: invalid cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return cursorPos{}, fmt.Errorf("memstore: invalid cursor: %w", err)
	}
	return cursorPos{createdAt: at, id: id}, nil
}

// ListCodes returns the owner's non-deleted codes newest-first, ordered by
// (created_at DESC, id DESC). The cursor names the last row returned; the next page is
// every row strictly after it in that order, so rows inserted mid-listing that sort
// before the cursor are simply not visited and rows already visited never reappear.
func (m *memStore) ListCodes(ctx context.Context, f domain.CodeFilter) ([]*domain.Code, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	var after *cursorPos
	if f.Cursor != "" {
		p, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		after = &p
	}
	dest := strings.ToLower(f.Destination)

	m.mu.Lock()
	defer m.mu.Unlock()

	var rows []*domain.Code
	for _, c := range m.codes {
		if c.UserID != f.UserID || c.State == domain.CodeDeleted {
			continue
		}
		if f.CreatedAfter != nil && !c.CreatedAt.After(*f.CreatedAfter) {
			continue
		}
		if f.CreatedBefore != nil && !c.CreatedAt.Before(*f.CreatedBefore) {
			continue
		}
		if dest != "" && !strings.Contains(strings.ToLower(c.Destination), dest) {
			continue
		}
		if after != nil && !beforeInListing(c, *after) {
			continue
		}
		rows = append(rows, c)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		}
		return rows[i].ID > rows[j].ID
	})

	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeCursor(cursorPos{createdAt: last.CreatedAt, id: last.ID})
	}
	out := make([]*domain.Code, len(rows))
	for i, c := range rows {
		out[i] = copyCode(c)
	}
	return out, next, nil
}

// beforeInListing reports whether c sorts strictly after the cursor position in the
// newest-first listing order, i.e. (created_at, id) < (cursor.created_at, cursor.id).
func beforeInListing(c *domain.Code, p cursorPos) bool {
	if !c.CreatedAt.Equal(p.createdAt) {
		return c.CreatedAt.Before(p.createdAt)
	}
	return c.ID < p.id
}

func (m *memStore) UpdateDestination(ctx context.Context, id, userID, dest string, expectedVersion int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.owned(id, userID)
	if err != nil {
		return err
	}
	if c.State == domain.CodeDeleted {
		return fmt.Errorf("memstore: code %q is deleted: %w", id, store.ErrConflict)
	}
	// Compare-and-increment on the integer counter; never on a timestamp.
	if c.Version != expectedVersion {
		return fmt.Errorf("memstore: code %q version %d != expected %d: %w", id, c.Version, expectedVersion, store.ErrConflict)
	}
	c.Destination = dest
	c.Version++
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *memStore) SetCodeState(ctx context.Context, id, userID string, state domain.CodeState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch state {
	case domain.CodeActive, domain.CodeDisabled:
	case domain.CodeDeleted:
		return errors.New("memstore: use DeleteCode to delete")
	default:
		return fmt.Errorf("memstore: unknown state %q", state)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.owned(id, userID)
	if err != nil {
		return err
	}
	if c.State == domain.CodeDeleted {
		return fmt.Errorf("memstore: code %q is deleted: %w", id, store.ErrConflict)
	}
	c.State = state
	c.Version++
	c.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *memStore) DeleteCode(ctx context.Context, id, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.owned(id, userID)
	if err != nil {
		return err
	}
	if c.State == domain.CodeDeleted {
		return nil
	}
	now := time.Now().UTC()
	c.State = domain.CodeDeleted
	c.DeletedAt = &now
	c.Version++
	c.UpdatedAt = now
	// The reservation stays, still pointing at the (now deleted) code (FR-018).
	return nil
}

// ---- aliases -------------------------------------------------------------------------

func (m *memStore) IsAliasAvailable(ctx context.Context, shortCode string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.ToLower(shortCode)
	if _, taken := m.byCode[key]; taken {
		return false, nil
	}
	if r, reserved := m.reservations[key]; reserved && r.taken() {
		return false, nil
	}
	return true, nil
}

func (m *memStore) ReleaseAlias(ctx context.Context, shortCode string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.ToLower(shortCode)
	r, ok := m.reservations[key]
	if !ok || !r.taken() {
		return fmt.Errorf("memstore: alias %q not reserved: %w", shortCode, store.ErrNotFound)
	}
	if c, live := m.codes[r.codeID]; live && c.State != domain.CodeDeleted {
		return fmt.Errorf("memstore: alias %q owned by live code: %w", shortCode, store.ErrConflict)
	}
	released := time.Now().UTC()
	r.releasedAt = &released
	// The deleted row keeps its short_code column, but it no longer blocks the namespace
	// and no longer resolves: the released alias is free to be re-registered.
	if id, exists := m.byCode[key]; exists {
		if c := m.codes[id]; c != nil && c.State == domain.CodeDeleted {
			delete(m.byCode, key)
		}
	}
	return nil
}

// ---- analytics -----------------------------------------------------------------------

func (m *memStore) InsertScanBatch(ctx context.Context, b domain.ScanBatch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Validate everything before writing anything: the batch is all-or-nothing.
	for i, ev := range b.Events {
		if _, ok := m.codes[ev.CodeID]; !ok {
			return fmt.Errorf("memstore: event %d references unknown code %q: %w", i, ev.CodeID, store.ErrNotFound)
		}
		if ev.OccurredAt.IsZero() {
			return fmt.Errorf("memstore: event %d has zero OccurredAt", i)
		}
	}
	for i, r := range b.Rollups {
		if _, ok := m.codes[r.CodeID]; !ok {
			return fmt.Errorf("memstore: rollup %d references unknown code %q: %w", i, r.CodeID, store.ErrNotFound)
		}
		if r.Dimension == "" || r.HourBucket.IsZero() {
			return fmt.Errorf("memstore: rollup %d is malformed", i)
		}
	}
	for _, ev := range b.Events {
		m.nextSeq++
		ev.OccurredAt = ev.OccurredAt.UTC()
		m.events = append(m.events, scanRow{seq: m.nextSeq, ev: ev})
	}
	for _, r := range b.Rollups {
		k := rollupKey{codeID: r.CodeID, hour: r.HourBucket.UTC().Truncate(time.Hour), dim: r.Dimension, value: r.Value}
		m.rollups[k] += r.Count
	}
	return nil
}

// QueryAnalytics reads rollups only, over hour buckets in [From, To).
func (m *memStore) QueryAnalytics(ctx context.Context, q domain.AnalyticsQuery) (*domain.AnalyticsResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bucket := q.Bucket
	if bucket == "" {
		bucket = domain.BucketDay
	}
	from, to := q.From.UTC(), q.To.UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	res := &domain.AnalyticsResult{
		Breakdowns: map[domain.Dimension][]domain.BreakdownValue{
			domain.DimUAFamily:       {},
			domain.DimDeviceCategory: {},
			domain.DimReferrerHost:   {},
			domain.DimIsBot:          {},
		},
	}
	series := map[time.Time]int64{}
	breakdown := map[domain.Dimension]map[string]int64{}
	for k, n := range m.rollups {
		if k.codeID != q.CodeID || k.hour.Before(from) || !k.hour.Before(to) {
			continue
		}
		if k.dim == domain.DimTotal {
			res.Total += n
			series[bucketStart(k.hour, bucket)] += n
			continue
		}
		if breakdown[k.dim] == nil {
			breakdown[k.dim] = map[string]int64{}
		}
		breakdown[k.dim][k.value] += n
	}
	res.Series = make([]domain.SeriesPoint, 0, len(series))
	for start, n := range series {
		res.Series = append(res.Series, domain.SeriesPoint{Start: start, Count: n})
	}
	sort.Slice(res.Series, func(i, j int) bool { return res.Series[i].Start.Before(res.Series[j].Start) })
	for dim, vals := range breakdown {
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
	return res, nil
}

// PruneScanEvents deletes at most limit raw events with OccurredAt < before, oldest
// first. Rollups are untouched.
func (m *memStore) PruneScanEvents(ctx context.Context, before time.Time, limit int) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, nil
	}
	before = before.UTC()
	m.mu.Lock()
	defer m.mu.Unlock()

	var victims []int
	for i, row := range m.events {
		if row.ev.OccurredAt.Before(before) {
			victims = append(victims, i)
		}
	}
	sort.Slice(victims, func(i, j int) bool {
		a, b := m.events[victims[i]], m.events[victims[j]]
		if !a.ev.OccurredAt.Equal(b.ev.OccurredAt) {
			return a.ev.OccurredAt.Before(b.ev.OccurredAt)
		}
		return a.seq < b.seq
	})
	if len(victims) > limit {
		victims = victims[:limit]
	}
	drop := make(map[int]bool, len(victims))
	for _, i := range victims {
		drop[i] = true
	}
	kept := m.events[:0]
	for i, row := range m.events {
		if !drop[i] {
			kept = append(kept, row)
		}
	}
	for i := len(kept); i < len(m.events); i++ {
		m.events[i] = scanRow{}
	}
	m.events = kept
	return int64(len(victims)), nil
}

// ---- bulk iteration ------------------------------------------------------------------
//
// Each walker snapshots the table under the lock and invokes fn with the lock released,
// because fn is allowed to call back into the store (export lists tokens per user from
// inside ForEachUser). A snapshot is fine here — this is a test fixture, not a driver.

func (m *memStore) ForEachUser(ctx context.Context, fn func(*domain.User) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	rows := make([]*domain.User, 0, len(m.users))
	for _, u := range m.users {
		rows = append(rows, copyUser(u))
	}
	m.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for _, u := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(u); err != nil {
			return err
		}
	}
	return nil
}

func (m *memStore) ForEachCode(ctx context.Context, fn func(*domain.Code) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	rows := make([]*domain.Code, 0, len(m.codes))
	for _, c := range m.codes {
		rows = append(rows, copyCode(c))
	}
	m.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for _, c := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(c); err != nil {
			return err
		}
	}
	return nil
}

func (m *memStore) ForEachRollup(ctx context.Context, fn func(domain.RollupDelta) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	rows := make([]domain.RollupDelta, 0, len(m.rollups))
	for k, n := range m.rollups {
		rows = append(rows, domain.RollupDelta{CodeID: k.codeID, HourBucket: k.hour, Dimension: k.dim, Value: k.value, Count: n})
	}
	m.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
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
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

func (m *memStore) ForEachReservation(ctx context.Context, fn func(domain.AliasReservation) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	rows := make([]domain.AliasReservation, 0, len(m.reservations))
	for sc, r := range m.reservations {
		rows = append(rows, domain.AliasReservation{
			ShortCode: sc, CodeID: r.codeID, ReservedAt: r.reservedAt.UTC(), ReleasedAt: copyTimePtr(r.releasedAt),
		})
	}
	m.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].ShortCode < rows[j].ShortCode })
	for _, r := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// ---- lifecycle -----------------------------------------------------------------------

func (m *memStore) Migrate(ctx context.Context) error { return ctx.Err() }
func (m *memStore) Ping(ctx context.Context) error    { return ctx.Err() }
func (m *memStore) Close() error                      { return nil }

// ---- copying (the store never aliases caller memory) ---------------------------------

func copyTimePtr(p *time.Time) *time.Time {
	if p == nil {
		return nil
	}
	t := p.UTC()
	return &t
}

func copyUser(u *domain.User) *domain.User {
	cp := *u
	cp.CreatedAt = u.CreatedAt.UTC()
	cp.LastLoginAt = copyTimePtr(u.LastLoginAt)
	return &cp
}

func copyToken(t *domain.APIToken) *domain.APIToken {
	cp := *t
	cp.SecretHash = append([]byte(nil), t.SecretHash...)
	cp.CreatedAt = t.CreatedAt.UTC()
	cp.LastUsedAt = copyTimePtr(t.LastUsedAt)
	cp.RevokedAt = copyTimePtr(t.RevokedAt)
	cp.ExpiresAt = copyTimePtr(t.ExpiresAt)
	return &cp
}

func copyCode(c *domain.Code) *domain.Code {
	cp := *c
	cp.CreatedAt = c.CreatedAt.UTC()
	cp.UpdatedAt = c.UpdatedAt.UTC()
	cp.DeletedAt = copyTimePtr(c.DeletedAt)
	return &cp
}
