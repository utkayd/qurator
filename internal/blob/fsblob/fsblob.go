package fsblob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/utkayd/qurator/internal/blob"
)

func init() {
	blob.Register("fs", func(_ context.Context, cfg blob.Config) (blob.BlobStore, error) { return Open(cfg.Path) })
}

// Store is the filesystem driver. Every object lives at
//
//	<root>/objects/<h[0:2]>/<h[2:4]>/<key>
//
// where h is the hex SHA-256 of the key, so a flat namespace of many codes never puts
// hundreds of thousands of entries in one directory. A JSON sidecar under <root>/meta
// with the same relative path carries the content type and ETag, so Stat never has to
// re-hash the object. Both files are written with the crash-safe sequence from
// contracts/store.md: temp file in the same directory → fsync → rename → fsync(dir).
type Store struct {
	root  string
	locks [64]sync.Mutex // striped per-key locks keep meta and object consistent under concurrent Put
}

var _ blob.BlobStore = (*Store)(nil)

type meta struct {
	ContentType string `json:"content_type"`
	ETag        string `json:"etag"`
	Size        int64  `json:"size"`
}

// Open creates the root directory if needed and returns the driver.
func Open(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("fsblob: empty path")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("fsblob: resolve root: %w", err)
	}
	for _, d := range []string{filepath.Join(abs, "objects"), filepath.Join(abs, "meta")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("fsblob: create %s: %w", d, err)
		}
	}
	return &Store{root: abs}, nil
}

func (s *Store) paths(key string) (obj, mt string, err error) {
	if err := blob.ValidateKey(key); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:4])
	rel := filepath.Join(h[0:2], h[2:4], filepath.FromSlash(key))
	return filepath.Join(s.root, "objects", rel), filepath.Join(s.root, "meta", rel+".json"), nil
}

func (s *Store) lock(key string) *sync.Mutex {
	f := fnv.New32a()
	_, _ = f.Write([]byte(key))
	return &s.locks[f.Sum32()%uint32(len(s.locks))]
}

func translate(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("fsblob: %w", blob.ErrBlobNotFound)
	}
	return err
}

// Put streams r to a temp file beside the final path, hashing as it goes, then commits
// object and sidecar durably. The returned ETag is the hex SHA-256 of the content.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	objPath, metaPath, err := s.paths(key)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(objPath), 0o750); err != nil {
		return "", fmt.Errorf("fsblob: mkdir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o750); err != nil {
		return "", fmt.Errorf("fsblob: mkdir: %w", err)
	}

	// Stage the object outside the lock so concurrent writers overlap on I/O and only
	// serialise on the two renames.
	tmp, err := os.CreateTemp(filepath.Dir(objPath), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("fsblob: temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }

	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hasher), r)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("fsblob: write: %w", err)
	}
	if size >= 0 && n != size {
		cleanup()
		return "", fmt.Errorf("fsblob: wrote %d bytes, declared %d", n, size)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("fsblob: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("fsblob: close: %w", err)
	}
	etag := hex.EncodeToString(hasher.Sum(nil))
	metaRaw, err := json.Marshal(meta{ContentType: contentType, ETag: etag, Size: n})
	if err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}

	mu := s.lock(key)
	mu.Lock()
	defer mu.Unlock()
	if err := os.Rename(tmpName, objPath); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("fsblob: rename: %w", err)
	}
	if err := syncDir(filepath.Dir(objPath)); err != nil {
		return "", err
	}
	if err := writeFileDurably(metaPath, metaRaw); err != nil {
		return "", err
	}
	return etag, nil
}

// writeFileDurably applies the same temp → fsync → rename → fsync(dir) sequence to a
// small file whose content is already in memory.
func writeFileDurably(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("fsblob: temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("fsblob: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("fsblob: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("fsblob: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("fsblob: rename: %w", err)
	}
	return syncDir(filepath.Dir(path))
}

// syncDir fsyncs a directory so a completed rename survives a crash. This is the step
// that is commonly omitted (research.md §3).
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("fsblob: open dir: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsblob: fsync dir: %w", err)
	}
	return nil
}

func (s *Store) readMeta(metaPath string) (*meta, error) {
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, translate(err)
	}
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("fsblob: corrupt sidecar %s: %w", filepath.Base(metaPath), err)
	}
	return &m, nil
}

func (s *Store) stat(key string) (*blob.BlobInfo, string, error) {
	objPath, metaPath, err := s.paths(key)
	if err != nil {
		return nil, "", err
	}
	mu := s.lock(key)
	mu.Lock()
	defer mu.Unlock()
	fi, err := os.Stat(objPath)
	if err != nil {
		return nil, "", translate(err)
	}
	m, err := s.readMeta(metaPath)
	if err != nil {
		return nil, "", err
	}
	return &blob.BlobInfo{
		Key:         key,
		Size:        fi.Size(),
		ContentType: m.ContentType,
		ETag:        m.ETag,
		ModTime:     fi.ModTime().UTC(),
	}, objPath, nil
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, *blob.BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	info, objPath, err := s.stat(key)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(objPath)
	if err != nil {
		return nil, nil, translate(err)
	}
	return f, info, nil
}

func (s *Store) Stat(ctx context.Context, key string) (*blob.BlobInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, _, err := s.stat(key)
	return info, err
}

func (s *Store) Delete(ctx context.Context, key string) error {
	objPath, metaPath, err := s.paths(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	mu := s.lock(key)
	mu.Lock()
	defer mu.Unlock()
	if err := os.Remove(objPath); err != nil {
		return translate(err)
	}
	if err := os.Remove(metaPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("fsblob: remove sidecar: %w", err)
	}
	return syncDir(filepath.Dir(objPath))
}

// Ping verifies the root is a writable directory.
func (s *Store) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fi, err := os.Stat(s.root)
	if err != nil {
		return fmt.Errorf("fsblob: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("fsblob: %s is not a directory", s.root)
	}
	probe, err := os.CreateTemp(s.root, ".ping-*")
	if err != nil {
		return fmt.Errorf("fsblob: root not writable: %w", err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}
