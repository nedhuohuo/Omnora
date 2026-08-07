package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCookieNamesSeparateProductionAndDevelopment(t *testing.T) {
	production := CookieNamesForSecureRequest(true)
	development := CookieNamesForSecureRequest(false)
	if !strings.HasPrefix(production.Session, "__Host-") || !productionCSRFNamesSecure(production) {
		t.Fatalf("production cookie names = %#v, want host-only secure names", production)
	}
	if strings.HasPrefix(development.Session, "__Host-") || production.Session == development.Session {
		t.Fatalf("development cookie names = %#v, must be distinct", development)
	}
}

func productionCSRFNamesSecure(names CookieNames) bool {
	return names.CSRF == "__Host-omnora_csrf" &&
		names.ShareCSRF == "__Host-omnora_share_csrf" &&
		names.DownloadCapability == "__Secure-omnora_download_ticket"
}

func TestCSRFCookieAttributesAndDeterministicExpiry(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	cookie := NewCSRFCookie("__Host-omnora_csrf", strings.Repeat("a", 43), true, now)
	if cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("csrf cookie attributes = %#v", cookie)
	}
	if got, want := cookie.Expires, now.Add(csrfCookieTTL); !got.Equal(want) {
		t.Fatalf("csrf cookie expiry = %s, want %s", got, want)
	}
}

func TestValidateCSRFConstantTimePair(t *testing.T) {
	token := strings.Repeat("a", 43)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/account/password", nil)
	request.AddCookie(&http.Cookie{Name: "omnora_dev_csrf", Value: token})
	request.Header.Set(CSRFHeaderName, token)
	if err := ValidateCSRF(request, "omnora_dev_csrf"); err != nil {
		t.Fatalf("ValidateCSRF() error = %v", err)
	}

	request.Header.Set(CSRFHeaderName, strings.Repeat("b", 43))
	if !errors.Is(ValidateCSRF(request, "omnora_dev_csrf"), ErrCSRFTokenMismatch) {
		t.Fatal("mismatched header was accepted")
	}
	request.Header.Del(CSRFHeaderName)
	if !errors.Is(ValidateCSRF(request, "omnora_dev_csrf"), ErrCSRFHeaderMissing) {
		t.Fatal("missing header was not rejected")
	}
	request.Header.Set(CSRFHeaderName, token)
	request.AddCookie(&http.Cookie{Name: "omnora_dev_csrf", Value: token})
	if !errors.Is(ValidateCSRF(request, "omnora_dev_csrf"), ErrCSRFCookieMissing) {
		t.Fatal("duplicate cookie was not rejected")
	}
}

func TestCSRFProtectionSkipsSafeAndBearerOnlyRequests(t *testing.T) {
	called := 0
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called++ })
	handler := CSRFProtection(next, "omnora_dev_csrf")

	get := httptest.NewRequest(http.MethodGet, "/api/v1/account", nil)
	handler.ServeHTTP(httptest.NewRecorder(), get)
	if called != 1 {
		t.Fatalf("safe request was not passed through, called=%d", called)
	}
	bearer := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	bearer.Header.Set("Authorization", "Bearer opaque")
	handler.ServeHTTP(httptest.NewRecorder(), bearer)
	if called != 2 {
		t.Fatalf("bearer-only request was not passed through, called=%d", called)
	}

	unsafe := httptest.NewRequest(http.MethodPost, "/api/v1/account/password", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, unsafe)
	if recorder.Code != http.StatusForbidden || called != 2 {
		t.Fatalf("unsafe request without csrf = status %d called=%d", recorder.Code, called)
	}
}

func TestCSRFProtectionAcceptsSharePair(t *testing.T) {
	token := strings.Repeat("c", 43)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/share/revoke", nil)
	request.AddCookie(&http.Cookie{Name: "omnora_dev_share_csrf", Value: token})
	request.Header.Set(CSRFHeaderName, token)
	recorder := httptest.NewRecorder()
	called := false
	CSRFProtection(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), "omnora_dev_share_csrf").ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !called {
		t.Fatalf("share csrf request = status %d called=%t", recorder.Code, called)
	}
}

func TestDownloadCapabilityCookieUsesExactPath(t *testing.T) {
	cookie := NewDownloadCapabilityCookie("omnora_dev_download_ticket", "/api/v1/share/downloads/ticket-1", "secret", false, time.Unix(1_700_000_000, 0))
	if cookie.Path != "/api/v1/share/downloads/ticket-1" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure {
		t.Fatalf("download capability cookie = %#v", cookie)
	}
}
