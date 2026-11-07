package store

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is a thread-safe in-memory Store. Data is lost on restart.
type MemoryStore struct {
	mu    sync.RWMutex
	links map[string]Link
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{links: make(map[string]Link)}
}

func (m *MemoryStore) Create(_ context.Context, l Link) (Link, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.links[l.Code]; ok {
		return Link{}, ErrConflict
	}
	l.CreatedAt = time.Now().UTC()
	l.Clicks = 0
	m.links[l.Code] = l
	return l, nil
}

func (m *MemoryStore) Get(_ context.Context, code string) (Link, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l, ok := m.links[code]
	if !ok {
		return Link{}, ErrNotFound
	}
	return l, nil
}

func (m *MemoryStore) Resolve(_ context.Context, code string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.links[code]
	if !ok {
		return "", ErrNotFound
	}
	l.Clicks++
	m.links[code] = l
	return l.URL, nil
}

func (m *MemoryStore) Delete(_ context.Context, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.links[code]; !ok {
		return ErrNotFound
	}
	delete(m.links, code)
	return nil
}

func (m *MemoryStore) Ping(context.Context) error { return nil }
func (m *MemoryStore) Close()                     {}
