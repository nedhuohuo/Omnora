package server

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"strings"
	"sync"

	"omnora/internal/httpx"
)

type networkPolicy struct {
	LANEnabled     bool
	LANCIDRs       []*net.IPNet
	ProxyEnabled   bool
	ProxyBindAddr  string
	TrustedProxies []*net.IPNet
	ActiveBindHint string
}

type networkPolicyStore struct {
	mu     sync.RWMutex
	policy networkPolicy
}

func (s *Server) hydrateNetworkPolicy(ctx context.Context) {
	if s.db == nil {
		return
	}
	policy, err := loadNetworkPolicy(ctx, s.sqlDB())
	if err != nil {
		return
	}
	s.network.mu.Lock()
	s.network.policy = policy
	s.network.mu.Unlock()
}

func loadNetworkPolicy(ctx context.Context, db *sql.DB) (networkPolicy, error) {
	rows, err := db.QueryContext(ctx, `
SELECT name, enabled, bind_addr, cidr_json
FROM network_entries
`)
	if err != nil {
		return networkPolicy{}, err
	}
	defer rows.Close()

	var policy networkPolicy
	for rows.Next() {
		var name, bindAddr, cidrJSON string
		var enabled int
		if err := rows.Scan(&name, &enabled, &bindAddr, &cidrJSON); err != nil {
			return networkPolicy{}, err
		}
		cidrs := parseCIDRs(unmarshalStrings(cidrJSON))
		switch name {
		case "lan_http":
			policy.LANEnabled = enabled == 1
			policy.LANCIDRs = cidrs
			policy.ActiveBindHint = strings.TrimSpace(bindAddr)
		case "proxy_https":
			policy.ProxyEnabled = enabled == 1
			policy.ProxyBindAddr = strings.TrimSpace(bindAddr)
			policy.TrustedProxies = cidrs
		}
	}
	return policy, rows.Err()
}

func parseCIDRs(values []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !strings.Contains(value, "/") {
			ip := net.ParseIP(value)
			if ip == nil {
				continue
			}
			if ip.To4() != nil {
				value = value + "/32"
			} else {
				value = value + "/128"
			}
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			continue
		}
		out = append(out, network)
	}
	return out
}

func (s *Server) currentNetworkPolicy() networkPolicy {
	s.network.mu.RLock()
	defer s.network.mu.RUnlock()
	return s.network.policy
}

// lanGate admits the direct peer when the LAN entry is enabled and its
// address falls inside the allowed CIDRs. The LAN listener is served directly
// (no reverse proxy in front), so forwarded headers are never trusted.
func (s *Server) lanGate(w http.ResponseWriter, r *http.Request) bool {
	policy := s.currentNetworkPolicy()
	if !policy.LANEnabled {
		return true
	}
	if len(policy.LANCIDRs) == 0 {
		// Enabled but misconfigured: fail closed rather than open the
		// listener to every client address.
		httpx.WriteError(w, r, http.StatusForbidden, "network_entry_unconfigured", "LAN entry is enabled but has no allowed CIDRs")
		return false
	}
	ip := clientIP(r, nil)
	if ip == nil || !ipInNetworks(ip, policy.LANCIDRs) {
		httpx.WriteError(w, r, http.StatusForbidden, "network_entry_denied", "client address is outside the allowed LAN CIDRs")
		return false
	}
	return true
}

// proxyGate admits a request only when the proxy entry is enabled, has
// trusted proxy CIDRs configured, and the direct peer is one of them. The
// direct peer is the reverse proxy itself; clientIP then recovers the real
// client from forwarded headers.
func (s *Server) proxyGate(w http.ResponseWriter, r *http.Request) bool {
	policy := s.currentNetworkPolicy()
	if !policy.ProxyEnabled {
		httpx.WriteError(w, r, http.StatusForbidden, "network_entry_disabled", "proxy entry is not enabled")
		return false
	}
	if len(policy.TrustedProxies) == 0 {
		// Enabled but no trusted proxies configured: fail closed.
		httpx.WriteError(w, r, http.StatusForbidden, "network_entry_unconfigured", "proxy entry has no trusted proxy CIDRs")
		return false
	}
	peer := clientIP(r, nil)
	if peer == nil || !ipInNetworks(peer, policy.TrustedProxies) {
		httpx.WriteError(w, r, http.StatusForbidden, "network_entry_denied", "direct peer is not a trusted proxy")
		return false
	}
	return true
}

// ProxyEntryState reports the configured state of the proxy_https entry so
// startup can decide whether to bind its listener.
func (s *Server) ProxyEntryState() (enabled bool, bindAddr string) {
	policy := s.currentNetworkPolicy()
	return policy.ProxyEnabled, policy.ProxyBindAddr
}

func clientIP(r *http.Request, trustedProxies []*net.IPNet) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	remote := net.ParseIP(host)
	if remote == nil {
		return nil
	}
	if len(trustedProxies) == 0 || !ipInNetworks(remote, trustedProxies) {
		return remote
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
		if parsed := net.ParseIP(first); parsed != nil {
			return parsed
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		if parsed := net.ParseIP(realIP); parsed != nil {
			return parsed
		}
	}
	return remote
}

func ipInNetworks(ip net.IP, networks []*net.IPNet) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
