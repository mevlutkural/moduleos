package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/caarlos0/env/v11"
)

const (
	maximumReadinessTimeout   = 5 * time.Second
	minimumReconcileBackoff   = time.Second
	maximumRequestConcurrency = 65536
	// defaultDockerEndpoint must match the DockerEndpoint envDefault tag.
	defaultDockerEndpoint = "unix:///var/run/docker.sock"
)

var dnsName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
var resourceName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,61}[a-z0-9])?$`)

var environmentKeys = []string{
	"ENV", "LOG_LEVEL", "LISTEN_ADDRESS", "PORT", "DOCKER_ENDPOINT", "DOCKER_SOCKET",
	"BASE_DOMAIN", "DATABASE_PATH", "API_KEY", "API_KEY_FILE", "INGRESS_NETWORK", "CORS_ALLOWED_ORIGINS",
	"ALLOWED_MOUNT_ROOTS", "RECONCILE_INTERVAL", "RECONCILE_MAX_BACKOFF", "RECONCILE_WORKERS",
	"DEPLOYMENT_TIMEOUT", "STABILIZATION_WINDOW", "SHUTDOWN_TIMEOUT", "READINESS_TIMEOUT",
	"HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "HTTP_BODY_LIMIT",
	"MAX_REPLICAS", "MAX_APPLICATIONS", "MAX_PROJECTS", "MAX_LOG_TAIL",
	"MAX_LOG_PAYLOAD_BYTES", "MAX_SSE_CONNECTIONS", "MAX_SSE_CONNECTIONS_PER_APP",
	"LOG_TAIL_TIMEOUT", "SSE_OPEN_TIMEOUT", "SSE_HEARTBEAT_INTERVAL", "SSE_IDLE_TIMEOUT",
	"SSE_MAX_DURATION", "SSE_INITIAL_TAIL", "REQUEST_CONCURRENCY",
}

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
	ReadinessTimeout        time.Duration `env:"READINESS_TIMEOUT" envDefault:"2s"`
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
	LogTailTimeout          time.Duration `env:"LOG_TAIL_TIMEOUT" envDefault:"10s"`
	SSEOpenTimeout          time.Duration `env:"SSE_OPEN_TIMEOUT" envDefault:"10s"`
	SSEHeartbeatInterval    time.Duration `env:"SSE_HEARTBEAT_INTERVAL" envDefault:"15s"`
	SSEIdleTimeout          time.Duration `env:"SSE_IDLE_TIMEOUT" envDefault:"5m"`
	SSEMaxDuration          time.Duration `env:"SSE_MAX_DURATION" envDefault:"30m"`
	SSEInitialTail          int           `env:"SSE_INITIAL_TAIL" envDefault:"50"`
	RequestConcurrency      int           `env:"REQUEST_CONCURRENCY" envDefault:"128"`
}

type environmentValue struct {
	value string
	set   bool
}

type dockerEndpointIdentity struct {
	transport string
	address   string
}

func Load() (*Config, error) {
	environment := captureEnvironment()
	if err := validateAPIKeySourceConflict(environment); err != nil {
		return nil, err
	}
	if err := validateExplicitEnvironment(environment); err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := env.ParseWithOptions(cfg, env.Options{Environment: environmentSnapshot(environment)}); err != nil {
		return nil, safeConfigurationParseError(err)
	}
	if err := validateEnvironmentName(cfg.Env); err != nil {
		return nil, err
	}
	if err := resolveAPIKey(cfg, environment, readAPIKeyFile); err != nil {
		return nil, err
	}
	if cors := environment["CORS_ALLOWED_ORIGINS"]; cors.set && cors.value == "" {
		cfg.CORSAllowedOrigins = nil
	}
	if err := resolveDockerEndpoint(cfg, environment); err != nil {
		return nil, err
	}
	origins, err := NormalizeCORSOrigins(cfg.CORSAllowedOrigins)
	if err != nil {
		return nil, fmt.Errorf("CORS_ALLOWED_ORIGINS: %w", err)
	}
	cfg.CORSAllowedOrigins = origins
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := validateEnvironmentRequirements(cfg, environment); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if err := validateEnvironmentName(c.Env); err != nil {
		return err
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
	endpoint, err := parseDockerEndpoint(c.DockerEndpoint)
	if err != nil {
		return fmt.Errorf("DOCKER_ENDPOINT %w", err)
	}
	if c.DockerSocket != "" {
		legacy, legacyErr := parseDockerEndpoint(c.DockerSocket)
		if legacyErr != nil {
			return fmt.Errorf("DOCKER_SOCKET %w", legacyErr)
		}
		if endpoint != legacy {
			return fmt.Errorf("DOCKER_ENDPOINT and DOCKER_SOCKET must resolve to the same endpoint when both are configured")
		}
	}
	if !dnsName.MatchString(c.BaseDomain) || !strings.Contains(c.BaseDomain, ".") {
		return fmt.Errorf("BASE_DOMAIN must be a valid DNS name")
	}
	if !resourceName.MatchString(c.IngressNetwork) {
		return fmt.Errorf("INGRESS_NETWORK contains invalid characters")
	}
	if err := validateAPIKeyContent(c.APIKey); err != nil {
		return fmt.Errorf("API_KEY %w", err)
	}
	if c.Env == "production" && len(c.APIKey) < minimumAPIKeyBytes {
		return fmt.Errorf("API_KEY must contain at least 32 characters in production")
	}
	if c.DatabasePath == "" {
		return fmt.Errorf("DATABASE_PATH cannot be empty")
	}
	if c.Env == "production" {
		if err := validateProductionSQLiteDatabase(c.DatabasePath); err != nil {
			return fmt.Errorf("DATABASE_PATH %w", err)
		}
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
	if len(c.AllowedMountRoots) == 0 {
		return fmt.Errorf("ALLOWED_MOUNT_ROOTS cannot be empty")
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
	if !isSQLiteMemoryDatabase(c.DatabasePath) {
		databaseDirectory := c.DatabaseDirectory()
		if databaseDirectory == "" {
			return fmt.Errorf("DATABASE_PATH directory cannot be resolved")
		}
		for _, root := range c.AllowedMountRoots {
			if pathFormsOverlap(root, databaseDirectory) {
				return fmt.Errorf("ALLOWED_MOUNT_ROOTS entries cannot contain or be inside the DATABASE_PATH directory")
			}
		}
	}
	if c.ReconcileInterval <= 0 || c.ReconcileMaxBackoff < minimumReconcileBackoff || c.DeploymentTimeout <= 0 || c.StabilizationWindow < 0 || c.ShutdownTimeout <= 0 {
		return fmt.Errorf("reconcile, deployment, and shutdown durations are outside supported bounds")
	}
	if c.ReadinessTimeout <= 0 || c.ReadinessTimeout > maximumReadinessTimeout {
		return fmt.Errorf("READINESS_TIMEOUT must be positive and at most 5s")
	}
	if c.ReadTimeout <= 0 || c.WriteTimeout <= 0 || c.IdleTimeout <= 0 {
		return fmt.Errorf("HTTP timeouts must be positive")
	}
	if c.ReconcileWorkers < 1 || c.ReconcileWorkers > 32 || c.MaxReplicas < 1 || c.MaxReplicas > 1000 || c.MaxApplications < 1 || c.MaxApplications > 10000 || c.MaxProjects < 2 || c.MaxProjects > 1000 {
		return fmt.Errorf("worker or replica limit is outside supported bounds")
	}
	if c.BodyLimit < 1024 || c.BodyLimit > 10*1024*1024 || c.MaxLogTail < 100 || c.MaxLogTail > 10000 || c.MaxLogPayloadBytes < 1024 || c.MaxLogPayloadBytes > 16*1024*1024 || c.MaxSSEConnections < 1 || c.MaxSSEConnections > 1024 || c.MaxSSEConnectionsPerApp < 1 || c.RequestConcurrency < 1 || c.RequestConcurrency > maximumRequestConcurrency {
		return fmt.Errorf("HTTP or log limit is outside supported bounds")
	}
	if c.MaxSSEConnectionsPerApp > c.MaxSSEConnections {
		return fmt.Errorf("per-app SSE limit cannot exceed global SSE limit")
	}
	if c.SSEInitialTail < 1 || c.SSEInitialTail > c.MaxLogTail {
		return fmt.Errorf("SSE_INITIAL_TAIL must be between 1 and MAX_LOG_TAIL")
	}
	if c.LogTailTimeout <= 0 || c.LogTailTimeout > time.Minute || c.SSEOpenTimeout <= 0 || c.SSEOpenTimeout > time.Minute {
		return fmt.Errorf("log tail and SSE open timeouts must be positive and at most one minute")
	}
	if c.SSEHeartbeatInterval <= 0 || c.SSEIdleTimeout <= 0 || c.SSEMaxDuration <= 0 || c.SSEHeartbeatInterval >= c.SSEIdleTimeout || c.SSEIdleTimeout >= c.SSEMaxDuration {
		return fmt.Errorf("SSE durations must satisfy heartbeat < idle < maximum duration")
	}
	if _, err := NormalizeCORSOrigins(c.CORSAllowedOrigins); err != nil {
		return fmt.Errorf("CORS_ALLOWED_ORIGINS: %w", err)
	}
	return nil
}

func captureEnvironment() map[string]environmentValue {
	values := make(map[string]environmentValue, len(environmentKeys))
	for _, key := range environmentKeys {
		value, set := os.LookupEnv(key)
		values[key] = environmentValue{value: value, set: set}
	}
	return values
}

func environmentSnapshot(environment map[string]environmentValue) map[string]string {
	values := make(map[string]string, len(environment))
	for key, value := range environment {
		if value.set {
			values[key] = value.value
		}
	}
	return values
}

func safeConfigurationParseError(parseErr error) error {
	var aggregate env.AggregateError
	if !errors.As(parseErr, &aggregate) {
		return errors.New("parse configuration: one or more environment values are invalid")
	}

	keys := make(map[string]struct{}, len(aggregate.Errors))
	configType := reflect.TypeOf(Config{})
	for _, item := range aggregate.Errors {
		var fieldError env.ParseError
		if !errors.As(item, &fieldError) {
			continue
		}
		field, found := configType.FieldByName(fieldError.Name)
		if !found {
			continue
		}
		key := strings.Split(field.Tag.Get("env"), ",")[0]
		if key != "" {
			keys[key] = struct{}{}
		}
	}
	if len(keys) == 0 {
		return errors.New("parse configuration: one or more environment values are invalid")
	}

	names := make([]string, 0, len(keys))
	for key := range keys {
		names = append(names, key)
	}
	sort.Strings(names)
	return fmt.Errorf("parse configuration: invalid value for %s", strings.Join(names, ", "))
}

func isSQLiteMemoryDatabase(value string) bool {
	driverName := value
	if queryIndex := strings.IndexRune(value, '?'); queryIndex >= 1 && !strings.HasPrefix(value, "file:") {
		driverName = value[:queryIndex]
	}
	if strings.EqualFold(driverName, ":memory:") {
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, "file") {
		return false
	}
	databaseName := parsed.Opaque
	if databaseName == "" {
		databaseName = parsed.Path
	}
	decodedName, err := url.PathUnescape(databaseName)
	if err != nil {
		return true
	}
	if nulIndex := strings.IndexRune(decodedName, '\x00'); nulIndex >= 0 {
		decodedName = decodedName[:nulIndex]
	}
	if decodedName == "" || strings.EqualFold(decodedName, ":memory:") {
		return true
	}
	for key, values := range parsed.Query() {
		if !strings.EqualFold(key, "mode") && !strings.EqualFold(key, "vfs") {
			continue
		}
		for _, candidate := range values {
			if (strings.EqualFold(key, "mode") && strings.EqualFold(candidate, "memory")) ||
				(strings.EqualFold(key, "vfs") && strings.EqualFold(candidate, "memdb")) {
				return true
			}
		}
	}
	return false
}

func validateProductionSQLiteDatabase(value string) error {
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return errors.New("cannot contain control characters")
	}
	if isSQLiteMemoryDatabase(value) {
		return errors.New("cannot use an in-memory database in production")
	}
	if strings.ContainsRune(value, '?') {
		return errors.New("must be a filesystem path without SQLite URI query parameters")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return errors.New("must be a valid filesystem path")
	}
	if parsed.Scheme != "" {
		return errors.New("must be a filesystem path, not a URI")
	}
	return nil
}

func validateExplicitEnvironment(environment map[string]environmentValue) error {
	allowedEmpty := map[string]struct{}{
		"API_KEY": {}, "CORS_ALLOWED_ORIGINS": {}, "DOCKER_SOCKET": {},
	}
	for _, key := range environmentKeys {
		value := environment[key]
		_, mayBeEmpty := allowedEmpty[key]
		if value.set && !mayBeEmpty && strings.TrimSpace(value.value) == "" {
			return fmt.Errorf("%s cannot be explicitly empty", key)
		}
	}
	return nil
}

func validateEnvironmentName(value string) error {
	if value != "development" && value != "production" {
		return fmt.Errorf("ENV must be development or production")
	}
	return nil
}

func validateEnvironmentRequirements(cfg *Config, environment map[string]environmentValue) error {
	if cfg.Env != "production" {
		return nil
	}
	if !environment["BASE_DOMAIN"].set {
		return fmt.Errorf("BASE_DOMAIN must be explicitly configured in production")
	}
	if !environment["DATABASE_PATH"].set {
		return fmt.Errorf("DATABASE_PATH must be explicitly configured in production")
	}
	return nil
}

// DockerSocketPath resolves the local Docker Unix socket path with the same
// DOCKER_ENDPOINT and DOCKER_SOCKET rules that Load applies. It reads only
// those two variables through lookup and performs no filesystem access, so
// the container entrypoint can share the daemon's endpoint policy without
// loading or validating unrelated configuration.
func DockerSocketPath(lookup func(string) (string, bool)) (string, error) {
	environment := make(map[string]environmentValue, 2)
	for _, key := range []string{"DOCKER_ENDPOINT", "DOCKER_SOCKET"} {
		value, set := lookup(key)
		environment[key] = environmentValue{value: value, set: set}
	}
	primary := environment["DOCKER_ENDPOINT"]
	if primary.set && strings.TrimSpace(primary.value) == "" {
		return "", errors.New("DOCKER_ENDPOINT cannot be explicitly empty")
	}
	cfg := &Config{DockerEndpoint: defaultDockerEndpoint, DockerSocket: environment["DOCKER_SOCKET"].value}
	if primary.set {
		cfg.DockerEndpoint = primary.value
	}
	if err := resolveDockerEndpoint(cfg, environment); err != nil {
		return "", err
	}
	identity, err := parseDockerEndpoint(cfg.DockerEndpoint)
	if err != nil {
		return "", fmt.Errorf("DOCKER_ENDPOINT %w", err)
	}
	return identity.address, nil
}

func resolveDockerEndpoint(cfg *Config, environment map[string]environmentValue) error {
	primary := environment["DOCKER_ENDPOINT"]
	legacy := environment["DOCKER_SOCKET"]
	legacyConfigured := legacy.set && legacy.value != ""

	if primary.set {
		primaryIdentity, err := parseDockerEndpoint(cfg.DockerEndpoint)
		if err != nil {
			return fmt.Errorf("DOCKER_ENDPOINT %w", err)
		}
		if legacyConfigured {
			legacyIdentity, legacyErr := parseDockerEndpoint(cfg.DockerSocket)
			if legacyErr != nil {
				return fmt.Errorf("DOCKER_SOCKET %w", legacyErr)
			}
			if primaryIdentity != legacyIdentity {
				return fmt.Errorf("DOCKER_ENDPOINT and DOCKER_SOCKET must resolve to the same endpoint when both are explicitly configured")
			}
		}
		return nil
	}
	if legacyConfigured {
		if _, err := parseDockerEndpoint(cfg.DockerSocket); err != nil {
			return fmt.Errorf("DOCKER_SOCKET %w", err)
		}
		cfg.DockerEndpoint = cfg.DockerSocket
	}
	return nil
}

func parseDockerEndpoint(value string) (dockerEndpointIdentity, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return dockerEndpointIdentity{}, errors.New("must be a non-empty endpoint without whitespace or control characters")
	}
	if strings.Contains(value, "%") {
		return dockerEndpointIdentity{}, errors.New("must not contain percent-encoded path data")
	}
	if filepath.IsAbs(value) {
		if strings.ContainsAny(value, "?#") || filepath.Clean(value) != value || value == string(filepath.Separator) {
			return dockerEndpointIdentity{}, errors.New("must use a canonical absolute Unix socket path")
		}
		return dockerEndpointIdentity{transport: "unix", address: value}, nil
	}
	if !strings.HasPrefix(value, "unix://") {
		return dockerEndpointIdentity{}, errors.New("must be unix:// or an absolute Unix socket path")
	}
	if strings.ContainsAny(value, "?#") {
		return dockerEndpointIdentity{}, errors.New("must not contain a query or fragment")
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return dockerEndpointIdentity{}, errors.New("must be a valid unix:// endpoint")
	}
	switch parsed.Scheme {
	case "unix":
		if parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawPath != "" || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" || !filepath.IsAbs(parsed.Path) || filepath.Clean(parsed.Path) != parsed.Path || parsed.Path == string(filepath.Separator) {
			return dockerEndpointIdentity{}, errors.New("must use a canonical absolute Unix socket path")
		}
		return dockerEndpointIdentity{transport: "unix", address: parsed.Path}, nil
	default:
		return dockerEndpointIdentity{}, errors.New("must be unix:// or an absolute Unix socket path")
	}
}

func NormalizeCORSOrigins(origins []string) ([]string, error) {
	if len(origins) == 0 {
		return nil, nil
	}
	unique := make(map[string]struct{}, len(origins))
	for index, raw := range origins {
		if strings.IndexFunc(raw, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("origin at index %d contains control characters", index)
		}
		raw = strings.TrimSpace(raw)
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(parsed.Host, "*") || strings.HasSuffix(parsed.Host, ":") {
			return nil, fmt.Errorf("origin at index %d must be an exact HTTP or HTTPS origin", index)
		}
		if port := parsed.Port(); port != "" {
			value, conversionErr := strconv.Atoi(port)
			if conversionErr != nil || value < 1 || value > 65535 {
				return nil, fmt.Errorf("origin at index %d must use a port between 1 and 65535", index)
			}
		}
		scheme := strings.ToLower(parsed.Scheme)
		hostname := strings.ToLower(parsed.Hostname())
		if strings.Contains(hostname, "%") {
			return nil, fmt.Errorf("origin at index %d must not contain an IPv6 zone identifier", index)
		}
		port := parsed.Port()
		if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
			port = ""
		}
		host := hostname
		if strings.Contains(hostname, ":") {
			host = "[" + hostname + "]"
		}
		if port != "" {
			host += ":" + port
		}
		unique[scheme+"://"+host] = struct{}{}
	}

	result := make([]string, 0, len(unique))
	for origin := range unique {
		result = append(result, origin)
	}
	sort.Strings(result)
	return result, nil
}

func configPathContains(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// DatabaseDirectory returns the absolute directory that holds the SQLite
// database and its -wal and -shm files, or "" for an in-memory database.
func (c *Config) DatabaseDirectory() string {
	if c.DatabasePath == "" || isSQLiteMemoryDatabase(c.DatabasePath) {
		return ""
	}
	directory, err := filepath.Abs(filepath.Dir(filepath.Clean(c.DatabasePath)))
	if err != nil {
		return ""
	}
	return directory
}

// pathFormsOverlap reports whether two paths are equal or one contains the
// other, comparing both their lexical and their symlink-resolved forms.
func pathFormsOverlap(left, right string) bool {
	for _, leftForm := range pathForms(left) {
		for _, rightForm := range pathForms(right) {
			if configPathContains(leftForm, rightForm) || configPathContains(rightForm, leftForm) {
				return true
			}
		}
	}
	return false
}

func pathForms(path string) []string {
	forms := []string{filepath.Clean(path)}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && filepath.Clean(resolved) != forms[0] {
		forms = append(forms, filepath.Clean(resolved))
	}
	return forms
}

func (c *Config) SafeSummary() map[string]any {
	database := "[configured]"
	if isSQLiteMemoryDatabase(c.DatabasePath) {
		database = "[memory]"
	}
	endpoint, err := parseDockerEndpoint(c.DockerEndpoint)
	transport := "[invalid]"
	if err == nil {
		transport = endpoint.transport
	}
	return map[string]any{
		"environment": c.Env, "listen_address": c.ListenAddress, "port": c.Port,
		"docker_endpoint": transport, "base_domain": c.BaseDomain,
		"database_path": database, "api_key": "[redacted]",
		"ingress_network": c.IngressNetwork, "reconcile_workers": c.ReconcileWorkers,
	}
}
