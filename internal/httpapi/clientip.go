package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// clientIP returns the rate-limit key for a request.
//
// trustedHops is the number of proxies in front of the app that append to
// X-Forwarded-For (ALB = 1, API Gateway HTTP API = 1, CloudFront + ALB = 2).
// Only entries added by those proxies are trusted, so a client can't spoof its
// IP by sending its own X-Forwarded-For header. 0 means use the TCP peer.
//
// IPv6 addresses are grouped by /64, because a single host usually controls a
// whole /64 and could otherwise rotate addresses to dodge the limit.
func clientIP(r *http.Request, trustedHops int) string {
	ip := remoteIP(r)
	if trustedHops > 0 {
		var parts []string
		for _, h := range r.Header.Values("X-Forwarded-For") {
			for _, p := range strings.Split(h, ",") {
				if p = strings.TrimSpace(p); p != "" {
					parts = append(parts, p)
				}
			}
		}
		if idx := len(parts) - trustedHops; idx >= 0 && idx < len(parts) {
			ip = parts[idx]
		}
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is6() {
		p, _ := addr.Prefix(64)
		return p.String()
	}
	return addr.String()
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
