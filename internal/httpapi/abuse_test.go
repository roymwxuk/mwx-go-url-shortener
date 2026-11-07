package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func getStatus(t *testing.T, c *http.Client, url string, hdr map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestRedirectRateLimit(t *testing.T) {
	opts := testOpts
	opts.Redirect = Limit{RPS: 0.001, Burst: 3}
	srv := newTestServerWith(t, opts)
	_, lr := create(t, srv.URL, `{"url":"https://example.com"}`)
	c := noRedirectClient()

	for i := range 3 {
		if got := getStatus(t, c, srv.URL+"/"+lr.Code, nil); got != http.StatusFound {
			t.Fatalf("request %d: status %d, want 302", i, got)
		}
	}
	if got := getStatus(t, c, srv.URL+"/"+lr.Code, nil); got != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", got)
	}
}

func TestNotFoundPenalty(t *testing.T) {
	opts := testOpts
	opts.NotFound = Limit{RPS: 0.001, Burst: 3}
	srv := newTestServerWith(t, opts)
	_, lr := create(t, srv.URL, `{"url":"https://example.com"}`)
	c := noRedirectClient()

	for _, code := range []string{"nope1", "nope2", "wp-login.php"} { // last one is malformed
		if got := getStatus(t, c, srv.URL+"/"+code, nil); got != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", code, got)
		}
	}
	// Budget spent: even a valid code is now refused for this IP.
	if got := getStatus(t, c, srv.URL+"/"+lr.Code, nil); got != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429 after too many 404s", got)
	}
}

func TestAPIRateLimit(t *testing.T) {
	opts := testOpts
	opts.API = Limit{RPS: 0.001, Burst: 2}
	srv := newTestServerWith(t, opts)

	for i := range 2 {
		if resp, _ := create(t, srv.URL, `{"url":"https://example.com"}`); resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %d: status %d", i, resp.StatusCode)
		}
	}
	resp, _ := create(t, srv.URL, `{"url":"https://example.com"}`)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status %d retry-after %q, want 429 with Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestLimitsArePerClientIPBehindProxy(t *testing.T) {
	opts := testOpts
	opts.TrustedProxyHops = 1
	opts.Redirect = Limit{RPS: 0.001, Burst: 1}
	srv := newTestServerWith(t, opts)
	_, lr := create(t, srv.URL, `{"url":"https://example.com"}`)
	c := noRedirectClient()
	u := srv.URL + "/" + lr.Code

	if got := getStatus(t, c, u, map[string]string{"X-Forwarded-For": "1.1.1.1"}); got != http.StatusFound {
		t.Fatalf("client A first: %d", got)
	}
	if got := getStatus(t, c, u, map[string]string{"X-Forwarded-For": "1.1.1.1"}); got != http.StatusTooManyRequests {
		t.Fatalf("client A second: %d, want 429", got)
	}
	if got := getStatus(t, c, u, map[string]string{"X-Forwarded-For": "2.2.2.2"}); got != http.StatusFound {
		t.Fatalf("client B: %d, want 302 (separate bucket)", got)
	}
	// Spoofing: client A prepends a fake IP; the proxy-appended entry still wins.
	if got := getStatus(t, c, u, map[string]string{"X-Forwarded-For": "9.9.9.9, 1.1.1.1"}); got != http.StatusTooManyRequests {
		t.Fatalf("spoofed: %d, want 429", got)
	}
}

func TestBlockedHosts(t *testing.T) {
	opts := testOpts
	opts.BlockedHosts = []string{"evil.example"}
	srv := newTestServerWith(t, opts)

	for _, u := range []string{
		"https://evil.example/x",
		"https://sub.evil.example/x",
		"http://short.test/abc", // own host: redirect loop
	} {
		if resp, _ := create(t, srv.URL, `{"url":"`+u+`"}`); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d, want 422", u, resp.StatusCode)
		}
	}
	if resp, _ := create(t, srv.URL, `{"url":"https://notevil.example"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("lookalike host should be allowed, got %d", resp.StatusCode)
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		xff    string
		hops   int
		want   string
	}{
		{"direct ignores xff", "10.0.0.1:1234", "1.2.3.4", 0, "10.0.0.1"},
		{"one hop", "10.0.0.1:1234", "1.2.3.4", 1, "1.2.3.4"},
		{"one hop spoofed", "10.0.0.1:1234", "6.6.6.6, 1.2.3.4", 1, "1.2.3.4"},
		{"two hops", "10.0.0.1:1234", "6.6.6.6, 1.2.3.4, 130.176.0.1", 2, "1.2.3.4"},
		{"missing xff falls back", "10.0.0.1:1234", "", 1, "10.0.0.1"},
		{"ipv6 grouped by /64", "[2001:db8:1:2:aaaa::1]:1234", "", 0, "2001:db8:1:2::/64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := clientIP(r, tc.hops); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
