package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"omnora/internal/access"
	"omnora/internal/domain"
)

type Config struct {
	HTTP              HTTPConfig
	Database          DatabaseConfig
	Initialization    InitializationConfig
	Secrets           SecretConfig
	Storage           StorageConfig
	Routes            access.RouteGroups
	RouteEnvOverrides map[domain.RouteGroup]bool
}

type StorageConfig struct {
	ManagedDir           string
	PredeclaredMountRoot string
}

type HTTPConfig struct {
	Addr string
	// ProxyHTTPSListen is the default bind address for the proxy_https entry
	// when its network_entries row does not set one. The entry sits behind an
	// external HTTPS reverse proxy, so this is a plain HTTP listener.
	ProxyHTTPSListen string
}

type DatabaseConfig struct {
	Path        string
	BusyTimeout time.Duration
}

type InitializationConfig struct {
	Token string
	TTL   time.Duration
}

type SecretConfig struct {
	TOTPEncryptionKey string
}

func LoadEnv() (Config, error) {
	cfg := Config{
		HTTP: HTTPConfig{
			Addr:             "127.0.0.1:8080",
			ProxyHTTPSListen: "127.0.0.1:8081",
		},
		Database: DatabaseConfig{
			BusyTimeout: 5 * time.Second,
		},
		Initialization: InitializationConfig{
			TTL: 30 * time.Minute,
		},
		Storage: StorageConfig{
			ManagedDir:           "/srv/omnora/managed",
			PredeclaredMountRoot: "/mnt/omnora",
		},
		Routes:            access.DefaultRouteGroups(),
		RouteEnvOverrides: map[domain.RouteGroup]bool{},
	}

	if value := strings.TrimSpace(os.Getenv("OMNORA_HTTP_ADDR")); value != "" {
		cfg.HTTP.Addr = value
	}
	if _, _, err := net.SplitHostPort(cfg.HTTP.Addr); err != nil {
		return Config{}, fmt.Errorf("OMNORA_HTTP_ADDR must be host:port: %w", err)
	}
	if value := strings.TrimSpace(os.Getenv("OMNORA_PROXY_HTTPS_LISTEN")); value != "" {
		cfg.HTTP.ProxyHTTPSListen = value
	}
	if _, _, err := net.SplitHostPort(cfg.HTTP.ProxyHTTPSListen); err != nil {
		return Config{}, fmt.Errorf("OMNORA_PROXY_HTTPS_LISTEN must be host:port: %w", err)
	}

	cfg.Database.Path = strings.TrimSpace(os.Getenv("OMNORA_DB_PATH"))
	if value := strings.TrimSpace(os.Getenv("OMNORA_SQLITE_BUSY_TIMEOUT")); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("OMNORA_SQLITE_BUSY_TIMEOUT: %w", err)
		}
		if timeout <= 0 {
			return Config{}, fmt.Errorf("OMNORA_SQLITE_BUSY_TIMEOUT must be positive")
		}
		cfg.Database.BusyTimeout = timeout
	}

	cfg.Initialization.Token = strings.TrimSpace(os.Getenv("OMNORA_INITIALIZATION_TOKEN"))
	if value := strings.TrimSpace(os.Getenv("OMNORA_INITIALIZATION_TOKEN_TTL")); value != "" {
		ttl, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("OMNORA_INITIALIZATION_TOKEN_TTL: %w", err)
		}
		if ttl <= 0 {
			return Config{}, fmt.Errorf("OMNORA_INITIALIZATION_TOKEN_TTL must be positive")
		}
		cfg.Initialization.TTL = ttl
	}
	cfg.Secrets.TOTPEncryptionKey = strings.TrimSpace(os.Getenv("OMNORA_TOTP_ENCRYPTION_KEY"))
	if value := strings.TrimSpace(os.Getenv("OMNORA_MANAGED_STORAGE_DIR")); value != "" {
		cfg.Storage.ManagedDir = value
	}
	if value := strings.TrimSpace(os.Getenv("OMNORA_PREDECLARED_MOUNT_ROOT")); value != "" {
		cfg.Storage.PredeclaredMountRoot = value
	}

	envByGroup := map[domain.RouteGroup]string{
		domain.RouteGroupMemberWeb: "OMNORA_ROUTE_MEMBER_WEB_ENABLED",
		domain.RouteGroupAdminWeb:  "OMNORA_ROUTE_ADMIN_WEB_ENABLED",
		domain.RouteGroupShare:     "OMNORA_ROUTE_SHARE_ENABLED",
		domain.RouteGroupREST:      "OMNORA_ROUTE_REST_ENABLED",
		domain.RouteGroupMCP:       "OMNORA_ROUTE_MCP_ENABLED",
		domain.RouteGroupOpenAPI:   "OMNORA_ROUTE_OPENAPI_ENABLED",
	}
	for group, name := range envByGroup {
		enabled, ok, err := parseOptionalBool(os.Getenv(name))
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", name, err)
		}
		if ok {
			cfg.Routes.Set(group, enabled)
			cfg.RouteEnvOverrides[group] = enabled
		}
	}

	return cfg, nil
}

func parseOptionalBool(value string) (bool, bool, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return false, false, nil
	}

	if parsed, err := strconv.ParseBool(value); err == nil {
		return parsed, true, nil
	}

	switch value {
	case "yes", "y", "on", "enabled":
		return true, true, nil
	case "no", "n", "off", "disabled":
		return false, true, nil
	default:
		return false, true, fmt.Errorf("invalid boolean %q", value)
	}
}
