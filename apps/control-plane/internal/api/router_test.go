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
	controlapp "github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/testkit/swarmfake"
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

type routerProjectService struct{}

type routerApplicationService struct{}

type routerRecordingQueue struct {
	mu    sync.Mutex
	names []string
}

func (q *routerRecordingQueue) Enqueue(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.names = append(q.names, name)
}

func (q *routerRecordingQueue) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.names)
}

func (*routerApplicationService) CreateApp(context.Context, controlapp.CreateAppRequest) (*store.Application, error) {
	return nil, store.ErrNotFound
}

func (*routerApplicationService) ListApps(context.Context) ([]*store.Application, error) {
	return []*store.Application{}, nil
}

func (*routerApplicationService) GetApp(context.Context, string) (*store.Application, error) {
	return nil, store.ErrNotFound
}

func (*routerApplicationService) UpdateApp(context.Context, controlapp.UpdateAppRequest) (*store.Application, error) {
	return nil, store.ErrNotFound
}

func (*routerApplicationService) DeleteAppIntent(context.Context, string, int64) (*store.Application, error) {
	return nil, store.ErrNotFound
}

func (*routerApplicationService) SetRunState(context.Context, string, store.DesiredRunState, int64) (*store.Application, error) {
	return nil, store.ErrNotFound
}

func (*routerApplicationService) ScaleAppIntent(context.Context, string, int, int64) (*store.Application, error) {
	return nil, store.ErrNotFound
}

func (*routerProjectService) CreateProject(context.Context, controlapp.CreateProjectRequest) (*store.Project, error) {
	return nil, store.ErrNotFound
}

func (*routerProjectService) ListProjects(context.Context) ([]*store.Project, error) {
	return []*store.Project{}, nil
}

func (*routerProjectService) GetProject(context.Context, string) (*store.Project, error) {
	return nil, store.ErrNotFound
}

func (*routerProjectService) DeleteProject(context.Context, string) error {
	return store.ErrNotFound
}

func (*routerProjectService) ListProjectApps(context.Context, string) ([]*store.Application, error) {
	return []*store.Application{}, nil
}

func (*routerProjectService) CreateProjectLink(context.Context, string, string, string) (*controlapp.ProjectLinkDetails, error) {
	return nil, store.ErrNotFound
}

func (*routerProjectService) ListProjectLinks(context.Context, string) ([]*controlapp.ProjectLinkDetails, error) {
	return []*controlapp.ProjectLinkDetails{}, nil
}

func (*routerProjectService) DeleteProjectLink(context.Context, string, string) error {
	return store.ErrNotFound
}

type routerErrorProjectService struct {
	err error
}

func (s *routerErrorProjectService) CreateProject(context.Context, controlapp.CreateProjectRequest) (*store.Project, error) {
	return nil, s.err
}

func (s *routerErrorProjectService) ListProjects(context.Context) ([]*store.Project, error) {
	return nil, s.err
}

func (s *routerErrorProjectService) GetProject(context.Context, string) (*store.Project, error) {
	return nil, s.err
}

func (s *routerErrorProjectService) DeleteProject(context.Context, string) error {
	return s.err
}

func (s *routerErrorProjectService) ListProjectApps(context.Context, string) ([]*store.Application, error) {
	return nil, s.err
}

func (s *routerErrorProjectService) CreateProjectLink(context.Context, string, string, string) (*controlapp.ProjectLinkDetails, error) {
	return nil, s.err
}

func (s *routerErrorProjectService) ListProjectLinks(context.Context, string) ([]*controlapp.ProjectLinkDetails, error) {
	return nil, s.err
}

func (s *routerErrorProjectService) DeleteProjectLink(context.Context, string, string) error {
	return s.err
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
		Projects:           &routerProjectService{},
		Applications:       &routerApplicationService{},
		Environment:        "development",
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
		{name: "projects", mutate: func(options *RouterOptions) { options.Projects = nil }},
		{name: "applications", mutate: func(options *RouterOptions) { options.Applications = nil }},
		{name: "environment", mutate: func(options *RouterOptions) { options.Environment = "staging" }},
		{name: "production API key missing", mutate: func(options *RouterOptions) { options.Environment = "production" }},
		{name: "production API key short", mutate: func(options *RouterOptions) {
			options.Environment = "production"
			options.APIKey = strings.Repeat("a", 31)
		}},
		{name: "API key whitespace", mutate: func(options *RouterOptions) { options.APIKey = "invalid key" }},
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
	var nilProjects *routerProjectService
	options = validRouterOptions()
	options.Projects = nilProjects
	if _, err := NewRouter(options); !errors.Is(err, ErrInvalidRouterOptions) {
		t.Fatalf("projects error = %v", err)
	}
	var nilApplications *routerApplicationService
	options = validRouterOptions()
	options.Applications = nilApplications
	if _, err := NewRouter(options); !errors.Is(err, ErrInvalidRouterOptions) {
		t.Fatalf("applications error = %v", err)
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

func TestRouterRegistersPhase6C1ContractRoutes(t *testing.T) {
	router, err := NewRouter(validRouterOptions())
	if err != nil {
		t.Fatal(err)
	}
	got := routeInventory(router.App)
	want := []string{
		"DELETE /api/v1/apps/:name",
		"DELETE /api/v1/projects/:slug",
		"DELETE /api/v1/projects/:slug/links/:link_id",
		"GET /api/v1/apps",
		"GET /api/v1/apps/:name",
		"GET /api/v1/live",
		"GET /api/v1/projects",
		"GET /api/v1/projects/:slug",
		"GET /api/v1/projects/:slug/apps",
		"GET /api/v1/projects/:slug/links",
		"GET /api/v1/ready",
		"GET /api/v1/version",
		"PATCH /api/v1/apps/:name",
		"POST /api/v1/apps",
		"POST /api/v1/apps/:name/scale",
		"POST /api/v1/apps/:name/start",
		"POST /api/v1/apps/:name/stop",
		"POST /api/v1/projects",
		"POST /api/v1/projects/:slug/links",
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
		{method: http.MethodGet, path: "/api/v1/apps/", wantStatus: 404, wantCode: "not_found"},
		{method: http.MethodGet, path: "/api/v1/apps/api/", wantStatus: 404, wantCode: "not_found"},
		{method: http.MethodGet, path: "/api/v1/apps/api%2Fother", wantStatus: 400, wantCode: "invalid_request"},
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
			fiberPath := strings.NewReplacer("{slug}", ":slug", "{link_id}", ":link_id", "{name}", ":name").Replace(path)
			contractRoutes = append(contractRoutes, strings.ToUpper(method)+" "+prefix+fiberPath)
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

func TestRouterAuthenticationModesAndProtectedBoundary(t *testing.T) {
	const apiKey = "0123456789abcdef0123456789abcdef"

	development, err := NewRouter(validRouterOptions())
	if err != nil {
		t.Fatal(err)
	}
	response := sendRequest(t, development.App, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	if response.StatusCode != fiber.StatusOK {
		t.Fatalf("development empty-key status = %d", response.StatusCode)
	}

	options := validRouterOptions()
	options.Environment = "production"
	options.APIKey = apiKey
	production, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/v1/live", "/api/v1/ready", "/api/v1/version"} {
		response := sendRequest(t, production.App, httptest.NewRequest(http.MethodGet, path, nil))
		if response.StatusCode != fiber.StatusOK {
			t.Fatalf("public %s status = %d", path, response.StatusCode)
		}
	}

	tests := []struct {
		name          string
		authorization []string
		path          string
		wantStatus    int
	}{
		{name: "missing", path: "/api/v1/projects", wantStatus: 401},
		{name: "wrong", authorization: []string{"Bearer wrong"}, path: "/api/v1/projects", wantStatus: 401},
		{name: "basic", authorization: []string{"Basic " + apiKey}, path: "/api/v1/projects", wantStatus: 401},
		{name: "empty", authorization: []string{"Bearer "}, path: "/api/v1/projects", wantStatus: 401},
		{name: "query token", path: "/api/v1/projects?access_token=" + apiKey, wantStatus: 401},
		{name: "comma joined duplicate", authorization: []string{"Bearer " + apiKey + ", Bearer " + apiKey}, path: "/api/v1/projects", wantStatus: 401},
		{name: "case insensitive scheme", authorization: []string{"bearer " + apiKey}, path: "/api/v1/projects", wantStatus: 200},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			for _, value := range test.authorization {
				request.Header.Add(fiber.HeaderAuthorization, value)
			}
			response := sendRequest(t, production.App, request)
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
			if test.wantStatus == 401 {
				body := decodeJSONResponse[apiresponse.ErrorResponse](t, response)
				if body.Error.Code != "unauthorized" || response.Header.Get(fiber.HeaderWWWAuthenticate) != "Bearer" || response.Header.Get(fiber.HeaderCacheControl) != "no-store" {
					t.Fatalf("unauthorized response = %#v/%#v", body, response.Header)
				}
			}
		})
	}
}

func TestRouterAuthenticationLogsNeverContainCredentials(t *testing.T) {
	const configured = "configured-private-api-key"
	const supplied = "supplied-private-api-key"
	var logs bytes.Buffer
	options := validRouterOptions()
	options.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	options.APIKey = configured
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects?access_token="+supplied, nil)
	request.Header.Set(fiber.HeaderAuthorization, "Bearer "+supplied)
	response := sendRequest(t, router.App, request)
	if response.StatusCode != 401 {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if strings.Contains(logs.String(), configured) || strings.Contains(logs.String(), supplied) || strings.Contains(logs.String(), "access_token") {
		t.Fatalf("credential leaked to logs: %q", logs.String())
	}
}

func TestRouterProjectCORSPreflightRunsBeforeAuthentication(t *testing.T) {
	options := validRouterOptions()
	options.Environment = "production"
	options.APIKey = strings.Repeat("a", 32)
	options.CORSAllowedOrigins = []string{"https://dashboard.example.com"}
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/projects", nil)
	request.Header.Set(fiber.HeaderOrigin, "https://dashboard.example.com")
	request.Header.Set(fiber.HeaderAccessControlRequestMethod, http.MethodPost)
	request.Header.Set(fiber.HeaderAccessControlRequestHeaders, "authorization, content-type, if-match")
	response := sendRequest(t, router.App, request)
	if response.StatusCode != fiber.StatusNoContent || response.Header.Get(fiber.HeaderAccessControlAllowOrigin) != "https://dashboard.example.com" || !strings.Contains(strings.ToLower(response.Header.Get(fiber.HeaderAccessControlAllowHeaders)), "if-match") {
		t.Fatalf("preflight = %d/%#v", response.StatusCode, response.Header)
	}
}

func TestRouterProjectLifecycleMatchesOpenAPIAndPersistsIntent(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	service := controlapp.NewService(st, swarmfake.New(), "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	options := validRouterOptions()
	options.Store = st
	options.Projects = service
	options.APIKey = "test-key"
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	contractRouter := contractRouterForTest(t)

	send := func(method, path, body string) (*http.Response, []byte) {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
		if body != "" {
			request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		}
		response := sendRequest(t, router.App, request)
		return response, validateOpenAPIResponse(t, contractRouter, request, response)
	}

	created, createdBody := send(http.MethodPost, "/api/v1/projects", `{"name":"Payments","slug":"payments"}`)
	if created.StatusCode != 201 || created.Header.Get(fiber.HeaderLocation) != "/api/v1/projects/payments" {
		t.Fatalf("create = %d/%q/%s", created.StatusCode, created.Header.Get(fiber.HeaderLocation), createdBody)
	}
	duplicate, duplicateBody := send(http.MethodPost, "/api/v1/projects", `{"name":"Payments","slug":"payments"}`)
	if duplicate.StatusCode != 409 || decodeJSONBytes[apiresponse.ErrorResponse](t, duplicateBody).Error.Code != "conflict" {
		t.Fatalf("duplicate = %d/%s", duplicate.StatusCode, duplicateBody)
	}
	invalidSlug, invalidSlugBody := send(http.MethodPost, "/api/v1/projects", `{"name":"Too Long","slug":"`+strings.Repeat("a", 49)+`"}`)
	if invalidSlug.StatusCode != 422 || decodeJSONBytes[apiresponse.ErrorResponse](t, invalidSlugBody).Error.Code != "validation_failed" {
		t.Fatalf("invalid slug = %d/%s", invalidSlug.StatusCode, invalidSlugBody)
	}
	reserved, reservedBody := send(http.MethodPost, "/api/v1/projects", `{"name":"Reserved","slug":"root"}`)
	if reserved.StatusCode != 409 || decodeJSONBytes[apiresponse.ErrorResponse](t, reservedBody).Error.Code != "conflict" {
		t.Fatalf("reserved project = %d/%s", reserved.StatusCode, reservedBody)
	}

	target, err := service.CreateProject(t.Context(), controlapp.CreateProjectRequest{Name: "Target", Slug: "target"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateApp(t.Context(), controlapp.CreateAppRequest{Name: "target-api", ProjectSlug: target.Slug, Image: "nginx:latest"}); err != nil {
		t.Fatal(err)
	}

	linkResponse, linkBody := send(http.MethodPost, "/api/v1/projects/payments/links", `{"target_app_name":"target-api","alias":"target.api"}`)
	if linkResponse.StatusCode != 201 {
		t.Fatalf("link create = %d/%s", linkResponse.StatusCode, linkBody)
	}
	link := decodeJSONBytes[handler.ProjectLinkResponse](t, linkBody)
	if link.Status != handler.ProjectLinkPending {
		t.Fatalf("link = %#v", link)
	}
	if _, err := service.CreateProject(t.Context(), controlapp.CreateProjectRequest{Name: "Other", Slug: "other"}); err != nil {
		t.Fatal(err)
	}
	wrongOwner, wrongOwnerBody := send(http.MethodDelete, "/api/v1/projects/other/links/"+link.ID, "")
	if wrongOwner.StatusCode != 404 || decodeJSONBytes[apiresponse.ErrorResponse](t, wrongOwnerBody).Error.Code != "not_found" {
		t.Fatalf("wrong-owner delete = %d/%s", wrongOwner.StatusCode, wrongOwnerBody)
	}

	appsResponse, appsBody := send(http.MethodGet, "/api/v1/projects/target/apps", "")
	if appsResponse.StatusCode != 200 || strings.Contains(string(appsBody), "nginx") {
		t.Fatalf("apps = %d/%s", appsResponse.StatusCode, appsBody)
	}
	linksResponse, linksBody := send(http.MethodGet, "/api/v1/projects/payments/links", "")
	if linksResponse.StatusCode != 200 || decodeJSONBytes[handler.ProjectLinkCollectionResponse](t, linksBody).Total != 1 {
		t.Fatalf("links = %d/%s", linksResponse.StatusCode, linksBody)
	}

	nonEmptyDelete, nonEmptyBody := send(http.MethodDelete, "/api/v1/projects/payments", "")
	if nonEmptyDelete.StatusCode != 409 || decodeJSONBytes[apiresponse.ErrorResponse](t, nonEmptyBody).Error.Code != "conflict" {
		t.Fatalf("non-empty delete = %d/%s", nonEmptyDelete.StatusCode, nonEmptyBody)
	}
	rootDelete, rootDeleteBody := send(http.MethodDelete, "/api/v1/projects/root", "")
	if rootDelete.StatusCode != 409 || decodeJSONBytes[apiresponse.ErrorResponse](t, rootDeleteBody).Error.Code != "conflict" {
		t.Fatalf("root delete = %d/%s", rootDelete.StatusCode, rootDeleteBody)
	}
	deletedLink, deletedLinkBody := send(http.MethodDelete, "/api/v1/projects/payments/links/"+link.ID, "")
	if deletedLink.StatusCode != 202 || decodeJSONBytes[handler.AcceptedOperationResponse](t, deletedLinkBody).Status != "accepted" {
		t.Fatalf("link delete = %d/%s", deletedLink.StatusCode, deletedLinkBody)
	}
	projectDelete, projectDeleteBody := send(http.MethodDelete, "/api/v1/projects/payments", "")
	if projectDelete.StatusCode != 202 || decodeJSONBytes[handler.AcceptedOperationResponse](t, projectDeleteBody).Status != "accepted" {
		t.Fatalf("project delete = %d/%s", projectDelete.StatusCode, projectDeleteBody)
	}
	persisted, err := st.GetProject(t.Context(), "payments")
	if err != nil || persisted.DeletionTimestamp == nil {
		t.Fatalf("deletion intent = %#v/%v", persisted, err)
	}
}

func TestRouterApplicationLifecycleMatchesOpenAPIAndPersistsIntent(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	queue := &routerRecordingQueue{}
	service := controlapp.NewService(st, swarmfake.New(), "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil))).WithReconcileQueue(queue)
	options := validRouterOptions()
	options.Store = st
	options.Projects = service
	options.Applications = service
	options.APIKey = "test-key"
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	contractRouter := contractRouterForTest(t)

	send := func(method, path, body, etag string) (*http.Response, []byte) {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
		if body != "" {
			request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		}
		if etag != "" {
			request.Header.Set(fiber.HeaderIfMatch, etag)
		}
		response := sendRequest(t, router.App, request)
		return response, validateOpenAPIResponse(t, contractRouter, request, response)
	}

	invalidCreate, invalidCreateBody := send(http.MethodPost, "/api/v1/apps", `{"name":"invalid-volume","image":"nginx:1.27","volumes":[{"source":"/srv/moduleos/data/../data","target":"/data"}]}`, "")
	if invalidCreate.StatusCode != fiber.StatusUnprocessableEntity || queue.count() != 0 {
		t.Fatalf("invalid create = %d queue=%d body=%s", invalidCreate.StatusCode, queue.count(), invalidCreateBody)
	}
	if _, err := st.GetApplication(t.Context(), "invalid-volume"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("invalid create persisted application: %v", err)
	}

	created, createdBody := send(http.MethodPost, "/api/v1/apps", `{"name":"api","image":"nginx:1.27","replicas":2,"env_vars":{"SECRET":"private"},"ports":[{"container_port":8080}],"volumes":[{"source":"/srv/moduleos/data","target":"/data","read_only":true}]}`, "")
	if created.StatusCode != fiber.StatusCreated || created.Header.Get(fiber.HeaderETag) != `"1"` || created.Header.Get(fiber.HeaderLocation) != "/api/v1/apps/api" || queue.count() != 1 {
		t.Fatalf("create = %d/%#v queue=%d body=%s", created.StatusCode, created.Header, queue.count(), createdBody)
	}
	for _, forbidden := range []string{"private", "/srv/moduleos/data", "reconcile_error_message", "finalizer_state"} {
		if strings.Contains(string(createdBody), forbidden) {
			t.Fatalf("create leaked %q: %s", forbidden, createdBody)
		}
	}

	listed, listedBody := send(http.MethodGet, "/api/v1/apps", "", "")
	if listed.StatusCode != fiber.StatusOK || decodeJSONBytes[handler.ApplicationCollectionResponse](t, listedBody).Total != 1 {
		t.Fatalf("list = %d/%s", listed.StatusCode, listedBody)
	}
	got, gotBody := send(http.MethodGet, "/api/v1/apps/api", "", "")
	if got.StatusCode != fiber.StatusOK || got.Header.Get(fiber.HeaderETag) != `"1"` {
		t.Fatalf("get = %d/%#v/%s", got.StatusCode, got.Header, gotBody)
	}
	portReplay, portReplayBody := send(http.MethodPatch, "/api/v1/apps/api", `{"ports":[{"container_port":8080,"published_port":0,"protocol":"tcp","publish_mode":"ingress"}]}`, `"1"`)
	if portReplay.StatusCode != fiber.StatusAccepted || portReplay.Header.Get(fiber.HeaderETag) != `"1"` || queue.count() != 1 {
		t.Fatalf("port replay = %d/%#v queue=%d body=%s", portReplay.StatusCode, portReplay.Header, queue.count(), portReplayBody)
	}
	invalidUpdate, invalidUpdateBody := send(http.MethodPatch, "/api/v1/apps/api", `{"volumes":[{"source":"/srv/moduleos/data","target":"/data/"}]}`, `"1"`)
	if invalidUpdate.StatusCode != fiber.StatusUnprocessableEntity || queue.count() != 1 {
		t.Fatalf("invalid update = %d queue=%d body=%s", invalidUpdate.StatusCode, queue.count(), invalidUpdateBody)
	}
	unchanged, err := st.GetApplication(t.Context(), "api")
	if err != nil || unchanged.DesiredGeneration != 1 || unchanged.Volumes != `[{"source":"/srv/moduleos/data","target":"/data","read_only":true}]` {
		t.Fatalf("invalid update mutated application = %#v/%v", unchanged, err)
	}

	updated, updatedBody := send(http.MethodPatch, "/api/v1/apps/api", `{"env_vars":{"MODE":"production"},"ports":[],"volumes":[]}`, `"1"`)
	if updated.StatusCode != fiber.StatusAccepted || updated.Header.Get(fiber.HeaderETag) != `"2"` || queue.count() != 2 {
		t.Fatalf("update = %d/%#v queue=%d body=%s", updated.StatusCode, updated.Header, queue.count(), updatedBody)
	}
	stale, staleBody := send(http.MethodPatch, "/api/v1/apps/api", `{"env_vars":{"MODE":"production"}}`, `"1"`)
	if stale.StatusCode != fiber.StatusPreconditionFailed || queue.count() != 2 || decodeJSONBytes[apiresponse.ErrorResponse](t, staleBody).Error.Code != "precondition_failed" {
		t.Fatalf("stale = %d queue=%d body=%s", stale.StatusCode, queue.count(), staleBody)
	}
	replayed, replayedBody := send(http.MethodPatch, "/api/v1/apps/api", `{"env_vars":{"MODE":"production"}}`, `"2"`)
	if replayed.StatusCode != fiber.StatusAccepted || replayed.Header.Get(fiber.HeaderETag) != `"2"` || queue.count() != 2 {
		t.Fatalf("replay = %d/%#v queue=%d body=%s", replayed.StatusCode, replayed.Header, queue.count(), replayedBody)
	}

	scaled, scaledBody := send(http.MethodPost, "/api/v1/apps/api/scale", `{"replicas":3}`, `"2"`)
	if scaled.StatusCode != fiber.StatusAccepted || scaled.Header.Get(fiber.HeaderETag) != `"3"` || queue.count() != 3 {
		t.Fatalf("scale = %d/%#v queue=%d body=%s", scaled.StatusCode, scaled.Header, queue.count(), scaledBody)
	}
	stopped, stoppedBody := send(http.MethodPost, "/api/v1/apps/api/stop", "", `"3"`)
	if stopped.StatusCode != fiber.StatusAccepted || stopped.Header.Get(fiber.HeaderETag) != `"4"` || queue.count() != 4 {
		t.Fatalf("stop = %d/%#v queue=%d body=%s", stopped.StatusCode, stopped.Header, queue.count(), stoppedBody)
	}
	stopReplay, stopReplayBody := send(http.MethodPost, "/api/v1/apps/api/stop", "", `"4"`)
	if stopReplay.StatusCode != fiber.StatusAccepted || stopReplay.Header.Get(fiber.HeaderETag) != `"4"` || queue.count() != 4 {
		t.Fatalf("stop replay = %d/%#v queue=%d body=%s", stopReplay.StatusCode, stopReplay.Header, queue.count(), stopReplayBody)
	}
	started, startedBody := send(http.MethodPost, "/api/v1/apps/api/start", "", `"4"`)
	if started.StatusCode != fiber.StatusAccepted || started.Header.Get(fiber.HeaderETag) != `"5"` || queue.count() != 5 {
		t.Fatalf("start = %d/%#v queue=%d body=%s", started.StatusCode, started.Header, queue.count(), startedBody)
	}

	deleting, deletingBody := send(http.MethodDelete, "/api/v1/apps/api", "", `"5"`)
	if deleting.StatusCode != fiber.StatusAccepted || deleting.Header.Get(fiber.HeaderETag) != `"6"` || queue.count() != 6 {
		t.Fatalf("delete = %d/%#v queue=%d body=%s", deleting.StatusCode, deleting.Header, queue.count(), deletingBody)
	}
	deleteReplay, deleteReplayBody := send(http.MethodDelete, "/api/v1/apps/api", "", `"6"`)
	if deleteReplay.StatusCode != fiber.StatusAccepted || deleteReplay.Header.Get(fiber.HeaderETag) != `"6"` || queue.count() != 6 {
		t.Fatalf("delete replay = %d/%#v queue=%d body=%s", deleteReplay.StatusCode, deleteReplay.Header, queue.count(), deleteReplayBody)
	}
	persisted, err := st.GetApplication(t.Context(), "api")
	if err != nil || persisted.DeletionTimestamp == nil || persisted.DesiredGeneration != 6 {
		t.Fatalf("persisted = %#v/%v", persisted, err)
	}
}

func TestRouterApplicationFailureResponsesMatchOpenAPI(t *testing.T) {
	contractRouter := contractRouterForTest(t)
	options := validRouterOptions()
	options.APIKey = "test-key"
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, method, path, body, contentType, etag string
		authorized                                  bool
		wantStatus                                  int
	}{
		{name: "unauthorized", method: http.MethodGet, path: "/api/v1/apps", wantStatus: 401},
		{name: "invalid path", method: http.MethodGet, path: "/api/v1/apps/Bad", authorized: true, wantStatus: 400},
		{name: "not found", method: http.MethodGet, path: "/api/v1/apps/missing", authorized: true, wantStatus: 404},
		{name: "missing precondition", method: http.MethodDelete, path: "/api/v1/apps/missing", authorized: true, wantStatus: 428},
		{name: "malformed precondition", method: http.MethodDelete, path: "/api/v1/apps/missing", etag: `W/"1"`, authorized: true, wantStatus: 400},
		{name: "malformed JSON", method: http.MethodPost, path: "/api/v1/apps", body: `{"name":`, contentType: fiber.MIMEApplicationJSON, authorized: true, wantStatus: 400},
		{name: "wrong media", method: http.MethodPost, path: "/api/v1/apps", body: `{}`, contentType: fiber.MIMETextPlain, authorized: true, wantStatus: 415},
		{name: "validation", method: http.MethodPost, path: "/api/v1/apps", body: `{}`, contentType: fiber.MIMEApplicationJSON, authorized: true, wantStatus: 422},
		{name: "missing project", method: http.MethodPost, path: "/api/v1/apps", body: `{"name":"api","project_slug":"missing","image":"nginx:1.27"}`, contentType: fiber.MIMEApplicationJSON, authorized: true, wantStatus: 404},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.contentType != "" {
				request.Header.Set(fiber.HeaderContentType, test.contentType)
			}
			if test.etag != "" {
				request.Header.Set(fiber.HeaderIfMatch, test.etag)
			}
			if test.authorized {
				request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
			}
			response := sendRequest(t, router.App, request)
			payload := validateOpenAPIResponse(t, contractRouter, request, response)
			var envelope apiresponse.ErrorResponse
			if err := json.Unmarshal(payload, &envelope); err != nil || response.StatusCode != test.wantStatus || envelope.Error.RequestID == "" || response.Header.Get(fiber.HeaderCacheControl) != "no-store" {
				t.Fatalf("response = %d/%#v/%v/%#v", response.StatusCode, envelope, err, response.Header)
			}
		})
	}
}

func TestRouterConcurrentApplicationMutationHasOneWinner(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	queue := &routerRecordingQueue{}
	service := controlapp.NewService(st, swarmfake.New(), "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil))).WithReconcileQueue(queue)
	created, err := service.CreateApp(t.Context(), controlapp.CreateAppRequest{Name: "race-app", Image: "nginx:1.27"})
	if err != nil {
		t.Fatal(err)
	}
	options := validRouterOptions()
	options.Store = st
	options.Projects = service
	options.Applications = service
	options.APIKey = "test-key"
	options.RequestConcurrency = 8
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}

	statuses := make(chan int, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			request := httptest.NewRequest(http.MethodPost, "/api/v1/apps/race-app/scale", strings.NewReader(`{"replicas":2}`))
			request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
			request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
			request.Header.Set(fiber.HeaderIfMatch, `"1"`)
			response, requestErr := router.App.Test(request, fiber.TestConfig{Timeout: 0})
			if requestErr != nil {
				statuses <- 0
				return
			}
			_ = response.Body.Close()
			statuses <- response.StatusCode
		}()
	}
	close(start)
	first, second := <-statuses, <-statuses
	if !((first == fiber.StatusAccepted && second == fiber.StatusPreconditionFailed) || (first == fiber.StatusPreconditionFailed && second == fiber.StatusAccepted)) {
		t.Fatalf("statuses = %d/%d", first, second)
	}
	persisted, err := st.GetApplication(t.Context(), created.Name)
	if err != nil || persisted.DesiredGeneration != 2 || persisted.Replicas != 2 || queue.count() != 2 {
		t.Fatalf("application = %#v err=%v queue=%d", persisted, err, queue.count())
	}
}

func TestRouterProjectFailureResponsesMatchOpenAPI(t *testing.T) {
	contractRouter := contractRouterForTest(t)
	options := validRouterOptions()
	options.APIKey = "test-key"
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		contentType string
		authorized  bool
		wantStatus  int
	}{
		{name: "unauthorized", method: http.MethodGet, path: "/api/v1/projects", wantStatus: 401},
		{name: "invalid path", method: http.MethodGet, path: "/api/v1/projects/UPPER", authorized: true, wantStatus: 400},
		{name: "not found", method: http.MethodGet, path: "/api/v1/projects/missing", authorized: true, wantStatus: 404},
		{name: "malformed JSON", method: http.MethodPost, path: "/api/v1/projects", body: `{"name":`, contentType: fiber.MIMEApplicationJSON, authorized: true, wantStatus: 400},
		{name: "wrong media", method: http.MethodPost, path: "/api/v1/projects", body: `{}`, contentType: fiber.MIMETextPlain, authorized: true, wantStatus: 415},
		{name: "validation", method: http.MethodPost, path: "/api/v1/projects", body: `{}`, contentType: fiber.MIMEApplicationJSON, authorized: true, wantStatus: 422},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.contentType != "" {
				request.Header.Set(fiber.HeaderContentType, test.contentType)
			}
			if test.authorized {
				request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
			}
			response := sendRequest(t, router.App, request)
			payload := validateOpenAPIResponse(t, contractRouter, request, response)
			body := decodeJSONBytes[apiresponse.ErrorResponse](t, payload)
			if response.StatusCode != test.wantStatus || body.Error.RequestID == "" || response.Header.Get(fiber.HeaderCacheControl) != "no-store" {
				t.Fatalf("response = %d/%#v/%#v", response.StatusCode, body, response.Header)
			}
		})
	}

	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "internal", err: errors.New("private database failure"), wantStatus: 500},
		{name: "cancelled", err: context.Canceled, wantStatus: 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			failureOptions := validRouterOptions()
			failureOptions.APIKey = "test-key"
			failureOptions.Projects = &routerErrorProjectService{err: test.err}
			failureRouter, err := NewRouter(failureOptions)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
			request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
			response := sendRequest(t, failureRouter.App, request)
			payload := validateOpenAPIResponse(t, contractRouter, request, response)
			if response.StatusCode != test.wantStatus || strings.Contains(string(payload), "private") {
				t.Fatalf("response = %d/%s", response.StatusCode, payload)
			}
		})
	}
}

func TestRouterProjectBodyLimitFailsClosed(t *testing.T) {
	options := validRouterOptions()
	options.APIKey = "test-key"
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := startRealHTTPServer(t, router.App)

	largeRequest, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/projects", strings.NewReader(`{"name":"`+strings.Repeat("x", 2048)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	largeRequest.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
	largeRequest.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	largeResponse, err := (&http.Client{Timeout: time.Second}).Do(largeRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = largeResponse.Body.Close() }()
	if largeResponse.StatusCode != fiber.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d", largeResponse.StatusCode)
	}
}

func TestRouterConcurrentDuplicateProjectCreateHasOneWinner(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	service := controlapp.NewService(st, swarmfake.New(), "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	options := validRouterOptions()
	options.Store = st
	options.Projects = service
	options.APIKey = "test-key"
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}

	const callers = 16
	options.RequestConcurrency = callers
	router, err = NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(chan int, callers)
	for range callers {
		go func() {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"Race","slug":"race"}`))
			request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
			request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
			response, requestErr := router.App.Test(request, fiber.TestConfig{Timeout: 0})
			if requestErr != nil {
				statuses <- 0
				return
			}
			_ = response.Body.Close()
			statuses <- response.StatusCode
		}()
	}
	winners := 0
	conflicts := 0
	for range callers {
		switch status := <-statuses; status {
		case fiber.StatusCreated:
			winners++
		case fiber.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected status %d", status)
		}
	}
	if winners != 1 || conflicts != callers-1 {
		t.Fatalf("winners/conflicts = %d/%d", winners, conflicts)
	}
}

func TestRouterConcurrentProjectLinkDeleteHasAcceptedAndMissingOutcomes(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	service := controlapp.NewService(st, swarmfake.New(), "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	target, err := service.CreateProject(t.Context(), controlapp.CreateProjectRequest{Name: "Target", Slug: "target"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateApp(t.Context(), controlapp.CreateAppRequest{Name: "target-app", ProjectSlug: target.Slug, Image: "nginx:latest"}); err != nil {
		t.Fatal(err)
	}
	source, err := service.CreateProject(t.Context(), controlapp.CreateProjectRequest{Name: "Source", Slug: "source"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := service.CreateProjectLink(t.Context(), source.Slug, "target-app", "target.api")
	if err != nil {
		t.Fatal(err)
	}
	options := validRouterOptions()
	options.Store = st
	options.Projects = service
	options.APIKey = "test-key"
	options.RequestConcurrency = 16
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}

	statuses := make(chan int, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/source/links/"+link.ID, nil)
			request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
			response, requestErr := router.App.Test(request, fiber.TestConfig{Timeout: 0})
			if requestErr != nil {
				statuses <- 0
				return
			}
			_ = response.Body.Close()
			statuses <- response.StatusCode
		}()
	}
	close(start)
	first, second := <-statuses, <-statuses
	if !((first == fiber.StatusAccepted && second == fiber.StatusNotFound) || (first == fiber.StatusNotFound && second == fiber.StatusAccepted)) {
		t.Fatalf("statuses = %d/%d, want 202/404", first, second)
	}
}

func TestRouterProjectLinkValidationBoundariesAndDeletionState(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	service := controlapp.NewService(st, swarmfake.New(), "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	source, err := service.CreateProject(t.Context(), controlapp.CreateProjectRequest{Name: "Source", Slug: "source"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.CreateProject(t.Context(), controlapp.CreateProjectRequest{Name: "Target", Slug: "target"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateApp(t.Context(), controlapp.CreateAppRequest{Name: "target-app", ProjectSlug: target.Slug, Image: "nginx:latest"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateApp(t.Context(), controlapp.CreateAppRequest{Name: "source-app", ProjectSlug: source.Slug, Image: "nginx:latest"}); err != nil {
		t.Fatal(err)
	}
	options := validRouterOptions()
	options.Store = st
	options.Projects = service
	options.APIKey = "test-key"
	options.RequestConcurrency = 32
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}

	postLink := func(sourceSlug, targetName, alias string) (*http.Response, []byte) {
		t.Helper()
		payload, err := json.Marshal(map[string]string{"target_app_name": targetName, "alias": alias})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+sourceSlug+"/links", bytes.NewReader(payload))
		request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
		request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		response := sendRequest(t, router.App, request)
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response, body
	}
	deleteLink := func(linkID string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/source/links/"+linkID, nil)
		request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
		response := sendRequest(t, router.App, request)
		if response.StatusCode != 202 {
			t.Fatalf("delete status = %d", response.StatusCode)
		}
	}

	validAlias := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	if len(validAlias) != 253 {
		t.Fatalf("test alias length = %d", len(validAlias))
	}
	valid, validBody := postLink("source", "target-app", validAlias)
	if valid.StatusCode != 201 {
		t.Fatalf("253-byte alias = %d/%s", valid.StatusCode, validBody)
	}
	deleteLink(decodeJSONBytes[handler.ProjectLinkResponse](t, validBody).ID)

	invalidAlias := validAlias + "e"
	invalid, invalidBody := postLink("source", "target-app", invalidAlias)
	if invalid.StatusCode != 422 || decodeJSONBytes[apiresponse.ErrorResponse](t, invalidBody).Error.Code != "validation_failed" {
		t.Fatalf("254-byte alias = %d/%s", invalid.StatusCode, invalidBody)
	}
	sameProject, sameProjectBody := postLink("source", "source-app", "source.local")
	if sameProject.StatusCode != 409 || decodeJSONBytes[apiresponse.ErrorResponse](t, sameProjectBody).Error.Code != "conflict" {
		t.Fatalf("same-project link = %d/%s", sameProject.StatusCode, sameProjectBody)
	}
	if _, err := st.CreateProjectDeletionIntent(t.Context(), source.Slug); err != nil {
		t.Fatal(err)
	}
	deleting, deletingBody := postLink("source", "target-app", "target.local")
	if deleting.StatusCode != 409 || decodeJSONBytes[apiresponse.ErrorResponse](t, deletingBody).Error.Code != "conflict" {
		t.Fatalf("deleting source = %d/%s", deleting.StatusCode, deletingBody)
	}
}

func decodeJSONBytes[T any](t *testing.T, payload []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("decode %T: %v: %s", value, err, payload)
	}
	return value
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
