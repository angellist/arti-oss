package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"sync"
	"time"
)

// InMemory is an in-process [Store] for tests.
type InMemory struct {
	mu      sync.RWMutex
	objects map[string]inmemObject
}

type inmemObject struct {
	data []byte
	info ObjectInfo
}

// NewInMemory returns a fresh in-memory blob store.
func NewInMemory() *InMemory {
	return &InMemory{objects: make(map[string]inmemObject)}
}

// Put implements [Store.Put]. Buffers fully (acceptable for tests).
func (s *InMemory) Put(_ context.Context, key string, r io.Reader, opts PutOpts) (PutResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return PutResult{}, err
	}
	sum := sha256.Sum256(data)
	shaHex := hex.EncodeToString(sum[:])

	obj := inmemObject{
		data: data,
		info: ObjectInfo{
			Key:          key,
			Size:         int64(len(data)),
			SHA256:       shaHex,
			ContentType:  opts.ContentType,
			LastModified: time.Now().UTC(),
		},
	}

	s.mu.Lock()
	s.objects[key] = obj
	s.mu.Unlock()

	return PutResult{Key: key, Size: obj.info.Size, SHA256: shaHex}, nil
}

// Get implements [Store.Get].
func (s *InMemory) Get(_ context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	s.mu.RLock()
	obj, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return nil, ObjectInfo{}, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(obj.data)), obj.info, nil
}

// Stat implements [Store.Stat].
func (s *InMemory) Stat(_ context.Context, key string) (ObjectInfo, error) {
	s.mu.RLock()
	obj, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return ObjectInfo{}, ErrNotFound
	}
	return obj.info, nil
}

// Delete implements [Store.Delete]. Deleting a missing key is not an error.
func (s *InMemory) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.objects, key)
	s.mu.Unlock()
	return nil
}

// List implements [Store.List]. Returns all objects whose key begins with
// prefix, ordered ascending.
func (s *InMemory) List(_ context.Context, prefix string) ([]ObjectInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ObjectInfo, 0, len(s.objects))
	for k, obj := range s.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, obj.info)
		}
	}
	// Stable sort by key for predictable list order in tests.
	sortObjectInfoByKey(out)
	return out, nil
}
