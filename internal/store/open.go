package store

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Opener constructs a Store from a DSN. Drivers register themselves in init().
type Opener func(ctx context.Context, dsn string) (Store, error)

var (
	mu      sync.RWMutex
	openers = map[string]Opener{}
)

// Register makes a driver available by name. Called from driver package init();
// importing the driver package is how a binary opts in (cmd/qurator does this).
func Register(name string, o Opener) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := openers[name]; dup {
		panic("store: driver registered twice: " + name)
	}
	openers[name] = o
}

// Open selects a driver by name. Unknown names return ErrUnknownDriver listing what is
// available, so a typo in QURATOR_DB_DRIVER is diagnosable from the error alone.
func Open(ctx context.Context, driver, dsn string) (Store, error) {
	mu.RLock()
	o, ok := openers[driver]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q (registered: %v)", ErrUnknownDriver, driver, Drivers())
	}
	return o(ctx, dsn)
}

// Drivers lists registered driver names, sorted.
func Drivers() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(openers))
	for k := range openers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
