package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/handler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
)

var configEnvironmentKeys = []string{
	"ENV", "LOG_LEVEL", "LISTEN_ADDRESS", "PORT", "DOCKER_ENDPOINT", "DOCKER_SOCKET",
	"BASE_DOMAIN", "DATABASE_PATH", "API_KEY", "API_KEY_FILE", "INGRESS_NETWORK", "CORS_ALLOWED_ORIGINS",
	"ALLOWED_MOUNT_ROOTS", "RECONCILE_INTERVAL", "RECONCILE_MAX_BACKOFF", "RECONCILE_WORKERS",
	"DEPLOYMENT_TIMEOUT", "STABILIZATION_WINDOW", "SHUTDOWN_TIMEOUT", "READINESS_TIMEOUT", "HTTP_READ_TIMEOUT",
	"HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "HTTP_BODY_LIMIT", "MAX_REPLICAS", "MAX_APPLICATIONS",
	"MAX_PROJECTS", "MAX_LOG_TAIL", "MAX_LOG_PAYLOAD_BYTES", "MAX_SSE_CONNECTIONS",
	"MAX_SSE_CONNECTIONS_PER_APP", "LOG_TAIL_TIMEOUT", "SSE_OPEN_TIMEOUT",
	"SSE_HEARTBEAT_INTERVAL", "SSE_IDLE_TIMEOUT", "SSE_MAX_DURATION", "SSE_INITIAL_TAIL",
	"REQUEST_CONCURRENCY",
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

func setAPIKeyFile(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("write API key file: %v", err)
	}
	t.Setenv("API_KEY_FILE", path)
	return path
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
	if cfg.ReadinessTimeout != 2*time.Second {
		t.Errorf("ReadinessTimeout: got %s, want 2s", cfg.ReadinessTimeout)
	}
	if cfg.APIKey != "" {
		t.Errorf("APIKey: got %q, want empty string", cfg.APIKey)
	}
	if cfg.LogTailTimeout != 10*time.Second || cfg.SSEOpenTimeout != 10*time.Second || cfg.SSEHeartbeatInterval != 15*time.Second || cfg.SSEIdleTimeout != 5*time.Minute || cfg.SSEMaxDuration != 30*time.Minute || cfg.SSEInitialTail != 50 {
		t.Errorf("log defaults are invalid: %#v", cfg)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("ENV", "production")
	t.Setenv("PORT", "8080")
	t.Setenv("BASE_DOMAIN", "mycompany.com")
	t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "moduleos.db"))
	apiKey := strings.Repeat("test-key-segment-", 2)
	setAPIKeyFile(t, apiKey)

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

func TestLoadRejectsEnvironmentAPIKeyInProduction(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("ENV", "production")
	secret := "environment-secret-must-not-be-exposed"
	t.Setenv("API_KEY", secret)
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "API_KEY") || strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() error = %v, want safe production API_KEY source rejection", err)
	}
}

func TestLoadAPIKeySourcePolicy(t *testing.T) {
	validKey := strings.Repeat("k", 32)
	tests := []struct {
		name        string
		environment string
		configure   func(*testing.T)
		wantKey     string
		wantError   string
	}{
		{name: "development without a source", environment: "development"},
		{name: "development environment source", environment: "development", configure: func(t *testing.T) { t.Setenv("API_KEY", "development-key") }, wantKey: "development-key"},
		{name: "development explicitly empty environment source", environment: "development", configure: func(t *testing.T) { t.Setenv("API_KEY", "") }},
		{name: "development file source", environment: "development", configure: func(t *testing.T) { setAPIKeyFile(t, validKey) }, wantKey: validKey},
		{name: "development explicitly empty file source", environment: "development", configure: func(t *testing.T) { t.Setenv("API_KEY_FILE", "") }, wantError: "API_KEY_FILE"},
		{name: "development conflicting sources", environment: "development", configure: func(t *testing.T) { t.Setenv("API_KEY", "development-key"); setAPIKeyFile(t, validKey) }, wantError: "API_KEY and API_KEY_FILE"},
		{name: "development conflicting empty sources", environment: "development", configure: func(t *testing.T) { t.Setenv("API_KEY", ""); t.Setenv("API_KEY_FILE", "") }, wantError: "API_KEY and API_KEY_FILE"},
		{name: "production without a source", environment: "production", wantError: "API_KEY_FILE"},
		{name: "production environment source", environment: "production", configure: func(t *testing.T) { t.Setenv("API_KEY", validKey) }, wantError: "API_KEY"},
		{name: "production explicitly empty environment source", environment: "production", configure: func(t *testing.T) { t.Setenv("API_KEY", "") }, wantError: "API_KEY"},
		{name: "production file source", environment: "production", configure: func(t *testing.T) { setAPIKeyFile(t, validKey) }, wantKey: validKey},
		{name: "production explicitly empty file source", environment: "production", configure: func(t *testing.T) { t.Setenv("API_KEY_FILE", "") }, wantError: "API_KEY_FILE"},
		{name: "production conflicting sources", environment: "production", configure: func(t *testing.T) { t.Setenv("API_KEY", validKey); setAPIKeyFile(t, validKey) }, wantError: "API_KEY and API_KEY_FILE"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv("ENV", test.environment)
			if test.environment == "production" {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "moduleos.db"))
			}
			if test.configure != nil {
				test.configure(t)
			}

			cfg, err := config.Load()
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Load() error = %v, want safe error containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.APIKey != test.wantKey {
				t.Fatalf("APIKey = %q, want %q", cfg.APIKey, test.wantKey)
			}
		})
	}
}

func TestLoadReadsAPIKeyFileOnlyDuringStartup(t *testing.T) {
	clearConfigEnvironment(t)
	initial := strings.Repeat("a", 32)
	path := setAPIKeyFile(t, initial)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("b", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != initial {
		t.Fatalf("APIKey changed after source mutation: %q", cfg.APIKey)
	}
}

func TestValidateRejectsUnsafeMountRoot(t *testing.T) {
	cfg := validConfig()
	cfg.AllowedMountRoots = []string{"/"}
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
		ReadinessTimeout: time.Second,
		ReadTimeout:      time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second,
		BodyLimit: 1024, MaxReplicas: 1, MaxApplications: 1, MaxProjects: 2,
		MaxLogTail: 100, MaxLogPayloadBytes: 1024, MaxSSEConnections: 1,
		MaxSSEConnectionsPerApp: 1, RequestConcurrency: 1,
		LogTailTimeout: 10 * time.Second, SSEOpenTimeout: 10 * time.Second,
		SSEHeartbeatInterval: 15 * time.Second, SSEIdleTimeout: 5 * time.Minute,
		SSEMaxDuration: 30 * time.Minute, SSEInitialTail: 50,
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
		{name: "API key whitespace", mutate: func(c *config.Config) { c.APIKey = "invalid key" }, wantMessage: "API_KEY"},
		{name: "API key NUL", mutate: func(c *config.Config) { c.APIKey = "invalid\x00key" }, wantMessage: "API_KEY"},
		{name: "API key vertical tab", mutate: func(c *config.Config) { c.APIKey = "invalid\vkey" }, wantMessage: "API_KEY"},
		{name: "API key Unicode whitespace", mutate: func(c *config.Config) { c.APIKey = "invalid\u00a0key" }, wantMessage: "API_KEY"},
		{name: "empty database path", mutate: func(c *config.Config) { c.DatabasePath = "" }, wantMessage: "DATABASE_PATH"},
		{name: "production memory database", mutate: func(c *config.Config) { c.Env = "production"; c.APIKey = strings.Repeat("a", 32) }, wantMessage: "DATABASE_PATH"},
		{name: "missing database parent", mutate: func(c *config.Config) { c.DatabasePath = missingDatabase }, wantMessage: "DATABASE_PATH"},
		{name: "relative mount root", mutate: func(c *config.Config) { c.AllowedMountRoots = []string{"data"} }, wantMessage: "ALLOWED_MOUNT_ROOTS"},
		{name: "root mount", mutate: func(c *config.Config) { c.AllowedMountRoots = []string{"/"} }, wantMessage: "ALLOWED_MOUNT_ROOTS"},
		{name: "noncanonical mount root", mutate: func(c *config.Config) { c.AllowedMountRoots = []string{"/srv/data/../data"} }, wantMessage: "ALLOWED_MOUNT_ROOTS"},
		{name: "overlapping mount roots", mutate: func(c *config.Config) { c.AllowedMountRoots = []string{"/srv/data", "/srv/data/app"} }, wantMessage: "ALLOWED_MOUNT_ROOTS"},
		{name: "reconcile duration", mutate: func(c *config.Config) { c.ReconcileInterval = 0 }, wantMessage: "durations"},
		{name: "backoff duration", mutate: func(c *config.Config) { c.ReconcileMaxBackoff = time.Second - time.Nanosecond }, wantMessage: "durations"},
		{name: "deployment duration", mutate: func(c *config.Config) { c.DeploymentTimeout = 0 }, wantMessage: "durations"},
		{name: "stabilization duration", mutate: func(c *config.Config) { c.StabilizationWindow = -time.Second }, wantMessage: "durations"},
		{name: "shutdown duration", mutate: func(c *config.Config) { c.ShutdownTimeout = 0 }, wantMessage: "durations"},
		{name: "readiness duration", mutate: func(c *config.Config) { c.ReadinessTimeout = 0 }, wantMessage: "READINESS_TIMEOUT"},
		{name: "readiness duration upper bound", mutate: func(c *config.Config) { c.ReadinessTimeout = handler.MaximumReadinessTimeout + time.Nanosecond }, wantMessage: "READINESS_TIMEOUT"},
		{name: "HTTP timeout", mutate: func(c *config.Config) { c.ReadTimeout = 0 }, wantMessage: "HTTP timeouts"},
		{name: "workers lower bound", mutate: func(c *config.Config) { c.ReconcileWorkers = 0 }, wantMessage: "worker or replica"},
		{name: "workers upper bound", mutate: func(c *config.Config) { c.ReconcileWorkers = 33 }, wantMessage: "worker or replica"},
		{name: "replica upper bound", mutate: func(c *config.Config) { c.MaxReplicas = 1001 }, wantMessage: "worker or replica"},
		{name: "application lower bound", mutate: func(c *config.Config) { c.MaxApplications = 0 }, wantMessage: "worker or replica"},
		{name: "project lower bound", mutate: func(c *config.Config) { c.MaxProjects = 1 }, wantMessage: "worker or replica"},
		{name: "body lower bound", mutate: func(c *config.Config) { c.BodyLimit = 1023 }, wantMessage: "HTTP or log limit"},
		{name: "body upper bound", mutate: func(c *config.Config) { c.BodyLimit = 10*1024*1024 + 1 }, wantMessage: "HTTP or log limit"},
		{name: "log tail lower bound", mutate: func(c *config.Config) { c.MaxLogTail = 99 }, wantMessage: "HTTP or log limit"},
		{name: "log tail upper bound", mutate: func(c *config.Config) { c.MaxLogTail = 10001 }, wantMessage: "HTTP or log limit"},
		{name: "log payload lower bound", mutate: func(c *config.Config) { c.MaxLogPayloadBytes = 1023 }, wantMessage: "HTTP or log limit"},
		{name: "log payload upper bound", mutate: func(c *config.Config) { c.MaxLogPayloadBytes = 16*1024*1024 + 1 }, wantMessage: "HTTP or log limit"},
		{name: "SSE global lower bound", mutate: func(c *config.Config) { c.MaxSSEConnections = 0 }, wantMessage: "HTTP or log limit"},
		{name: "SSE global upper bound", mutate: func(c *config.Config) { c.MaxSSEConnections = 1025 }, wantMessage: "HTTP or log limit"},
		{name: "SSE app lower bound", mutate: func(c *config.Config) { c.MaxSSEConnectionsPerApp = 0 }, wantMessage: "HTTP or log limit"},
		{name: "request concurrency lower bound", mutate: func(c *config.Config) { c.RequestConcurrency = 0 }, wantMessage: "HTTP or log limit"},
		{name: "request concurrency upper bound", mutate: func(c *config.Config) { c.RequestConcurrency = 65537 }, wantMessage: "HTTP or log limit"},
		{name: "SSE app exceeds global", mutate: func(c *config.Config) { c.MaxSSEConnectionsPerApp = 2 }, wantMessage: "per-app SSE"},
		{name: "initial tail lower bound", mutate: func(c *config.Config) { c.SSEInitialTail = 0 }, wantMessage: "SSE_INITIAL_TAIL"},
		{name: "initial tail upper bound", mutate: func(c *config.Config) { c.SSEInitialTail = 101 }, wantMessage: "SSE_INITIAL_TAIL"},
		{name: "tail timeout lower bound", mutate: func(c *config.Config) { c.LogTailTimeout = 0 }, wantMessage: "log tail and SSE open"},
		{name: "tail timeout upper bound", mutate: func(c *config.Config) { c.LogTailTimeout = time.Minute + time.Nanosecond }, wantMessage: "log tail and SSE open"},
		{name: "open timeout lower bound", mutate: func(c *config.Config) { c.SSEOpenTimeout = 0 }, wantMessage: "log tail and SSE open"},
		{name: "heartbeat lower bound", mutate: func(c *config.Config) { c.SSEHeartbeatInterval = 0 }, wantMessage: "SSE durations"},
		{name: "heartbeat reaches idle", mutate: func(c *config.Config) { c.SSEHeartbeatInterval = c.SSEIdleTimeout }, wantMessage: "SSE durations"},
		{name: "idle reaches maximum", mutate: func(c *config.Config) { c.SSEIdleTimeout = c.SSEMaxDuration }, wantMessage: "SSE durations"},
		{name: "maximum lower bound", mutate: func(c *config.Config) { c.SSEMaxDuration = 0 }, wantMessage: "SSE durations"},
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
	for _, endpoint := range []string{"unix:///var/run/docker.sock", "/var/run/docker.sock"} {
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
	secretValue := "eventually-super-secret"
	t.Setenv("RECONCILE_INTERVAL", secretValue)
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "parse configuration") || !strings.Contains(err.Error(), "RECONCILE_INTERVAL") || strings.Contains(err.Error(), secretValue) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestSafeSummaryRedactsSecrets(t *testing.T) {
	cfg := validConfig()
	cfg.APIKey = "super-secret-api-key"
	want := map[string]any{
		"environment": "development", "listen_address": "127.0.0.1", "port": "3000",
		"docker_endpoint": "unix", "base_domain": "moduleos.local",
		"database_path": "[memory]", "api_key": "[redacted]",
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

func TestLoadEnforcesProductionRequirements(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(*testing.T)
		wantMessage string
	}{
		{
			name: "base domain must be explicit",
			configure: func(t *testing.T) {
				t.Setenv("DATABASE_PATH", filepath.Join(t.TempDir(), "moduleos.db"))
			},
			wantMessage: "BASE_DOMAIN",
		},
		{
			name: "database path must be explicit",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "memory database is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", ":memory:")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "memory URI database is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file::memory:?cache=shared")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "queried bare memory database is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", ":memory:?_pragma=busy_timeout(1)")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "percent-encoded memory URI database is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:%3Amemory%3A")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "NUL-truncated memory URI database is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:%3Amemory%3A%00ignored")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "empty file URI database is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "named memory URI database is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:moduleos?cache=shared&mode=memory")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "memdb VFS database is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:moduleos?vfs=memdb")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "encoded case-insensitive memdb VFS is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:moduleos?VFS=%6demdb")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "locking override is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:moduleos.db?nolock=1")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "encoded locking override is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:moduleos.db?%6eolock=1")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "custom VFS selection is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:moduleos.db?vfs=unix-dotfile")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "read-only mode is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:moduleos.db?mode=ro")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "immutable mode is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:moduleos.db?immutable=1")
			},
			wantMessage: "DATABASE_PATH",
		},
		{
			name: "query-only pragma is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "moduleos.db?_pragma=query_only(1)")
			},
			wantMessage: "DATABASE_PATH",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv("ENV", "production")
			setAPIKeyFile(t, strings.Repeat("k", 32))
			test.configure(t)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("Load() error = %v, want %s", err, test.wantMessage)
			}
		})
	}
}

func TestValidateProductionSQLiteDatabaseBoundaries(t *testing.T) {
	tests := []struct {
		name         string
		databasePath string
		wantError    bool
	}{
		{name: "ordinary filesystem path", databasePath: filepath.Join(t.TempDir(), "moduleos.db")},
		{name: "literal NUL", databasePath: "moduleos.db\x00ignored", wantError: true},
		{name: "malformed file URI", databasePath: "file://%gh", wantError: true},
		{name: "persistent file URI", databasePath: "file:moduleos.db", wantError: true},
		{name: "encoded NUL in query", databasePath: "file:moduleos.db?cache=%00", wantError: true},
		{name: "encoded NUL in query key", databasePath: "file:moduleos.db?%00=value", wantError: true},
		{name: "encoded NUL in fragment", databasePath: "file:moduleos.db#%00", wantError: true},
		{name: "malformed file URI query", databasePath: "file:moduleos.db?cache=%zz", wantError: true},
		{name: "locking override even when disabled", databasePath: "file:moduleos.db?nolock=0", wantError: true},
		{name: "case-insensitive VFS selection", databasePath: "file:moduleos.db?VFS=unix-dotfile", wantError: true},
		{name: "read-only URI mode", databasePath: "file:moduleos.db?mode=ro", wantError: true},
		{name: "immutable URI", databasePath: "file:moduleos.db?immutable=1", wantError: true},
		{name: "query-only pragma", databasePath: "moduleos.db?_pragma=query_only(1)", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Env = "production"
			cfg.APIKey = strings.Repeat("k", 32)
			cfg.DatabasePath = test.databasePath

			err := cfg.Validate()
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "DATABASE_PATH") {
					t.Fatalf("Validate() error = %v, want DATABASE_PATH error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestLoadAcceptsExplicitProductionValuesEqualToDevelopmentDefaults(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("ENV", "production")
	t.Setenv("BASE_DOMAIN", "moduleos.local")
	t.Setenv("DATABASE_PATH", "./moduleos.db")
	setAPIKeyFile(t, strings.Repeat("k", 32))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseDomain != "moduleos.local" || cfg.DatabasePath != "./moduleos.db" {
		t.Fatalf("production defaults were not preserved: %#v", cfg)
	}
}

func TestLoadExplicitEmptyValuePolicy(t *testing.T) {
	for _, key := range configEnvironmentKeys {
		if key == "API_KEY" || key == "CORS_ALLOWED_ORIGINS" || key == "DOCKER_SOCKET" {
			continue
		}
		t.Run(key, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv(key, "")
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("Load() error = %v, want explicit %s rejection", err, key)
			}
		})
	}

	t.Run("empty optional values", func(t *testing.T) {
		clearConfigEnvironment(t)
		t.Setenv("API_KEY", "")
		t.Setenv("CORS_ALLOWED_ORIGINS", "")
		t.Setenv("DOCKER_SOCKET", "")
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.APIKey != "" || cfg.CORSAllowedOrigins != nil || cfg.DockerEndpoint != "unix:///var/run/docker.sock" {
			t.Fatalf("empty optional values resolved incorrectly: %#v", cfg)
		}
	})
}

func TestLoadDockerEndpointAliasPolicy(t *testing.T) {
	tests := []struct {
		name      string
		primary   string
		legacy    string
		want      string
		wantErr   bool
		forbidden []string
	}{
		{name: "primary only", primary: "unix:///run/primary.sock", want: "unix:///run/primary.sock"},
		{name: "legacy only", legacy: "/run/custom-docker.sock", want: "/run/custom-docker.sock"},
		{name: "identical aliases", primary: "unix:///run/docker.sock", legacy: "unix:///run/docker.sock", want: "unix:///run/docker.sock"},
		{name: "equivalent Unix aliases", primary: "unix:///run/docker.sock", legacy: "/run/docker.sock", want: "unix:///run/docker.sock"},
		{name: "conflicting Unix aliases", primary: "unix:///run/primary.sock", legacy: "/run/legacy.sock", wantErr: true, forbidden: []string{"/run/primary.sock", "/run/legacy.sock"}},
		{name: "unsupported primary transport", primary: "tcp://docker.internal:2375", legacy: "/run/docker.sock", wantErr: true, forbidden: []string{"docker.internal", "/run/docker.sock"}},
		{name: "invalid primary is not hidden", primary: "relative.sock", legacy: "/run/docker.sock", wantErr: true, forbidden: []string{"relative.sock", "/run/docker.sock"}},
		{name: "invalid legacy is not ignored", primary: "unix:///run/docker.sock", legacy: "relative.sock", wantErr: true, forbidden: []string{"relative.sock", "/run/docker.sock"}},
		{name: "whitespace legacy is not ignored", legacy: " \t ", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			if test.primary != "" {
				t.Setenv("DOCKER_ENDPOINT", test.primary)
			}
			if test.legacy != "" {
				t.Setenv("DOCKER_SOCKET", test.legacy)
			}
			cfg, err := config.Load()
			if test.wantErr {
				if err == nil {
					t.Fatal("expected endpoint configuration to fail")
				}
				for _, secret := range test.forbidden {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("error exposed endpoint material %q: %v", secret, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.DockerEndpoint != test.want {
				t.Fatalf("DockerEndpoint = %q, want %q", cfg.DockerEndpoint, test.want)
			}
		})
	}
}

func TestDockerSocketPathMatchesLoadEndpointPolicy(t *testing.T) {
	type variable struct {
		value string
		set   bool
	}
	tests := []struct {
		name      string
		primary   variable
		legacy    variable
		want      string
		wantErr   bool
		forbidden []string
	}{
		{name: "default", want: "/var/run/docker.sock"},
		{name: "primary only", primary: variable{"unix:///run/primary.sock", true}, want: "/run/primary.sock"},
		{name: "primary absolute path", primary: variable{"/run/primary.sock", true}, want: "/run/primary.sock"},
		{name: "legacy only", legacy: variable{"/run/custom-docker.sock", true}, want: "/run/custom-docker.sock"},
		{name: "explicitly empty legacy is ignored", legacy: variable{"", true}, want: "/var/run/docker.sock"},
		{name: "equivalent Unix aliases", primary: variable{"unix:///run/docker.sock", true}, legacy: variable{"/run/docker.sock", true}, want: "/run/docker.sock"},
		{name: "conflicting Unix aliases", primary: variable{"unix:///run/primary.sock", true}, legacy: variable{"/run/legacy.sock", true}, wantErr: true, forbidden: []string{"/run/primary.sock", "/run/legacy.sock"}},
		{name: "unsupported primary transport", primary: variable{"tcp://docker.internal:2375", true}, wantErr: true, forbidden: []string{"docker.internal"}},
		{name: "invalid legacy is not ignored", primary: variable{"unix:///run/docker.sock", true}, legacy: variable{"relative.sock", true}, wantErr: true, forbidden: []string{"relative.sock"}},
		{name: "whitespace legacy is not ignored", legacy: variable{" \t ", true}, wantErr: true},
		{name: "explicitly empty primary", primary: variable{"", true}, wantErr: true},
		{name: "whitespace primary", primary: variable{" ", true}, wantErr: true},
		{name: "query secret", primary: variable{"unix:///run/docker.sock?token=super-secret", true}, wantErr: true, forbidden: []string{"super-secret"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				switch key {
				case "DOCKER_ENDPOINT":
					return test.primary.value, test.primary.set
				case "DOCKER_SOCKET":
					return test.legacy.value, test.legacy.set
				default:
					t.Fatalf("DockerSocketPath read unrelated variable %q", key)
					return "", false
				}
			}
			path, err := config.DockerSocketPath(lookup)

			clearConfigEnvironment(t)
			if test.primary.set {
				t.Setenv("DOCKER_ENDPOINT", test.primary.value)
			}
			if test.legacy.set {
				t.Setenv("DOCKER_SOCKET", test.legacy.value)
			}
			cfg, loadErr := config.Load()

			if test.wantErr {
				if err == nil {
					t.Fatalf("DockerSocketPath() = %q, want error", path)
				}
				if loadErr == nil {
					t.Fatal("Load accepted an endpoint DockerSocketPath rejected")
				}
				for _, secret := range test.forbidden {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("error exposed endpoint material %q: %v", secret, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("DockerSocketPath() error = %v", err)
			}
			if loadErr != nil {
				t.Fatalf("Load() error = %v", loadErr)
			}
			if path != test.want {
				t.Fatalf("DockerSocketPath() = %q, want %q", path, test.want)
			}
			if loaded := strings.TrimPrefix(cfg.DockerEndpoint, "unix://"); loaded != path {
				t.Fatalf("Load resolved socket %q, DockerSocketPath resolved %q", loaded, path)
			}
		})
	}
}

func TestValidateRejectsUnsafeDockerEndpointFormsWithoutEchoingValues(t *testing.T) {
	tests := []string{
		"relative.sock",
		"/var/run/../run/docker.sock",
		"/var/run/docker.sock?token=secret",
		"unix://relative.sock",
		"unix:/run/docker.sock",
		"UNIX:///run/docker.sock",
		"unix://host/run/docker.sock",
		"unix:///",
		"unix:///run/docker.sock?",
		"unix:///run/docker.sock#",
		"unix:///run/docker\x00.sock",
		"tcp://",
		"tcp://docker.internal",
		"tcp://docker.internal:0",
		"tcp://docker.internal:65536",
		"tcp://docker.internal:not-a-port",
		"tcp://*:2375",
		"tcp://bad_host:2375",
		"tcp://-docker.internal:2375",
		"tcp://docker..internal:2375",
		"tcp://docker. internal:2375",
		"tcp://docker.\u00a0internal:2375",
		"TCP://docker.internal:2375",
		"tcp://user:super-secret@docker.internal:2375",
		"tcp://docker.internal:2375/path",
		"tcp://docker.internal:2375?",
		"tcp://docker.internal:2375?token=super-secret",
		"tcp://docker.internal:2375#",
		"tcp://docker.internal:2375#fragment",
		"tcp://[fe80::1%25en0]:2375",
		"tcp://docker.internal:2375 ",
		"unix:///srv/moduleos-data/docker%20engine.sock",
		"/srv/moduleos-data/docker%20engine.sock",
		"ssh://docker.internal:22",
	}
	for index, endpoint := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			cfg := validConfig()
			cfg.DockerEndpoint = endpoint
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() accepted endpoint %q", endpoint)
			}
			if strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("error exposed endpoint material: %v", err)
			}
		})
	}
}

func TestReadinessAndCapacityBoundaries(t *testing.T) {
	cfg := validConfig()
	cfg.ReadinessTimeout = handler.MaximumReadinessTimeout
	cfg.RequestConcurrency = 65536
	cfg.ReconcileMaxBackoff = time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected supported upper/lower bounds: %v", err)
	}
}

func TestNormalizeCORSOrigins(t *testing.T) {
	input := []string{
		" HTTPS://EXAMPLE.COM:8443 ",
		"https://example.com:8443",
		"https://EXAMPLE.COM:443",
		"https://example.com",
		"http://EXAMPLE.COM:80",
		"http://example.com",
		"https://[2001:DB8::1]:443",
	}
	original := append([]string(nil), input...)
	got, err := config.NormalizeCORSOrigins(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://example.com", "https://[2001:db8::1]", "https://example.com", "https://example.com:8443"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeCORSOrigins() = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("NormalizeCORSOrigins mutated input: %v", input)
	}
	if got, err := config.NormalizeCORSOrigins(nil); err != nil || got != nil {
		t.Fatalf("nil origins = %v/%v", got, err)
	}
}

func TestNormalizeCORSOriginsRejectsUnsafeFormsWithoutEchoingValues(t *testing.T) {
	tests := []string{
		"*",
		"https://user:super-secret@example.com",
		"https://example.com/path",
		"https://example.com?token=super-secret",
		"https://example.com#fragment",
		"https://example.com:",
		"https://example.com:0",
		"https://example.com:65536",
		"file://example.com",
		"https://[fe80::1%25en0]",
		"https://example.com\nforged",
		"https://example.com\x00forged",
	}
	for index, origin := range tests {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			_, err := config.NormalizeCORSOrigins([]string{origin})
			if err == nil {
				t.Fatalf("NormalizeCORSOrigins accepted %q", origin)
			}
			if strings.Contains(err.Error(), origin) || strings.Contains(err.Error(), "super-secret") || !strings.Contains(err.Error(), "index 0") {
				t.Fatalf("unsafe error = %v", err)
			}
		})
	}
}

func TestLoadNormalizesCORSOrigins(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://EXAMPLE.COM:443,http://localhost:3000,https://example.com")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://localhost:3000", "https://example.com"}
	if !reflect.DeepEqual(cfg.CORSAllowedOrigins, want) {
		t.Fatalf("CORSAllowedOrigins = %v, want %v", cfg.CORSAllowedOrigins, want)
	}
}

func TestLoadRejectsControlOnlyCORSOrigin(t *testing.T) {
	for _, origin := range []string{"\n", "\t", "\r"} {
		t.Run(fmt.Sprintf("%q", origin), func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv("CORS_ALLOWED_ORIGINS", origin)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") || strings.Contains(err.Error(), origin) {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestSafeSummaryClassifiesSensitivePathsAndEndpoints(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		database  string
		transport string
		dbSummary string
		forbidden []string
	}{
		{name: "Unix", endpoint: "unix:///private/run/docker.sock", database: "/private/data/moduleos.db", transport: "unix", dbSummary: "[configured]", forbidden: []string{"/private/run", "/private/data"}},
		{name: "unsupported TCP", endpoint: "tcp://private.example:2375", database: ":memory:", transport: "[invalid]", dbSummary: "[memory]", forbidden: []string{"private.example", "2375"}},
		{name: "memory VFS", endpoint: "unix:///private/run/docker.sock", database: "file:volatile-state?vfs=memdb", transport: "unix", dbSummary: "[memory]", forbidden: []string{"/private/run", "volatile-state", "memdb"}},
		{name: "encoded memory URI", endpoint: "unix:///private/run/docker.sock", database: "file:%3Amemory%3A", transport: "unix", dbSummary: "[memory]", forbidden: []string{"/private/run", "%3Amemory%3A"}},
		{name: "invalid credential endpoint", endpoint: "tcp://user:super-secret@private.example:2375", database: "/private/data/moduleos.db", transport: "[invalid]", dbSummary: "[configured]", forbidden: []string{"user", "super-secret", "private.example", "/private/data"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.DockerEndpoint = test.endpoint
			cfg.DatabasePath = test.database
			cfg.APIKey = "api-super-secret"
			before := *cfg
			summary := cfg.SafeSummary()
			if summary["docker_endpoint"] != test.transport || summary["database_path"] != test.dbSummary || summary["api_key"] != "[redacted]" {
				t.Fatalf("SafeSummary() = %#v", summary)
			}
			serialized := fmt.Sprint(summary)
			for _, forbidden := range append(test.forbidden, cfg.APIKey) {
				if strings.Contains(serialized, forbidden) {
					t.Fatalf("summary exposed %q: %s", forbidden, serialized)
				}
			}
			if !reflect.DeepEqual(*cfg, before) || !reflect.DeepEqual(summary, cfg.SafeSummary()) {
				t.Fatal("SafeSummary was not stable and pure")
			}
		})
	}
}

func TestDatabaseDirectory(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "", want: ""},
		{path: ":memory:", want: ""},
		{path: "file::memory:?cache=shared", want: ""},
		{path: "/var/lib/moduleos/moduleos.db", want: "/var/lib/moduleos"},
		{path: "/var/lib/moduleos/../moduleos/state.db", want: "/var/lib/moduleos"},
		{path: "data/moduleos.db", want: filepath.Join(workingDirectory, "data")},
		{path: "moduleos.db", want: workingDirectory},
	} {
		cfg := validConfig()
		cfg.DatabasePath = test.path
		if got := cfg.DatabaseDirectory(); got != test.want {
			t.Fatalf("DatabaseDirectory(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestValidateRejectsMountRootsOverlappingTheDatabaseDirectory(t *testing.T) {
	base := t.TempDir()
	database := filepath.Join(base, "state")
	if err := os.MkdirAll(filepath.Join(database, "apps"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "state-link")
	if err := os.Symlink(database, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	resolvedDatabase, err := filepath.EvalSymlinks(database)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name         string
		databasePath string
		root         string
	}{
		{name: "root is the database directory", databasePath: filepath.Join(database, "moduleos.db"), root: database},
		{name: "root contains the database directory", databasePath: filepath.Join(database, "moduleos.db"), root: base},
		{name: "root is inside the database directory", databasePath: filepath.Join(database, "moduleos.db"), root: filepath.Join(database, "apps")},
		{name: "root resolves to the database directory", databasePath: filepath.Join(database, "moduleos.db"), root: link},
		{name: "database path resolves into the root", databasePath: filepath.Join(link, "moduleos.db"), root: resolvedDatabase},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.DatabasePath = test.databasePath
			cfg.AllowedMountRoots = []string{"/srv/unrelated-root", test.root}
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "DATABASE_PATH directory") {
				t.Fatalf("Validate() error = %v, want the database-directory overlap error", err)
			}
			for _, path := range []string{base, database, link, test.root} {
				if strings.Contains(err.Error(), path) {
					t.Fatalf("error exposed a path: %v", err)
				}
			}
		})
	}
}

func TestValidateRejectsARelativeDatabasePathInsideAMountRoot(t *testing.T) {
	database := t.TempDir()
	t.Chdir(database)
	cfg := validConfig()
	cfg.DatabasePath = "moduleos.db"
	cfg.AllowedMountRoots = []string{database}

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "DATABASE_PATH directory") {
		t.Fatalf("Validate() error = %v, want the database-directory overlap error", err)
	}
}

func TestValidateAcceptsMountRootsSeparateFromTheDatabaseDirectory(t *testing.T) {
	base := t.TempDir()
	database := filepath.Join(base, "state")
	if err := os.Mkdir(database, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := validConfig()
	cfg.DatabasePath = filepath.Join(database, "moduleos.db")
	cfg.AllowedMountRoots = []string{filepath.Join(base, "apps"), filepath.Join(base, "state-archive")}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	memory := validConfig()
	memory.AllowedMountRoots = []string{base}
	if err := memory.Validate(); err != nil {
		t.Fatalf("in-memory database Validate() error = %v", err)
	}
}

func TestValidateFailsClosedWhenTheDatabaseDirectoryCannotBeResolved(t *testing.T) {
	removed := filepath.Join(t.TempDir(), "removed")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(removed)
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PWD", "")
	if _, err := os.Getwd(); err == nil {
		t.Skip("the platform still resolves a removed working directory")
	}
	cfg := validConfig()
	cfg.DatabasePath = "moduleos.db"

	err := cfg.Validate()

	if err == nil {
		t.Fatal("Validate() accepted a database directory that cannot be resolved")
	}
}

func TestLoadRejectsAMountRootOverlappingTheDatabaseDirectory(t *testing.T) {
	clearConfigEnvironment(t)
	database := t.TempDir()
	t.Setenv("DATABASE_PATH", filepath.Join(database, "moduleos.db"))
	t.Setenv("ALLOWED_MOUNT_ROOTS", database)

	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_PATH directory") {
		t.Fatalf("Load() error = %v, want the database-directory overlap error", err)
	}
	if _, err := os.Stat(filepath.Join(database, "moduleos.db")); !os.IsNotExist(err) {
		t.Fatalf("Load created the database before rejecting the configuration: %v", err)
	}
}
