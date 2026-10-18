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
	"BASE_DOMAIN", "DATABASE_PATH", "API_KEY", "INGRESS_NETWORK", "CORS_ALLOWED_ORIGINS",
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
	for _, endpoint := range []string{"unix:///var/run/docker.sock", "tcp://127.0.0.1:2375", "tcp://docker.internal:2375", "tcp://[::1]:2375", "/var/run/docker.sock"} {
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
			name: "percent-encoded memory URI database is forbidden",
			configure: func(t *testing.T) {
				t.Setenv("BASE_DOMAIN", "moduleos.example")
				t.Setenv("DATABASE_PATH", "file:%3Amemory%3A")
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearConfigEnvironment(t)
			t.Setenv("ENV", "production")
			t.Setenv("API_KEY", strings.Repeat("k", 32))
			test.configure(t)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("Load() error = %v, want %s", err, test.wantMessage)
			}
		})
	}
}

func TestLoadAcceptsExplicitProductionValuesEqualToDevelopmentDefaults(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("ENV", "production")
	t.Setenv("BASE_DOMAIN", "moduleos.local")
	t.Setenv("DATABASE_PATH", "./moduleos.db")
	t.Setenv("API_KEY", strings.Repeat("k", 32))

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
		{name: "primary only", primary: "tcp://docker.internal:2375", want: "tcp://docker.internal:2375"},
		{name: "legacy only", legacy: "/run/custom-docker.sock", want: "/run/custom-docker.sock"},
		{name: "identical aliases", primary: "unix:///run/docker.sock", legacy: "unix:///run/docker.sock", want: "unix:///run/docker.sock"},
		{name: "equivalent Unix aliases", primary: "unix:///run/docker.sock", legacy: "/run/docker.sock", want: "unix:///run/docker.sock"},
		{name: "conflicting Unix aliases", primary: "unix:///run/primary.sock", legacy: "/run/legacy.sock", wantErr: true, forbidden: []string{"/run/primary.sock", "/run/legacy.sock"}},
		{name: "conflicting TCP aliases", primary: "tcp://first.example:2375", legacy: "tcp://second.example:2375", wantErr: true, forbidden: []string{"first.example", "second.example"}},
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
		"tcp://docker.internal:2375?token=super-secret",
		"tcp://docker.internal:2375#fragment",
		"tcp://[fe80::1%25en0]:2375",
		"tcp://docker.internal:2375 ",
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
		{name: "TCP", endpoint: "tcp://private.example:2375", database: ":memory:", transport: "tcp", dbSummary: "[memory]", forbidden: []string{"private.example", "2375"}},
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
