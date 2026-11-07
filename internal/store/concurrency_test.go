package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
)

const concurrency = 1000

// Each test runs against every Store implementation: the in-memory store always,
// and Postgres when TEST_DATABASE_URL is set (e.g. via `make test-pg`).
func stores(t *testing.T) map[string]func(t *testing.T) Store {
	return map[string]func(t *testing.T) Store{
		"memory":   func(*testing.T) Store { return NewMemoryStore() },
		"postgres": newTestPostgres,
	}
}

func newTestPostgres(t *testing.T) Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		// Skipping is fine locally, but in CI it would silently drop the only
		// tests that exercise Postgres's real UNIQUE constraint.
		if os.Getenv("CI") != "" {
			t.Fatal("TEST_DATABASE_URL must be set in CI")
		}
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `TRUNCATE links`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// parallel runs fn(i) for i in [0,n) in n goroutines released at the same moment,
// so the calls overlap as much as possible.
func parallel(n int, fn func(i int)) {
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	ready.Add(n)
	done.Add(n)
	for i := range n {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			fn(i)
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
}

// 1,000 requests race for the SAME code: exactly one may win.
func TestConcurrentCreateSameCode(t *testing.T) {
	for name, newStore := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()

			var mu sync.Mutex
			var wins, conflicts int
			var winner string
			var other []error
			parallel(concurrency, func(i int) {
				url := fmt.Sprintf("https://example.com/%d", i)
				_, err := s.Create(ctx, Link{Code: "race", URL: url})
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					wins++
					winner = url
				case errors.Is(err, ErrConflict):
					conflicts++
				default:
					other = append(other, err)
				}
			})

			if len(other) > 0 {
				t.Fatalf("unexpected errors: %v", other[0])
			}
			if wins != 1 || conflicts != concurrency-1 {
				t.Fatalf("wins=%d conflicts=%d, want 1 and %d", wins, conflicts, concurrency-1)
			}
			// The stored row must belong to the winner, not a loser that overwrote it.
			got, err := s.Get(ctx, "race")
			if err != nil || got.URL != winner {
				t.Fatalf("stored url=%q err=%v, want winner %q", got.URL, err, winner)
			}
		})
	}
}

// 1,000 requests with DISTINCT codes: none may be lost or mixed up.
func TestConcurrentCreateDistinctCodes(t *testing.T) {
	for name, newStore := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()

			errs := make([]error, concurrency)
			parallel(concurrency, func(i int) {
				_, errs[i] = s.Create(ctx, Link{
					Code: fmt.Sprintf("code%04d", i),
					URL:  fmt.Sprintf("https://example.com/%d", i),
				})
			})

			for i := range concurrency {
				if errs[i] != nil {
					t.Fatalf("create %d: %v", i, errs[i])
				}
				got, err := s.Get(ctx, fmt.Sprintf("code%04d", i))
				if err != nil || got.URL != fmt.Sprintf("https://example.com/%d", i) {
					t.Fatalf("code%04d: url=%q err=%v", i, got.URL, err)
				}
			}
		})
	}
}

// 1,000 concurrent redirects of one link: the click counter must not lose updates.
func TestConcurrentResolveCountsEveryClick(t *testing.T) {
	for name, newStore := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			if _, err := s.Create(ctx, Link{Code: "hot", URL: "https://example.com"}); err != nil {
				t.Fatal(err)
			}

			errs := make([]error, concurrency)
			parallel(concurrency, func(i int) {
				_, errs[i] = s.Resolve(ctx, "hot")
			})
			for _, err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}

			got, err := s.Get(ctx, "hot")
			if err != nil {
				t.Fatal(err)
			}
			if got.Clicks != concurrency {
				t.Fatalf("clicks = %d, want %d (lost updates)", got.Clicks, concurrency)
			}
		})
	}
}
