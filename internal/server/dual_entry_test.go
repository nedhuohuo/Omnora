package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"omnora/internal/config"
	"omnora/internal/domain"
	"omnora/internal/httpx"
	"omnora/internal/identity"
	"omnora/internal/store"
)

func newDualEntryTestServer(t *testing.T) (*store.DB, *Server) {
	t.Helper()
	db, err := store.OpenSQLite(context.Background(), store.SQLiteOptions{
		Path:        filepath.Join(t.TempDir(), "dual-entry-test.db"),
		BusyTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewServer(config.Config{
		Routes:            map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
		RouteEnvOverrides: map[domain.RouteGroup]bool{domain.RouteGroupREST: true},
	}, db)
	return db, s
}

// enableProxyViaLAN flips the proxy_https entry on through the admin API on
// the LAN handler, which is the supported bootstrap path (the proxy entry
// gates all requests, so it cannot configure itself).
func enableProxyViaLAN(t *testing.T, s *Server, lan http.Handler, adminCookie *http.Cookie, cidrs ...string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"name":    EntryProxy,
		"enabled": true,
		"cidrs":   cidrs,
	})
	if err != nil {
		t.Fatalf("marshal proxy entry: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/network-entries", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(adminCookie)
	req.RemoteAddr = "127.0.0.1:3456"
	rec := httptest.NewRecorder()
	lan.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable proxy entry status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestProxyGateFailClosedWhenDisabled(t *testing.T) {
	_, s := newDualEntryTestServer(t)
	proxy := s.HandlerFor(EntryProxy)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
	req.RemoteAddr = "127.0.0.1:3456"
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("disabled proxy entry status = %d, want 403 (fail closed), body = %s", rec.Code, rec.Body.String())
	}
}

func TestProxyGateTrustedPeerAndSpoofedXFF(t *testing.T) {
	db, s := newDualEntryTestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	lan := s.HandlerFor(EntryLAN)
	proxy := s.HandlerFor(EntryProxy)
	enableProxyViaLAN(t, s, lan, issueAPITestSession(t, db, admin.ID), "127.0.0.1/32")

	// Trusted direct peer passes the gate; the downstream route still requires
	// a session (401 without a cookie).
	trusted := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
	trusted.RemoteAddr = "127.0.0.1:3456"
	trustedRec := httptest.NewRecorder()
	proxy.ServeHTTP(trustedRec, trusted)
	if trustedRec.Code != http.StatusUnauthorized {
		t.Fatalf("trusted peer status = %d, want 401 (gate passed, no session), body = %s", trustedRec.Code, trustedRec.Body.String())
	}

	// Untrusted direct peer is rejected at the gate.
	untrusted := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
	untrusted.RemoteAddr = "203.0.113.10:3456"
	untrustedRec := httptest.NewRecorder()
	proxy.ServeHTTP(untrustedRec, untrusted)
	if untrustedRec.Code != http.StatusForbidden {
		t.Fatalf("untrusted peer status = %d, want 403, body = %s", untrustedRec.Code, untrustedRec.Body.String())
	}

	// An untrusted peer cannot spoof a trusted source via X-Forwarded-For:
	// the gate checks the direct peer, never forwarded headers.
	spoofed := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
	spoofed.RemoteAddr = "203.0.113.10:3456"
	spoofed.Header.Set("X-Forwarded-For", "127.0.0.1")
	spoofedRec := httptest.NewRecorder()
	proxy.ServeHTTP(spoofedRec, spoofed)
	if spoofedRec.Code != http.StatusForbidden {
		t.Fatalf("spoofed XFF status = %d, want 403, body = %s", spoofedRec.Code, spoofedRec.Body.String())
	}
}

func TestSessionEntryIsolation(t *testing.T) {
	db, s := newDualEntryTestServer(t)
	admin, _ := createAPITestAccounts(t, db)
	lan := s.HandlerFor(EntryLAN)
	proxy := s.HandlerFor(EntryProxy)
	adminCookie := issueAPITestSession(t, db, admin.ID)
	enableProxyViaLAN(t, s, lan, adminCookie, "127.0.0.1/32")

	// A LAN-issued session works on the LAN entry and is rejected on proxy.
	lanSession := issueAPITestSession(t, db, admin.ID)
	lanOK := authorizedEntryRequest(t, lan, "/api/v1/spaces", lanSession, "127.0.0.1:3456")
	if lanOK != http.StatusOK {
		t.Fatalf("LAN session on LAN entry = %d, want 200, body = %s", lanOK, "")
	}
	cross := authorizedEntryRequest(t, proxy, "/api/v1/spaces", lanSession, "127.0.0.1:3456")
	if cross != http.StatusUnauthorized {
		t.Fatalf("LAN session on proxy entry = %d, want 401", cross)
	}

	// A proxy-issued session works on the proxy entry and is rejected on LAN.
	proxySession := issueAPITestSessionWithEntry(t, db, admin.ID, EntryProxy)
	proxyOK := authorizedEntryRequest(t, proxy, "/api/v1/spaces", proxySession, "127.0.0.1:3456")
	if proxyOK != http.StatusOK {
		t.Fatalf("proxy session on proxy entry = %d, want 200", proxyOK)
	}
	crossBack := authorizedEntryRequest(t, lan, "/api/v1/spaces", proxySession, "127.0.0.1:3456")
	if crossBack != http.StatusUnauthorized {
		t.Fatalf("proxy session on LAN entry = %d, want 401", crossBack)
	}
}

func TestCookieSecureOnProxyEntry(t *testing.T) {
	lanReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", nil)
	lanReq = lanReq.WithContext(httpx.WithEntry(lanReq.Context(), EntryLAN))
	proxyReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", nil)
	proxyReq = proxyReq.WithContext(httpx.WithEntry(proxyReq.Context(), EntryProxy))

	expires := time.Now().Add(time.Hour)
	if lan := sessionCookie(lanReq, "tok", expires); lan.Secure {
		t.Fatal("LAN session cookie must not be Secure")
	}
	if proxy := sessionCookie(proxyReq, "tok", expires); !proxy.Secure {
		t.Fatal("proxy session cookie must be Secure")
	}
	if lan := shareSessionCookie(lanReq, "tok", expires); lan.Secure {
		t.Fatal("LAN share cookie must not be Secure")
	}
	if proxy := shareSessionCookie(proxyReq, "tok", expires); !proxy.Secure {
		t.Fatal("proxy share cookie must be Secure")
	}
}

func authorizedEntryRequest(t *testing.T, handler http.Handler, path string, cookie *http.Cookie, remoteAddr string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

func issueAPITestSessionWithEntry(t *testing.T, db *store.DB, accountID, entry string) *http.Cookie {
	t.Helper()
	issued, err := identity.New(db.SQL(), identity.Options{}).CreateSession(context.Background(), identity.SessionRequest{
		AccountID: accountID,
		TTL:       time.Hour,
		Entry:     entry,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: issued.Token}
}
