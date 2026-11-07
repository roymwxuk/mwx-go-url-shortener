// Package config loads runtime configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Addr        string // listen address, e.g. ":8080"
	BaseURL     string // public base URL used to build short links
	DatabaseURL string // Postgres DSN; empty means use the in-memory store

	// TrustedProxyHops is how many proxies in front of the app append to
	// X-Forwarded-For (0 = direct, 1 = ALB or API Gateway, 2 = CloudFront+ALB).
	TrustedProxyHops int

	RedirectRPS, NotFoundRPS, APIRPS       float64
	RedirectBurst, NotFoundBurst, APIBurst int

	// BlockedHosts are destination hosts (and their subdomains) that may not be
	// shortened. The service's own host is always blocked to prevent loops.
	BlockedHosts []string
}

func Load() Config {
	addr := getenv("ADDR", ":8080")
	return Config{
		Addr:             addr,
		BaseURL:          strings.TrimRight(getenv("BASE_URL", "http://localhost"+addr), "/"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		TrustedProxyHops: getInt("TRUSTED_PROXY_HOPS", 0),
		RedirectRPS:      getFloat("RATE_REDIRECT_RPS", 10),
		RedirectBurst:    getInt("RATE_REDIRECT_BURST", 30),
		NotFoundRPS:      getFloat("RATE_NOTFOUND_RPS", 0.2),
		NotFoundBurst:    getInt("RATE_NOTFOUND_BURST", 10),
		APIRPS:           getFloat("RATE_API_RPS", 1),
		APIBurst:         getInt("RATE_API_BURST", 10),
		BlockedHosts:     splitList(os.Getenv("BLOCKED_HOSTS")),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func getFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}
