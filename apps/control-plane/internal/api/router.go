package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/handler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/middleware"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
	"github.com/valyala/fasthttp"
)

const (
	minimumBodyLimit          = 1024
	maximumBodyLimit          = 10 * 1024 * 1024
	maximumRequestConcurrency = 65536
	requestHeaderBufferSize   = 8 * 1024
)

var recognizedHTTPMethods = []string{
	"ACL", "BASELINE-CONTROL", "BIND", "CHECKIN", "CHECKOUT", "CONNECT", "COPY",
	"DELETE", "GET", "HEAD", "LABEL", "LINK", "LOCK", "MERGE", "MKACTIVITY",
	"MKCALENDAR", "MKCOL", "MKREDIRECTREF", "MKWORKSPACE", "MOVE", "OPTIONS",
	"ORDERPATCH", "PATCH", "POST", "PRI", "PROPFIND", "PROPPATCH", "PUT", "QUERY",
	"REBIND", "REPORT", "SEARCH", "TRACE", "UNBIND", "UNCHECKOUT", "UNLINK",
	"UNLOCK", "UPDATE", "UPDATEREDIRECTREF", "VERSION-CONTROL",
}

var ErrInvalidRouterOptions = errors.New("invalid router options")

type RouterOptions struct {
	Logger             *slog.Logger
	Store              handler.HealthStore
	Runtime            handler.HealthRuntime
	Projects           handler.ProjectService
	Applications       handler.ApplicationService
	Deployments        handler.DeploymentService
	Logs               handler.LogService
	Environment        string
	APIKey             string
	IngressNetwork     string
	BodyLimit          int
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
	ReadinessTimeout   time.Duration
	RequestConcurrency int
	CORSAllowedOrigins []string
	LogOptions         handler.LogOptions
}

type Router struct {
	App    *fiber.App
	Health *handler.HealthHandler
	Logs   *handler.LogsHandler
}

func (r *Router) Serve(listener net.Listener) error {
	return r.App.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true})
}

func (r *Router) Shutdown(ctx context.Context) error {
	err := r.App.ShutdownWithContext(ctx)
	if errors.Is(err, fiber.ErrNotRunning) {
		return nil
	}
	return err
}

func NewRouter(options RouterOptions) (*Router, error) {
	origins, err := validateRouterOptions(options)
	if err != nil {
		return nil, err
	}

	health, err := handler.NewHealthHandler(options.Store, options.Runtime, strings.TrimSpace(options.IngressNetwork), options.ReadinessTimeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRouterOptions, err)
	}
	concurrency, err := middleware.Concurrency(options.RequestConcurrency)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRouterOptions, err)
	}
	projects, err := handler.NewProjectHandler(options.Projects, options.Logger)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRouterOptions, err)
	}
	applications, err := handler.NewApplicationHandler(options.Applications, options.Logger)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRouterOptions, err)
	}
	deployments, err := handler.NewDeploymentHandler(options.Deployments, options.Logger, time.Now)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRouterOptions, err)
	}
	logs, err := handler.NewLogsHandler(options.Logs, options.Logger, options.LogOptions)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRouterOptions, err)
	}
	var authenticate fiber.Handler
	if options.APIKey != "" {
		authenticate, err = middleware.BearerAuth(options.APIKey)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidRouterOptions, err)
		}
	}

	app := fiber.New(fiber.Config{
		AppName:                 "ModuleOS",
		BodyLimit:               options.BodyLimit,
		ReadBufferSize:          requestHeaderBufferSize,
		ReadTimeout:             options.ReadTimeout,
		WriteTimeout:            options.WriteTimeout,
		IdleTimeout:             options.IdleTimeout,
		StrictRouting:           true,
		CaseSensitive:           true,
		DisableHeadAutoRegister: true,
		TrustProxy:              false,
		ProxyHeader:             "",
		RequestMethods:          recognizedHTTPMethods,
		ErrorHandler:            apiresponse.NewErrorHandler(options.Logger),
	})
	app.Server().HeaderReceived = func(header *fasthttp.RequestHeader) fasthttp.RequestConfig {
		if isLogStreamRequest(header.Method(), header.RequestURI()) {
			return fasthttp.RequestConfig{WriteTimeout: options.LogOptions.MaxDuration}
		}
		return fasthttp.RequestConfig{}
	}
	app.Hooks().OnPreShutdown(func() error {
		logs.CloseActiveStreams()
		return nil
	})
	app.Use(middleware.RequestID())
	app.Use(middleware.RequestLogger(options.Logger))
	app.Use(middleware.Recovery(options.Logger))
	app.Use(middleware.SecurityHeaders())
	if len(origins) > 0 {
		app.Use(cors.New(cors.Config{
			AllowOrigins:        origins,
			AllowMethods:        []string{fiber.MethodGet, fiber.MethodPost, fiber.MethodPatch, fiber.MethodDelete, fiber.MethodOptions},
			AllowHeaders:        []string{fiber.HeaderAuthorization, fiber.HeaderContentType, fiber.HeaderIfMatch, apiresponse.RequestIDHeader, "Idempotency-Key"},
			ExposeHeaders:       []string{apiresponse.RequestIDHeader, fiber.HeaderETag, fiber.HeaderLocation, fiber.HeaderRetryAfter},
			AllowCredentials:    false,
			AllowPrivateNetwork: false,
			MaxAge:              600,
		}))
	}
	v1 := app.Group("/api/v1")
	v1.Get("/live", health.Live)
	v1.Get("/version", health.Version)
	v1.Use(concurrency)
	v1.Get("/ready", health.Ready)
	protected := v1.Group("")
	protected.Use(middleware.NoStore())
	if authenticate != nil {
		protected.Use(authenticate)
	}
	protected.Post("/projects", projects.Create)
	protected.Get("/projects", projects.List)
	protected.Get("/projects/:slug/apps", projects.ListApplications)
	protected.Post("/projects/:slug/links", projects.CreateLink)
	protected.Get("/projects/:slug/links", projects.ListLinks)
	protected.Delete("/projects/:slug/links/:link_id", projects.DeleteLink)
	protected.Get("/projects/:slug", projects.Get)
	protected.Delete("/projects/:slug", projects.Delete)
	protected.Post("/apps", applications.Create)
	protected.Get("/apps", applications.List)
	protected.Get("/apps/:name", applications.Get)
	protected.Patch("/apps/:name", applications.Update)
	protected.Delete("/apps/:name", applications.Delete)
	protected.Post("/apps/:name/start", applications.Start)
	protected.Post("/apps/:name/stop", applications.Stop)
	protected.Post("/apps/:name/scale", applications.Scale)
	protected.Post("/apps/:name/deploy", deployments.Deploy)
	protected.Get("/apps/:name/deployments", deployments.List)
	protected.Get("/apps/:name/deployments/:id", deployments.Get)
	protected.Post("/apps/:name/deployments/:id/rollback", deployments.Rollback)
	protected.Get("/apps/:name/logs", logs.Tail)
	protected.Get("/apps/:name/logs/stream", logs.Stream)

	return &Router{App: app, Health: health, Logs: logs}, nil
}

func validateRouterOptions(options RouterOptions) ([]string, error) {
	switch {
	case options.Logger == nil:
		return nil, fmt.Errorf("%w: logger is required", ErrInvalidRouterOptions)
	case options.Store == nil:
		return nil, fmt.Errorf("%w: health store is required", ErrInvalidRouterOptions)
	case options.Runtime == nil:
		return nil, fmt.Errorf("%w: health runtime is required", ErrInvalidRouterOptions)
	case options.Applications == nil:
		return nil, fmt.Errorf("%w: application service is required", ErrInvalidRouterOptions)
	case options.Deployments == nil:
		return nil, fmt.Errorf("%w: deployment service is required", ErrInvalidRouterOptions)
	case options.Logs == nil:
		return nil, fmt.Errorf("%w: log service is required", ErrInvalidRouterOptions)
	case options.Environment != "development" && options.Environment != "production":
		return nil, fmt.Errorf("%w: environment must be development or production", ErrInvalidRouterOptions)
	case options.Environment == "production" && len(options.APIKey) < 32:
		return nil, fmt.Errorf("%w: production API key must contain at least 32 bytes", ErrInvalidRouterOptions)
	case strings.TrimSpace(options.IngressNetwork) == "":
		return nil, fmt.Errorf("%w: ingress network is required", ErrInvalidRouterOptions)
	case options.BodyLimit < minimumBodyLimit || options.BodyLimit > maximumBodyLimit:
		return nil, fmt.Errorf("%w: body limit must be between %d and %d bytes", ErrInvalidRouterOptions, minimumBodyLimit, maximumBodyLimit)
	case options.ReadTimeout <= 0 || options.WriteTimeout <= 0 || options.IdleTimeout <= 0 || options.ReadinessTimeout <= 0:
		return nil, fmt.Errorf("%w: HTTP and readiness timeouts must be positive", ErrInvalidRouterOptions)
	case options.RequestConcurrency < 1 || options.RequestConcurrency > maximumRequestConcurrency:
		return nil, fmt.Errorf("%w: request concurrency must be between 1 and %d", ErrInvalidRouterOptions, maximumRequestConcurrency)
	}

	origins, err := normalizeOrigins(options.CORSAllowedOrigins)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRouterOptions, err)
	}
	return origins, nil
}

func isLogStreamRequest(method, requestURI []byte) bool {
	if string(method) != fiber.MethodGet {
		return false
	}
	path := string(requestURI)
	if query := strings.IndexByte(path, '?'); query >= 0 {
		path = path[:query]
	}
	const prefix = "/api/v1/apps/"
	const suffix = "/logs/stream"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if len(name) < 1 || len(name) > 63 || !asciiLowerDigit(name[0]) || !asciiLowerDigit(name[len(name)-1]) {
		return false
	}
	for index := 1; index < len(name)-1; index++ {
		if !asciiLowerDigit(name[index]) && name[index] != '-' {
			return false
		}
	}
	return true
}

func asciiLowerDigit(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9')
}

func normalizeOrigins(origins []string) ([]string, error) {
	return config.NormalizeCORSOrigins(origins)
}
