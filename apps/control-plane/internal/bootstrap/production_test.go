package bootstrap

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/daemon"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/watcher"
)

var errProductionFactory = errors.New("production factory failed")

type testStoreResource struct {
	store.Store
	events     *[]string
	closeCount int
}

func (resource *testStoreResource) Close() error {
	resource.closeCount++
	*resource.events = append(*resource.events, "database")
	return nil
}

type testRuntimeResource struct {
	swarm.Client
	events      *[]string
	closeCount  int
	healthCalls int
}

func (resource *testRuntimeResource) Close() error {
	resource.closeCount++
	*resource.events = append(*resource.events, "docker")
	return nil
}

func (resource *testRuntimeResource) Health(context.Context) error {
	resource.healthCalls++
	return errors.New("Docker is deliberately unavailable")
}

type testReconcileLoop struct {
	enqueued []string
	runs     int
}

func (loop *testReconcileLoop) Enqueue(name string) { loop.enqueued = append(loop.enqueued, name) }
func (loop *testReconcileLoop) Run(ctx context.Context) {
	loop.runs++
	<-ctx.Done()
}

type testWorkerLoop struct{ runs int }

func (loop *testWorkerLoop) Run(ctx context.Context) {
	loop.runs++
	<-ctx.Done()
}

type recordingRegistrar struct {
	entries  []daemon.Cleanup
	rejectAt int
}

func (registrar *recordingRegistrar) Add(cleanup daemon.Cleanup) error {
	if len(registrar.entries)+1 == registrar.rejectAt {
		return errProductionFactory
	}
	registrar.entries = append(registrar.entries, cleanup)
	return nil
}

func phase7CTestConfig() *config.Config {
	return &config.Config{
		Env: "development", LogLevel: "debug", ListenAddress: "::1", Port: "4321",
		DockerEndpoint: "unix:///run/moduleos/docker.sock", BaseDomain: "apps.example.test",
		DatabasePath: ":memory:", APIKey: "sentinel-api-key", IngressNetwork: "sentinel-ingress",
		CORSAllowedOrigins: []string{"https://one.example", "https://two.example"},
		AllowedMountRoots:  []string{"/srv/one", "/srv/two"}, ReconcileInterval: 17 * time.Second,
		ReconcileMaxBackoff: 43 * time.Second, ReconcileWorkers: 7, DeploymentTimeout: 71 * time.Second,
		StabilizationWindow: 11 * time.Second, ShutdownTimeout: 19 * time.Second,
		ReadinessTimeout: 1300 * time.Millisecond, ReadTimeout: 23 * time.Second,
		WriteTimeout: 29 * time.Second, IdleTimeout: 31 * time.Second, BodyLimit: 3456,
		MaxReplicas: 37, MaxApplications: 41, MaxProjects: 43, MaxLogTail: 1200,
		MaxLogPayloadBytes: 234567, MaxSSEConnections: 47, MaxSSEConnectionsPerApp: 5,
		LogTailTimeout: 7 * time.Second, SSEOpenTimeout: 8 * time.Second,
		SSEHeartbeatInterval: 9 * time.Second, SSEIdleTimeout: 53 * time.Second,
		SSEMaxDuration: 97 * time.Second, SSEInitialTail: 59, RequestConcurrency: 61,
	}
}

func TestProductionOptionProjectionsMapEveryConfigField(t *testing.T) {
	cfg := phase7CTestConfig()
	application := applicationSettingsFromConfig(cfg)
	wantApplication := applicationSettings{
		baseDomain: cfg.BaseDomain, ingressNetwork: cfg.IngressNetwork, maxReplicas: cfg.MaxReplicas,
		allowedMounts: cfg.AllowedMountRoots, deploymentTimeout: cfg.DeploymentTimeout,
		maxApplications: cfg.MaxApplications, maxProjects: cfg.MaxProjects, dockerEndpoint: cfg.DockerEndpoint,
	}
	if !reflect.DeepEqual(application, wantApplication) {
		t.Fatalf("application projection = %#v, want %#v", application, wantApplication)
	}
	reconciliation := reconcilerSettingsFromConfig(cfg)
	if reconciliation != (reconcilerSettings{cfg.ReconcileInterval, cfg.ReconcileWorkers, cfg.ReconcileMaxBackoff, cfg.StabilizationWindow}) {
		t.Fatalf("reconciler projection = %#v", reconciliation)
	}
	logs := logOptionsFromConfig(cfg)
	if logs.MaxTail != cfg.MaxLogTail || logs.MaxPayloadBytes != cfg.MaxLogPayloadBytes ||
		logs.MaxConnections != cfg.MaxSSEConnections || logs.MaxConnectionsPerApp != cfg.MaxSSEConnectionsPerApp ||
		logs.InitialTail != cfg.SSEInitialTail || logs.TailTimeout != cfg.LogTailTimeout ||
		logs.OpenTimeout != cfg.SSEOpenTimeout || logs.HeartbeatInterval != cfg.SSEHeartbeatInterval ||
		logs.IdleTimeout != cfg.SSEIdleTimeout || logs.MaxDuration != cfg.SSEMaxDuration {
		t.Fatalf("log projection = %#v", logs)
	}

	events := []string{}
	st := &testStoreResource{events: &events}
	runtime := &testRuntimeResource{events: &events}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := app.NewService(st, runtime, cfg.BaseDomain, log)
	router := routerOptionsFromConfig(cfg, log, st, runtime, service)
	if router.Logger != log || router.Store != st || router.Runtime != runtime || router.Projects != service ||
		router.Applications != service || router.Deployments != service || router.Logs != service ||
		router.Environment != cfg.Env || router.APIKey != cfg.APIKey || router.IngressNetwork != cfg.IngressNetwork ||
		router.BodyLimit != cfg.BodyLimit || router.ReadTimeout != cfg.ReadTimeout ||
		router.WriteTimeout != cfg.WriteTimeout || router.IdleTimeout != cfg.IdleTimeout ||
		router.ReadinessTimeout != cfg.ReadinessTimeout || router.RequestConcurrency != cfg.RequestConcurrency ||
		!reflect.DeepEqual(router.CORSAllowedOrigins, cfg.CORSAllowedOrigins) || !reflect.DeepEqual(router.LogOptions, logs) {
		t.Fatalf("router projection = %#v", router)
	}
	application.allowedMounts[0] = "/mutated"
	router.CORSAllowedOrigins[0] = "https://mutated.example"
	if cfg.AllowedMountRoots[0] != "/srv/one" || cfg.CORSAllowedOrigins[0] != "https://one.example" {
		t.Fatal("projection retained mutable config slice storage")
	}
}

type productionCapture struct {
	order               []string
	events              []string
	store               *testStoreResource
	runtime             *testRuntimeResource
	applicationSettings applicationSettings
	reconcilerSettings  reconcilerSettings
	routerOptions       api.RouterOptions
	reconcile           *testReconcileLoop
	watch               *testWorkerLoop
	watcherQueue        watcher.Enqueuer
	server              *fakeServer
	readiness           *fakeReadiness
}

func productionFactoriesForTest(capture *productionCapture, failAt string) productionFactories {
	capture.store = &testStoreResource{events: &capture.events}
	capture.runtime = &testRuntimeResource{events: &capture.events}
	capture.reconcile = &testReconcileLoop{}
	capture.watch = &testWorkerLoop{}
	capture.server = &fakeServer{}
	capture.readiness = &fakeReadiness{}
	failed := func(name string) error {
		capture.order = append(capture.order, name)
		if failAt == name {
			return errProductionFactory
		}
		return nil
	}
	return productionFactories{
		openStore: func(string) (closeableStore, error) {
			if err := failed("store"); err != nil {
				return nil, err
			}
			return capture.store, nil
		},
		openRuntime: func(string) (closeableRuntime, error) {
			if err := failed("runtime"); err != nil {
				return nil, err
			}
			return capture.runtime, nil
		},
		newApplication: func(st closeableStore, runtime closeableRuntime, log *slog.Logger, settings applicationSettings) (*app.Service, error) {
			if err := failed("application"); err != nil {
				return nil, err
			}
			capture.applicationSettings = settings
			return app.NewService(st, runtime, settings.baseDomain, log), nil
		},
		newReconciler: func(_ closeableRuntime, _ *app.Service, _ *slog.Logger, reconcilerSettings reconcilerSettings) (reconcileLoop, error) {
			if err := failed("reconciler"); err != nil {
				return nil, err
			}
			capture.reconcilerSettings = reconcilerSettings
			return capture.reconcile, nil
		},
		newWatcher: func(_ closeableRuntime, queue watcher.Enqueuer, _ *slog.Logger) (workerLoop, error) {
			if err := failed("watcher"); err != nil {
				return nil, err
			}
			capture.watcherQueue = queue
			return capture.watch, nil
		},
		newRouter: func(options api.RouterOptions) (serverBinding, error) {
			if err := failed("router"); err != nil {
				return serverBinding{}, err
			}
			capture.routerOptions = options
			return serverBinding{server: capture.server, readiness: capture.readiness}, nil
		},
	}
}

func TestProductionBuilderComposesStableAssemblyWithoutDockerProbe(t *testing.T) {
	capture := &productionCapture{}
	cfg := phase7CTestConfig()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	registrar := &recordingRegistrar{}
	assembly, err := newProductionBuilder(cfg, log, productionFactoriesForTest(capture, ""))(registrar)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(capture.order, []string{"store", "runtime", "application", "reconciler", "watcher", "router"}) {
		t.Fatalf("construction order = %v", capture.order)
	}
	if capture.runtime.healthCalls != 0 {
		t.Fatalf("startup probed Docker %d times", capture.runtime.healthCalls)
	}
	if capture.watcherQueue != capture.reconcile {
		t.Fatal("watcher did not receive the exact reconciler queue")
	}
	if assembly.Logger != log || assembly.Server != capture.server || assembly.Readiness != capture.readiness {
		t.Fatal("assembly collaborators do not preserve identity")
	}
	if len(assembly.Workers) != 2 || assembly.Workers[0].Name != "reconciler" || assembly.Workers[1].Name != "watcher" {
		t.Fatalf("workers = %#v", assembly.Workers)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, worker := range assembly.Workers {
		if err := worker.Run(ctx); err != nil {
			t.Fatalf("worker %q returned %v", worker.Name, err)
		}
	}
	if capture.reconcile.runs != 1 || capture.watch.runs != 1 {
		t.Fatalf("worker adapters did not delegate: reconciler=%d watcher=%d", capture.reconcile.runs, capture.watch.runs)
	}
	if len(registrar.entries) != 2 || registrar.entries[0].Name != "database" || registrar.entries[1].Name != "docker" {
		t.Fatalf("cleanups = %#v", registrar.entries)
	}
	if capture.routerOptions.Store != capture.store || capture.routerOptions.Runtime != capture.runtime || capture.routerOptions.Projects == nil {
		t.Fatal("router did not receive constructed collaborators")
	}
}

func TestDefaultProductionFactoriesBuildRealAssemblyWithoutDockerReachability(t *testing.T) {
	cfg := phase7CTestConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "moduleos.db")
	registrar := &recordingRegistrar{}
	assembly, err := newProductionBuilder(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), defaultProductionFactories())(registrar)
	if err != nil {
		t.Fatal(err)
	}
	if assembly.Server == nil || assembly.Readiness == nil || len(assembly.Workers) != 2 {
		t.Fatalf("assembly = %#v", assembly)
	}
	if len(registrar.entries) != 2 {
		t.Fatalf("cleanup count = %d", len(registrar.entries))
	}
	for index := len(registrar.entries) - 1; index >= 0; index-- {
		if err := registrar.entries[index].Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProductionBuilderUnwindsConstructionFailuresInReverseOrder(t *testing.T) {
	tests := []struct {
		failAt string
		want   []string
	}{
		{failAt: "store"},
		{failAt: "runtime", want: []string{"database"}},
		{failAt: "application", want: []string{"docker", "database"}},
		{failAt: "reconciler", want: []string{"docker", "database"}},
		{failAt: "watcher", want: []string{"docker", "database"}},
		{failAt: "router", want: []string{"docker", "database"}},
	}
	for _, test := range tests {
		t.Run(test.failAt, func(t *testing.T) {
			capture := &productionCapture{}
			_, err := Prepare(Options{
				Build: newProductionBuilder(phase7CTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), productionFactoriesForTest(capture, test.failAt)),
				Listen: func(string, string) (net.Listener, error) {
					t.Fatal("listener called after build failure")
					return nil, nil
				},
				Network: "tcp", Address: "127.0.0.1:1", CleanupTimeout: time.Second, ShutdownTimeout: time.Second,
			})
			if !errors.Is(err, errProductionFactory) {
				t.Fatalf("Prepare() error = %v", err)
			}
			if !reflect.DeepEqual(capture.events, test.want) {
				t.Fatalf("cleanup order = %v, want %v", capture.events, test.want)
			}
		})
	}
}

func TestProductionBuilderListenerFailureAndDaemonShutdownOwnResources(t *testing.T) {
	t.Run("listener failure", func(t *testing.T) {
		capture := &productionCapture{}
		_, err := Prepare(Options{
			Build:   newProductionBuilder(phase7CTestConfig(), slog.Default(), productionFactoriesForTest(capture, "")),
			Listen:  func(string, string) (net.Listener, error) { return nil, errors.New("bind failed") },
			Network: "tcp", Address: "127.0.0.1:1", CleanupTimeout: time.Second, ShutdownTimeout: time.Second,
		})
		if err == nil {
			t.Fatal("listener failure was accepted")
		}
		if !reflect.DeepEqual(capture.events, []string{"docker", "database"}) {
			t.Fatalf("cleanup order = %v", capture.events)
		}
		if capture.reconcile.runs != 0 || capture.watch.runs != 0 {
			t.Fatal("listener failure started a worker")
		}
	})

	t.Run("daemon ownership", func(t *testing.T) {
		capture := &productionCapture{}
		prepared, err := Prepare(Options{
			Build:   newProductionBuilder(phase7CTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), productionFactoriesForTest(capture, "")),
			Listen:  func(string, string) (net.Listener, error) { return &fakeListener{}, nil },
			Network: "tcp", Address: "127.0.0.1:1", CleanupTimeout: time.Second, ShutdownTimeout: time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(capture.events) != 0 {
			t.Fatalf("bootstrap closed transferred resources: %v", capture.events)
		}
		outcome := prepared.Run(context.Background())
		if outcome.ExitCode != 1 || outcome.Cause != daemon.CauseServer {
			t.Fatalf("outcome = %#v", outcome)
		}
		if !reflect.DeepEqual(capture.events, []string{"docker", "database"}) {
			t.Fatalf("cleanup order = %v", capture.events)
		}
		if capture.runtime.closeCount != 1 || capture.store.closeCount != 1 {
			t.Fatalf("close counts = %d/%d", capture.runtime.closeCount, capture.store.closeCount)
		}
	})
}

func TestProductionBuilderClosesResourceWhenCleanupRegistrationFails(t *testing.T) {
	for _, rejectAt := range []int{1, 2} {
		t.Run(string(rune('0'+rejectAt)), func(t *testing.T) {
			capture := &productionCapture{}
			registrar := &recordingRegistrar{rejectAt: rejectAt}
			_, err := newProductionBuilder(phase7CTestConfig(), slog.Default(), productionFactoriesForTest(capture, ""))(registrar)
			if !errors.Is(err, errProductionFactory) {
				t.Fatalf("builder error = %v", err)
			}
			if rejectAt == 1 {
				if capture.store.closeCount != 1 || capture.runtime.closeCount != 0 {
					t.Fatalf("close counts = %d/%d", capture.store.closeCount, capture.runtime.closeCount)
				}
				return
			}
			if capture.runtime.closeCount != 1 || capture.store.closeCount != 0 {
				t.Fatalf("close counts = %d/%d", capture.store.closeCount, capture.runtime.closeCount)
			}
			if err := registrar.entries[0].Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(capture.events, []string{"docker", "database"}) {
				t.Fatalf("cleanup order = %v", capture.events)
			}
		})
	}
}

func TestCloseWithContextHonorsCancellation(t *testing.T) {
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := closeWithContext(func() error { called = true; return nil })(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close error = %v", err)
	}
	if called {
		t.Fatal("close ran after its context was canceled")
	}
}
