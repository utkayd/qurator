package codes

import (
	"strings"
	"time"

	"github.com/maypok86/otter/v2"

	"github.com/utkayd/qurator/internal/domain"
)

const (
	// CacheEntries bounds the resolver cache; W-TinyLFU keeps hot campaign codes resident
	// under a skewed workload (research.md §4).
	CacheEntries = 50_000
	// CacheTTL is the backstop staleness window for what explicit invalidation cannot
	// reach (a restart, a future second instance). It leaves 30s of SC-008's 60s budget.
	CacheTTL = 30 * time.Second
)

// Resolution is what the scan path needs and nothing more. A zero CodeID with
// Found=false is a cached negative: unknown codes are remembered too, so a hot
// misprinted or probing short code costs one lookup per TTL, not one per scan.
type Resolution struct {
	CodeID      string
	Destination string
	State       domain.CodeState
	Found       bool
}

// Cache maps lower-cased short codes to resolutions.
type Cache struct {
	c *otter.Cache[string, Resolution]
}

// NewCache builds the bounded, TTL-expiring cache.
func NewCache() *Cache {
	return &Cache{c: otter.Must(&otter.Options[string, Resolution]{
		MaximumSize:      CacheEntries,
		ExpiryCalculator: otter.ExpiryCreating[string, Resolution](CacheTTL),
	})}
}

func cacheKey(shortCode string) string { return strings.ToLower(strings.TrimSpace(shortCode)) }

// Get returns the cached resolution for a short code, if present and unexpired.
func (c *Cache) Get(shortCode string) (Resolution, bool) {
	return c.c.GetIfPresent(cacheKey(shortCode))
}

// Set stores a resolution.
func (c *Cache) Set(shortCode string, r Resolution) {
	c.c.Set(cacheKey(shortCode), r)
}

// Invalidate drops one short code. The service calls it on every mutation so a
// destination change or state change is visible on the very next scan of this instance.
func (c *Cache) Invalidate(shortCode string) {
	c.c.Invalidate(cacheKey(shortCode))
}
