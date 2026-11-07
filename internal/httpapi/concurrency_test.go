package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/roymwxuk/mwx-go-url-shortener/internal/store"
)

const concurrency = 1000

// Requests go straight to the handler (no sockets), so 1,000 in flight at once
// doesn't hit OS file-descriptor limits, while the handler and store code still
// run concurrently.
func newConcurrencyServer(st store.Store) *Server {
	opts := testOpts
	opts.API = Limit{RPS: 1e6, Burst: 1e6} // the test comes from one IP
	return New(st, opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func post(h http.Handler, body string) (int, linkResponse) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/links", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var lr linkResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	return rec.Code, lr
}

// parallel runs fn(i) in n goroutines released together to maximise overlap.
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

// The main scenario: 1,000 simultaneous creates must yield 1,000 distinct codes,
// each pointing at the URL its own request sent.
func TestConcurrentCreateUniqueCodes(t *testing.T) {
	st := store.NewMemoryStore()
	h := newConcurrencyServer(st).Routes()

	statuses := make([]int, concurrency)
	codes := make([]string, concurrency)
	parallel(concurrency, func(i int) {
		c, lr := post(h, fmt.Sprintf(`{"url":"https://example.com/%d"}`, i))
		statuses[i], codes[i] = c, lr.Code
	})

	seen := make(map[string]int, concurrency)
	for i := range concurrency {
		if statuses[i] != http.StatusCreated {
			t.Fatalf("request %d: status %d", i, statuses[i])
		}
		if j, dup := seen[codes[i]]; dup {
			t.Fatalf("requests %d and %d both got code %q", j, i, codes[i])
		}
		seen[codes[i]] = i
		got, err := st.Get(context.Background(), codes[i])
		if err != nil || got.URL != fmt.Sprintf("https://example.com/%d", i) {
			t.Fatalf("code %q: url=%q err=%v", codes[i], got.URL, err)
		}
	}
}

// Force real collisions: codes are drawn from a small pool, so many requests
// pick a code another request already holds and must retry.
func TestConcurrentCreateRetriesOnCollision(t *testing.T) {
	st := store.NewMemoryStore()
	s := newConcurrencyServer(st)
	var calls atomic.Int64
	// Pool of 20,000 for 1,000 links: ~25 collisions expected, while the chance
	// any request fails all 5 attempts is ~1e-4 per run.
	s.newCode = func() (string, error) {
		calls.Add(1)
		return fmt.Sprintf("p%05d", rand.IntN(20_000)), nil
	}
	h := s.Routes()

	statuses := make([]int, concurrency)
	codes := make([]string, concurrency)
	parallel(concurrency, func(i int) {
		c, lr := post(h, fmt.Sprintf(`{"url":"https://example.com/%d"}`, i))
		statuses[i], codes[i] = c, lr.Code
	})

	seen := make(map[string]bool, concurrency)
	for i := range concurrency {
		if statuses[i] != http.StatusCreated {
			t.Fatalf("request %d: status %d", i, statuses[i])
		}
		if seen[codes[i]] {
			t.Fatalf("duplicate code %q", codes[i])
		}
		seen[codes[i]] = true
		got, _ := st.Get(context.Background(), codes[i])
		if got.URL != fmt.Sprintf("https://example.com/%d", i) {
			t.Fatalf("code %q: url=%q, want request %d's url (overwritten?)", codes[i], got.URL, i)
		}
	}
	retries := calls.Load() - concurrency
	t.Logf("%d retries after collisions", retries)
	if retries == 0 {
		t.Fatal("no collisions happened; the retry path was not exercised")
	}
}

// Deterministic retry behaviour: a taken code is skipped, and the handler gives
// up after maxCodeAttempts instead of looping forever.
func TestCreateRetrySequence(t *testing.T) {
	st := store.NewMemoryStore()
	if _, err := st.Create(context.Background(), store.Link{Code: "taken", URL: "https://a.example"}); err != nil {
		t.Fatal(err)
	}

	t.Run("succeeds after collisions", func(t *testing.T) {
		s := newConcurrencyServer(st)
		seq := []string{"taken", "taken", "fresh"}
		var n int
		s.newCode = func() (string, error) { c := seq[n]; n++; return c, nil }

		code, lr := post(s.Routes(), `{"url":"https://b.example"}`)
		if code != http.StatusCreated || lr.Code != "fresh" || n != 3 {
			t.Fatalf("status=%d code=%q attempts=%d", code, lr.Code, n)
		}
	})

	t.Run("gives up after max attempts", func(t *testing.T) {
		s := newConcurrencyServer(st)
		var n int
		s.newCode = func() (string, error) { n++; return "taken", nil }

		code, _ := post(s.Routes(), `{"url":"https://b.example"}`)
		if code != http.StatusInternalServerError || n != maxCodeAttempts {
			t.Fatalf("status=%d attempts=%d, want 500 after %d", code, n, maxCodeAttempts)
		}
		if got, _ := st.Get(context.Background(), "taken"); got.URL != "https://a.example" {
			t.Fatalf("existing link was overwritten: %q", got.URL)
		}
	})
}

// 1,000 requests for the same custom alias: exactly one 201, the rest 409.
func TestConcurrentSameCustomCode(t *testing.T) {
	h := newConcurrencyServer(store.NewMemoryStore()).Routes()

	var created, conflict, other atomic.Int64
	parallel(concurrency, func(i int) {
		code, _ := post(h, fmt.Sprintf(`{"url":"https://example.com/%d","custom_code":"promo"}`, i))
		switch code {
		case http.StatusCreated:
			created.Add(1)
		case http.StatusConflict:
			conflict.Add(1)
		default:
			other.Add(1)
		}
	})
	if created.Load() != 1 || conflict.Load() != concurrency-1 || other.Load() != 0 {
		t.Fatalf("201=%d 409=%d other=%d", created.Load(), conflict.Load(), other.Load())
	}
}
