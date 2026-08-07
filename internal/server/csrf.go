package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"omnora/internal/httpx"
)

const (
	// CSRFHeaderName is the header used for the non-HttpOnly half of the
	// double-submit token.
	CSRFHeaderName = "X-CSRF-Token"
	csrfHeaderName = CSRFHeaderName
	csrfCookieTTL  = 12 * time.Hour
	csrfTokenBytes = 32
)

var (
	ErrCSRFCookieMissing = errors.New("csrf cookie is missing")
	ErrCSRFHeaderMissing = errors.New("csrf header is missing")
	ErrCSRFTokenMismatch = errors.New("csrf cookie and header do not match")
)

// CookieNames is the complete set of browser credential cookie names.  Keep
// the production names host-only (__Host-) and use separate development names
// so a loopback HTTP cookie can never be mistaken for an external cookie.
type CookieNames struct {
	Session            string
	CSRF               string
	ShareSession       string
	ShareCSRF          string
	DownloadCapability string
}

var productionCookieNames = CookieNames{
	Session:            "__Host-omnora_session",
	CSRF:               "__Host-omnora_csrf",
	ShareSession:       "__Host-omnora_share_session",
	ShareCSRF:          "__Host-omnora_share_csrf",
	DownloadCapability: "__Secure-omnora_download_ticket",
}

var developmentCookieNames = CookieNames{
	Session:            "omnora_dev_session",
	CSRF:               "omnora_dev_csrf",
	ShareSession:       "omnora_dev_share_session",
	ShareCSRF:          "omnora_dev_share_csrf",
	DownloadCapability: "omnora_dev_download_ticket",
}

// ProductionCookieNames returns the HTTPS cookie names.  The value is copied
// so callers cannot mutate package-level policy.
func ProductionCookieNames() CookieNames {
	return productionCookieNames
}

// DevelopmentCookieNames returns the explicit loopback-development cookie
// names.  The value is copied so callers cannot mutate package-level policy.
func DevelopmentCookieNames() CookieNames {
	return developmentCookieNames
}

// CookieNamesForSecureRequest selects cookie names from the externally
// configured scheme.  HTTPS uses production names; loopback HTTP uses names
// that cannot collide with the production cookies.
func CookieNamesForSecureRequest(secure bool) CookieNames {
	if secure {
		return productionCookieNames
	}
	return developmentCookieNames
}

// cookieNamesForRequest is the package-local convenience used by route
// registration.  The trust-boundary middleware places the externally-derived
// security decision in the request context; TLS is the safe fallback for
// in-process handlers.
func cookieNamesForRequest(r *http.Request) CookieNames {
	return CookieNamesForSecureRequest(isSecureRequest(r))
}

// cookieNames is retained as the concise package-local spelling used by route
// registration code.
func cookieNames(r *http.Request) CookieNames {
	return cookieNamesForRequest(r)
}

// CookieNamesForRequest exposes the same selection for tests and integrations
// outside the server package.
func CookieNamesForRequest(r *http.Request) CookieNames {
	return cookieNamesForRequest(r)
}

// (s *Server).CookieNamesForRequest keeps selection tied to the configured
// external policy when a caller has a Server available.  The request context
// remains authoritative for proxy-terminated TLS.
func (s *Server) CookieNamesForRequest(r *http.Request) CookieNames {
	secure := isSecureRequest(r)
	if s != nil && s.httpPolicy != nil && s.httpPolicy.secureExternal {
		secure = true
	}
	return CookieNamesForSecureRequest(secure)
}

func (s *Server) cookieNames(r *http.Request) CookieNames {
	return s.CookieNamesForRequest(r)
}

// NewCSRFCookie constructs the non-HttpOnly CSRF half of a double-submit pair.
// now is injected explicitly so expiry serialization is deterministic in
// tests; callers should pass time.Now().UTC() in production.
func NewCSRFCookie(name, token string, secure bool, now time.Time) *http.Cookie {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		Expires:  now.UTC().Add(csrfCookieTTL),
		MaxAge:   int(csrfCookieTTL / time.Second),
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// SetCSRFCookie generates and writes a fresh CSRF cookie, returning the token
// that the browser client must mirror in X-CSRF-Token.
func SetCSRFCookie(w http.ResponseWriter, name string, secure bool, now time.Time) (string, error) {
	token, err := newCSRFToken()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, NewCSRFCookie(name, token, secure, now))
	return token, nil
}

// EnsureCSRFCookie reuses a valid request cookie when present and otherwise
// rotates in a new pair.  It does not accept a header as proof when creating a
// token; callers should use ValidateCSRF for unsafe requests.
func EnsureCSRFCookie(w http.ResponseWriter, r *http.Request, name string, secure bool, now time.Time) (string, error) {
	if cookie, err := r.Cookie(name); err == nil && validCSRFToken(cookie.Value) {
		return cookie.Value, nil
	}
	return SetCSRFCookie(w, name, secure, now)
}

// ClearCSRFCookie expires a CSRF cookie with the same security attributes used
// when it was issued.
func ClearCSRFCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ValidateCSRF validates the cookie/header pair with a constant-time compare.
// The cookie name is supplied by the route's auth mode, allowing account and
// share-session CSRF pairs to remain independent.
func ValidateCSRF(r *http.Request, cookieName string) error {
	return ValidateCSRFHeader(r, cookieName, CSRFHeaderName)
}

func requireCSRF(r *http.Request, cookieName string) error {
	return ValidateCSRF(r, cookieName)
}

// ValidateCSRFHeader is the configurable form used by integrations that have
// a non-default header name.
func ValidateCSRFHeader(r *http.Request, cookieName, headerName string) error {
	if r == nil {
		return ErrCSRFCookieMissing
	}
	var cookieValue string
	for _, cookie := range r.Cookies() {
		if cookie.Name != cookieName {
			continue
		}
		if cookieValue != "" {
			return ErrCSRFCookieMissing
		}
		cookieValue = cookie.Value
	}
	if !validCSRFToken(cookieValue) {
		return ErrCSRFCookieMissing
	}
	values := r.Header.Values(headerName)
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return ErrCSRFHeaderMissing
	}
	headerValue := strings.TrimSpace(values[0])
	if !validCSRFToken(headerValue) {
		return ErrCSRFTokenMismatch
	}
	if subtle.ConstantTimeCompare([]byte(cookieValue), []byte(headerValue)) != 1 {
		return ErrCSRFTokenMismatch
	}
	return nil
}

// CSRFProtection wraps an unsafe cookie-auth route.  Safe requests and
// bearer-only requests bypass this browser-token check; host/origin checks are
// intentionally left to trustBoundary.  A route with a bearer credential and
// incidental cookies should select bearer auth before applying this wrapper.
func CSRFProtection(next http.Handler, cookieName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !unsafeHTTPMethod(r.Method) || bearerOnlyRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if err := ValidateCSRF(r, cookieName); err != nil {
			httpx.WriteError(w, r, http.StatusForbidden, "csrf_required", "a valid CSRF token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCSRF is an alias that reads naturally at route registration sites.
func RequireCSRF(next http.Handler, cookieName string) http.Handler {
	return CSRFProtection(next, cookieName)
}

func csrfProtection(next http.Handler, cookieName string) http.Handler {
	return CSRFProtection(next, cookieName)
}

// NewDownloadCapabilityCookie creates the exact-path, HttpOnly capability
// cookie used by a share download route.  pathSuffix must be the concrete
// download path (for example /api/v1/share/downloads/<ticketId>).
func NewDownloadCapabilityCookie(name, pathSuffix, value string, secure bool, expiresAt time.Time) *http.Cookie {
	pathSuffix = "/" + strings.TrimPrefix(path.Clean(pathSuffix), "/")
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     pathSuffix,
		Expires:  expiresAt.UTC(),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

func newCSRFToken() (string, error) {
	buf := make([]byte, csrfTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func validCSRFToken(value string) bool {
	// Tokens generated by SetCSRFCookie are 256-bit, but validation stays
	// shape-oriented so deterministic test fixtures and externally rotated
	// tokens remain compatible.  Entropy is supplied by the generator, not by
	// accepting an arbitrary minimum length here.
	return len(value) > 0 && len(value) <= 256 && strings.TrimSpace(value) == value
}
