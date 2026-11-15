package netutil

import (
	"net/http/httptest"
	"testing"
)

func TestIsLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:1", "[::1]:2", "127.0.0.2:3"} {
		if !IsLoopback(addr) {
			t.Errorf("IsLoopback(%q) = false", addr)
		}
	}
	for _, addr := range []string{"10.0.0.1:1", "192.168.1.2:3", "example.com:80", ""} {
		if IsLoopback(addr) {
			t.Errorf("IsLoopback(%q) = true", addr)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:11500": true,
		"localhost:11500": true,
		"[::1]:11500":     true,
		"evil.example":    false,
		"127.0.0.1.evil":  false,
		"":                true,
	}
	for host, want := range cases {
		req := httptest.NewRequest("GET", "http://"+host+"/", nil)
		req.Host = host
		if got := HostAllowed(req); got != want {
			t.Errorf("HostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	cases := []struct {
		host   string
		origin string
		want   bool
	}{
		{"127.0.0.1:11500", "", true},
		{"127.0.0.1:11500", "http://127.0.0.1:11500", true},
		{"127.0.0.1:11500", "http://localhost:5173", true},
		{"127.0.0.1:11500", "http://evil.example", false},
		{"127.0.0.1:11500", "not a url", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("POST", "http://"+tc.host+"/", nil)
		req.Host = tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if got := OriginAllowed(req); got != tc.want {
			t.Errorf("OriginAllowed(host=%q origin=%q) = %v, want %v", tc.host, tc.origin, got, tc.want)
		}
	}
}

func TestGuard(t *testing.T) {
	req := httptest.NewRequest("GET", "http://127.0.0.1:11500/", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Host = "127.0.0.1:11500"
	if !Guard(req) {
		t.Error("loopback request rejected")
	}
	req.RemoteAddr = "10.0.0.1:5555"
	if Guard(req) {
		t.Error("non-loopback request accepted")
	}
}
