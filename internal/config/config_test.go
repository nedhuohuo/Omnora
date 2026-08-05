package config

import (
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
	if cfg.Initialization.TTL != 30*time.Minute {
		t.Fatalf("initialization ttl = %s", cfg.Initialization.TTL)
	}
	for _, group := range domain.AllRouteGroups {
		if cfg.Routes.Enabled(group) {
			t.Fatalf("route group %s should be disabled by default", group)
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

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"OMNORA_HTTP_ADDR",
		"OMNORA_LOG_FORMAT",
		"OMNORA_LOG_LEVEL",
		"OMNORA_DB_PATH",
		"OMNORA_SQLITE_BUSY_TIMEOUT",
		"OMNORA_INITIALIZATION_TOKEN",
		"OMNORA_INITIALIZATION_TOKEN_TTL",
		"OMNORA_TOTP_ENCRYPTION_KEY",
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
