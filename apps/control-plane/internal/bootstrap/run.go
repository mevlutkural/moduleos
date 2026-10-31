package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/banner"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/buildinfo"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/daemon"
	modulelogger "github.com/mevlutkural/moduleos/apps/control-plane/internal/logger"
)

type process interface {
	Run(context.Context) daemon.Outcome
}

type runDependencies struct {
	loadConfig    func() (*config.Config, error)
	newLogger     func(string, ...string) *slog.Logger
	printBanner   func(io.Writer, string, string, string, string) error
	notifyContext func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	prepare       func(Options) (process, error)
	listen        ListenerFactory
	newBuilder    func(*config.Config, *slog.Logger) Builder
	stdout        io.Writer
	stderr        io.Writer
}

// Run composes and executes the production control-plane daemon.
func Run() int {
	return run(defaultRunDependencies())
}

func defaultRunDependencies() runDependencies {
	return runDependencies{
		loadConfig:    config.Load,
		newLogger:     modulelogger.New,
		printBanner:   banner.Print,
		notifyContext: signal.NotifyContext,
		prepare: func(options Options) (process, error) {
			return Prepare(options)
		},
		listen: net.Listen,
		newBuilder: func(cfg *config.Config, log *slog.Logger) Builder {
			return newProductionBuilder(cfg, log, defaultProductionFactories())
		},
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

func run(dependencies runDependencies) int {
	if !validRunDependencies(dependencies) {
		return 1
	}
	fallback := slog.New(slog.NewJSONHandler(dependencies.stderr, nil))
	cfg, err := dependencies.loadConfig()
	if err != nil || cfg == nil {
		fallback.Error("bootstrap failed", configFailureAttributes(err)...)
		return 1
	}

	log := dependencies.newLogger(cfg.Env, cfg.LogLevel)
	if log == nil {
		fallback.Error("bootstrap failed", "phase", "logger", "reason", reasonLoggerUnavailable)
		return 1
	}
	if err := dependencies.printBanner(dependencies.stdout, buildinfo.Current().Version, cfg.Env, cfg.Port, cfg.BaseDomain); err != nil {
		log.Error("bootstrap failed", "phase", "banner", "reason", reasonBannerFailed)
		return 1
	}
	log.Info("configuration loaded", "config", cfg.SafeSummary())

	operator, stopSignals := dependencies.notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	prepared, err := dependencies.prepare(productionOptions(cfg, dependencies.newBuilder(cfg, log), dependencies.listen))
	if err != nil || isNilInterface(prepared) {
		log.Error("bootstrap failed", "phase", "prepare", "reason", startupReason(err, reasonInternalError))
		return 1
	}
	return prepared.Run(operator).ExitCode
}

func productionOptions(cfg *config.Config, builder Builder, listen ListenerFactory) Options {
	return Options{
		Build: builder, Listen: listen, Network: "tcp",
		Address:        net.JoinHostPort(cfg.ListenAddress, cfg.Port),
		CleanupTimeout: cfg.ShutdownTimeout, ShutdownTimeout: cfg.ShutdownTimeout,
	}
}

func validRunDependencies(dependencies runDependencies) bool {
	return dependencies.loadConfig != nil && dependencies.newLogger != nil && dependencies.printBanner != nil &&
		dependencies.notifyContext != nil && dependencies.prepare != nil && dependencies.listen != nil &&
		dependencies.newBuilder != nil && dependencies.stdout != nil && dependencies.stderr != nil
}
