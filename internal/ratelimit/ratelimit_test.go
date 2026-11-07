package ratelimit

import (
	"testing"
	"time"
)

func TestAllowAndRefill(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(1, 2, 100, time.Minute)
	l.now = func() time.Time { return now }

	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("burst of 2 should be allowed")
	}
	if l.Allow("a") {
		t.Fatal("third request should be denied")
	}
	if !l.Allow("b") {
		t.Fatal("other key must be independent")
	}
	now = now.Add(time.Second)
	if !l.Allow("a") {
		t.Fatal("token should refill after 1s")
	}
}

func TestMaxKeysOverflowAndCleanup(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(1, 1, 2, time.Minute)
	l.now = func() time.Time { return now }

	l.Allow("a")
	l.Allow("b")
	if !l.Allow("c") { // first overflow client gets the shared bucket
		t.Fatal("overflow bucket should start with a token")
	}
	if l.Allow("d") { // shares the now-empty overflow bucket
		t.Fatal("overflow clients should share one bucket")
	}
	if l.Len() != 3 { // a, b, overflow
		t.Fatalf("len = %d, want 3", l.Len())
	}

	now = now.Add(2 * time.Minute)
	l.Cleanup()
	if l.Len() != 0 {
		t.Fatalf("len after cleanup = %d, want 0", l.Len())
	}
}

func TestExhausted(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(1, 1, 100, time.Minute)
	l.now = func() time.Time { return now }

	if l.Exhausted("x") {
		t.Fatal("unknown key is not exhausted")
	}
	l.Allow("x")
	if !l.Exhausted("x") {
		t.Fatal("key should be exhausted after using its only token")
	}
}
