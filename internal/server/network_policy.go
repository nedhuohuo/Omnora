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
	LANEnabled      bool
	LANCIDRs        []*net.IPNet
	TrustedProxies  []*net.IPNet
	ActiveBindHint  string
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

func (s *Server) networkGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		policy := s.currentNetworkPolicy()
		if !policy.LANEnabled || len(policy.LANCIDRs) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r, policy.TrustedProxies)
		if ip == nil || !ipInNetworks(ip, policy.LANCIDRs) {
			httpx.WriteError(w, r, http.StatusForbidden, "network_entry_denied", "client address is outside the allowed LAN CIDRs")
			return
		}
		next.ServeHTTP(w, r)
	})
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
