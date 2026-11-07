// Package ratelimit provides a per-key token-bucket limiter with idle eviction.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// overflowKey is shared by all new clients once maxKeys is reached, so a flood
// of distinct IPs cannot exhaust memory. They then share one bucket.
const overflowKey = "\x00overflow"

type entry struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// Limiter keeps one token bucket per key (e.g. client IP).
type Limiter struct {
	mu      sync.Mutex
	keys    map[string]*entry
	rps     rate.Limit
	burst   int
	maxKeys int
	idleTTL time.Duration
	now     func() time.Time
}

// New returns a limiter allowing rps requests/second per key with the given burst.
// Keys idle longer than idleTTL are evicted by Cleanup.
func New(rps float64, burst, maxKeys int, idleTTL time.Duration) *Limiter {
	return &Limiter{
		keys:    make(map[string]*entry),
		rps:     rate.Limit(rps),
		burst:   burst,
		maxKeys: maxKeys,
		idleTTL: idleTTL,
		now:     time.Now,
	}
}

func (l *Limiter) get(key string) *rate.Limiter {
	e, ok := l.keys[key]
	if !ok {
		if len(l.keys) >= l.maxKeys {
			key = overflowKey
			if e, ok = l.keys[key]; ok {
				e.lastSeen = l.now()
				return e.lim
			}
		}
		e = &entry{lim: rate.NewLimiter(l.rps, l.burst)}
		l.keys[key] = e
	}
	e.lastSeen = l.now()
	return e.lim
}

// Allow consumes one token for key and reports whether it was available.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.get(key).AllowN(l.now(), 1)
}

// Exhausted reports whether key currently has no tokens, without consuming one.
func (l *Limiter) Exhausted(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.keys[key]
	if !ok {
		return false
	}
	return e.lim.TokensAt(l.now()) < 1
}

// Cleanup evicts idle keys. Call it periodically (see Run).
func (l *Limiter) Cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.now().Add(-l.idleTTL)
	for k, e := range l.keys {
		if e.lastSeen.Before(cutoff) {
			delete(l.keys, k)
		}
	}
}

// Run calls Cleanup every interval until stop is closed.
func (l *Limiter) Run(interval time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			l.Cleanup()
		case <-stop:
			return
		}
	}
}

// Len returns the number of tracked keys (for tests/metrics).
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}
