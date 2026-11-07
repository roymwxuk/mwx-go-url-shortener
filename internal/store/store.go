// Package store defines persistence for short links.
package store

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("link not found")
	ErrConflict = errors.New("code already exists")
)

type Link struct {
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	Clicks    int64     `json:"clicks"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is implemented by MemoryStore (dev/tests) and PostgresStore (prod).
type Store interface {
	Create(ctx context.Context, l Link) (Link, error) // returns ErrConflict if code exists
	Get(ctx context.Context, code string) (Link, error)
	// Resolve returns the target URL and increments the click counter.
	Resolve(ctx context.Context, code string) (string, error)
	Delete(ctx context.Context, code string) error
	Ping(ctx context.Context) error
	Close()
}
