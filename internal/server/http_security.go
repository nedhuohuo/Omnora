package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"omnora/internal/audit"
	"omnora/internal/config"
	"omnora/internal/httpx"
	"omnora/internal/ratelimit"
)

type httpSecurityContextKey string

const (
	clientIPContextKey   httpSecurityContextKey = "omnora-client-ip"
	secureRequestContext httpSecurityContextKey = "omnora-secure-request"
)

// HTTPPolicy is the immutable request trust policy derived from configuration.
// Empty policy is used by in-process handler tests that intentionally bypass
// environment loading; the executable validates business exposure before it
// constructs a listener.
type HTTPPolicy struct {
	allowedHosts   map[string]struct{}
	allowedOrigins map[string]struct{}
	proxyNetworks  []*net.IPNet
	secureExternal bool
}

func newHTTPPolicy(cfg config.HTTPConfig) (*HTTPPolicy, error) {
	if strings.TrimSpace(cfg.PublicURL) == "" && len(cfg.AllowedHosts) == 0 && len(cfg.AllowedOrigins) == 0 && len(cfg.TrustedProxyCIDRs) == 0 {
		return nil, nil
	}
	policy := &HTTPPolicy{
		allowedHosts:   make(map[string]struct{}),
		allowedOrigins: make(map[string]struct{}),
		proxyNetworks:  append([]*net.IPNet(nil), cfg.TrustedProxyCIDRs...),
	}
	if cfg.PublicURL != "" {
		parsed, err := url.Parse(cfg.PublicURL)
		if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, errors.New("invalid public URL")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, errors.New("public URL scheme must be http or https")
		}
		policy.secureExternal = parsed.Scheme == "https"
		if len(cfg.AllowedHosts) == 0 {
			policy.allowedHosts[canonicalHost(parsed.Host)] = struct{}{}
		}
		if len(cfg.AllowedOrigins) == 0 {
			policy.allowedOrigins[canonicalOrigin(parsed)] = struct{}{}
		}
	}
	for _, host := range cfg.AllowedHosts {
		canonical := canonicalHost(host)
		if canonical == "" || strings.Contains(canonical, "*") {
			return nil, fmt.Errorf("invalid allowed host %q", host)
		}
		policy.allowedHosts[canonical] = struct{}{}
	}
	for _, origin := range cfg.AllowedOrigins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("invalid allowed origin %q", origin)
		}
		policy.allowedOrigins[canonicalOrigin(parsed)] = struct{}{}
	}
	if len(policy.allowedHosts) == 0 && len(policy.allowedOrigins) == 0 {
		return nil, errors.New("HTTP policy has no host or origin allowlist")
	}
	return policy, nil
}

func canonicalHost(value string) string {
	value = strings.TrimSpace(strings.ToLower(strings.TrimSuffix(value, ".")))
	if value == "" {
		return ""
	}
	if host, port, err := net.SplitHostPort(value); err == nil {
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if strings.Contains(host, ":") {
			return net.JoinHostPort(host, port)
		}
		return host + ":" + port
	}
	return value
}

func canonicalOrigin(value *url.URL) string {
	return strings.ToLower(value.Scheme) + "://" + canonicalHost(value.Host)
}

func (s *Server) trustBoundary(next http.Handler) http.Handler {
	if s.httpPolicy == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.httpPolicy.allowedHosts[canonicalHost(r.Host)]; !ok {
			httpx.WriteError(w, r, http.StatusMisdirectedRequest, "invalid_host", "request host is not allowed")
			return
		}
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
				httpx.WriteError(w, r, http.StatusForbidden, "invalid_origin", "request origin is not allowed")
				return
			}
			if _, ok := s.httpPolicy.allowedOrigins[canonicalOrigin(parsed)]; !ok {
				httpx.WriteError(w, r, http.StatusForbidden, "invalid_origin", "request origin is not allowed")
				return
			}
		}
		if unsafeHTTPMethod(r.Method) && origin == "" && !bearerOnlyRequest(r) {
			httpx.WriteError(w, r, http.StatusForbidden, "origin_required", "an allowed origin is required")
			return
		}
		clientIP, err := s.httpPolicy.clientIP(r)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "invalid_forwarded_headers", "client forwarding headers are invalid")
			return
		}
		ctx := contextWithClientIP(r.Context(), clientIP)
		ctx = contextWithSecureRequest(ctx, s.httpPolicy.secureExternal || r.TLS != nil)
		if s.httpPolicy.secureExternal {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unsafeHTTPMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}

func bearerOnlyRequest(r *http.Request) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))), "bearer ") && r.Header.Get("Cookie") == ""
}

func (p *HTTPPolicy) clientIP(r *http.Request) (net.IP, error) {
	peer, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		peer = strings.TrimSpace(r.RemoteAddr)
	}
	peerIP := net.ParseIP(peer)
	if peerIP == nil {
		return nil, errors.New("remote address is invalid")
	}
	if !p.isTrustedProxy(peerIP) {
		return peerIP, nil
	}
	forwarded := strings.TrimSpace(r.Header.Get("Forwarded"))
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if forwarded != "" && xff != "" {
		return nil, errors.New("multiple forwarding standards")
	}
	if forwarded == "" && xff == "" {
		return peerIP, nil
	}
	var chain []net.IP
	if xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 8 {
			return nil, errors.New("forwarding chain is too long")
		}
		for _, part := range parts {
			ip := net.ParseIP(strings.TrimSpace(part))
			if ip == nil {
				return nil, errors.New("forwarded address is invalid")
			}
			chain = append(chain, ip)
		}
	} else {
		for _, element := range strings.Split(forwarded, ",") {
			var found string
			for _, item := range strings.Split(element, ";") {
				keyValue := strings.SplitN(strings.TrimSpace(item), "=", 2)
				if len(keyValue) == 2 && strings.EqualFold(keyValue[0], "for") {
					found = strings.Trim(strings.TrimSpace(keyValue[1]), "\"")
				}
			}
			ip := net.ParseIP(strings.Trim(found, "[]"))
			if ip == nil {
				return nil, errors.New("forwarded address is invalid")
			}
			chain = append(chain, ip)
		}
	}
	candidate := peerIP
	foundUntrusted := false
	for index := len(chain) - 1; index >= 0; index-- {
		if !p.isTrustedProxy(candidate) {
			foundUntrusted = true
			break
		}
		candidate = chain[index]
	}
	if !foundUntrusted && p.isTrustedProxy(candidate) {
		return peerIP, nil
	}
	return candidate, nil
}

func (p *HTTPPolicy) isTrustedProxy(ip net.IP) bool {
	for _, network := range p.proxyNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func contextWithClientIP(ctx context.Context, ip net.IP) context.Context {
	return context.WithValue(ctx, clientIPContextKey, ip.String())
}

func contextWithSecureRequest(ctx context.Context, secure bool) context.Context {
	return context.WithValue(ctx, secureRequestContext, secure)
}

func clientIPFromRequest(r *http.Request) string {
	if value, ok := r.Context().Value(clientIPContextKey).(string); ok && value != "" {
		return value
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (s *Server) rateLimitKeys(r *http.Request, subject string) ratelimit.Keys {
	key := []byte(s.cfg.Secrets.AuditHMACKey)
	return ratelimit.Keys{
		IP:      audit.HashForAudit(key, audit.HashDomainClientIP, clientIPFromRequest(r)),
		Subject: audit.HashForAudit(key, audit.HashDomainSubject, strings.ToLower(strings.TrimSpace(subject))),
	}
}

func (s *Server) checkCredentialRateLimit(r *http.Request, scope ratelimit.Scope, subject string) ratelimit.Decision {
	if s.authLimiter == nil {
		return ratelimit.Decision{Allowed: true}
	}
	return s.authLimiter.Check(scope, s.rateLimitKeys(r, subject))
}

func (s *Server) recordCredentialFailure(r *http.Request, scope ratelimit.Scope, subject string) ratelimit.Decision {
	if s.authLimiter == nil {
		return ratelimit.Decision{Allowed: true}
	}
	return s.authLimiter.Failure(scope, s.rateLimitKeys(r, subject))
}

func (s *Server) recordCredentialSuccess(r *http.Request, scope ratelimit.Scope, subject string) {
	if s.authLimiter != nil {
		s.authLimiter.Success(scope, s.rateLimitKeys(r, subject).Subject)
	}
}

func writeRateLimited(w http.ResponseWriter, r *http.Request, decision ratelimit.Decision) {
	ratelimit.RetryAfterHeader(w, decision.RetryAfter)
	httpx.WriteError(w, r, http.StatusTooManyRequests, "rate_limited", "too many credential attempts")
}
