package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/handler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

type routerHealthStore struct {
	pingErr      error
	listErr      error
	applications []*store.Application
	panicOnPing  bool
	waitForPing  bool
	pingStarted  chan struct{}
}

func (s *routerHealthStore) Ping(ctx context.Context) error {
	if s.panicOnPing {
		panic("private-readiness-panic")
	}
	if s.pingStarted != nil {
		close(s.pingStarted)
	}
	if s.waitForPing {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.pingErr
}

func (s *routerHealthStore) ListApplications(context.Context) ([]*store.Application, error) {
	return s.applications, s.listErr
}

type routerHealthRuntime struct {
	healthErr  error
	networkErr error
}

func (r *routerHealthRuntime) Health(context.Context) error {
	return r.healthErr
}

func (r *routerHealthRuntime) GetNetwork(_ context.Context, name string) (*swarm.NetworkInfo, error) {
	if r.networkErr != nil {
		return nil, r.networkErr
	}
	return &swarm.NetworkInfo{Name: name, Driver: "overlay", Attachable: true}, nil
}

func validRouterOptions() RouterOptions {
	return RouterOptions{
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:              &routerHealthStore{},
		Runtime:            &routerHealthRuntime{},
		IngressNetwork:     "moduleos-ingress",
		BodyLimit:          1024,
		ReadTimeout:        time.Second,
		WriteTimeout:       time.Second,
		IdleTimeout:        time.Second,
		ReadinessTimeout:   time.Second,
		RequestConcurrency: 8,
	}
}

func sendRequest(t *testing.T, app *fiber.App, request *http.Request, config ...fiber.TestConfig) *http.Response {
	t.Helper()
	response, err := app.Test(request, config...)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func decodeJSONResponse[T any](t *testing.T, response *http.Response) T {
	t.Helper()
	var result T
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

func startRealHTTPServer(t *testing.T, app *fiber.App) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true})
	}()
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = app.ShutdownWithContext(shutdownContext)
		_ = listener.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("HTTP test server did not stop")
		}
	})
	return "http://" + listener.Addr().String()
}

func TestNewRouterRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RouterOptions)
	}{
		{name: "logger", mutate: func(options *RouterOptions) { options.Logger = nil }},
		{name: "store", mutate: func(options *RouterOptions) { options.Store = nil }},
		{name: "runtime", mutate: func(options *RouterOptions) { options.Runtime = nil }},
		{name: "ingress", mutate: func(options *RouterOptions) { options.IngressNetwork = " " }},
		{name: "body too small", mutate: func(options *RouterOptions) { options.BodyLimit = 1023 }},
		{name: "body too large", mutate: func(options *RouterOptions) { options.BodyLimit = 10*1024*1024 + 1 }},
		{name: "read timeout", mutate: func(options *RouterOptions) { options.ReadTimeout = 0 }},
		{name: "write timeout", mutate: func(options *RouterOptions) { options.WriteTimeout = -time.Second }},
		{name: "idle timeout", mutate: func(options *RouterOptions) { options.IdleTimeout = 0 }},
		{name: "readiness timeout", mutate: func(options *RouterOptions) { options.ReadinessTimeout = 0 }},
		{name: "concurrency", mutate: func(options *RouterOptions) { options.RequestConcurrency = 0 }},
		{name: "concurrency too large", mutate: func(options *RouterOptions) { options.RequestConcurrency = maximumRequestConcurrency + 1 }},
		{name: "wildcard CORS", mutate: func(options *RouterOptions) { options.CORSAllowedOrigins = []string{"*"} }},
		{name: "origin path", mutate: func(options *RouterOptions) { options.CORSAllowedOrigins = []string{"https://example.com/path"} }},
		{name: "origin credentials", mutate: func(options *RouterOptions) { options.CORSAllowedOrigins = []string{"https://user@example.com"} }},
		{name: "origin query", mutate: func(options *RouterOptions) { options.CORSAllowedOrigins = []string{"https://example.com?x=1"} }},
		{name: "origin empty query", mutate: func(options *RouterOptions) { options.CORSAllowedOrigins = []string{"https://example.com?"} }},
		{name: "origin fragment", mutate: func(options *RouterOptions) { options.CORSAllowedOrigins = []string{"https://example.com#fragment"} }},
		{name: "origin scheme", mutate: func(options *RouterOptions) { options.CORSAllowedOrigins = []string{"file://example.com"} }},
		{name: "origin empty port", mutate: func(options *RouterOptions) { options.CORSAllowedOrigins = []string{"https://example.com:"} }},
		{name: "origin zero port", mutate: func(options *RouterOptions) { options.CORSAllowedOrigins = []string{"https://example.com:0"} }},
		{name: "origin oversized port", mutate: func(options *RouterOptions) { options.CORSAllowedOrigins = []string{"https://example.com:65536"} }},
		{name: "IPv6 zone identifier", mutate: func(options *RouterOptions) { options.CORSAllowedOrigins = []string{"https://[fe80::1%25en0]"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validRouterOptions()
			test.mutate(&options)
			if _, err := NewRouter(options); !errors.Is(err, ErrInvalidRouterOptions) {
				t.Fatalf("error = %v, want ErrInvalidRouterOptions", err)
			}
		})
	}
}

func FuzzNormalizeOrigins(f *testing.F) {
	for _, origin := range []string{"https://example.com", " HTTPS://EXAMPLE.COM:8443 ", "*", "null", "https://user@example.com", "https://example.com/path", "https://example.com?", "https://example.com:", "https://example.com:0", "https://example.com:65536"} {
		f.Add(origin)
	}
	f.Fuzz(func(t *testing.T, origin string) {
		normalized, err := normalizeOrigins([]string{origin})
		if err != nil {
			return
		}
		if len(normalized) != 1 || (!strings.HasPrefix(normalized[0], "http://") && !strings.HasPrefix(normalized[0], "https://")) {
			t.Fatalf("accepted origin %q normalized to %v", origin, normalized)
		}
	})
}

func TestNewRouterRejectsTypedNilHealthDependencies(t *testing.T) {
	var nilStore *routerHealthStore
	options := validRouterOptions()
	options.Store = nilStore
	if _, err := NewRouter(options); !errors.Is(err, ErrInvalidRouterOptions) {
		t.Fatalf("store error = %v", err)
	}
	var nilRuntime *routerHealthRuntime
	options = validRouterOptions()
	options.Runtime = nilRuntime
	if _, err := NewRouter(options); !errors.Is(err, ErrInvalidRouterOptions) {
		t.Fatalf("runtime error = %v", err)
	}
}

func TestNormalizeOriginsTrimsCanonicalizesDeduplicatesAndSorts(t *testing.T) {
	origins, err := normalizeOrigins([]string{
		" HTTPS://EXAMPLE.COM:8443 ",
		"http://localhost:3000",
		"https://example.com:8443",
		"https://EXAMPLE.COM:443",
		"https://example.com",
		"http://EXAMPLE.COM:80",
		"http://example.com",
		"https://[2001:DB8::1]:443",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://example.com", "http://localhost:3000", "https://[2001:db8::1]", "https://example.com", "https://example.com:8443"}
	if strings.Join(origins, ",") != strings.Join(want, ",") {
		t.Fatalf("origins = %v, want %v", origins, want)
	}
	if origins, err := normalizeOrigins(nil); err != nil || origins != nil {
		t.Fatalf("nil origins = %v/%v", origins, err)
	}
}

func TestRouterRegistersOnlyPhase6ASystemRoutes(t *testing.T) {
	router, err := NewRouter(validRouterOptions())
	if err != nil {
		t.Fatal(err)
	}
	got := routeInventory(router.App)
	want := []string{
		"GET /api/v1/live",
		"GET /api/v1/ready",
		"GET /api/v1/version",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("routes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRouterSystemEndpointsArePublicAndHardened(t *testing.T) {
	router, err := NewRouter(validRouterOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/live", "/api/v1/ready", "/api/v1/version"} {
		t.Run(path, func(t *testing.T) {
			response := sendRequest(t, router.App, httptest.NewRequest(http.MethodGet, path, nil))
			if response.StatusCode != fiber.StatusOK {
				t.Fatalf("status = %d", response.StatusCode)
			}
			if _, err := uuid.Parse(response.Header.Get(apiresponse.RequestIDHeader)); err != nil {
				t.Fatalf("request ID invalid: %v", err)
			}
			if response.Header.Get(fiber.HeaderXContentTypeOptions) != "nosniff" || response.Header.Get("Referrer-Policy") != "no-referrer" || response.Header.Get(fiber.HeaderCacheControl) != "no-store" {
				t.Fatalf("headers = %#v", response.Header)
			}
		})
	}
}

func TestRouterUsesStableErrorsAndStrictPaths(t *testing.T) {
	router, err := NewRouter(validRouterOptions())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		method     string
		path       string
		wantStatus int
		wantCode   string
	}{
		{method: http.MethodGet, path: "/api/v1/health", wantStatus: 404, wantCode: "not_found"},
		{method: http.MethodGet, path: "/api/v1/live/", wantStatus: 404, wantCode: "not_found"},
		{method: http.MethodGet, path: "/API/v1/live", wantStatus: 404, wantCode: "not_found"},
		{method: http.MethodPost, path: "/api/v1/live", wantStatus: 405, wantCode: "method_not_allowed"},
		{method: "PROPFIND", path: "/api/v1/live", wantStatus: 405, wantCode: "method_not_allowed"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := sendRequest(t, router.App, httptest.NewRequest(test.method, test.path, nil))
			body := decodeJSONResponse[apiresponse.ErrorResponse](t, response)
			if response.StatusCode != test.wantStatus || body.Error.Code != test.wantCode || body.Error.RequestID == "" {
				t.Fatalf("status/body = %d/%#v", response.StatusCode, body)
			}
			if response.Header.Get(fiber.HeaderXContentTypeOptions) != "nosniff" || response.Header.Get(fiber.HeaderCacheControl) != "no-store" {
				t.Fatalf("error security headers = %#v", response.Header)
			}
		})
	}
}

func TestRouterCORSAllowsOnlyConfiguredExactOrigins(t *testing.T) {
	options := validRouterOptions()
	options.CORSAllowedOrigins = []string{"https://dashboard.example.com"}
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}

	allowed := httptest.NewRequest(http.MethodOptions, "/api/v1/live", nil)
	allowed.Header.Set(fiber.HeaderOrigin, "https://dashboard.example.com")
	allowed.Header.Set(fiber.HeaderAccessControlRequestMethod, http.MethodGet)
	allowedResponse := sendRequest(t, router.App, allowed)
	if allowedResponse.Header.Get(fiber.HeaderAccessControlAllowOrigin) != "https://dashboard.example.com" || allowedResponse.Header.Get(fiber.HeaderAccessControlAllowCredentials) != "" {
		t.Fatalf("allowed CORS headers = %#v", allowedResponse.Header)
	}

	denied := httptest.NewRequest(http.MethodOptions, "/api/v1/live", nil)
	denied.Header.Set(fiber.HeaderOrigin, "https://attacker.example.com")
	denied.Header.Set(fiber.HeaderAccessControlRequestMethod, http.MethodGet)
	deniedResponse := sendRequest(t, router.App, denied)
	if deniedResponse.Header.Get(fiber.HeaderAccessControlAllowOrigin) != "" {
		t.Fatalf("denied origin was allowed: %#v", deniedResponse.Header)
	}
}

func TestRouterConcurrencyProtectsReadinessButNotLightweightSystemRoutes(t *testing.T) {
	options := validRouterOptions()
	options.RequestConcurrency = 1
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBlock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBlock)
	router.App.Get("/api/v1/block", func(c fiber.Ctx) error {
		close(entered)
		<-release
		return c.SendStatus(fiber.StatusNoContent)
	})
	router.App.Get("/api/v1/limited", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })

	blockedDone := make(chan *http.Response, 1)
	go func() {
		response, _ := router.App.Test(httptest.NewRequest(http.MethodGet, "/api/v1/block", nil), fiber.TestConfig{Timeout: 0})
		blockedDone <- response
	}()
	<-entered

	limited := sendRequest(t, router.App, httptest.NewRequest(http.MethodGet, "/api/v1/limited", nil))
	limitedBody := decodeJSONResponse[apiresponse.ErrorResponse](t, limited)
	if limited.StatusCode != fiber.StatusServiceUnavailable || limitedBody.Error.Code != "server_busy" {
		t.Fatalf("limited response = %d/%#v", limited.StatusCode, limitedBody)
	}
	readyRequest := httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil)
	ready := sendRequest(t, router.App, readyRequest)
	readyBody := validateOpenAPIResponse(t, contractRouterForTest(t), readyRequest, ready)
	var readyError apiresponse.ErrorResponse
	if err := json.Unmarshal(readyBody, &readyError); err != nil {
		t.Fatalf("decode readiness overload response: %v", err)
	}
	if ready.StatusCode != fiber.StatusServiceUnavailable || readyError.Error.Code != "server_busy" || !readyError.Error.Retryable || ready.Header.Get(fiber.HeaderRetryAfter) != "1" {
		t.Fatalf("readiness overload response = %d/%#v, Retry-After=%q", ready.StatusCode, readyError, ready.Header.Get(fiber.HeaderRetryAfter))
	}
	live := sendRequest(t, router.App, httptest.NewRequest(http.MethodGet, "/api/v1/live", nil))
	if live.StatusCode != fiber.StatusOK {
		t.Fatalf("liveness was blocked by resource concurrency, status = %d", live.StatusCode)
	}
	version := sendRequest(t, router.App, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if version.StatusCode != fiber.StatusOK {
		t.Fatalf("version was blocked by resource concurrency, status = %d", version.StatusCode)
	}

	releaseBlock()
	if response := <-blockedDone; response == nil || response.StatusCode != fiber.StatusNoContent {
		t.Fatalf("blocked response = %#v", response)
	} else {
		_ = response.Body.Close()
	}
}

func TestRouterBodyLimitUsesStableErrorEnvelope(t *testing.T) {
	var logs bytes.Buffer
	options := validRouterOptions()
	options.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	router.App.Post("/body", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
	baseURL := startRealHTTPServer(t, router.App)
	request, err := http.NewRequest(http.MethodPost, baseURL+"/body", strings.NewReader(strings.Repeat("x", 1025)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(fiber.HeaderContentType, fiber.MIMETextPlain)
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	body := decodeJSONResponse[apiresponse.ErrorResponse](t, response)
	if response.StatusCode != fiber.StatusRequestEntityTooLarge || body.Error.Code != "payload_too_large" {
		t.Fatalf("status/body = %d/%#v", response.StatusCode, body)
	}
	if response.Header.Get(fiber.HeaderXContentTypeOptions) != "nosniff" || response.Header.Get("Referrer-Policy") != "no-referrer" || response.Header.Get(fiber.HeaderCacheControl) != "no-store" {
		t.Fatalf("parser error security headers = %#v", response.Header)
	}
	if !strings.Contains(logs.String(), "status=413") || !strings.Contains(logs.String(), "route=<unmatched>") || strings.Contains(logs.String(), "status=200") {
		t.Fatalf("parser failure was logged incorrectly: %q", logs.String())
	}
}

func TestRouterHeaderLimitUsesStableErrorEnvelope(t *testing.T) {
	var logs bytes.Buffer
	options := validRouterOptions()
	options.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := startRealHTTPServer(t, router.App)
	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Oversized", strings.Repeat("x", 8192))
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	body := decodeJSONResponse[apiresponse.ErrorResponse](t, response)
	if response.StatusCode != fiber.StatusRequestHeaderFieldsTooLarge || body.Error.Code != "headers_too_large" {
		t.Fatalf("status/body = %d/%#v", response.StatusCode, body)
	}
	if _, err := uuid.Parse(body.Error.RequestID); err != nil {
		t.Fatalf("request ID = %q: %v", body.Error.RequestID, err)
	}
	if response.Header.Get(fiber.HeaderXContentTypeOptions) != "nosniff" || response.Header.Get("Referrer-Policy") != "no-referrer" || response.Header.Get(fiber.HeaderCacheControl) != "no-store" {
		t.Fatalf("parser error security headers = %#v", response.Header)
	}
	if !strings.Contains(logs.String(), "status=431") || !strings.Contains(logs.String(), "route=<unmatched>") || strings.Contains(logs.String(), "status=200") {
		t.Fatalf("parser failure was logged incorrectly: %q", logs.String())
	}
}

func TestRouterShutdownCancelsInFlightReadinessProbe(t *testing.T) {
	pingStarted := make(chan struct{})
	options := validRouterOptions()
	options.ReadinessTimeout = handler.MaximumReadinessTimeout
	options.Store = &routerHealthStore{waitForPing: true, pingStarted: pingStarted}
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := startRealHTTPServer(t, router.App)
	type requestResult struct {
		response *http.Response
		err      error
	}
	result := make(chan requestResult, 1)
	go func() {
		response, requestErr := (&http.Client{Timeout: 2 * time.Second}).Get(baseURL + "/api/v1/ready")
		result <- requestResult{response: response, err: requestErr}
	}()
	select {
	case <-pingStarted:
	case <-time.After(time.Second):
		t.Fatal("readiness probe did not start")
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := router.App.ShutdownWithContext(shutdownContext); err != nil {
		t.Fatalf("shutdown did not cancel readiness probe: %v", err)
	}
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("readiness request failed during shutdown: %v", got.err)
		}
		defer func() { _ = got.response.Body.Close() }()
		if got.response.StatusCode != fiber.StatusServiceUnavailable {
			t.Fatalf("readiness status during shutdown = %d, want 503", got.response.StatusCode)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness request remained blocked after shutdown")
	}
}

func TestRouterRecoversPanicWithoutLeakingValue(t *testing.T) {
	var logs bytes.Buffer
	options := validRouterOptions()
	options.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	router.App.Get("/panic", func(fiber.Ctx) error { panic("private-panic-value") })
	response := sendRequest(t, router.App, httptest.NewRequest(http.MethodGet, "/panic", nil))
	body := decodeJSONResponse[apiresponse.ErrorResponse](t, response)
	if response.StatusCode != fiber.StatusInternalServerError || body.Error.Code != "internal_error" {
		t.Fatalf("status/body = %d/%#v", response.StatusCode, body)
	}
	if strings.Contains(logs.String(), "private-panic-value") || strings.Contains(body.Error.Message, "private-panic-value") {
		t.Fatalf("panic value leaked: logs=%q body=%#v", logs.String(), body)
	}
	if !strings.Contains(logs.String(), "HTTP request") || !strings.Contains(logs.String(), "route=/panic") || !strings.Contains(logs.String(), "status=500") {
		t.Fatalf("panic request was not recorded by access logging: %q", logs.String())
	}
}

func TestRouterRoutesMatchOpenAPIContract(t *testing.T) {
	router, err := NewRouter(validRouterOptions())
	if err != nil {
		t.Fatal(err)
	}
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile("../../../../packages/api-contract/openapi.yaml")
	if err != nil {
		t.Fatalf("load OpenAPI contract: %v", err)
	}
	if err := document.Validate(t.Context()); err != nil {
		t.Fatalf("validate OpenAPI contract: %v", err)
	}
	if len(document.Servers) != 1 {
		t.Fatalf("server count = %d, want 1", len(document.Servers))
	}
	prefix := strings.TrimSuffix(document.Servers[0].URL, "/")
	var contractRoutes []string
	for _, path := range document.Paths.InMatchingOrder() {
		item := document.Paths.Find(path)
		for method := range item.Operations() {
			contractRoutes = append(contractRoutes, strings.ToUpper(method)+" "+prefix+path)
		}
	}
	sort.Strings(contractRoutes)
	if got := routeInventory(router.App); strings.Join(got, "\n") != strings.Join(contractRoutes, "\n") {
		t.Fatalf("runtime routes:\n%s\ncontract routes:\n%s", strings.Join(got, "\n"), strings.Join(contractRoutes, "\n"))
	}
}

func TestRouterSystemResponsesMatchOpenAPIContract(t *testing.T) {
	contractRouter := contractRouterForTest(t)
	tests := []struct {
		name    string
		path    string
		options RouterOptions
	}{
		{name: "liveness", path: "/api/v1/live", options: validRouterOptions()},
		{name: "readiness", path: "/api/v1/ready", options: validRouterOptions()},
		{name: "readiness unavailable", path: "/api/v1/ready", options: func() RouterOptions {
			options := validRouterOptions()
			options.Store = &routerHealthStore{pingErr: errors.New("database unavailable")}
			return options
		}()},
		{name: "readiness panic", path: "/api/v1/ready", options: func() RouterOptions {
			options := validRouterOptions()
			options.Store = &routerHealthStore{panicOnPing: true}
			return options
		}()},
		{name: "version", path: "/api/v1/version", options: validRouterOptions()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router, err := NewRouter(test.options)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := sendRequest(t, router.App, request)
			validateOpenAPIResponse(t, contractRouter, request, response)
		})
	}
}

func contractRouterForTest(t *testing.T) routers.Router {
	t.Helper()
	contractRouter, err := legacy.NewRouter(loadOpenAPIContract(t))
	if err != nil {
		t.Fatalf("create contract router: %v", err)
	}
	return contractRouter
}

func validateOpenAPIResponse(t *testing.T, contractRouter routers.Router, request *http.Request, response *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	route, pathParams, err := contractRouter.FindRoute(request)
	if err != nil {
		t.Fatalf("find contract route: %v", err)
	}
	validationInput := &openapi3filter.RequestValidationInput{Request: request, PathParams: pathParams, Route: route}
	responseInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: validationInput,
		Status:                 response.StatusCode,
		Header:                 response.Header,
		Options: &openapi3filter.Options{
			IncludeResponseStatus: true,
			SchemaValidationOptions: []openapi3.SchemaValidationOption{
				openapi3.EnableFormatValidation(),
				openapi3.EnableJSONSchema2020(),
			},
		},
	}
	responseInput.SetBodyBytes(body)
	if err := openapi3filter.ValidateResponse(t.Context(), responseInput); err != nil {
		t.Fatalf("response %d %s does not match OpenAPI: %v\nbody: %s", response.StatusCode, request.URL.Path, err, body)
	}
	return body
}

func loadOpenAPIContract(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile("../../../../packages/api-contract/openapi.yaml")
	if err != nil {
		t.Fatalf("load OpenAPI contract: %v", err)
	}
	if err := document.Validate(t.Context(), openapi3.EnableExamplesValidation(), openapi3.EnableSchemaFormatValidation()); err != nil {
		t.Fatalf("validate OpenAPI contract: %v", err)
	}
	return document
}

func routeInventory(app *fiber.App) []string {
	routes := app.GetRoutes(true)
	result := make([]string, 0, len(routes))
	for _, route := range routes {
		result = append(result, route.Method+" "+route.Path)
	}
	sort.Strings(result)
	return result
}
