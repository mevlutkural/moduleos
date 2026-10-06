package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
)

func TestLoad_Defaults(t *testing.T) {
	// Unset any env vars that may be set in CI.
	for _, key := range []string{"ENV", "PORT", "DOCKER_ENDPOINT", "DOCKER_SOCKET", "BASE_DOMAIN", "DATABASE_PATH", "API_KEY"} {
		_ = os.Unsetenv(key)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.Env != "development" {
		t.Errorf("Env: got %q, want %q", cfg.Env, "development")
	}
	if cfg.Port != "3000" {
		t.Errorf("Port: got %q, want %q", cfg.Port, "3000")
	}
	if cfg.DockerEndpoint != "unix:///var/run/docker.sock" {
		t.Errorf("DockerEndpoint: got %q", cfg.DockerEndpoint)
	}
	if cfg.BaseDomain != "moduleos.local" {
		t.Errorf("BaseDomain: got %q, want %q", cfg.BaseDomain, "moduleos.local")
	}
	if cfg.DatabasePath != "./moduleos.db" {
		t.Errorf("DatabasePath: got %q, want %q", cfg.DatabasePath, "./moduleos.db")
	}
	if cfg.APIKey != "" {
		t.Errorf("APIKey: got %q, want empty string", cfg.APIKey)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("PORT", "8080")
	t.Setenv("BASE_DOMAIN", "mycompany.com")
	apiKey := strings.Repeat("test-key-segment-", 2)
	t.Setenv("API_KEY", apiKey)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.Env != "production" {
		t.Errorf("Env: got %q, want %q", cfg.Env, "production")
	}
	if cfg.Port != "8080" {
		t.Errorf("Port: got %q, want %q", cfg.Port, "8080")
	}
	if cfg.BaseDomain != "mycompany.com" {
		t.Errorf("BaseDomain: got %q, want %q", cfg.BaseDomain, "mycompany.com")
	}
	if cfg.APIKey != apiKey {
		t.Errorf("APIKey override was not applied")
	}
}

func TestLoadRejectsInsecureProductionConfiguration(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("API_KEY", "short")
	if _, err := config.Load(); err == nil {
		t.Fatal("expected weak production API key to be rejected")
	}
}

func TestValidateRejectsUnsafeMountRoot(t *testing.T) {
	cfg := &config.Config{Env: "development", LogLevel: "info", ListenAddress: "127.0.0.1", Port: "3000", DockerEndpoint: "unix:///var/run/docker.sock", BaseDomain: "moduleos.local", DatabasePath: ":memory:", IngressNetwork: "moduleos-ingress", AllowedMountRoots: []string{"/"}, ReconcileInterval: time.Second, ReconcileMaxBackoff: time.Second, DeploymentTimeout: time.Minute, StabilizationWindow: time.Second, ShutdownTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ReconcileWorkers: 1, MaxReplicas: 1, BodyLimit: 1024, MaxLogTail: 1, MaxLogPayloadBytes: 1024, MaxSSEConnections: 1, MaxSSEConnectionsPerApp: 1, RequestConcurrency: 1}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected root mount allowlist to be rejected")
	}
}

func TestLoadRejectsUnwritableDatabaseParent(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	t.Setenv("DATABASE_PATH", filepath.Join(directory, "moduleos.db"))
	if _, err := config.Load(); err == nil {
		t.Fatal("expected unwritable database parent to be rejected")
	}
}
