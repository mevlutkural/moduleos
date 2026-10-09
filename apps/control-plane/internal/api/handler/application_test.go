package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	controlapp "github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

type applicationServiceStub struct {
	create func(context.Context, controlapp.CreateAppRequest) (*store.Application, error)
	list   func(context.Context) ([]*store.Application, error)
	get    func(context.Context, string) (*store.Application, error)
	update func(context.Context, controlapp.UpdateAppRequest) (*store.Application, error)
	delete func(context.Context, string, int64) (*store.Application, error)
	run    func(context.Context, string, store.DesiredRunState, int64) (*store.Application, error)
	scale  func(context.Context, string, int, int64) (*store.Application, error)
}

func (s *applicationServiceStub) CreateApp(ctx context.Context, request controlapp.CreateAppRequest) (*store.Application, error) {
	if s.create != nil {
		return s.create(ctx, request)
	}
	return validApplication(), nil
}

func (s *applicationServiceStub) ListApps(ctx context.Context) ([]*store.Application, error) {
	if s.list != nil {
		return s.list(ctx)
	}
	return []*store.Application{validApplication()}, nil
}

func (s *applicationServiceStub) GetApp(ctx context.Context, name string) (*store.Application, error) {
	if s.get != nil {
		return s.get(ctx, name)
	}
	return validApplication(), nil
}

func (s *applicationServiceStub) UpdateApp(ctx context.Context, request controlapp.UpdateAppRequest) (*store.Application, error) {
	if s.update != nil {
		return s.update(ctx, request)
	}
	application := validApplication()
	application.DesiredGeneration = 2
	return application, nil
}

func (s *applicationServiceStub) DeleteAppIntent(ctx context.Context, name string, generation int64) (*store.Application, error) {
	if s.delete != nil {
		return s.delete(ctx, name, generation)
	}
	return validApplication(), nil
}

func (s *applicationServiceStub) SetRunState(ctx context.Context, name string, state store.DesiredRunState, generation int64) (*store.Application, error) {
	if s.run != nil {
		return s.run(ctx, name, state, generation)
	}
	return validApplication(), nil
}

func (s *applicationServiceStub) ScaleAppIntent(ctx context.Context, name string, replicas int, generation int64) (*store.Application, error) {
	if s.scale != nil {
		return s.scale(ctx, name, replicas, generation)
	}
	return validApplication(), nil
}

func validApplication() *store.Application {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.FixedZone("test", 3*60*60))
	return &store.Application{
		ID:                    "20000000-0000-4000-8000-000000000001",
		ProjectID:             store.RootProjectID,
		Name:                  "api",
		SourceType:            store.SourceTypeImage,
		Image:                 "nginx:1.27",
		Status:                store.AppStatusCreated,
		Replicas:              1,
		DesiredRunState:       store.DesiredRunStateRunning,
		DesiredGeneration:     1,
		ObservedGeneration:    0,
		ObservedState:         store.ObservedStatePending,
		EnvVars:               `{"SECRET":"private"}`,
		Ports:                 `[{"container_port":8080,"published_port":0,"protocol":"","publish_mode":""}]`,
		Volumes:               `[{"source":"/srv/moduleos/data","target":"/data","read_only":true}]`,
		CreatedAt:             now,
		UpdatedAt:             now,
		ReconcileErrorCode:    "retry_pending",
		ReconcileRetryable:    true,
		ReconcileErrorMessage: "private dependency detail",
	}
}

func newApplicationServer(t *testing.T, service ApplicationService, logOutput io.Writer) *fiber.App {
	t.Helper()
	if logOutput == nil {
		logOutput = io.Discard
	}
	logger := slog.New(slog.NewTextHandler(logOutput, nil))
	handler, err := NewApplicationHandler(service, logger)
	if err != nil {
		t.Fatal(err)
	}
	server := fiber.New(fiber.Config{ErrorHandler: apiresponse.NewErrorHandler(logger), StrictRouting: true})
	server.Post("/apps", handler.Create)
	server.Get("/apps", handler.List)
	server.Get("/apps/:name", handler.Get)
	server.Patch("/apps/:name", handler.Update)
	server.Delete("/apps/:name", handler.Delete)
	server.Post("/apps/:name/start", handler.Start)
	server.Post("/apps/:name/stop", handler.Stop)
	server.Post("/apps/:name/scale", handler.Scale)
	return server
}

func applicationRequest(t *testing.T, server *fiber.App, method, path, body, ifMatch string) *http.Response {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	if ifMatch != "" {
		request.Header.Set(fiber.HeaderIfMatch, ifMatch)
	}
	response, err := server.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestNewApplicationHandlerRejectsMissingDependencies(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewApplicationHandler(nil, logger); !errors.Is(err, ErrMissingApplicationService) {
		t.Fatalf("service error = %v", err)
	}
	var typedNil *applicationServiceStub
	if _, err := NewApplicationHandler(typedNil, logger); !errors.Is(err, ErrMissingApplicationService) {
		t.Fatalf("typed nil error = %v", err)
	}
	if _, err := NewApplicationHandler(&applicationServiceStub{}, nil); !errors.Is(err, ErrMissingApplicationLogger) {
		t.Fatalf("logger error = %v", err)
	}
}

func TestApplicationImageReferenceBoundary(t *testing.T) {
	longNamePrefix := "registry.example.com/"
	longName := longNamePrefix + strings.Repeat("g", maximumDesiredImageReferenceLength-len(longNamePrefix))
	longObserved := longName + "@sha256:" + strings.Repeat("a", 64)
	for _, value := range []string{
		"nginx",
		"ghcr.io/mevlutkural/moduleos:v0.1.0",
		"registry.example.com:5000/team/api@sha256:" + strings.Repeat("a", 64),
		longName,
	} {
		if !validImageReference(value) {
			t.Errorf("valid image reference %q rejected", value)
		}
	}
	for _, value := range []string{
		"bad image",
		"example.com/UPPERCASE",
		"nginx:",
		"nginx@sha256:short",
		"nginx@sha256:" + strings.Repeat("a", 32),
		"nginx@sha512:" + strings.Repeat("a", 128),
		strings.Repeat("a", 64),
		strings.Repeat("repo", 64),
	} {
		if validImageReference(value) {
			t.Errorf("invalid image reference %q accepted", value)
		}
	}
	if !validOptionalPersistedImageReference(longObserved) {
		t.Errorf("resolved image reference with appended digest rejected")
	}
	application := validApplication()
	application.Image = longObserved
	application.ObservedImage = longObserved
	if _, err := mapApplicationResponse(application); err != nil {
		t.Fatalf("application with runtime-resolved image rejected: %v", err)
	}
	if validOptionalPersistedImageReference(longName + ":x@sha256:" + strings.Repeat("a", 64)) {
		t.Error("observed image beyond generated-reference ceiling accepted")
	}
}

func TestApplicationCreateMapsInputAndRedactsOutput(t *testing.T) {
	called := false
	service := &applicationServiceStub{create: func(_ context.Context, request controlapp.CreateAppRequest) (*store.Application, error) {
		called = true
		if request.Name != "api" || request.ProjectSlug != "root" || request.Image != "nginx:1.27" || request.Replicas != 2 || request.EnvVars["SECRET"] != "private" || len(request.Ports) != 1 || len(request.Volumes) != 1 || !request.Expose || request.IngressContainerPort != 8080 || request.SourceType != store.SourceTypeImage {
			t.Fatalf("request = %#v", request)
		}
		application := validApplication()
		application.Expose = true
		application.IngressContainerPort = 8080
		return application, nil
	}}
	server := newApplicationServer(t, service, nil)
	body := `{"name":"api","project_slug":"root","image":"nginx:1.27","replicas":2,"env_vars":{"SECRET":"private"},"ports":[{"container_port":8080}],"volumes":[{"source":"/srv/moduleos/data","target":"/data","read_only":true}],"expose":true,"ingress_container_port":8080}`
	response := applicationRequest(t, server, http.MethodPost, "/apps", body, "")
	if !called || response.StatusCode != fiber.StatusCreated || response.Header.Get(fiber.HeaderLocation) != "/api/v1/apps/api" || response.Header.Get(fiber.HeaderETag) != `"1"` {
		t.Fatalf("status/headers = %d %#v", response.StatusCode, response.Header)
	}
	encoded, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private", "/srv/moduleos/data", "private dependency detail", "finalizer_state", "resume_replicas", "domain"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("response leaked %q: %s", secret, encoded)
		}
	}
	var decoded ApplicationResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.EnvVars["SECRET"].Redacted || len(decoded.Volumes) != 1 || !decoded.Volumes[0].SourceRedacted || decoded.Ports[0].Protocol != "tcp" || decoded.Ports[0].PublishMode != "ingress" || decoded.CreatedAt.Location() != time.UTC {
		t.Fatalf("response = %#v", decoded)
	}
}

func TestApplicationCreateDefaultsAndRejectsInvalidRequests(t *testing.T) {
	service := &applicationServiceStub{create: func(_ context.Context, request controlapp.CreateAppRequest) (*store.Application, error) {
		if request.Replicas != 1 {
			t.Fatalf("default replicas = %d", request.Replicas)
		}
		return validApplication(), nil
	}}
	server := newApplicationServer(t, service, nil)
	response := applicationRequest(t, server, http.MethodPost, "/apps", `{"name":"api","image":"nginx:1.27"}`, "")
	if response.StatusCode != fiber.StatusCreated {
		t.Fatalf("default request status = %d", response.StatusCode)
	}

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{name: "malformed", body: `{`, status: fiber.StatusBadRequest},
		{name: "unknown", body: `{"name":"api","image":"nginx:1.27","unknown":true}`, status: fiber.StatusBadRequest},
		{name: "invalid name", body: `{"name":"Bad Name","image":"nginx:1.27"}`, status: fiber.StatusUnprocessableEntity},
		{name: "invalid project", body: `{"name":"api","project_slug":"Bad","image":"nginx:1.27"}`, status: fiber.StatusUnprocessableEntity},
		{name: "empty project", body: `{"name":"api","project_slug":"","image":"nginx:1.27"}`, status: fiber.StatusUnprocessableEntity},
		{name: "null project", body: `{"name":"api","project_slug":null,"image":"nginx:1.27"}`, status: fiber.StatusUnprocessableEntity},
		{name: "invalid image", body: `{"name":"api","image":" bad image "}`, status: fiber.StatusUnprocessableEntity},
		{name: "negative replicas", body: `{"name":"api","image":"nginx:1.27","replicas":-1}`, status: fiber.StatusUnprocessableEntity},
		{name: "zero replicas", body: `{"name":"api","image":"nginx:1.27","replicas":0}`, status: fiber.StatusUnprocessableEntity},
		{name: "null replicas", body: `{"name":"api","image":"nginx:1.27","replicas":null}`, status: fiber.StatusUnprocessableEntity},
		{name: "null env", body: `{"name":"api","image":"nginx:1.27","env_vars":null}`, status: fiber.StatusUnprocessableEntity},
		{name: "null ports", body: `{"name":"api","image":"nginx:1.27","ports":null}`, status: fiber.StatusUnprocessableEntity},
		{name: "null volumes", body: `{"name":"api","image":"nginx:1.27","volumes":null}`, status: fiber.StatusUnprocessableEntity},
		{name: "null expose", body: `{"name":"api","image":"nginx:1.27","expose":null}`, status: fiber.StatusUnprocessableEntity},
		{name: "null ingress", body: `{"name":"api","image":"nginx:1.27","ingress_container_port":null}`, status: fiber.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := applicationRequest(t, server, http.MethodPost, "/apps", test.body, "")
			if response.StatusCode != test.status {
				t.Fatalf("status = %d", response.StatusCode)
			}
		})
	}
}

func TestApplicationValidationPrecedesServiceMutation(t *testing.T) {
	serviceCalls := 0
	service := &applicationServiceStub{
		create: func(context.Context, controlapp.CreateAppRequest) (*store.Application, error) {
			serviceCalls++
			return validApplication(), nil
		},
		update: func(context.Context, controlapp.UpdateAppRequest) (*store.Application, error) {
			serviceCalls++
			return validApplication(), nil
		},
	}
	server := newApplicationServer(t, service, nil)
	tests := []struct {
		name, method, path, body, etag, field string
	}{
		{name: "create parent traversal", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","volumes":[{"source":"/srv/data/../data","target":"/data"}]}`, field: "volumes"},
		{name: "create trailing separator", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","volumes":[{"source":"/srv/data/","target":"/data"}]}`, field: "volumes"},
		{name: "create relative source", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","volumes":[{"source":"srv/data","target":"/data"}]}`, field: "volumes"},
		{name: "create duplicate target", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","volumes":[{"source":"/srv/one","target":"/data"},{"source":"/srv/two","target":"/data"}]}`, field: "volumes"},
		{name: "create root target", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","volumes":[{"source":"/srv/data","target":"/"}]}`, field: "volumes"},
		{name: "create environment NUL", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","env_vars":{"TOKEN":"a\u0000b"}}`, field: "env_vars"},
		{name: "create null environment value", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","env_vars":{"TOKEN":null}}`, field: "env_vars"},
		{name: "create invalid image", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"bad image"}`, field: "image"},
		{name: "create ambiguous hex image", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, field: "image"},
		{name: "create exposed without ingress", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","expose":true}`, field: "ingress_container_port"},
		{name: "create exposed with zero ingress", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","expose":true,"ingress_container_port":0}`, field: "ingress_container_port"},
		{name: "create null published port", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","ports":[{"container_port":8080,"published_port":null}]}`, field: "ports"},
		{name: "create null port protocol", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","ports":[{"container_port":8080,"protocol":null}]}`, field: "ports"},
		{name: "create null publish mode", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","ports":[{"container_port":8080,"publish_mode":null}]}`, field: "ports"},
		{name: "create null mount read only", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","volumes":[{"source":"/srv/data","target":"/data","read_only":null}]}`, field: "volumes"},
		{name: "update mount NUL", method: http.MethodPatch, path: "/apps/api", body: `{"volumes":[{"source":"/srv/data","target":"/data\u0000suffix"}]}`, etag: `"1"`, field: "volumes"},
		{name: "update null environment value", method: http.MethodPatch, path: "/apps/api", body: `{"env_vars":{"TOKEN":null}}`, etag: `"1"`, field: "env_vars"},
		{name: "update exposed with zero ingress", method: http.MethodPatch, path: "/apps/api", body: `{"expose":true,"ingress_container_port":0}`, etag: `"1"`, field: "ingress_container_port"},
		{name: "update null port protocol", method: http.MethodPatch, path: "/apps/api", body: `{"ports":[{"container_port":8080,"protocol":null}]}`, etag: `"1"`, field: "ports"},
		{name: "update null mount read only", method: http.MethodPatch, path: "/apps/api", body: `{"volumes":[{"source":"/srv/data","target":"/data","read_only":null}]}`, etag: `"1"`, field: "volumes"},
		{name: "update noncanonical target", method: http.MethodPatch, path: "/apps/api", body: `{"volumes":[{"source":"/srv/data","target":"/data/"}]}`, etag: `"1"`, field: "volumes"},
		{name: "create ingress port overflow", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27","ingress_container_port":65536}`, field: "ingress_container_port"},
		{name: "update ingress port overflow", method: http.MethodPatch, path: "/apps/api", body: `{"ingress_container_port":65536}`, etag: `"1"`, field: "ingress_container_port"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := applicationRequest(t, server, test.method, test.path, test.body, test.etag)
			if response.StatusCode != fiber.StatusUnprocessableEntity {
				t.Fatalf("status = %d", response.StatusCode)
			}
			var envelope apiresponse.ErrorResponse
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || envelope.Error.Fields[test.field] == "" {
				t.Fatalf("error = %#v / %v", envelope, err)
			}
		})
	}
	if serviceCalls != 0 {
		t.Fatalf("service calls = %d, want zero", serviceCalls)
	}
}

func TestApplicationListAndGet(t *testing.T) {
	server := newApplicationServer(t, &applicationServiceStub{}, nil)
	list := applicationRequest(t, server, http.MethodGet, "/apps", "", "")
	if list.StatusCode != fiber.StatusOK {
		t.Fatalf("list status = %d", list.StatusCode)
	}
	var collection ApplicationCollectionResponse
	if err := json.NewDecoder(list.Body).Decode(&collection); err != nil || collection.Total != 1 || len(collection.Applications) != 1 {
		t.Fatalf("collection = %#v, err = %v", collection, err)
	}
	get := applicationRequest(t, server, http.MethodGet, "/apps/api", "", "")
	if get.StatusCode != fiber.StatusOK || get.Header.Get(fiber.HeaderETag) != `"1"` {
		t.Fatalf("get = %d %#v", get.StatusCode, get.Header)
	}
	invalid := applicationRequest(t, server, http.MethodGet, "/apps/Bad", "", "")
	if invalid.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("invalid path status = %d", invalid.StatusCode)
	}
}

func TestApplicationMutationsEnforcePreconditionsAndBodies(t *testing.T) {
	var runState store.DesiredRunState
	var scaleReplicas int
	service := &applicationServiceStub{
		run: func(_ context.Context, _ string, state store.DesiredRunState, generation int64) (*store.Application, error) {
			runState = state
			if generation != 1 {
				t.Fatalf("generation = %d", generation)
			}
			return validApplication(), nil
		},
		scale: func(_ context.Context, _ string, replicas int, generation int64) (*store.Application, error) {
			scaleReplicas = replicas
			return validApplication(), nil
		},
		update: func(_ context.Context, request controlapp.UpdateAppRequest) (*store.Application, error) {
			if request.ExpectedGeneration != 1 || request.Image != nil || request.EnvVars["MODE"] != "prod" || request.Ports == nil || request.Volumes == nil || request.Expose == nil || request.IngressContainerPort == nil {
				t.Fatalf("update request = %#v", request)
			}
			application := validApplication()
			application.DesiredGeneration = 2
			return application, nil
		},
	}
	server := newApplicationServer(t, service, nil)

	for _, test := range []struct {
		method, path, body, operation string
		status                        int
	}{
		{http.MethodPost, "/apps/api/start", "", "start", fiber.StatusAccepted},
		{http.MethodPost, "/apps/api/stop", "", "stop", fiber.StatusAccepted},
		{http.MethodDelete, "/apps/api", "", "delete", fiber.StatusAccepted},
		{http.MethodPost, "/apps/api/scale", `{"replicas":0}`, "scale", fiber.StatusAccepted},
		{http.MethodPatch, "/apps/api", `{"env_vars":{"MODE":"prod"},"ports":[],"volumes":[],"expose":false,"ingress_container_port":0}`, "", fiber.StatusAccepted},
	} {
		response := applicationRequest(t, server, test.method, test.path, test.body, `"1"`)
		if response.StatusCode != test.status || response.Header.Get(fiber.HeaderETag) == "" {
			t.Fatalf("%s status/etag = %d/%q", test.path, response.StatusCode, response.Header.Get(fiber.HeaderETag))
		}
	}
	if runState != store.DesiredRunStateStopped || scaleReplicas != 0 {
		t.Fatalf("run state/replicas = %q/%d", runState, scaleReplicas)
	}

	for _, test := range []struct {
		name, method, path, body, tag string
		status                        int
	}{
		{"missing precondition", http.MethodDelete, "/apps/api", "", "", fiber.StatusPreconditionRequired},
		{"malformed precondition", http.MethodDelete, "/apps/api", "", `W/"1"`, fiber.StatusBadRequest},
		{"invalid path", http.MethodDelete, "/apps/Bad", "", `"1"`, fiber.StatusBadRequest},
		{"nonempty start", http.MethodPost, "/apps/api/start", `{}`, `"1"`, fiber.StatusBadRequest},
		{"empty patch", http.MethodPatch, "/apps/api", `{}`, `"1"`, fiber.StatusUnprocessableEntity},
		{"null patch", http.MethodPatch, "/apps/api", `{"env_vars":null}`, `"1"`, fiber.StatusUnprocessableEntity},
		{"missing scale", http.MethodPost, "/apps/api/scale", `{}`, `"1"`, fiber.StatusUnprocessableEntity},
		{"null scale", http.MethodPost, "/apps/api/scale", `{"replicas":null}`, `"1"`, fiber.StatusUnprocessableEntity},
		{"negative scale", http.MethodPost, "/apps/api/scale", `{"replicas":-1}`, `"1"`, fiber.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := applicationRequest(t, server, test.method, test.path, test.body, test.tag)
			if response.StatusCode != test.status {
				t.Fatalf("status = %d", response.StatusCode)
			}
		})
	}
}

func TestApplicationServiceErrorsUseStableSafeMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "cancelled", err: context.Canceled, status: fiber.StatusServiceUnavailable, code: "dependency_unavailable"},
		{name: "deadline", err: context.DeadlineExceeded, status: fiber.StatusServiceUnavailable, code: "dependency_unavailable"},
		{name: "missing", err: store.ErrNotFound, status: fiber.StatusNotFound, code: "not_found"},
		{name: "stale", err: store.ErrGenerationConflict, status: fiber.StatusPreconditionFailed, code: "precondition_failed"},
		{name: "conflict", err: store.ErrConflict, status: fiber.StatusConflict, code: "conflict"},
		{name: "transition", err: store.ErrInvalidTransition, status: fiber.StatusConflict, code: "conflict"},
		{name: "corrupt persisted data", err: store.ErrInvalidData, status: fiber.StatusInternalServerError, code: "internal_error"},
		{name: "opaque", err: errors.New("private database DSN"), status: fiber.StatusInternalServerError, code: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			server := newApplicationServer(t, &applicationServiceStub{get: func(context.Context, string) (*store.Application, error) { return nil, test.err }}, &logs)
			response := applicationRequest(t, server, http.MethodGet, "/apps/api", "", "")
			if response.StatusCode != test.status {
				t.Fatalf("status = %d", response.StatusCode)
			}
			var envelope apiresponse.ErrorResponse
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || envelope.Error.Code != test.code {
				t.Fatalf("error = %#v / %v", envelope, err)
			}
			if strings.Contains(logs.String(), "private database DSN") {
				t.Fatalf("logs leaked dependency detail: %s", logs.String())
			}
		})
	}
}

func TestApplicationMutationValidationErrorMapsToUnprocessableEntity(t *testing.T) {
	server := newApplicationServer(t, &applicationServiceStub{create: func(context.Context, controlapp.CreateAppRequest) (*store.Application, error) {
		return nil, store.ErrInvalidData
	}}, nil)
	response := applicationRequest(t, server, http.MethodPost, "/apps", `{"name":"api","image":"nginx:1.27"}`, "")
	if response.StatusCode != fiber.StatusUnprocessableEntity {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestApplicationStartConflictUsesClientError(t *testing.T) {
	server := newApplicationServer(t, &applicationServiceStub{run: func(context.Context, string, store.DesiredRunState, int64) (*store.Application, error) {
		return nil, store.ErrConflict
	}}, nil)
	response := applicationRequest(t, server, http.MethodPost, "/apps/api/start", "", `"1"`)
	if response.StatusCode != fiber.StatusConflict {
		t.Fatalf("status = %d, want %d", response.StatusCode, fiber.StatusConflict)
	}
	var envelope apiresponse.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || envelope.Error.Code != "conflict" {
		t.Fatalf("error = %#v / %v", envelope, err)
	}
}

func TestApplicationHandlersRejectMismatchedServiceIdentity(t *testing.T) {
	other := validApplication()
	other.Name = "other"
	tests := []struct {
		name, method, path, body, etag string
		service                        *applicationServiceStub
	}{
		{name: "create", method: http.MethodPost, path: "/apps", body: `{"name":"api","image":"nginx:1.27"}`, service: &applicationServiceStub{create: func(context.Context, controlapp.CreateAppRequest) (*store.Application, error) { return other, nil }}},
		{name: "get", method: http.MethodGet, path: "/apps/api", service: &applicationServiceStub{get: func(context.Context, string) (*store.Application, error) { return other, nil }}},
		{name: "update", method: http.MethodPatch, path: "/apps/api", body: `{"env_vars":{}}`, etag: `"1"`, service: &applicationServiceStub{update: func(context.Context, controlapp.UpdateAppRequest) (*store.Application, error) { return other, nil }}},
		{name: "operation", method: http.MethodDelete, path: "/apps/api", etag: `"1"`, service: &applicationServiceStub{delete: func(context.Context, string, int64) (*store.Application, error) { return other, nil }}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newApplicationServer(t, test.service, nil)
			response := applicationRequest(t, server, test.method, test.path, test.body, test.etag)
			if response.StatusCode != fiber.StatusInternalServerError {
				t.Fatalf("status = %d", response.StatusCode)
			}
		})
	}
}

func TestApplicationMapperFailsClosedForCorruptState(t *testing.T) {
	mutations := []func(*store.Application){
		func(value *store.Application) { value.ID = "invalid" },
		func(value *store.Application) { value.ProjectID = "invalid" },
		func(value *store.Application) { value.Name = "Bad" },
		func(value *store.Application) { value.SourceType = store.SourceTypeTar },
		func(value *store.Application) { value.Image = "bad image" },
		func(value *store.Application) { value.ObservedImage = "bad image" },
		func(value *store.Application) { value.Status = "unknown" },
		func(value *store.Application) { value.DesiredRunState = "unknown" },
		func(value *store.Application) { value.ObservedState = "unknown" },
		func(value *store.Application) { value.Replicas = -1 },
		func(value *store.Application) { value.DesiredGeneration = 0 },
		func(value *store.Application) { value.ObservedGeneration = 2 },
		func(value *store.Application) { value.ReconcileAttempt = -1 },
		func(value *store.Application) { value.CreatedAt = time.Time{} },
		func(value *store.Application) { value.UpdatedAt = value.CreatedAt.Add(-time.Second) },
		func(value *store.Application) { value.ReconcileErrorCode = "Bad Code" },
		func(value *store.Application) { zero := time.Time{}; value.LastTransitionAt = &zero },
		func(value *store.Application) { value.EnvVars = `{"BAD-KEY":"value"}` },
		func(value *store.Application) { value.EnvVars = `null` },
		func(value *store.Application) { value.EnvVars = `{"TOKEN":"a\u0000b"}` },
		func(value *store.Application) { value.EnvVars = `{ "A":"1"}` },
		func(value *store.Application) {
			value.Ports = `[{"container_port":0,"published_port":0,"protocol":"","publish_mode":""}]`
		},
		func(value *store.Application) { value.Ports = `null` },
		func(value *store.Application) {
			value.Volumes = `[{"source":"relative","target":"/data","read_only":false}]`
		},
		func(value *store.Application) { value.Volumes = `null` },
		func(value *store.Application) {
			value.Volumes = `[{"source":"/srv/moduleos/data","target":"/","read_only":false}]`
		},
		func(value *store.Application) {
			value.Volumes = `[{"source":"/srv/moduleos/data","target":"/data\u0000suffix","read_only":false}]`
		},
		func(value *store.Application) { value.Expose = true },
		func(value *store.Application) { value.IngressContainerPort = 65536 },
	}
	for index, mutate := range mutations {
		application := validApplication()
		mutate(application)
		if _, err := mapApplicationResponse(application); !errors.Is(err, errInvalidPublicState) {
			t.Errorf("mutation %d error = %v", index, err)
		}
	}
	if _, err := mapApplicationResponse(nil); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("nil error = %v", err)
	}
	if _, err := mapApplicationCollection(make([]*store.Application, maximumPublicApplications+1)); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("oversized collection error = %v", err)
	}
}

func TestApplicationResponseFailureIsSafe(t *testing.T) {
	corrupt := validApplication()
	corrupt.EnvVars = `{"SECRET":"private"} trailing`
	var logs bytes.Buffer
	server := newApplicationServer(t, &applicationServiceStub{get: func(context.Context, string) (*store.Application, error) { return corrupt, nil }}, &logs)
	response := applicationRequest(t, server, http.MethodGet, "/apps/api", "", "")
	if response.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if bytes.Contains(body, []byte("private")) || strings.Contains(logs.String(), "private") {
		t.Fatalf("corrupt state leaked: body=%s logs=%s", body, logs.String())
	}
}

func TestWriteApplicationOperationRejectsInvalidResult(t *testing.T) {
	server := newApplicationServer(t, &applicationServiceStub{delete: func(context.Context, string, int64) (*store.Application, error) { return nil, nil }}, nil)
	response := applicationRequest(t, server, http.MethodDelete, "/apps/api", "", `"1"`)
	if response.StatusCode != fiber.StatusInternalServerError {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestApplicationCollectionUsesEmptyArray(t *testing.T) {
	server := newApplicationServer(t, &applicationServiceStub{list: func(context.Context) ([]*store.Application, error) { return nil, nil }}, nil)
	response := applicationRequest(t, server, http.MethodGet, "/apps", "", "")
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != fiber.StatusOK || !bytes.Contains(body, []byte(`"applications":[]`)) {
		t.Fatalf("status/body = %d/%s", response.StatusCode, body)
	}
}

func TestApplicationOperationResponseUsesCanonicalName(t *testing.T) {
	application := validApplication()
	application.Name = "api"
	application.DesiredGeneration = 4
	server := newApplicationServer(t, &applicationServiceStub{delete: func(context.Context, string, int64) (*store.Application, error) { return application, nil }}, nil)
	response := applicationRequest(t, server, http.MethodDelete, "/apps/api", "", `"1"`)
	if response.Header.Get(fiber.HeaderETag) != `"4"` {
		t.Fatalf("ETag = %q", response.Header.Get(fiber.HeaderETag))
	}
	var operation ApplicationOperationResponse
	if err := json.NewDecoder(response.Body).Decode(&operation); err != nil || operation.Resource != "api" || operation.Generation != 4 {
		t.Fatalf("operation = %#v / %v", operation, err)
	}
}

func TestCanonicalUUIDHelperAssumption(t *testing.T) {
	if !canonicalUUID(uuid.MustParse(store.RootProjectID).String()) {
		t.Fatal("root project UUID must remain canonical")
	}
}
