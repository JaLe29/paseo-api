// Package config loads the service configuration from the environment.
// All paseo daemon settings live here — there is no hardcoded host, only defaults.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings for the service.
type Config struct {
	// Address this HTTP API listens on (e.g. ":3000").
	ListenAddr string

	// Shared token (x-api-token header). When empty, the gate is disabled.
	APIToken string

	// The paseo daemon (instance) to connect to — "IP:port" (e.g. "192.168.0.3:6666").
	PaseoHost string
	// Optional daemon password (sent as a Bearer token during the WS handshake).
	PaseoPassword string

	// Default agent provider and model. A request may override these.
	DefaultProvider string
	DefaultModel    string
	// Default working directory on the host, mode and thinking level.
	DefaultCwd      string
	DefaultMode     string
	DefaultThinking string

	// Maximum time to wait for an agent to finish.
	WaitTimeout time.Duration
	// Timeout for establishing the WS connection to the daemon.
	ConnectTimeout time.Duration
}

// Load reads the configuration from environment variables and fills in defaults.
// PASEO_HOST is the only required variable.
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:      envOr("PORT", ":3000"),
		APIToken:        os.Getenv("API_TOKEN"),
		PaseoHost:       os.Getenv("PASEO_HOST"),
		PaseoPassword:   os.Getenv("PASEO_PASSWORD"),
		DefaultProvider: envOr("PASEO_PROVIDER", "claude"),
		DefaultModel:    envOr("PASEO_MODEL", "claude-opus-4-8"),
		DefaultCwd:      envOr("PASEO_CWD", "/app"),
		DefaultMode:     envOr("PASEO_MODE", "bypassPermissions"),
		DefaultThinking: envOr("PASEO_THINKING", "low"),
		WaitTimeout:     envDuration("PASEO_WAIT_TIMEOUT", 5*time.Minute),
		ConnectTimeout:  envDuration("PASEO_CONNECT_TIMEOUT", 15*time.Second),
	}

	if strings.TrimSpace(cfg.PaseoHost) == "" {
		return Config{}, fmt.Errorf("PASEO_HOST must be set (e.g. 192.168.0.3:6666)")
	}

	// If the user passes only a port number (PORT=3000), turn it into ":3000".
	if !strings.Contains(cfg.ListenAddr, ":") {
		cfg.ListenAddr = ":" + cfg.ListenAddr
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// envDuration accepts either a Go duration ("5m", "30s") or a bare number (seconds).
func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second
	}
	return fallback
}
