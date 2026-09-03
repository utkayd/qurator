package blob

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Config carries every setting a blob driver might need; each driver reads its own.
type Config struct {
	Path      string // fs
	Endpoint  string // s3
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	PathStyle bool
}

// Opener constructs a BlobStore. Drivers register themselves in init().
type Opener func(ctx context.Context, cfg Config) (BlobStore, error)

var (
	mu      sync.RWMutex
	openers = map[string]Opener{}
)

// Register makes a driver available by name.
func Register(name string, o Opener) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := openers[name]; dup {
		panic("blob: driver registered twice: " + name)
	}
	openers[name] = o
}

// Open selects a driver by name; unknown names return ErrUnknownDriver.
func Open(ctx context.Context, driver string, cfg Config) (BlobStore, error) {
	mu.RLock()
	o, ok := openers[driver]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q (registered: %v)", ErrUnknownDriver, driver, Drivers())
	}
	return o(ctx, cfg)
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
