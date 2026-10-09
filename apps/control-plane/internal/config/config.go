package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

var dnsName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
var resourceName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,61}[a-z0-9])?$`)

type Config struct {
	Env                     string        `env:"ENV" envDefault:"development"`
	LogLevel                string        `env:"LOG_LEVEL" envDefault:"info"`
	ListenAddress           string        `env:"LISTEN_ADDRESS" envDefault:"127.0.0.1"`
	Port                    string        `env:"PORT" envDefault:"3000"`
	DockerEndpoint          string        `env:"DOCKER_ENDPOINT" envDefault:"unix:///var/run/docker.sock"`
	DockerSocket            string        `env:"DOCKER_SOCKET" envDefault:""`
	BaseDomain              string        `env:"BASE_DOMAIN" envDefault:"moduleos.local"`
	DatabasePath            string        `env:"DATABASE_PATH" envDefault:"./moduleos.db"`
	APIKey                  string        `env:"API_KEY" envDefault:""`
	IngressNetwork          string        `env:"INGRESS_NETWORK" envDefault:"moduleos-ingress"`
	CORSAllowedOrigins      []string      `env:"CORS_ALLOWED_ORIGINS" envSeparator:","`
	AllowedMountRoots       []string      `env:"ALLOWED_MOUNT_ROOTS" envSeparator:"," envDefault:"/srv/moduleos-data"`
	ReconcileInterval       time.Duration `env:"RECONCILE_INTERVAL" envDefault:"30s"`
	ReconcileMaxBackoff     time.Duration `env:"RECONCILE_MAX_BACKOFF" envDefault:"30s"`
	ReconcileWorkers        int           `env:"RECONCILE_WORKERS" envDefault:"4"`
	DeploymentTimeout       time.Duration `env:"DEPLOYMENT_TIMEOUT" envDefault:"5m"`
	StabilizationWindow     time.Duration `env:"STABILIZATION_WINDOW" envDefault:"10s"`
	ShutdownTimeout         time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`
	ReadTimeout             time.Duration `env:"HTTP_READ_TIMEOUT" envDefault:"10s"`
	WriteTimeout            time.Duration `env:"HTTP_WRITE_TIMEOUT" envDefault:"30s"`
	IdleTimeout             time.Duration `env:"HTTP_IDLE_TIMEOUT" envDefault:"60s"`
	BodyLimit               int           `env:"HTTP_BODY_LIMIT" envDefault:"1048576"`
	MaxReplicas             int           `env:"MAX_REPLICAS" envDefault:"20"`
	MaxApplications         int           `env:"MAX_APPLICATIONS" envDefault:"100"`
	MaxProjects             int           `env:"MAX_PROJECTS" envDefault:"20"`
	MaxLogTail              int           `env:"MAX_LOG_TAIL" envDefault:"1000"`
	MaxLogPayloadBytes      int64         `env:"MAX_LOG_PAYLOAD_BYTES" envDefault:"1048576"`
	MaxSSEConnections       int           `env:"MAX_SSE_CONNECTIONS" envDefault:"20"`
	MaxSSEConnectionsPerApp int           `env:"MAX_SSE_CONNECTIONS_PER_APP" envDefault:"3"`
	RequestConcurrency      int           `env:"REQUEST_CONCURRENCY" envDefault:"128"`
}

func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parse configuration: %w", err)
	}
	if cfg.DockerSocket != "" {
		cfg.DockerEndpoint = cfg.DockerSocket
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.Env != "development" && c.Env != "production" {
		return fmt.Errorf("ENV must be development or production")
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
	if net.ParseIP(c.ListenAddress) == nil && c.ListenAddress != "localhost" {
		return fmt.Errorf("LISTEN_ADDRESS must be an IP address or localhost")
	}
	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("PORT must be between 1 and 65535")
	}
	if !strings.HasPrefix(c.DockerEndpoint, "unix://") && !strings.HasPrefix(c.DockerEndpoint, "tcp://") && !filepath.IsAbs(c.DockerEndpoint) {
		return fmt.Errorf("DOCKER_ENDPOINT must be unix://, tcp://, or an absolute socket path")
	}
	if strings.HasPrefix(c.DockerEndpoint, "unix://") {
		path := strings.TrimPrefix(c.DockerEndpoint, "unix://")
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("DOCKER_ENDPOINT Unix socket path must be canonical and absolute")
		}
	} else if filepath.IsAbs(c.DockerEndpoint) && filepath.Clean(c.DockerEndpoint) != c.DockerEndpoint {
		return fmt.Errorf("DOCKER_ENDPOINT socket path must be canonical")
	}
	if !dnsName.MatchString(c.BaseDomain) || !strings.Contains(c.BaseDomain, ".") {
		return fmt.Errorf("BASE_DOMAIN must be a valid DNS name")
	}
	if !resourceName.MatchString(c.IngressNetwork) {
		return fmt.Errorf("INGRESS_NETWORK contains invalid characters")
	}
	if c.Env == "production" && len(c.APIKey) < 32 {
		return fmt.Errorf("API_KEY must contain at least 32 characters in production")
	}
	if c.DatabasePath == "" {
		return fmt.Errorf("DATABASE_PATH cannot be empty")
	}
	if c.DatabasePath != ":memory:" {
		parent := filepath.Dir(c.DatabasePath)
		info, statErr := os.Stat(parent)
		if statErr != nil || !info.IsDir() {
			return fmt.Errorf("DATABASE_PATH parent directory is not accessible")
		}
		if info.Mode().Perm()&0o222 == 0 {
			return fmt.Errorf("DATABASE_PATH parent directory is not writable")
		}
	}
	for index, root := range c.AllowedMountRoots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || strings.ContainsRune(root, '\x00') {
			return fmt.Errorf("ALLOWED_MOUNT_ROOTS entries must be canonical absolute paths and cannot be root")
		}
		for _, previous := range c.AllowedMountRoots[:index] {
			if configPathContains(previous, root) || configPathContains(root, previous) {
				return fmt.Errorf("ALLOWED_MOUNT_ROOTS entries cannot overlap")
			}
		}
	}
	if c.ReconcileInterval <= 0 || c.ReconcileMaxBackoff <= 0 || c.DeploymentTimeout <= 0 || c.StabilizationWindow < 0 || c.ShutdownTimeout <= 0 {
		return fmt.Errorf("reconcile, deployment, and shutdown durations must be positive")
	}
	if c.ReadTimeout <= 0 || c.WriteTimeout <= 0 || c.IdleTimeout <= 0 {
		return fmt.Errorf("HTTP timeouts must be positive")
	}
	if c.ReconcileWorkers < 1 || c.ReconcileWorkers > 32 || c.MaxReplicas < 1 || c.MaxReplicas > 1000 || c.MaxApplications < 1 || c.MaxApplications > 10000 || c.MaxProjects < 2 || c.MaxProjects > 1000 {
		return fmt.Errorf("worker or replica limit is outside supported bounds")
	}
	if c.BodyLimit < 1024 || c.BodyLimit > 10*1024*1024 || c.MaxLogTail < 1 || c.MaxLogPayloadBytes < 1024 || c.MaxSSEConnections < 1 || c.MaxSSEConnectionsPerApp < 1 || c.RequestConcurrency < 1 {
		return fmt.Errorf("HTTP or log limit is outside supported bounds")
	}
	if c.MaxSSEConnectionsPerApp > c.MaxSSEConnections {
		return fmt.Errorf("per-app SSE limit cannot exceed global SSE limit")
	}
	return nil
}

func configPathContains(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (c *Config) SafeSummary() map[string]any {
	return map[string]any{
		"environment": c.Env, "listen_address": c.ListenAddress, "port": c.Port,
		"docker_endpoint": c.DockerEndpoint, "base_domain": c.BaseDomain,
		"database_path": c.DatabasePath, "api_key": "[redacted]",
		"ingress_network": c.IngressNetwork, "reconcile_workers": c.ReconcileWorkers,
	}
}
