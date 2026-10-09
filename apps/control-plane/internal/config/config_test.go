package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
)

var configEnvironmentKeys = []string{
	"ENV", "LOG_LEVEL", "LISTEN_ADDRESS", "PORT", "DOCKER_ENDPOINT", "DOCKER_SOCKET",
	"BASE_DOMAIN", "DATABASE_PATH", "API_KEY", "INGRESS_NETWORK", "CORS_ALLOWED_ORIGINS",
	"ALLOWED_MOUNT_ROOTS", "RECONCILE_INTERVAL", "RECONCILE_MAX_BACKOFF", "RECONCILE_WORKERS",
	"DEPLOYMENT_TIMEOUT", "STABILIZATION_WINDOW", "SHUTDOWN_TIMEOUT", "HTTP_READ_TIMEOUT",
	"HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "HTTP_BODY_LIMIT", "MAX_REPLICAS", "MAX_APPLICATIONS",
	"MAX_PROJECTS", "MAX_LOG_TAIL", "MAX_LOG_PAYLOAD_BYTES", "MAX_SSE_CONNECTIONS",
	"MAX_SSE_CONNECTIONS_PER_APP", "REQUEST_CONCURRENCY",
}

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range configEnvironmentKeys {
		value, exists := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, value)
				return
			}
			_ = os.Unsetenv(key)
		})
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearConfigEnvironment(t)

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
	clearConfigEnvironment(t)
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
	clearConfigEnvironment(t)
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
	clearConfigEnvironment(t)
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

func validConfig() *config.Config {
	return &config.Config{
		Env: "development", LogLevel: "info", ListenAddress: "127.0.0.1", Port: "3000",
		DockerEndpoint: "unix:///var/run/docker.sock", BaseDomain: "moduleos.local", DatabasePath: ":memory:",
		IngressNetwork: "moduleos-ingress", AllowedMountRoots: []string{"/srv/moduleos-data"},
		ReconcileInterval: time.Second, ReconcileMaxBackoff: time.Second, ReconcileWorkers: 1,
		DeploymentTimeout: time.Minute, StabilizationWindow: 0, ShutdownTimeout: time.Second,
		ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second,
		BodyLimit: 1024, MaxReplicas: 1, MaxApplications: 1, MaxProjects: 2,
		MaxLogTail: 1, MaxLogPayloadBytes: 1024, MaxSSEConnections: 1,
		MaxSSEConnectionsPerApp: 1, RequestConcurrency: 1,
	}
}

func TestValidateRejectsInvalidConfigurationBoundaries(t *testing.T) {
	missingDatabase := filepath.Join(t.TempDir(), "missing", "moduleos.db")
	tests := []struct {
		name        string
		mutate      func(*config.Config)
		wantMessage string
	}{
		{name: "environment", mutate: func(c *config.Config) { c.Env = "staging" }, wantMessage: "ENV"},
		{name: "log level", mutate: func(c *config.Config) { c.LogLevel = "trace" }, wantMessage: "LOG_LEVEL"},
		{name: "listen address", mutate: func(c *config.Config) { c.ListenAddress = "all" }, wantMessage: "LISTEN_ADDRESS"},
		{name: "port syntax", mutate: func(c *config.Config) { c.Port = "http" }, wantMessage: "PORT"},
		{name: "port upper bound", mutate: func(c *config.Config) { c.Port = "65536" }, wantMessage: "PORT"},
		{name: "Docker endpoint", mutate: func(c *config.Config) { c.DockerEndpoint = "relative.sock" }, wantMessage: "DOCKER_ENDPOINT"},
		{name: "relative Unix endpoint", mutate: func(c *config.Config) { c.DockerEndpoint = "unix://relative.sock" }, wantMessage: "DOCKER_ENDPOINT"},
		{name: "noncanonical Unix endpoint", mutate: func(c *config.Config) { c.DockerEndpoint = "unix:///var/run/../run/docker.sock" }, wantMessage: "DOCKER_ENDPOINT"},
		{name: "noncanonical absolute endpoint", mutate: func(c *config.Config) { c.DockerEndpoint = "/var/run/../run/docker.sock" }, wantMessage: "DOCKER_ENDPOINT"},
		{name: "base domain", mutate: func(c *config.Config) { c.BaseDomain = "localhost" }, wantMessage: "BASE_DOMAIN"},
		{name: "ingress network", mutate: func(c *config.Config) { c.IngressNetwork = "Invalid Network" }, wantMessage: "INGRESS_NETWORK"},
		{name: "production API key", mutate: func(c *config.Config) { c.Env = "production" }, wantMessage: "API_KEY"},
		{name: "empty database path", mutate: func(c *config.Config) { c.DatabasePath = "" }, wantMessage: "DATABASE_PATH"},
		{name: "missing database parent", mutate: func(c *config.Config) { c.DatabasePath = missingDatabase }, wantMessage: "DATABASE_PATH"},
		{name: "relative mount root", mutate: func(c *config.Config) { c.AllowedMountRoots = []string{"data"} }, wantMessage: "ALLOWED_MOUNT_ROOTS"},
		{name: "root mount", mutate: func(c *config.Config) { c.AllowedMountRoots = []string{"/"} }, wantMessage: "ALLOWED_MOUNT_ROOTS"},
		{name: "noncanonical mount root", mutate: func(c *config.Config) { c.AllowedMountRoots = []string{"/srv/data/../data"} }, wantMessage: "ALLOWED_MOUNT_ROOTS"},
		{name: "overlapping mount roots", mutate: func(c *config.Config) { c.AllowedMountRoots = []string{"/srv/data", "/srv/data/app"} }, wantMessage: "ALLOWED_MOUNT_ROOTS"},
		{name: "reconcile duration", mutate: func(c *config.Config) { c.ReconcileInterval = 0 }, wantMessage: "durations"},
		{name: "backoff duration", mutate: func(c *config.Config) { c.ReconcileMaxBackoff = 0 }, wantMessage: "durations"},
		{name: "deployment duration", mutate: func(c *config.Config) { c.DeploymentTimeout = 0 }, wantMessage: "durations"},
		{name: "stabilization duration", mutate: func(c *config.Config) { c.StabilizationWindow = -time.Second }, wantMessage: "durations"},
		{name: "shutdown duration", mutate: func(c *config.Config) { c.ShutdownTimeout = 0 }, wantMessage: "durations"},
		{name: "HTTP timeout", mutate: func(c *config.Config) { c.ReadTimeout = 0 }, wantMessage: "HTTP timeouts"},
		{name: "workers lower bound", mutate: func(c *config.Config) { c.ReconcileWorkers = 0 }, wantMessage: "worker or replica"},
		{name: "workers upper bound", mutate: func(c *config.Config) { c.ReconcileWorkers = 33 }, wantMessage: "worker or replica"},
		{name: "replica upper bound", mutate: func(c *config.Config) { c.MaxReplicas = 1001 }, wantMessage: "worker or replica"},
		{name: "application lower bound", mutate: func(c *config.Config) { c.MaxApplications = 0 }, wantMessage: "worker or replica"},
		{name: "project lower bound", mutate: func(c *config.Config) { c.MaxProjects = 1 }, wantMessage: "worker or replica"},
		{name: "body lower bound", mutate: func(c *config.Config) { c.BodyLimit = 1023 }, wantMessage: "HTTP or log limit"},
		{name: "body upper bound", mutate: func(c *config.Config) { c.BodyLimit = 10*1024*1024 + 1 }, wantMessage: "HTTP or log limit"},
		{name: "log tail lower bound", mutate: func(c *config.Config) { c.MaxLogTail = 0 }, wantMessage: "HTTP or log limit"},
		{name: "log payload lower bound", mutate: func(c *config.Config) { c.MaxLogPayloadBytes = 1023 }, wantMessage: "HTTP or log limit"},
		{name: "SSE global lower bound", mutate: func(c *config.Config) { c.MaxSSEConnections = 0 }, wantMessage: "HTTP or log limit"},
		{name: "SSE app lower bound", mutate: func(c *config.Config) { c.MaxSSEConnectionsPerApp = 0 }, wantMessage: "HTTP or log limit"},
		{name: "request concurrency lower bound", mutate: func(c *config.Config) { c.RequestConcurrency = 0 }, wantMessage: "HTTP or log limit"},
		{name: "SSE app exceeds global", mutate: func(c *config.Config) { c.MaxSSEConnectionsPerApp = 2 }, wantMessage: "per-app SSE"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.mutate(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("Validate() error = %v, want message containing %q", err, test.wantMessage)
			}
		})
	}
}

func TestValidateAcceptsSupportedEndpointAndListenForms(t *testing.T) {
	for _, endpoint := range []string{"unix:///var/run/docker.sock", "tcp://127.0.0.1:2375", "/var/run/docker.sock"} {
		cfg := validConfig()
		cfg.ListenAddress = "localhost"
		cfg.DockerEndpoint = endpoint
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() rejected endpoint %q: %v", endpoint, err)
		}
	}
}

func TestLoadSupportsLegacyDockerSocketOverride(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("DOCKER_SOCKET", "/run/custom-docker.sock")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DockerEndpoint != "/run/custom-docker.sock" {
		t.Fatalf("DockerEndpoint = %q", cfg.DockerEndpoint)
	}
}

func TestLoadRejectsMalformedEnvironmentValue(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("RECONCILE_INTERVAL", "eventually")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "parse configuration") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestSafeSummaryRedactsSecrets(t *testing.T) {
	cfg := validConfig()
	cfg.APIKey = "super-secret-api-key"
	want := map[string]any{
		"environment": "development", "listen_address": "127.0.0.1", "port": "3000",
		"docker_endpoint": "unix:///var/run/docker.sock", "base_domain": "moduleos.local",
		"database_path": ":memory:", "api_key": "[redacted]",
		"ingress_network": "moduleos-ingress", "reconcile_workers": 1,
	}
	summary := cfg.SafeSummary()
	if !reflect.DeepEqual(summary, want) {
		t.Fatalf("SafeSummary() = %#v, want %#v", summary, want)
	}
	if strings.Contains(fmt.Sprint(summary), cfg.APIKey) {
		t.Fatal("SafeSummary exposed API key")
	}
}
