package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/utkayd/qurator/internal/config"
	"github.com/utkayd/qurator/internal/domain"
	"github.com/utkayd/qurator/internal/store"
)

// Sentinel errors. Handlers map them to the error catalogue.
var (
	// ErrUnauthorized covers a missing, malformed, expired, or otherwise invalid credential.
	ErrUnauthorized = errors.New("auth: unauthorized")
	// ErrTokenRevoked is distinct so a CI job learns its token was revoked (errors.md).
	ErrTokenRevoked = errors.New("auth: token revoked")
)

// Default tuning. The cache TTL is the worst-case revocation propagation delay, half the
// 60-second budget SC-011 allows.
const (
	DefaultSessionTTL = 12 * time.Hour
	DefaultCacheTTL   = 30 * time.Second
	touchInterval     = time.Minute
)

// AuthOptions configures an Authenticator. Zero values fall back to the defaults above.
type AuthOptions struct {
	SigningSecret config.Secret            // HS256 key for session JWTs
	DevMode       bool                     // allow an empty SigningSecret (ephemeral key)
	SessionTTL    time.Duration            // session lifetime; default 12h
	TokenPepper   config.Secret            // optional HMAC pepper for API-token hashes
	ForwardAuth   config.ForwardAuthConfig // delegated identity mode
	CacheTTL      time.Duration            // positive cache TTL; default 30s
	Logger        *slog.Logger             // default slog.Default()
}

// Authenticator verifies every credential kind and issues sessions and tokens.
type Authenticator struct {
	store      store.Store
	now        func() time.Time
	log        *slog.Logger
	signingKey []byte
	sessionTTL time.Duration
	cacheTTL   time.Duration
	pepper     []byte

	fwdEnabled bool
	fwdHeader  string
	fwdTrusted []*net.IPNet

	tokens *ttlCache[*domain.APIToken] // positive cache keyed by token ID
	users  *ttlCache[*domain.User]     // positive cache keyed by user ID

	touchMu   sync.Mutex
	lastTouch map[string]time.Time
}

// New builds an Authenticator. It refuses an empty signing secret unless DevMode is set
// (FR-040), in which case a random ephemeral key is derived and a warning logged: every
// session dies with the process. Forward-auth enabled with no trusted CIDR is refused
// (fail closed).
func New(st store.Store, cfg AuthOptions, now func() time.Time) (*Authenticator, error) {
	if now == nil {
		now = time.Now
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	key := []byte(cfg.SigningSecret.Reveal())
	if len(key) == 0 {
		if !cfg.DevMode {
			return nil, errors.New("auth: no signing secret configured and dev_mode is off (FR-040)")
		}
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("auth: derive ephemeral signing key: %w", err)
		}
		log.Warn("auth: dev_mode with no signing secret; using an ephemeral key — every session is invalidated at restart")
	}
	a := &Authenticator{
		store:      st,
		now:        now,
		log:        log,
		signingKey: key,
		sessionTTL: cfg.SessionTTL,
		cacheTTL:   cfg.CacheTTL,
		pepper:     []byte(cfg.TokenPepper.Reveal()),
		fwdEnabled: cfg.ForwardAuth.Enabled,
		fwdHeader:  cfg.ForwardAuth.Header,
		lastTouch:  map[string]time.Time{},
	}
	if a.sessionTTL <= 0 {
		a.sessionTTL = DefaultSessionTTL
	}
	if a.cacheTTL <= 0 {
		a.cacheTTL = DefaultCacheTTL
	}
	if a.fwdHeader == "" {
		a.fwdHeader = "X-Forwarded-Email"
	}
	if a.fwdEnabled {
		if len(cfg.ForwardAuth.TrustedCIDRs) == 0 {
			return nil, errors.New("auth: forward_auth.enabled with no trusted_cidrs (refusing to trust the identity header from any peer)")
		}
		for _, c := range cfg.ForwardAuth.TrustedCIDRs {
			_, n, err := net.ParseCIDR(c)
			if err != nil {
				return nil, fmt.Errorf("auth: forward_auth.trusted_cidrs: invalid CIDR %q: %w", c, err)
			}
			a.fwdTrusted = append(a.fwdTrusted, n)
		}
	}
	a.tokens = newTTLCache[*domain.APIToken](now, a.cacheTTL)
	a.users = newTTLCache[*domain.User](now, a.cacheTTL)
	return a, nil
}

// SessionTTL reports the configured session lifetime.
func (a *Authenticator) SessionTTL() time.Duration { return a.sessionTTL }

// ttlCache is a small positive cache: entries are only ever written after a successful
// verification and expire after ttl. It is deliberately not a blacklist (research.md §2).
type ttlCache[T any] struct {
	mu      sync.Mutex
	now     func() time.Time
	ttl     time.Duration
	entries map[string]cacheEntry[T]
}

type cacheEntry[T any] struct {
	val T
	exp time.Time
}

func newTTLCache[T any](now func() time.Time, ttl time.Duration) *ttlCache[T] {
	return &ttlCache[T]{now: now, ttl: ttl, entries: map[string]cacheEntry[T]{}}
}

func (c *ttlCache[T]) get(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		var zero T
		return zero, false
	}
	if !c.now().Before(e.exp) {
		delete(c.entries, key)
		var zero T
		return zero, false
	}
	return e.val, true
}

func (c *ttlCache[T]) put(key string, val T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	// Opportunistic sweep keeps the map bounded without a background goroutine.
	if len(c.entries) >= 4096 {
		for k, e := range c.entries {
			if !now.Before(e.exp) {
				delete(c.entries, k)
			}
		}
	}
	c.entries[key] = cacheEntry[T]{val: val, exp: now.Add(c.ttl)}
}

func (c *ttlCache[T]) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]cacheEntry[T]{}
}
