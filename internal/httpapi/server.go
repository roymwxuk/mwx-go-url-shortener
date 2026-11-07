// Package httpapi exposes the URL shortener over HTTP.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/roymwxuk/mwx-go-url-shortener/internal/ratelimit"
	"github.com/roymwxuk/mwx-go-url-shortener/internal/shortener"
	"github.com/roymwxuk/mwx-go-url-shortener/internal/store"
)

const maxURLLength = 2048

var reservedCodes = map[string]bool{"api": true, "healthz": true}

// Limit is a token-bucket rate: RPS sustained, Burst short-term.
type Limit struct {
	RPS   float64
	Burst int
}

type Options struct {
	BaseURL          string
	TrustedProxyHops int
	Redirect         Limit // per-IP limit on GET /{code}
	NotFound         Limit // per-IP budget of 404s; once spent, the IP gets 429 on redirects
	API              Limit // per-IP limit on /api/v1/*
	BlockedHosts     []string
}

// rateLimiterMaxKeys caps tracked IPs per limiter (~100 B each) so a flood of
// distinct IPs can't exhaust memory.
const rateLimiterMaxKeys = 100_000

// maxCodeAttempts bounds retries when a random code collides with an existing one.
const maxCodeAttempts = 5

type Server struct {
	store        store.Store
	opts         Options
	log          *slog.Logger
	redirectLim  *ratelimit.Limiter
	notFoundLim  *ratelimit.Limiter
	apiLim       *ratelimit.Limiter
	blockedHosts []string
	// newCode generates random short codes; tests swap it to force collisions.
	newCode func() (string, error)
}

func New(st store.Store, opts Options, log *slog.Logger) *Server {
	newLim := func(l Limit) *ratelimit.Limiter {
		return ratelimit.New(l.RPS, l.Burst, rateLimiterMaxKeys, 10*time.Minute)
	}
	s := &Server{
		store:       st,
		opts:        opts,
		log:         log,
		redirectLim: newLim(opts.Redirect),
		notFoundLim: newLim(opts.NotFound),
		apiLim:      newLim(opts.API),
		newCode:     func() (string, error) { return shortener.NewCode(shortener.DefaultLength) },
	}
	s.blockedHosts = append(s.blockedHosts, opts.BlockedHosts...)
	if u, err := url.Parse(opts.BaseURL); err == nil && u.Hostname() != "" {
		s.blockedHosts = append(s.blockedHosts, strings.ToLower(u.Hostname()))
	}
	return s
}

// RunJanitor evicts idle rate-limit entries until stop is closed.
func (s *Server) RunJanitor(stop <-chan struct{}) {
	for _, l := range []*ratelimit.Limiter{s.redirectLim, s.notFoundLim, s.apiLim} {
		go l.Run(time.Minute, stop)
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth) // not limited: used by load balancer health checks
	mux.Handle("POST /api/v1/links", s.limit(s.apiLim, s.handleCreate))
	mux.Handle("GET /api/v1/links/{code}", s.limit(s.apiLim, s.handleGet))
	mux.Handle("DELETE /api/v1/links/{code}", s.limit(s.apiLim, s.handleDelete))
	mux.Handle("GET /{code}", s.limit(s.redirectLim, s.handleRedirect))
	return s.logRequests(mux)
}

func (s *Server) limit(l *ratelimit.Limiter, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(clientIP(r, s.opts.TrustedProxyHops)) {
			tooManyRequests(w)
			return
		}
		next(w, r)
	})
}

func tooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(1))
	writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
}

type createRequest struct {
	URL        string `json:"url"`
	CustomCode string `json:"custom_code,omitempty"`
}

type linkResponse struct {
	store.Link
	ShortURL string `json:"short_url"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !validTargetURL(req.URL) {
		writeError(w, http.StatusBadRequest, "url must be an absolute http(s) URL")
		return
	}
	if s.isBlockedHost(req.URL) {
		writeError(w, http.StatusUnprocessableEntity, "destination host is not allowed")
		return
	}

	if req.CustomCode != "" {
		if !shortener.ValidCustomCode(req.CustomCode) || reservedCodes[req.CustomCode] {
			writeError(w, http.StatusBadRequest, "custom_code must be 3-32 chars of [A-Za-z0-9_-] and not reserved")
			return
		}
		link, err := s.store.Create(r.Context(), store.Link{Code: req.CustomCode, URL: req.URL})
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "custom_code already in use")
			return
		}
		if err != nil {
			s.internalError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, s.toResponse(link))
		return
	}

	// Random code; retry on the (rare) collision. Uniqueness is enforced by the
	// store (PRIMARY KEY in Postgres), not by checking first, so concurrent
	// requests can't both claim the same code.
	for range maxCodeAttempts {
		code, err := s.newCode()
		if err != nil {
			s.internalError(w, err)
			return
		}
		link, err := s.store.Create(r.Context(), store.Link{Code: code, URL: req.URL})
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			s.internalError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, s.toResponse(link))
		return
	}
	s.internalError(w, errors.New("could not generate unique code"))
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	link, err := s.store.Get(r.Context(), r.PathValue("code"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toResponse(link))
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	err := s.store.Delete(r.Context(), r.PathValue("code"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRedirect(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, s.opts.TrustedProxyHops)
	// Clients that keep hitting unknown codes are likely enumerating; stop them
	// before they reach the database.
	if s.notFoundLim.Exhausted(ip) {
		tooManyRequests(w)
		return
	}
	code := r.PathValue("code")
	// Malformed codes (favicon.ico, wp-login.php, ...) can't exist; skip the DB.
	if !shortener.ValidCustomCode(code) {
		s.notFoundLim.Allow(ip)
		http.NotFound(w, r)
		return
	}
	target, err := s.store.Resolve(r.Context(), code)
	if errors.Is(err, store.ErrNotFound) {
		s.notFoundLim.Allow(ip)
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) toResponse(l store.Link) linkResponse {
	return linkResponse{Link: l, ShortURL: s.opts.BaseURL + "/" + l.Code}
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.log.Error("internal error", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// isBlockedHost reports whether raw's host equals or is a subdomain of a blocked host.
func (s *Server) isBlockedHost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return true
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	for _, b := range s.blockedHosts {
		if host == b || strings.HasSuffix(host, "."+b) {
			return true
		}
	}
	return false
}

func validTargetURL(raw string) bool {
	if raw == "" || len(raw) > maxURLLength {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", time.Since(start))
	})
}
