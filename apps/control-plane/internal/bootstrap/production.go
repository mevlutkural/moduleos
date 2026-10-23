package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/handler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/daemon"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/reconciler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/watcher"
)

type closeableStore interface {
	store.Store
	Close() error
}

type closeableRuntime interface {
	swarm.Client
	Close() error
}

type reconcileLoop interface {
	app.ReconcileQueue
	Run(context.Context)
}

type workerLoop interface {
	Run(context.Context)
}

type serverBinding struct {
	server    daemon.Server
	readiness daemon.Readiness
}

type applicationSettings struct {
	baseDomain        string
	ingressNetwork    string
	maxReplicas       int
	allowedMounts     []string
	deploymentTimeout time.Duration
	maxApplications   int
	maxProjects       int
	dockerEndpoint    string
}

type reconcilerSettings struct {
	interval      time.Duration
	workers       int
	maxBackoff    time.Duration
	stabilization time.Duration
}

type productionFactories struct {
	openStore      func(string) (closeableStore, error)
	openRuntime    func(string) (closeableRuntime, error)
	newApplication func(closeableStore, closeableRuntime, *slog.Logger, applicationSettings) (*app.Service, error)
	newReconciler  func(closeableRuntime, *app.Service, *slog.Logger, reconcilerSettings) (reconcileLoop, error)
	newWatcher     func(closeableRuntime, watcher.Enqueuer, *slog.Logger) (workerLoop, error)
	newRouter      func(api.RouterOptions) (serverBinding, error)
}

func defaultProductionFactories() productionFactories {
	return productionFactories{
		openStore: func(path string) (closeableStore, error) {
			return store.NewSQLiteStore(path)
		},
		openRuntime: func(endpoint string) (closeableRuntime, error) {
			return swarm.NewDockerClientWithEndpoint(endpoint)
		},
		newApplication: func(st closeableStore, runtime closeableRuntime, log *slog.Logger, settings applicationSettings) (*app.Service, error) {
			service := app.NewService(st, runtime, settings.baseDomain, log).
				WithIngressNetwork(settings.ingressNetwork).
				WithRuntimeLimits(settings.maxReplicas, settings.allowedMounts, settings.deploymentTimeout).
				WithResourceLimits(settings.maxApplications, settings.maxProjects).
				WithDockerEndpoint(settings.dockerEndpoint)
			return service, nil
		},
		newReconciler: func(runtime closeableRuntime, service *app.Service, log *slog.Logger, settings reconcilerSettings) (reconcileLoop, error) {
			return reconciler.New(runtime, service, log).
				WithInterval(settings.interval).
				WithWorkers(settings.workers).
				WithMaxBackoff(settings.maxBackoff).
				WithStabilizationWindow(settings.stabilization), nil
		},
		newWatcher: func(runtime closeableRuntime, queue watcher.Enqueuer, log *slog.Logger) (workerLoop, error) {
			return watcher.New(runtime, queue, log), nil
		},
		newRouter: func(options api.RouterOptions) (serverBinding, error) {
			router, err := api.NewRouter(options)
			if err != nil {
				return serverBinding{}, err
			}
			return serverBinding{server: router, readiness: router.Health}, nil
		},
	}
}

func applicationSettingsFromConfig(cfg *config.Config) applicationSettings {
	return applicationSettings{
		baseDomain: cfg.BaseDomain, ingressNetwork: cfg.IngressNetwork,
		maxReplicas: cfg.MaxReplicas, allowedMounts: append([]string(nil), cfg.AllowedMountRoots...),
		deploymentTimeout: cfg.DeploymentTimeout, maxApplications: cfg.MaxApplications,
		maxProjects: cfg.MaxProjects, dockerEndpoint: cfg.DockerEndpoint,
	}
}

func reconcilerSettingsFromConfig(cfg *config.Config) reconcilerSettings {
	return reconcilerSettings{
		interval: cfg.ReconcileInterval, workers: cfg.ReconcileWorkers,
		maxBackoff: cfg.ReconcileMaxBackoff, stabilization: cfg.StabilizationWindow,
	}
}

func logOptionsFromConfig(cfg *config.Config) handler.LogOptions {
	return handler.LogOptions{
		MaxTail: cfg.MaxLogTail, MaxPayloadBytes: cfg.MaxLogPayloadBytes,
		MaxConnections: cfg.MaxSSEConnections, MaxConnectionsPerApp: cfg.MaxSSEConnectionsPerApp,
		InitialTail: cfg.SSEInitialTail, TailTimeout: cfg.LogTailTimeout,
		OpenTimeout: cfg.SSEOpenTimeout, HeartbeatInterval: cfg.SSEHeartbeatInterval,
		IdleTimeout: cfg.SSEIdleTimeout, MaxDuration: cfg.SSEMaxDuration,
	}
}

func routerOptionsFromConfig(cfg *config.Config, log *slog.Logger, st closeableStore, runtime closeableRuntime, service *app.Service) api.RouterOptions {
	return api.RouterOptions{
		Logger: log, Store: st, Runtime: runtime, Projects: service,
		Applications: service, Deployments: service, Logs: service,
		Environment: cfg.Env, APIKey: cfg.APIKey, IngressNetwork: cfg.IngressNetwork,
		BodyLimit: cfg.BodyLimit, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout,
		IdleTimeout: cfg.IdleTimeout, ReadinessTimeout: cfg.ReadinessTimeout,
		RequestConcurrency: cfg.RequestConcurrency,
		CORSAllowedOrigins: append([]string(nil), cfg.CORSAllowedOrigins...),
		LogOptions:         logOptionsFromConfig(cfg),
	}
}

func newProductionBuilder(cfg *config.Config, log *slog.Logger, factories productionFactories) Builder {
	return func(registrar Registrar) (Assembly, error) {
		st, err := factories.openStore(cfg.DatabasePath)
		if err != nil {
			return Assembly{}, fmt.Errorf("open database: %w", err)
		}
		if err := registrar.Add(daemon.Cleanup{Name: "database", Run: closeWithContext(st.Close)}); err != nil {
			_ = st.Close()
			return Assembly{}, fmt.Errorf("register database cleanup: %w", err)
		}

		runtime, err := factories.openRuntime(cfg.DockerEndpoint)
		if err != nil {
			return Assembly{}, fmt.Errorf("construct Docker client: %w", err)
		}
		if err := registrar.Add(daemon.Cleanup{Name: "docker", Run: closeWithContext(runtime.Close)}); err != nil {
			_ = runtime.Close()
			return Assembly{}, fmt.Errorf("register Docker cleanup: %w", err)
		}

		service, err := factories.newApplication(st, runtime, log, applicationSettingsFromConfig(cfg))
		if err != nil {
			return Assembly{}, fmt.Errorf("construct application service: %w", err)
		}
		reconcile, err := factories.newReconciler(runtime, service, log, reconcilerSettingsFromConfig(cfg))
		if err != nil {
			return Assembly{}, fmt.Errorf("construct reconciler: %w", err)
		}
		service.WithReconcileQueue(reconcile)
		watch, err := factories.newWatcher(runtime, reconcile, log)
		if err != nil {
			return Assembly{}, fmt.Errorf("construct event watcher: %w", err)
		}

		binding, err := factories.newRouter(routerOptionsFromConfig(cfg, log, st, runtime, service))
		if err != nil {
			return Assembly{}, fmt.Errorf("construct HTTP router: %w", err)
		}
		return Assembly{
			Logger: log, Server: binding.server, Readiness: binding.readiness,
			Workers: []daemon.Worker{
				{Name: "reconciler", Run: func(ctx context.Context) error { reconcile.Run(ctx); return nil }},
				{Name: "watcher", Run: func(ctx context.Context) error { watch.Run(ctx); return nil }},
			},
		}, nil
	}
}

func closeWithContext(close func() error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return close()
	}
}
