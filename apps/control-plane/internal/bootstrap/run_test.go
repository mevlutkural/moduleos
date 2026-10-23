package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/banner"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/daemon"
)

type recordingProcess struct {
	outcome daemon.Outcome
	ctx     context.Context
	runs    int
}

func (process *recordingProcess) Run(ctx context.Context) daemon.Outcome {
	process.ctx = ctx
	process.runs++
	return process.outcome
}

func successfulRunDependencies(cfg *config.Config) (runDependencies, *recordingProcess, *Options, *bytes.Buffer, *bytes.Buffer, *int) {
	proc := &recordingProcess{outcome: daemon.Outcome{ExitCode: 23}}
	preparedOptions := &Options{}
	stdout := &bytes.Buffer{}
	logs := &bytes.Buffer{}
	stops := new(int)
	return runDependencies{
		loadConfig: func() (*config.Config, error) { return cfg, nil },
		newLogger: func(string, ...string) *slog.Logger {
			return slog.New(slog.NewJSONHandler(logs, nil))
		},
		printBanner: banner.Print,
		notifyContext: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			return ctx, func() { *stops++ }
		},
		prepare: func(options Options) (process, error) {
			*preparedOptions = options
			return proc, nil
		},
		listen: func(string, string) (net.Listener, error) { return nil, errors.New("not called by orchestration test") },
		newBuilder: func(*config.Config, *slog.Logger) Builder {
			return func(Registrar) (Assembly, error) { return Assembly{}, errors.New("not called by orchestration test") }
		},
		stdout: stdout,
		stderr: io.Discard,
	}, proc, preparedOptions, stdout, logs, stops
}

func TestRunRejectsIncompleteDependenciesWithoutSideEffects(t *testing.T) {
	mutations := []func(*runDependencies){
		func(d *runDependencies) { d.loadConfig = nil }, func(d *runDependencies) { d.newLogger = nil },
		func(d *runDependencies) { d.printBanner = nil }, func(d *runDependencies) { d.notifyContext = nil },
		func(d *runDependencies) { d.prepare = nil }, func(d *runDependencies) { d.listen = nil },
		func(d *runDependencies) { d.newBuilder = nil }, func(d *runDependencies) { d.stdout = nil },
		func(d *runDependencies) { d.stderr = nil },
	}
	for index, mutate := range mutations {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			dependencies, process, _, _, _, _ := successfulRunDependencies(phase7CTestConfig())
			called := false
			original := dependencies.loadConfig
			dependencies.loadConfig = func() (*config.Config, error) { called = true; return original() }
			mutate(&dependencies)
			if exit := run(dependencies); exit != 1 {
				t.Fatalf("exit = %d", exit)
			}
			if process.runs != 0 {
				t.Fatal("invalid dependencies ran process")
			}
			if index != 0 && called {
				t.Fatal("invalid dependencies loaded config")
			}
		})
	}
}

func TestRunExportedBoundaryRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv("ENV", "invalid")
	if exit := Run(); exit != 1 {
		t.Fatalf("Run() = %d", exit)
	}
}

func TestDefaultRunDependenciesAreComplete(t *testing.T) {
	dependencies := defaultRunDependencies()
	if !validRunDependencies(dependencies) {
		t.Fatal("default dependencies are incomplete")
	}
	if dependencies.newBuilder(phase7CTestConfig(), slog.Default()) == nil {
		t.Fatal("default builder factory returned nil")
	}
	if _, err := dependencies.prepare(Options{}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("prepare error = %v", err)
	}
	ctx, stop := dependencies.notifyContext(context.Background(), os.Interrupt)
	if ctx == nil || stop == nil {
		t.Fatal("default signal adapter returned nil")
	}
	stop()
}

func TestRunConfigFailureIsSafeAndStopsBeforeLaterSideEffects(t *testing.T) {
	stderr := &bytes.Buffer{}
	sensitive := "private-api-key-and-path"
	dependencies, process, _, _, _, _ := successfulRunDependencies(phase7CTestConfig())
	dependencies.stderr = stderr
	dependencies.loadConfig = func() (*config.Config, error) { return nil, errors.New(sensitive) }
	later := false
	dependencies.newLogger = func(string, ...string) *slog.Logger { later = true; return slog.Default() }
	if exit := run(dependencies); exit != 1 {
		t.Fatalf("exit = %d", exit)
	}
	if later || process.runs != 0 {
		t.Fatal("config failure reached a later side effect")
	}
	if strings.Contains(stderr.String(), sensitive) || !strings.Contains(stderr.String(), `"phase":"config"`) {
		t.Fatalf("fallback log was not safe JSON: %q", stderr.String())
	}
}

func TestRunStopsAtLoggerAndBannerFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*runDependencies)
	}{
		{name: "logger", mutate: func(dependencies *runDependencies) {
			dependencies.newLogger = func(string, ...string) *slog.Logger { return nil }
		}},
		{name: "banner", mutate: func(dependencies *runDependencies) {
			dependencies.printBanner = func(io.Writer, string, string, string, string) error { return errors.New("private banner error") }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies, process, _, _, _, stops := successfulRunDependencies(phase7CTestConfig())
			test.mutate(&dependencies)
			if exit := run(dependencies); exit != 1 {
				t.Fatalf("exit = %d", exit)
			}
			if process.runs != 0 || *stops != 0 {
				t.Fatal("early failure registered signals or ran process")
			}
		})
	}
}

func TestRunProjectsLifecycleOptionsAndReturnsExactOutcome(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "::1"} {
		t.Run(host, func(t *testing.T) {
			cfg := phase7CTestConfig()
			cfg.ListenAddress = host
			dependencies, process, options, _, _, stops := successfulRunDependencies(cfg)
			if exit := run(dependencies); exit != process.outcome.ExitCode {
				t.Fatalf("exit = %d", exit)
			}
			if process.runs != 1 || process.ctx == nil {
				t.Fatal("prepared process was not run exactly once")
			}
			if *stops != 1 {
				t.Fatalf("signal stop calls = %d", *stops)
			}
			if options.Network != "tcp" || options.Address != net.JoinHostPort(host, cfg.Port) ||
				options.CleanupTimeout != cfg.ShutdownTimeout || options.ShutdownTimeout != cfg.ShutdownTimeout ||
				options.Build == nil || options.Listen == nil {
				t.Fatalf("prepare options = %#v", options)
			}
		})
	}
}

func TestRunStopsSignalAdapterWhenPreparationFails(t *testing.T) {
	dependencies, proc, _, _, logs, stops := successfulRunDependencies(phase7CTestConfig())
	dependencies.prepare = func(Options) (process, error) { return nil, errors.New("private prepare error") }
	if exit := run(dependencies); exit != 1 {
		t.Fatalf("exit = %d", exit)
	}
	if *stops != 1 || proc.runs != 0 {
		t.Fatal("preparation failure did not stop signals cleanly")
	}
	if strings.Contains(logs.String(), "private prepare error") || !strings.Contains(logs.String(), `"phase":"prepare"`) {
		t.Fatalf("prepare failure log = %q", logs.String())
	}
}

func TestRunPassesPreCanceledOperatorContext(t *testing.T) {
	dependencies, process, _, _, _, _ := successfulRunDependencies(phase7CTestConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dependencies.notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		return ctx, func() {}
	}
	if exit := run(dependencies); exit != process.outcome.ExitCode {
		t.Fatalf("exit = %d", exit)
	}
	if !errors.Is(process.ctx.Err(), context.Canceled) {
		t.Fatalf("process context error = %v", process.ctx.Err())
	}
}

func TestRunBannerAndConfigLoggingAreEnvironmentAwareAndSafe(t *testing.T) {
	for _, environment := range []string{"development", "production"} {
		t.Run(environment, func(t *testing.T) {
			cfg := phase7CTestConfig()
			cfg.Env = environment
			cfg.APIKey = "api-secret-that-must-never-be-logged"
			cfg.DatabasePath = "/private/moduleos.db"
			cfg.DockerEndpoint = "unix:///private/docker.sock"
			dependencies, _, _, stdout, logs, _ := successfulRunDependencies(cfg)
			if exit := run(dependencies); exit != 23 {
				t.Fatalf("exit = %d", exit)
			}
			if environment == "production" && stdout.Len() != 0 {
				t.Fatalf("production banner = %q", stdout.String())
			}
			if environment == "development" && stdout.Len() == 0 {
				t.Fatal("development banner was not written")
			}
			for _, secret := range []string{cfg.APIKey, cfg.DatabasePath, cfg.DockerEndpoint} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("logs exposed %q: %s", secret, logs.String())
				}
			}
			if !strings.Contains(logs.String(), `"database_path":"[configured]"`) || !strings.Contains(logs.String(), `"docker_endpoint":"unix"`) {
				t.Fatalf("safe summary missing: %s", logs.String())
			}
		})
	}
}
