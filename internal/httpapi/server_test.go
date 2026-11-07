package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/roymwxuk/mwx-go-url-shortener/internal/store"
)

// generous limits so functional tests aren't throttled
var testOpts = Options{
	BaseURL:  "http://short.test",
	Redirect: Limit{RPS: 1000, Burst: 1000},
	NotFound: Limit{RPS: 1000, Burst: 1000},
	API:      Limit{RPS: 1000, Burst: 1000},
}

func newTestServer(t *testing.T) *httptest.Server {
	return newTestServerWith(t, testOpts)
}

func newTestServerWith(t *testing.T, opts Options) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(store.NewMemoryStore(), opts, log).Routes())
	t.Cleanup(srv.Close)
	return srv
}

func create(t *testing.T, base string, body string) (*http.Response, linkResponse) {
	t.Helper()
	resp, err := http.Post(base+"/api/v1/links", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var lr linkResponse
	_ = json.NewDecoder(resp.Body).Decode(&lr)
	return resp, lr
}

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func TestCreateAndRedirect(t *testing.T) {
	srv := newTestServer(t)

	resp, lr := create(t, srv.URL, `{"url":"https://example.com/page"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	if lr.Code == "" || lr.ShortURL != "http://short.test/"+lr.Code {
		t.Fatalf("unexpected response: %+v", lr)
	}

	r, err := noRedirectClient().Get(srv.URL + "/" + lr.Code)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "https://example.com/page" {
		t.Fatalf("redirect: status=%d location=%q", r.StatusCode, r.Header.Get("Location"))
	}

	g, err := http.Get(srv.URL + "/api/v1/links/" + lr.Code)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Body.Close()
	var got linkResponse
	_ = json.NewDecoder(g.Body).Decode(&got)
	if got.Clicks != 1 {
		t.Fatalf("clicks = %d, want 1", got.Clicks)
	}
}

func TestCustomCode(t *testing.T) {
	srv := newTestServer(t)

	resp, lr := create(t, srv.URL, `{"url":"https://example.com","custom_code":"my-link"}`)
	if resp.StatusCode != http.StatusCreated || lr.Code != "my-link" {
		t.Fatalf("status=%d code=%q", resp.StatusCode, lr.Code)
	}
	resp, _ = create(t, srv.URL, `{"url":"https://example.org","custom_code":"my-link"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409", resp.StatusCode)
	}
}

func TestCreateValidation(t *testing.T) {
	srv := newTestServer(t)
	cases := map[string]string{
		"bad json":      `{`,
		"empty url":     `{"url":""}`,
		"no scheme":     `{"url":"example.com"}`,
		"ftp scheme":    `{"url":"ftp://example.com"}`,
		"js scheme":     `{"url":"javascript:alert(1)"}`,
		"short custom":  `{"url":"https://example.com","custom_code":"ab"}`,
		"bad chars":     `{"url":"https://example.com","custom_code":"a b/c"}`,
		"reserved code": `{"url":"https://example.com","custom_code":"api"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			resp, _ := create(t, srv.URL, body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestDeleteAndNotFound(t *testing.T) {
	srv := newTestServer(t)
	_, lr := create(t, srv.URL, `{"url":"https://example.com"}`)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/links/"+lr.Code, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}

	r, err := noRedirectClient().Get(srv.URL + "/" + lr.Code)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete status = %d, want 404", r.StatusCode)
	}
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
