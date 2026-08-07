package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"omnora/internal/config"
)

func TestHTTPPolicyDerivesExactHostAndOriginAndSecureCookies(t *testing.T) {
	policy, err := newHTTPPolicy(config.HTTPConfig{PublicURL: "https://files.example.test/"})
	if err != nil {
		t.Fatalf("newHTTPPolicy() error = %v", err)
	}
	if _, ok := policy.allowedHosts["files.example.test"]; !ok {
		t.Fatalf("allowed hosts = %#v", policy.allowedHosts)
	}
	if _, ok := policy.allowedOrigins["https://files.example.test"]; !ok {
		t.Fatalf("allowed origins = %#v", policy.allowedOrigins)
	}
	if !policy.secureExternal {
		t.Fatal("HTTPS public URL did not enable secure external policy")
	}
}

func TestTrustBoundaryRejectsHostAndOriginViolations(t *testing.T) {
	policy, err := newHTTPPolicy(config.HTTPConfig{PublicURL: "https://files.example.test"})
	if err != nil {
		t.Fatalf("newHTTPPolicy() error = %v", err)
	}
	s := &Server{httpPolicy: policy}
	next := s.trustBoundary(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	badHost := httptest.NewRequest(http.MethodGet, "https://evil.example.test/", nil)
	badHost.RemoteAddr = "198.51.100.5:1234"
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, badHost)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("bad host status = %d", rec.Code)
	}

	missingOrigin := httptest.NewRequest(http.MethodPost, "https://files.example.test/api", nil)
	missingOrigin.Host = "files.example.test"
	missingOrigin.RemoteAddr = "198.51.100.5:1234"
	rec = httptest.NewRecorder()
	next.ServeHTTP(rec, missingOrigin)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing origin status = %d", rec.Code)
	}

	good := httptest.NewRequest(http.MethodPost, "https://files.example.test/api", nil)
	good.Host = "files.example.test"
	good.Header.Set("Origin", "https://files.example.test")
	good.RemoteAddr = "198.51.100.5:1234"
	rec = httptest.NewRecorder()
	next.ServeHTTP(rec, good)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("good request status = %d", rec.Code)
	}
	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Fatal("good request missing HSTS")
	}
}

func TestTrustBoundaryAllowsOriginlessBearerButValidatesSuppliedOrigin(t *testing.T) {
	policy, err := newHTTPPolicy(config.HTTPConfig{PublicURL: "https://files.example.test"})
	if err != nil {
		t.Fatalf("newHTTPPolicy() error = %v", err)
	}
	s := &Server{httpPolicy: policy}
	next := s.trustBoundary(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	bearer := httptest.NewRequest(http.MethodPost, "https://files.example.test/mcp", nil)
	bearer.Host = "files.example.test"
	bearer.RemoteAddr = "198.51.100.5:1234"
	bearer.Header.Set("Authorization", "Bearer opaque")
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, bearer)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("originless bearer status = %d", rec.Code)
	}

	bearer.Header.Set("Origin", "https://evil.example.test")
	rec = httptest.NewRecorder()
	next.ServeHTTP(rec, bearer)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad bearer origin status = %d", rec.Code)
	}
}

func TestTrustedClientIPStripsOnlyTrustedProxyChain(t *testing.T) {
	_, network, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	policy := &HTTPPolicy{proxyNetworks: []*net.IPNet{network}}
	r := httptest.NewRequest(http.MethodGet, "http://files.example.test/", nil)
	r.RemoteAddr = "10.0.0.2:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.5, 10.0.0.1")
	ip, err := policy.clientIP(r)
	if err != nil {
		t.Fatalf("clientIP() error = %v", err)
	}
	if got := ip.String(); got != "198.51.100.5" {
		t.Fatalf("client IP = %s", got)
	}

	r.Header.Set("X-Forwarded-For", "10.0.0.1")
	ip, err = policy.clientIP(r)
	if err != nil {
		t.Fatalf("all-trusted clientIP() error = %v", err)
	}
	if got := ip.String(); got != "10.0.0.2" {
		t.Fatalf("all-trusted client IP = %s, want peer", got)
	}
}
