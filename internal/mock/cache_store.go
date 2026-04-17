package mock

import (
	"context"
	"sync"
	"time"

	"github.com/contextos/contextos/internal/types"
)

// Compile-time check that MemoryCacheStore implements types.CacheStore.
var _ types.CacheStore = (*MemoryCacheStore)(nil)

type cacheEntry struct {
	value     []byte
	expiresAt time.Time
}

// MemoryCacheStore is an in-memory implementation of types.CacheStore with TTL support.
type MemoryCacheStore struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	subs    map[string]map[chan []byte]struct{}
}

// NewMemoryCacheStore creates a new MemoryCacheStore.
func NewMemoryCacheStore() *MemoryCacheStore {
	return &MemoryCacheStore{
		entries: make(map[string]cacheEntry),
		subs:    make(map[string]map[chan []byte]struct{}),
	}
}

// Get retrieves a value by key. Returns nil, nil if not found or expired.
func (m *MemoryCacheStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	e, ok := m.entries[key]
	m.mu.RUnlock()

	if !ok {
		return nil, nil
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		m.mu.Lock()
		delete(m.entries, key)
		m.mu.Unlock()
		return nil, nil
	}

	// Return a copy to prevent external mutation.
	val := make([]byte, len(e.value))
	copy(val, e.value)
	return val, nil
}

// Set stores a value with the given TTL. A zero TTL means no expiration.
func (m *MemoryCacheStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	val := make([]byte, len(value))
	copy(val, value)

	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	m.entries[key] = cacheEntry{value: val, expiresAt: exp}
	return nil
}

// Delete removes a key.
func (m *MemoryCacheStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.entries, key)
	return nil
}

// SetNX sets a value only if the key does not already exist. Returns true if the key was set.
func (m *MemoryCacheStore) SetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if key exists and is not expired.
	if e, ok := m.entries[key]; ok {
		if e.expiresAt.IsZero() || time.Now().Before(e.expiresAt) {
			return false, nil
		}
	}

	val := make([]byte, len(value))
	copy(val, value)

	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	m.entries[key] = cacheEntry{value: val, expiresAt: exp}
	return true, nil
}

// Publish broadcasts a message to in-memory subscribers of the given channel.
func (m *MemoryCacheStore) Publish(_ context.Context, channel string, value []byte) error {
	m.mu.RLock()
	subs := m.subs[channel]
	m.mu.RUnlock()

	for ch := range subs {
		msg := make([]byte, len(value))
		copy(msg, value)
		select {
		case ch <- msg:
		default:
		}
	}
	return nil
}

// Subscribe registers an in-memory subscriber for the given channel.
func (m *MemoryCacheStore) Subscribe(_ context.Context, channel string) (<-chan []byte, func() error, error) {
	ch := make(chan []byte, 16)
	m.mu.Lock()
	if m.subs[channel] == nil {
		m.subs[channel] = make(map[chan []byte]struct{})
	}
	m.subs[channel][ch] = struct{}{}
	m.mu.Unlock()

	closeFn := func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.subs[channel], ch)
		close(ch)
		return nil
	}
	return ch, closeFn, nil
}
