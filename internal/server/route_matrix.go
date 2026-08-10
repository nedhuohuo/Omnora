package server

import (
	"net/http"
	"strings"
	"time"

	"omnora/internal/httpx"
	"omnora/internal/identity"
)

// RouteAuthMode is the browser/API credential contract for a route. The
// matrix is deliberately data, rather than a collection of ad-hoc wrappers,
// so a new mutation must make its authentication and CSRF posture explicit.
type RouteAuthMode string

const (
	RouteAuthHealth      RouteAuthMode = "health"
	RouteAuthPublic      RouteAuthMode = "public_pre_auth"
	RouteAuthCookie      RouteAuthMode = "cookie_session"
	RouteAuthShareCookie RouteAuthMode = "share_cookie"
	RouteAuthBearer      RouteAuthMode = "bearer"
)

type RouteRule struct {
	Method             string
	Pattern            string
	Mode               RouteAuthMode
	CSRF               bool
	RequiresRecentAuth bool
	AllowEnrollment    bool
}

// routeMatrix is the reviewable authentication contract for HTTP routes. A
// trailing /* entry is a prefix rule; exact entries above the prefix rules win.
var routeMatrix = []RouteRule{
	{Method: "*", Pattern: "/healthz", Mode: RouteAuthHealth},
	{Method: "*", Pattern: "/readyz", Mode: RouteAuthHealth},
	{Method: "GET", Pattern: "/app/*", Mode: RouteAuthPublic},
	{Method: "GET", Pattern: "/admin/*", Mode: RouteAuthPublic},
	{Method: "GET", Pattern: "/share/*", Mode: RouteAuthPublic},
	{Method: "GET", Pattern: "/openapi/*", Mode: RouteAuthPublic},
	// These endpoints issue the first credential and therefore cannot rely on
	// a pre-existing browser CSRF cookie. The trust boundary still requires an
	// allowed Origin for unsafe browser requests.
	{Method: "POST", Pattern: "/api/v1/auth/session", Mode: RouteAuthPublic},
	{Method: "POST", Pattern: "/api/v1/initialize", Mode: RouteAuthPublic},
	{Method: "POST", Pattern: "/api/v1/share-sessions", Mode: RouteAuthPublic},
	{Method: "GET", Pattern: "/api/v1/bootstrap", Mode: RouteAuthCookie},
	{Method: "GET", Pattern: "/api/v1/auth/session", Mode: RouteAuthCookie, AllowEnrollment: true},
	{Method: "POST", Pattern: "/api/v1/account/reauthenticate", Mode: RouteAuthCookie, CSRF: true},
	// Enrollment may call setup/confirm without recent reauth. Full-session TOTP
	// replacement is gated inside the handlers with RequiresRecentAuth semantics.
	{Method: "POST", Pattern: "/api/v1/account/totp/setup", Mode: RouteAuthCookie, CSRF: true, AllowEnrollment: true, RequiresRecentAuth: true},
	{Method: "POST", Pattern: "/api/v1/account/totp/confirm", Mode: RouteAuthCookie, CSRF: true, AllowEnrollment: true, RequiresRecentAuth: true},
	{Method: "DELETE", Pattern: "/api/v1/auth/session", Mode: RouteAuthCookie, CSRF: true, AllowEnrollment: true},
	// Password change verifies the current password and, when enabled, TOTP in
	// the same request; a separate recent-reauthentication round trip would
	// verify the same credentials twice.
	{Method: "PATCH", Pattern: "/api/v1/account/password", Mode: RouteAuthCookie, CSRF: true},
	{Method: "DELETE", Pattern: "/api/v1/account/sessions/*", Mode: RouteAuthCookie, CSRF: true},
	{Method: "POST", Pattern: "/api/v1/account/totp/disable", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	// Recent reauthentication protects credential issuance, administrator account
	// mutations, and hard-to-undo control-plane damage.
	{Method: "POST", Pattern: "/api/v1/ai-tokens", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "DELETE", Pattern: "/api/v1/ai-tokens/*", Mode: RouteAuthCookie, CSRF: true},
	{Method: "POST", Pattern: "/api/v1/admin/backups", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "POST", Pattern: "/api/v1/admin/backups/*/restore", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "POST", Pattern: "/api/v1/admin/users/*/disable", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "POST", Pattern: "/api/v1/admin/users/*/enable", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "POST", Pattern: "/api/v1/admin/users/*/revoke-sessions", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	// Admin user creation grants a full new credential, so it requires a recent
	// reauthentication like other control-plane mutations.
	{Method: "POST", Pattern: "/api/v1/admin/users", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "POST", Pattern: "/api/v1/admin/mounts", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "PATCH", Pattern: "/api/v1/admin/mounts/*", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "DELETE", Pattern: "/api/v1/admin/mounts/*", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "POST", Pattern: "/api/v1/admin/mounts/*/reverify", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "PUT", Pattern: "/api/v1/admin/mounts/*/grants/*", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "DELETE", Pattern: "/api/v1/admin/mounts/*/grants/*", Mode: RouteAuthCookie, CSRF: true, RequiresRecentAuth: true},
	{Method: "GET", Pattern: "/api/v1/admin/*", Mode: RouteAuthCookie},
	{Method: "HEAD", Pattern: "/api/v1/admin/*", Mode: RouteAuthCookie},
	{Method: "*", Pattern: "/api/v1/admin/*", Mode: RouteAuthCookie, CSRF: true},
	{Method: "GET", Pattern: "/api/v1/share/*", Mode: RouteAuthShareCookie},
	{Method: "HEAD", Pattern: "/api/v1/share/*", Mode: RouteAuthShareCookie},
	{Method: "*", Pattern: "/api/v1/share/*", Mode: RouteAuthShareCookie, CSRF: true},
	{Method: "*", Pattern: "/mcp/*", Mode: RouteAuthBearer},
	{Method: "*", Pattern: "/mcp", Mode: RouteAuthBearer},
	{Method: "*", Pattern: "/api/v1/*", Mode: RouteAuthCookie, CSRF: true},
}

func routeRuleFor(r *http.Request) RouteRule {
	if r == nil {
		return RouteRule{Mode: RouteAuthPublic}
	}
	method, requestPath := r.Method, r.URL.Path
	best := RouteRule{Mode: RouteAuthPublic}
	bestScore := -1
	for _, rule := range routeMatrix {
		if rule.Method != "*" && rule.Method != method {
			continue
		}
		if !routePatternMatches(rule.Pattern, requestPath) {
			continue
		}
		score := len(strings.TrimSuffix(rule.Pattern, "/*"))
		if rule.Method == method {
			score += 10000
		}
		if score > bestScore {
			best, bestScore = rule, score
		}
	}
	return best
}

func routePatternMatches(pattern, requestPath string) bool {
	if !strings.ContainsRune(pattern, '*') {
		return pattern == requestPath
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(requestPath, parts[0]) {
		return false
	}
	cursor := len(parts[0])
	for i := 1; i < len(parts); i++ {
		literal := parts[i]
		if literal == "" {
			if i == len(parts)-1 {
				return true
			}
			continue
		}
		idx := strings.Index(requestPath[cursor:], literal)
		if idx < 0 {
			return false
		}
		cursor += idx + len(literal)
	}
	return cursor == len(requestPath)
}

func (s *Server) routeSecurity(rule RouteRule, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Tests and explicitly in-process callers have no external trust policy.
		// The executable enables RequireHTTPTrustBoundary so trustBoundary
		// already fails closed before business handlers run. Keeping this
		// switch makes unit handlers usable without cookies.
		if s.httpPolicy == nil {
			next.ServeHTTP(w, r)
			return
		}
		names := s.cookieNames(r)
		csrfName := names.CSRF
		if rule.Mode == RouteAuthShareCookie || (rule.Mode == RouteAuthPublic && (r.URL.Path == "/api/v1/share" || strings.HasPrefix(r.URL.Path, "/api/v1/share/"))) {
			csrfName = names.ShareCSRF
		}
		if !unsafeHTTPMethod(r.Method) {
			if rule.Mode == RouteAuthCookie || rule.Mode == RouteAuthShareCookie || rule.CSRF {
				if _, err := EnsureCSRFCookie(w, r, csrfName, isSecureRequest(r), time.Now().UTC()); err != nil {
					httpx.WriteError(w, r, http.StatusInternalServerError, "csrf_unavailable", "CSRF protection is unavailable")
					return
				}
			}
		} else if rule.CSRF && !bearerOnlyRequest(r) {
			if err := ValidateCSRF(r, csrfName); err != nil {
				httpx.WriteError(w, r, http.StatusForbidden, "csrf_required", "a valid CSRF token is required")
				return
			}
		}
		session, hasSession := s.sessionFromRequest(r, names.Session)
		if rule.Mode == RouteAuthCookie && !rule.AllowEnrollment {
			if hasSession && session.Purpose == identity.SessionPurposeTOTPEnrollment {
				httpx.WriteError(w, r, http.StatusForbidden, "enrollment_required", "complete TOTP enrollment before using this route")
				return
			}
		}
		// Enrollment sessions may complete first-time TOTP without recent reauth.
		// Full sessions replacing an active TOTP must reauthenticate first.
		enrollmentBypassesReauth := rule.AllowEnrollment && hasSession && session.Purpose == identity.SessionPurposeTOTPEnrollment
		if rule.RequiresRecentAuth && !enrollmentBypassesReauth {
			if hasSession && time.Since(session.ReauthenticatedAt) > identity.RecentReauthenticationTTL {
				httpx.WriteError(w, r, http.StatusForbidden, "reauthentication_required", "recent reauthentication is required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sessionFromRequest(r *http.Request, cookieName string) (identity.Session, bool) {
	if s == nil || s.sqlDB() == nil {
		return identity.Session{}, false
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return identity.Session{}, false
	}
	session, err := identity.New(s.sqlDB(), identity.Options{}).VerifySession(r.Context(), cookie.Value)
	return session, err == nil
}
