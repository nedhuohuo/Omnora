package config

import (
	"reflect"
	"testing"
	"time"

	"omnora/internal/domain"
)

func TestLoadEnvDefaultsFailClosed(t *testing.T) {
	clearEnv(t)

	cfg, err := LoadEnv()
	if err != nil {
		t.Fatalf("LoadEnv() error = %v", err)
	}

	if cfg.HTTP.Addr != "127.0.0.1:8080" {
		t.Fatalf("HTTP addr = %q", cfg.HTTP.Addr)
	}
	if cfg.Database.Path != "" {
		t.Fatalf("DB path = %q, want empty", cfg.Database.Path)
	}
	if cfg.Log.Format != "text" || cfg.Log.Level != "info" {
		t.Fatalf("log config = %#v, want text/info", cfg.Log)
	}
	if cfg.Database.BusyTimeout != 5*time.Second {
		t.Fatalf("busy timeout = %s", cfg.Database.BusyTimeout)
	}
	if cfg.Initialization.Token != "" {
		t.Fatalf("initialization token = %q, want empty", cfg.Initialization.Token)
	}
	if cfg.Secrets.AuditHMACKey != "" {
		t.Fatalf("audit HMAC key = %q, want empty", cfg.Secrets.AuditHMACKey)
	}
	if cfg.Initialization.TTL != 30*time.Minute {
		t.Fatalf("initialization ttl = %s", cfg.Initialization.TTL)
	}
	for _, group := range domain.AllRouteGroups {
		if cfg.Routes.Enabled(group) {
			t.Fatalf("route group %s should be disabled by default", group)
		}
	}
}

func TestLoadEnvParsesAuditHMACKey(t *testing.T) {
	clearEnv(t)
	key := "audit-hmac-key-012345678901234567890123456789"
	t.Setenv("OMNORA_AUDIT_HMAC_KEY", key)
	cfg, err := LoadEnv()
	if err != nil {
		t.Fatalf("LoadEnv() error = %v", err)
	}
	if cfg.Secrets.AuditHMACKey != key {
		t.Fatalf("audit HMAC key = %q, want configured value", cfg.Secrets.AuditHMACKey)
	}
	if err := cfg.ValidateAuditHMACKey(); err != nil {
		t.Fatalf("ValidateAuditHMACKey() error = %v", err)
	}
}

func TestValidateAuditHMACKeyRejectsShortOrMissingKey(t *testing.T) {
	for _, key := range []string{"", "too-short", "audit-hmac-key-01234567890123456789012\n"} {
		cfg := Config{Secrets: SecretConfig{AuditHMACKey: key}}
		if err := cfg.ValidateAuditHMACKey(); err == nil {
			t.Fatalf("ValidateAuditHMACKey(%q) error = nil", key)
		}
	}
}

func TestLoadEnvRouteGroups(t *testing.T) {
	clearEnv(t)
	t.Setenv("OMNORA_ROUTE_REST_ENABLED", "true")
	t.Setenv("OMNORA_ROUTE_SHARE_ENABLED", "on")

	cfg, err := LoadEnv()
	if err != nil {
		t.Fatalf("LoadEnv() error = %v", err)
	}

	if !cfg.Routes.Enabled(domain.RouteGroupREST) {
		t.Fatal("REST route group should be enabled")
	}
	if !cfg.Routes.Enabled(domain.RouteGroupShare) {
		t.Fatal("share route group should be enabled")
	}
	if cfg.Routes.Enabled(domain.RouteGroupAdminWeb) {
		t.Fatal("admin route group should remain disabled")
	}
	if enabled, ok := cfg.RouteEnvOverrides[domain.RouteGroupREST]; !ok || !enabled {
		t.Fatalf("REST env override = %v, %v", enabled, ok)
	}
	if _, ok := cfg.RouteEnvOverrides[domain.RouteGroupAdminWeb]; ok {
		t.Fatal("admin env override should be absent when unset")
	}
}

func TestLoadEnvLogging(t *testing.T) {
	clearEnv(t)
	t.Setenv("OMNORA_LOG_FORMAT", "json")
	t.Setenv("OMNORA_LOG_LEVEL", "debug")

	cfg, err := LoadEnv()
	if err != nil {
		t.Fatalf("LoadEnv() error = %v", err)
	}
	if cfg.Log.Format != "json" || cfg.Log.Level != "debug" {
		t.Fatalf("log config = %#v, want json/debug", cfg.Log)
	}
}

func TestLoadEnvRejectsInvalidLogging(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "format", env: "OMNORA_LOG_FORMAT", value: "xml"},
		{name: "level", env: "OMNORA_LOG_LEVEL", value: "trace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tc.env, tc.value)
			if _, err := LoadEnv(); err == nil {
				t.Fatal("LoadEnv() error = nil, want invalid logging config")
			}
		})
	}
}

func TestLoadEnvRejectsInvalidBool(t *testing.T) {
	clearEnv(t)
	t.Setenv("OMNORA_ROUTE_MCP_ENABLED", "sometimes")

	if _, err := LoadEnv(); err == nil {
		t.Fatal("LoadEnv() error = nil, want invalid boolean")
	}
}

func TestLoadEnvParsesMCPNetworkConfig(t *testing.T) {
	clearEnv(t)
	t.Setenv("OMNORA_MCP_ALLOWED_HOSTS", "mcp.example.test:8443,localhost")
	t.Setenv("OMNORA_MCP_ALLOWED_ORIGINS", "https://mcp.example.test:8443")
	t.Setenv("OMNORA_MCP_MAX_BODY_BYTES", "2097152")

	cfg, err := LoadEnv()
	if err != nil {
		t.Fatalf("LoadEnv() error = %v", err)
	}
	if got, want := cfg.MCP.AllowedHosts, []string{"mcp.example.test:8443", "localhost"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MCP.AllowedHosts = %#v, want %#v", got, want)
	}
	if got, want := cfg.MCP.AllowedOrigins, []string{"https://mcp.example.test:8443"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("MCP.AllowedOrigins = %#v, want %#v", got, want)
	}
	if cfg.MCP.MaxBodyBytes != 2097152 {
		t.Fatalf("MCP.MaxBodyBytes = %d, want 2097152", cfg.MCP.MaxBodyBytes)
	}
}

func TestLoadEnvDefaultsMCPBodyLimit(t *testing.T) {
	clearEnv(t)
	cfg, err := LoadEnv()
	if err != nil {
		t.Fatalf("LoadEnv() error = %v", err)
	}
	if cfg.MCP.MaxBodyBytes != 1<<20 {
		t.Fatalf("MCP.MaxBodyBytes = %d, want %d", cfg.MCP.MaxBodyBytes, 1<<20)
	}
}

func TestLoadEnvRejectsInvalidMCPNetworkConfig(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "wildcard host", env: "OMNORA_MCP_ALLOWED_HOSTS", value: "*.example.test"},
		{name: "zero body limit", env: "OMNORA_MCP_MAX_BODY_BYTES", value: "0"},
		{name: "non numeric body limit", env: "OMNORA_MCP_MAX_BODY_BYTES", value: "large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tc.env, tc.value)
			if _, err := LoadEnv(); err == nil {
				t.Fatal("LoadEnv() error = nil, want invalid MCP configuration")
			}
		})
	}
}

func TestLoadEnvParsesHTTPTrustConfiguration(t *testing.T) {
	clearEnv(t)
	t.Setenv("OMNORA_PUBLIC_URL", "https://files.example.test/")
	t.Setenv("OMNORA_ALLOWED_HOSTS", "files.example.test, files.example.test:443")
	t.Setenv("OMNORA_ALLOWED_ORIGINS", "https://files.example.test")
	t.Setenv("OMNORA_TRUSTED_PROXY_CIDRS", "10.0.0.0/8, 192.0.2.0/24")
	cfg, err := LoadEnv()
	if err != nil {
		t.Fatalf("LoadEnv() error = %v", err)
	}
	if cfg.HTTP.PublicURL != "https://files.example.test/" {
		t.Fatalf("public URL = %q", cfg.HTTP.PublicURL)
	}
	if len(cfg.HTTP.AllowedHosts) != 2 || len(cfg.HTTP.AllowedOrigins) != 1 || len(cfg.HTTP.TrustedProxyCIDRs) != 2 {
		t.Fatalf("trust config = %#v", cfg.HTTP)
	}
}

func TestLoadEnvRejectsInvalidTrustConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   string
		value string
	}{
		{name: "wildcard host", env: "OMNORA_ALLOWED_HOSTS", value: "*"},
		{name: "invalid cidr", env: "OMNORA_TRUSTED_PROXY_CIDRS", value: "not-a-cidr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tc.env, tc.value)
			if _, err := LoadEnv(); err == nil {
				t.Fatal("LoadEnv() error = nil")
			}
		})
	}
}

func TestValidateBusinessExposureRequiresSecurePublicURL(t *testing.T) {
	for _, tc := range []struct {
		name      string
		publicURL string
		wantError bool
	}{
		{name: "missing", wantError: true},
		{name: "external http", publicURL: "http://files.example.test", wantError: true},
		{name: "loopback http", publicURL: "http://127.0.0.1:8080", wantError: false},
		{name: "https", publicURL: "https://files.example.test", wantError: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{HTTP: HTTPConfig{PublicURL: tc.publicURL}, Routes: map[domain.RouteGroup]bool{domain.RouteGroupREST: true}}
			if err := cfg.ValidateBusinessExposure(); (err != nil) != tc.wantError {
				t.Fatalf("ValidateBusinessExposure() error = %v, wantError=%v", err, tc.wantError)
			}
		})
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"OMNORA_HTTP_ADDR",
		"OMNORA_PUBLIC_URL",
		"OMNORA_ALLOWED_HOSTS",
		"OMNORA_ALLOWED_ORIGINS",
		"OMNORA_TRUSTED_PROXY_CIDRS",
		"OMNORA_MCP_ALLOWED_HOSTS",
		"OMNORA_MCP_ALLOWED_ORIGINS",
		"OMNORA_MCP_MAX_BODY_BYTES",
		"OMNORA_LOG_FORMAT",
		"OMNORA_LOG_LEVEL",
		"OMNORA_DB_PATH",
		"OMNORA_SQLITE_BUSY_TIMEOUT",
		"OMNORA_INITIALIZATION_TOKEN",
		"OMNORA_INITIALIZATION_TOKEN_TTL",
		"OMNORA_TOTP_ENCRYPTION_KEY",
		"OMNORA_AUDIT_HMAC_KEY",
		"OMNORA_ROUTE_MEMBER_WEB_ENABLED",
		"OMNORA_ROUTE_ADMIN_WEB_ENABLED",
		"OMNORA_ROUTE_SHARE_ENABLED",
		"OMNORA_ROUTE_REST_ENABLED",
		"OMNORA_ROUTE_MCP_ENABLED",
		"OMNORA_ROUTE_OPENAPI_ENABLED",
	} {
		t.Setenv(name, "")
	}
}
