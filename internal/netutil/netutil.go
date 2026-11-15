// Package netutil holds the small HTTP trust checks shared by the local
// portal and the workbench API.
package netutil

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// IsLoopback reports whether remoteAddr is a loopback client.
func IsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsLocalHost reports whether host (without port) is loopback or localhost.
func IsLocalHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || host == "localhost" {
		return true
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// HostAllowed rejects DNS-rebinding style Host headers. Only localhost and
// loopback literals are accepted; the service is local-only by design.
func HostAllowed(r *http.Request) bool {
	host := r.Host
	if host == "" {
		return true
	}
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	return IsLocalHost(host)
}

// OriginAllowed accepts a missing Origin (curl, native clients) or an Origin
// whose host is local. A non-local Origin must match the request Host.
func OriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	if IsLocalHost(hostOnly(parsed.Host)) {
		return true
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

// Guard is the shared check for UI/API routes.
func Guard(r *http.Request) bool {
	return IsLoopback(r.RemoteAddr) && HostAllowed(r) && OriginAllowed(r)
}

func hostOnly(host string) string {
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		return parsed
	}
	return host
}
