package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/handler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/middleware"
)

const (
	minimumBodyLimit          = 1024
	maximumBodyLimit          = 10 * 1024 * 1024
	maximumRequestConcurrency = 65536
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
	IngressNetwork     string
	BodyLimit          int
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
	ReadinessTimeout   time.Duration
	RequestConcurrency int
	CORSAllowedOrigins []string
}

type Router struct {
	App    *fiber.App
	Health *handler.HealthHandler
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

	app := fiber.New(fiber.Config{
		AppName:                 "ModuleOS",
		BodyLimit:               options.BodyLimit,
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

	return &Router{App: app, Health: health}, nil
}

func validateRouterOptions(options RouterOptions) ([]string, error) {
	switch {
	case options.Logger == nil:
		return nil, fmt.Errorf("%w: logger is required", ErrInvalidRouterOptions)
	case options.Store == nil:
		return nil, fmt.Errorf("%w: health store is required", ErrInvalidRouterOptions)
	case options.Runtime == nil:
		return nil, fmt.Errorf("%w: health runtime is required", ErrInvalidRouterOptions)
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

func normalizeOrigins(origins []string) ([]string, error) {
	if len(origins) == 0 {
		return nil, nil
	}
	unique := make(map[string]struct{}, len(origins))
	for _, raw := range origins {
		raw = strings.TrimSpace(raw)
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.ForceQuery || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(parsed.Host, "*") || strings.HasSuffix(parsed.Host, ":") {
			return nil, fmt.Errorf("CORS origin %q must be an exact HTTP or HTTPS origin", raw)
		}
		if port := parsed.Port(); port != "" {
			value, err := strconv.Atoi(port)
			if err != nil || value < 1 || value > 65535 {
				return nil, fmt.Errorf("CORS origin %q must use a port between 1 and 65535", raw)
			}
		}
		scheme := strings.ToLower(parsed.Scheme)
		hostname := strings.ToLower(parsed.Hostname())
		if strings.Contains(hostname, "%") {
			return nil, fmt.Errorf("CORS origin %q must not contain an IPv6 zone identifier", raw)
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
		normalized := scheme + "://" + host
		unique[normalized] = struct{}{}
	}

	result := make([]string, 0, len(unique))
	for origin := range unique {
		result = append(result, origin)
	}
	sort.Strings(result)
	return result, nil
}
