package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/daemon"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

const reasonSentinel = "sentinel-private-detail"

func failureRecord(t *testing.T, output string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == "bootstrap failed" {
			if found != nil {
				t.Fatalf("more than one bootstrap failure record: %q", output)
			}
			found = record
		}
	}
	if found == nil {
		t.Fatalf("no bootstrap failure record in %q", output)
	}
	if strings.Contains(output, reasonSentinel) {
		t.Fatalf("failure output leaked private detail: %q", output)
	}
	return found
}

func requireFailure(t *testing.T, record map[string]any, phase, reason, variable string) {
	t.Helper()
	if record["phase"] != phase || record["reason"] != reason {
		t.Fatalf("record = %v, want phase %q reason %q", record, phase, reason)
	}
	got, present := record["variable"]
	if variable == "" && present {
		t.Fatalf("record names variable %v, want none", got)
	}
	if variable != "" && got != variable {
		t.Fatalf("record variable = %v, want %q", got, variable)
	}
}

func isolateConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"ENV", "LOG_LEVEL", "LISTEN_ADDRESS", "PORT", "DOCKER_ENDPOINT", "DOCKER_SOCKET",
		"BASE_DOMAIN", "DATABASE_PATH", "API_KEY", "API_KEY_FILE", "INGRESS_NETWORK", "CORS_ALLOWED_ORIGINS",
		"ALLOWED_MOUNT_ROOTS", "RECONCILE_INTERVAL", "RECONCILE_MAX_BACKOFF", "RECONCILE_WORKERS",
		"DEPLOYMENT_TIMEOUT", "STABILIZATION_WINDOW", "SHUTDOWN_TIMEOUT", "READINESS_TIMEOUT",
		"HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "HTTP_BODY_LIMIT",
		"MAX_REPLICAS", "MAX_APPLICATIONS", "MAX_PROJECTS", "MAX_LOG_TAIL",
		"MAX_LOG_PAYLOAD_BYTES", "MAX_SSE_CONNECTIONS", "MAX_SSE_CONNECTIONS_PER_APP",
		"LOG_TAIL_TIMEOUT", "SSE_OPEN_TIMEOUT", "SSE_HEARTBEAT_INTERVAL", "SSE_IDLE_TIMEOUT",
		"SSE_MAX_DURATION", "SSE_INITIAL_TAIL", "REQUEST_CONCURRENCY",
	} {
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

func TestRunLogsConfigFailureReasonAndVariable(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("PORT", reasonSentinel)
	stderr := &bytes.Buffer{}
	dependencies, _, _, _, _, _ := successfulRunDependencies(phase7CTestConfig())
	dependencies.stderr = stderr
	dependencies.loadConfig = config.Load

	if exit := run(dependencies); exit != 1 {
		t.Fatalf("exit = %d", exit)
	}

	requireFailure(t, failureRecord(t, stderr.String()), "config", config.ReasonValueInvalid, "PORT")
}

func TestRunLogsAGenericConfigReasonForUnclassifiedFailures(t *testing.T) {
	for name, load := range map[string]func() (*config.Config, error){
		"unclassified error": func() (*config.Config, error) { return nil, errors.New(reasonSentinel) },
		"nil configuration":  func() (*config.Config, error) { return nil, nil },
	} {
		t.Run(name, func(t *testing.T) {
			stderr := &bytes.Buffer{}
			dependencies, _, _, _, _, _ := successfulRunDependencies(phase7CTestConfig())
			dependencies.stderr = stderr
			dependencies.loadConfig = load

			if exit := run(dependencies); exit != 1 {
				t.Fatalf("exit = %d", exit)
			}

			requireFailure(t, failureRecord(t, stderr.String()), "config", reasonConfigInvalid, "")
		})
	}
}

func TestRunLogsLoggerAndBannerReasons(t *testing.T) {
	stderr := &bytes.Buffer{}
	dependencies, _, _, _, _, _ := successfulRunDependencies(phase7CTestConfig())
	dependencies.stderr = stderr
	dependencies.newLogger = func(string, ...string) *slog.Logger { return nil }
	if exit := run(dependencies); exit != 1 {
		t.Fatalf("logger exit = %d", exit)
	}
	requireFailure(t, failureRecord(t, stderr.String()), "logger", reasonLoggerUnavailable, "")

	dependencies, _, _, _, logs, _ := successfulRunDependencies(phase7CTestConfig())
	dependencies.printBanner = func(io.Writer, string, string, string, string) error { return errors.New(reasonSentinel) }
	if exit := run(dependencies); exit != 1 {
		t.Fatalf("banner exit = %d", exit)
	}
	requireFailure(t, failureRecord(t, logs.String()), "banner", reasonBannerFailed, "")
}

func TestRunLogsPrepareFailureReasons(t *testing.T) {
	private := errors.New(reasonSentinel)
	assembly := func(Registrar) (Assembly, error) {
		return Assembly{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Server: &fakeServer{}, Readiness: &fakeReadiness{},
			Workers: []daemon.Worker{{Name: "worker", Run: func(context.Context) error { return nil }}},
		}, nil
	}
	tests := []struct {
		name   string
		build  Builder
		listen ListenerFactory
		want   string
	}{
		{name: "database open", build: func(Registrar) (Assembly, error) {
			return Assembly{}, withReason(reasonDatabaseOpenFailed, private)
		}, want: reasonDatabaseOpenFailed},
		{name: "database migration", build: func(Registrar) (Assembly, error) {
			return Assembly{}, withReason(reasonDatabaseMigrationFailed, private)
		}, want: reasonDatabaseMigrationFailed},
		{name: "unclassified assembly error", build: func(Registrar) (Assembly, error) { return Assembly{}, private }, want: reasonAssemblyFailed},
		{name: "builder panic", build: func(Registrar) (Assembly, error) { panic(reasonSentinel) }, want: reasonInternalError},
		{name: "invalid assembly", build: func(Registrar) (Assembly, error) { return Assembly{}, nil }, want: reasonInternalError},
		{name: "listener bind", build: assembly, listen: func(string, string) (net.Listener, error) { return nil, private }, want: reasonListenerBindFailed},
		{name: "listener panic", build: assembly, listen: func(string, string) (net.Listener, error) { panic(reasonSentinel) }, want: reasonInternalError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _, _, _, logs, _ := successfulRunDependencies(phase7CTestConfig())
			dependencies.newBuilder = func(*config.Config, *slog.Logger) Builder { return test.build }
			if test.listen != nil {
				dependencies.listen = test.listen
			}
			dependencies.prepare = func(options Options) (process, error) {
				prepared, err := Prepare(options)
				if prepared == nil {
					return nil, err
				}
				return prepared, err
			}

			if exit := run(dependencies); exit != 1 {
				t.Fatalf("exit = %d", exit)
			}

			requireFailure(t, failureRecord(t, logs.String()), "prepare", test.want, "")
		})
	}
}

func TestRunLogsInternalReasonForUnclassifiedPrepareResults(t *testing.T) {
	for name, prepare := range map[string]func(Options) (process, error){
		"plain error":    func(Options) (process, error) { return nil, errors.New(reasonSentinel) },
		"nil process":    func(Options) (process, error) { return nil, nil },
		"invalid option": func(Options) (process, error) { _, err := Prepare(Options{}); return nil, err },
	} {
		t.Run(name, func(t *testing.T) {
			dependencies, _, _, _, logs, _ := successfulRunDependencies(phase7CTestConfig())
			dependencies.prepare = prepare
			if exit := run(dependencies); exit != 1 {
				t.Fatalf("exit = %d", exit)
			}
			requireFailure(t, failureRecord(t, logs.String()), "prepare", reasonInternalError, "")
		})
	}
}

func TestProductionBuilderClassifiesConstructionFailures(t *testing.T) {
	migration := fmt.Errorf("%w: %s", store.ErrMigrationFailed, reasonSentinel)
	tests := []struct {
		name    string
		mutate  func(*productionFactories)
		failAt  string
		want    string
		message string
	}{
		{name: "database open", failAt: "store", want: reasonDatabaseOpenFailed},
		{name: "database migration", mutate: func(factories *productionFactories) {
			factories.openStore = func(string) (closeableStore, error) { return nil, migration }
		}, want: reasonDatabaseMigrationFailed},
		{name: "Docker client", failAt: "runtime", want: reasonDockerClientInvalid},
		{name: "application service", failAt: "application", want: reasonAssemblyFailed},
		{name: "router", failAt: "router", want: reasonAssemblyFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capture := &productionCapture{}
			factories := productionFactoriesForTest(capture, test.failAt)
			if test.mutate != nil {
				test.mutate(&factories)
			}
			_, err := Prepare(Options{
				Build:   newProductionBuilder(phase7CTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), factories),
				Listen:  func(string, string) (net.Listener, error) { return nil, errors.New("listener not expected") },
				Network: "tcp", Address: "127.0.0.1:1", CleanupTimeout: time.Second, ShutdownTimeout: time.Second,
			})
			if got := startupReason(err, "unclassified"); got != test.want {
				t.Fatalf("reason = %q, want %q (err = %v)", got, test.want, err)
			}
			if strings.Contains(err.Error(), reasonSentinel) {
				t.Fatalf("prepare error leaked private detail: %v", err)
			}
		})
	}
}

func TestStartupFailurePreservesMessageAndCause(t *testing.T) {
	cause := errors.New("cause")
	err := withReason(reasonDatabaseOpenFailed, cause)
	if err.Error() != "cause" || !errors.Is(err, cause) || startupReason(err, "") != reasonDatabaseOpenFailed {
		t.Fatalf("withReason() = %v", err)
	}
	if startupReason(errors.New("plain"), "fallback") != "fallback" {
		t.Fatal("unclassified error did not use the fallback")
	}
}

type panickingAsError struct{}

func (*panickingAsError) Error() string { return reasonSentinel }
func (*panickingAsError) As(any) bool   { panic(reasonSentinel) }
func (*panickingAsError) Unwrap() error { return nil }

type panickingReasonError struct{}

func (*panickingReasonError) Error() string         { return reasonSentinel }
func (*panickingReasonError) StartupReason() string { panic(reasonSentinel) }

func TestClassificationContainsPanickingCollaboratorErrors(t *testing.T) {
	assembly := func(Registrar) (Assembly, error) {
		return Assembly{
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Server: &fakeServer{}, Readiness: &fakeReadiness{},
			Workers: []daemon.Worker{{Name: "worker", Run: func(context.Context) error { return nil }}},
		}, nil
	}
	tests := []struct {
		failure error
		stage   string
		want    string
	}{
		// errors.Is panics for both collaborators.
		{failure: &panickingMatchError{}, stage: "builder", want: reasonInternalError},
		{failure: &panickingMatchError{}, stage: "listener", want: reasonInternalError},
		// As and StartupReason are consulted only for builder errors; a
		// listener failure never inspects them, so it stays a bind failure.
		{failure: &panickingAsError{}, stage: "builder", want: reasonInternalError},
		{failure: &panickingAsError{}, stage: "listener", want: reasonListenerBindFailed},
		{failure: &panickingReasonError{}, stage: "builder", want: reasonInternalError},
		{failure: &panickingReasonError{}, stage: "listener", want: reasonListenerBindFailed},
	}
	for _, test := range tests {
		failure, stage := test.failure, test.stage
		{
			t.Run(fmt.Sprintf("%T/%s", failure, stage), func(t *testing.T) {
				dependencies, _, _, _, logs, _ := successfulRunDependencies(phase7CTestConfig())
				dependencies.newBuilder = func(*config.Config, *slog.Logger) Builder {
					if stage == "builder" {
						return func(Registrar) (Assembly, error) { return Assembly{}, failure }
					}
					return assembly
				}
				dependencies.listen = func(string, string) (net.Listener, error) { return nil, failure }
				dependencies.prepare = func(options Options) (process, error) {
					prepared, err := Prepare(options)
					if prepared == nil {
						return nil, err
					}
					return prepared, err
				}

				if exit := run(dependencies); exit != 1 {
					t.Fatalf("exit = %d", exit)
				}

				record := failureRecord(t, logs.String())
				requireFailure(t, record, "prepare", test.want, "")
				if strings.Contains(logs.String(), "private classification panic") {
					t.Fatalf("classification panic text leaked: %q", logs.String())
				}
			})
		}
	}
}

func TestConfigClassificationContainsPanickingErrors(t *testing.T) {
	stderr := &bytes.Buffer{}
	dependencies, _, _, _, _, _ := successfulRunDependencies(phase7CTestConfig())
	dependencies.stderr = stderr
	dependencies.loadConfig = func() (*config.Config, error) { return nil, &panickingAsError{} }

	if exit := run(dependencies); exit != 1 {
		t.Fatalf("exit = %d", exit)
	}

	requireFailure(t, failureRecord(t, stderr.String()), "config", reasonConfigInvalid, "")
}

func TestStartupReasonContainsPanics(t *testing.T) {
	for _, err := range []error{&panickingAsError{}, &panickingReasonError{}} {
		if got := startupReason(err, "fallback"); got != reasonInternalError {
			t.Fatalf("startupReason(%T) = %q, want %q", err, got, reasonInternalError)
		}
	}
}
