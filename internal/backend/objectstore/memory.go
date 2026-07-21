package objectstore

import (
	"context"
	"errors"
	"sync"

	"github.com/hookship/hook-service/internal/runtime"
)

// MemoryStore is an in-process PayloadStore used by tests, demos, and the
// loopback evaluation profile. It enforces the same immutability and end-to-end
// checksum guarantees as the S3 client so the object-store acceptance path can
// be exercised without any external service. It is safe for concurrent use.
type MemoryStore struct {
	mu      sync.Mutex
	objects map[string]memoryObject
	seq     uint64

	// failPut, when set, is consulted before each write; a non-nil return aborts
	// the PUT so tests can drive the no-false-acceptance and orphan paths.
	failPut func(key string) error
	// failGet, when set, is consulted before each read.
	failGet func(key string) error
}

type memoryObject struct {
	data     []byte
	checksum runtime.Hash
	version  string
}

var _ runtime.PayloadStore = (*MemoryStore)(nil)

// NewMemoryStore builds an empty in-memory object store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{objects: make(map[string]memoryObject)}
}

// SetPutFault installs a fault hook consulted before each PutImmutable write.
func (m *MemoryStore) SetPutFault(fn func(key string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failPut = fn
}

// SetGetFault installs a fault hook consulted before each Get read.
func (m *MemoryStore) SetGetFault(fn func(key string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failGet = fn
}

// ObjectCount reports how many immutable objects are currently stored.
func (m *MemoryStore) ObjectCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.objects)
}

// Has reports whether an object exists at key.
func (m *MemoryStore) Has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[key]
	return ok
}

// Name identifies the store.
func (m *MemoryStore) Name() string { return "memory" }

// PutImmutable writes ciphertext, rejecting a conflicting overwrite.
func (m *MemoryStore) PutImmutable(ctx context.Context, key string, data []byte, checksum runtime.Hash) (runtime.ObjectPutResult, error) {
	if len(data) == 0 {
		return runtime.ObjectPutResult{}, errors.New("objectstore: ciphertext must not be empty")
	}
	if runtime.SHA256(data) != checksum {
		return runtime.ObjectPutResult{}, errors.New("objectstore: ciphertext does not match declared checksum")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut != nil {
		if err := m.failPut(key); err != nil {
			return runtime.ObjectPutResult{}, err
		}
	}
	if existing, ok := m.objects[key]; ok {
		if existing.checksum != checksum {
			return runtime.ObjectPutResult{}, runtime.ErrPayloadObjectConflict
		}
		return runtime.ObjectPutResult{Version: existing.version, AlreadyExisted: true}, nil
	}
	m.seq++
	version := "mem-" + checksum.String()[:16]
	stored := memoryObject{data: append([]byte(nil), data...), checksum: checksum, version: version}
	m.objects[key] = stored
	return runtime.ObjectPutResult{Version: version}, nil
}

// Get reads and verifies ciphertext.
func (m *MemoryStore) Get(ctx context.Context, key string, checksum runtime.Hash) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet != nil {
		if err := m.failGet(key); err != nil {
			return nil, err
		}
	}
	object, ok := m.objects[key]
	if !ok {
		return nil, runtime.ErrPayloadObjectNotFound
	}
	if object.checksum != checksum || runtime.SHA256(object.data) != checksum {
		return nil, errors.New("objectstore: object failed checksum verification")
	}
	return append([]byte(nil), object.data...), nil
}

// Delete removes the object at key. Missing keys succeed.
func (m *MemoryStore) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

// Ping always succeeds for the in-memory store.
func (m *MemoryStore) Ping(ctx context.Context) error { return nil }
