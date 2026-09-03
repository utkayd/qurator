package blobtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"
	"time"

	"github.com/utkayd/qurator/internal/blob"
)

// memEntry is one stored object.
type memEntry struct {
	data        []byte
	contentType string
	etag        string
	modTime     time.Time
}

// memBlob is an in-memory blob.BlobStore for tests.
type memBlob struct {
	mu      sync.Mutex
	objects map[string]memEntry
}

// NewMemBlob returns a fresh, empty in-memory BlobStore.
func NewMemBlob() blob.BlobStore {
	return &memBlob{objects: make(map[string]memEntry)}
}

func etagFor(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (m *memBlob) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	if err := blob.ValidateKey(key); err != nil {
		return "", err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	etag := etagFor(data)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memEntry{
		data:        data,
		contentType: contentType,
		etag:        etag,
		modTime:     time.Now(),
	}
	return etag, nil
}

func (m *memBlob) Get(ctx context.Context, key string) (io.ReadCloser, *blob.BlobInfo, error) {
	if err := blob.ValidateKey(key); err != nil {
		return nil, nil, err
	}

	m.mu.Lock()
	entry, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, nil, blob.ErrBlobNotFound
	}

	info := &blob.BlobInfo{
		Key:         key,
		Size:        int64(len(entry.data)),
		ContentType: entry.contentType,
		ETag:        entry.etag,
		ModTime:     entry.modTime,
	}
	return io.NopCloser(bytes.NewReader(entry.data)), info, nil
}

func (m *memBlob) Stat(ctx context.Context, key string) (*blob.BlobInfo, error) {
	if err := blob.ValidateKey(key); err != nil {
		return nil, err
	}

	m.mu.Lock()
	entry, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, blob.ErrBlobNotFound
	}

	return &blob.BlobInfo{
		Key:         key,
		Size:        int64(len(entry.data)),
		ContentType: entry.contentType,
		ETag:        entry.etag,
		ModTime:     entry.modTime,
	}, nil
}

func (m *memBlob) Delete(ctx context.Context, key string) error {
	if err := blob.ValidateKey(key); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[key]; !ok {
		return blob.ErrBlobNotFound
	}
	delete(m.objects, key)
	return nil
}

func (m *memBlob) Ping(ctx context.Context) error {
	return nil
}
