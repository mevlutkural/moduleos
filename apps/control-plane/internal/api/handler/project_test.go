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
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

type projectServiceStub struct {
	createProject     func(context.Context, app.CreateProjectRequest) (*store.Project, error)
	listProjects      func(context.Context) ([]*store.Project, error)
	getProject        func(context.Context, string) (*store.Project, error)
	deleteProject     func(context.Context, string) error
	listProjectApps   func(context.Context, string) ([]*store.Application, error)
	createProjectLink func(context.Context, string, string, string) (*app.ProjectLinkDetails, error)
	listProjectLinks  func(context.Context, string) ([]*app.ProjectLinkDetails, error)
	deleteProjectLink func(context.Context, string, string) error
}

func (s *projectServiceStub) CreateProject(ctx context.Context, request app.CreateProjectRequest) (*store.Project, error) {
	if s.createProject == nil {
		return nil, store.ErrNotFound
	}
	return s.createProject(ctx, request)
}

func (s *projectServiceStub) ListProjects(ctx context.Context) ([]*store.Project, error) {
	if s.listProjects == nil {
		return nil, nil
	}
	return s.listProjects(ctx)
}

func (s *projectServiceStub) GetProject(ctx context.Context, slug string) (*store.Project, error) {
	if s.getProject == nil {
		return nil, store.ErrNotFound
	}
	return s.getProject(ctx, slug)
}

func (s *projectServiceStub) DeleteProject(ctx context.Context, slug string) error {
	if s.deleteProject == nil {
		return store.ErrNotFound
	}
	return s.deleteProject(ctx, slug)
}

func (s *projectServiceStub) ListProjectApps(ctx context.Context, slug string) ([]*store.Application, error) {
	if s.listProjectApps == nil {
		return nil, nil
	}
	return s.listProjectApps(ctx, slug)
}

func (s *projectServiceStub) CreateProjectLink(ctx context.Context, slug, target, alias string) (*app.ProjectLinkDetails, error) {
	if s.createProjectLink == nil {
		return nil, store.ErrNotFound
	}
	return s.createProjectLink(ctx, slug, target, alias)
}

func (s *projectServiceStub) ListProjectLinks(ctx context.Context, slug string) ([]*app.ProjectLinkDetails, error) {
	if s.listProjectLinks == nil {
		return nil, nil
	}
	return s.listProjectLinks(ctx, slug)
}

func (s *projectServiceStub) DeleteProjectLink(ctx context.Context, slug, linkID string) error {
	if s.deleteProjectLink == nil {
		return store.ErrNotFound
	}
	return s.deleteProjectLink(ctx, slug, linkID)
}

func validProject() *store.Project {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.FixedZone("test", 2*60*60))
	return &store.Project{
		ID: uuid.NewString(), Name: "Payments", Slug: "payments", Network: "private-network",
		ObservedState: store.ObservedStateReady, ReconcileErrorMessage: "private-diagnostic",
		CreatedAt: now, UpdatedAt: now,
	}
}

func validProjectLink() *app.ProjectLinkDetails {
	return &app.ProjectLinkDetails{
		ProjectLink: &store.ProjectLink{
			ID: uuid.NewString(), SourceProjectID: uuid.NewString(), TargetAppID: uuid.NewString(),
			Alias: "target.api", CreatedAt: time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC),
			DesiredGeneration: 1,
		},
		SourceProjectSlug: "source", TargetProjectSlug: "target", TargetAppName: "api",
	}
}

func projectHandlerTestApp(t *testing.T, service ProjectService) *fiber.App {
	t.Helper()
	handler, err := NewProjectHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server := fiber.New()
	server.Post("/projects", handler.Create)
	server.Get("/projects", handler.List)
	server.Get("/projects/:slug/apps", handler.ListApplications)
	server.Post("/projects/:slug/links", handler.CreateLink)
	server.Get("/projects/:slug/links", handler.ListLinks)
	server.Delete("/projects/:slug/links/:link_id", handler.DeleteLink)
	server.Get("/projects/:slug", handler.Get)
	server.Delete("/projects/:slug", handler.Delete)
	return server
}

func projectRequest(t *testing.T, server *fiber.App, method, path, body string) (*http.Response, []byte) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	response, err := server.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, payload
}

func decodeProjectError(t *testing.T, payload []byte) apiresponse.ErrorResponse {
	t.Helper()
	var response apiresponse.ErrorResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("decode error: %v: %s", err, payload)
	}
	return response
}

func TestNewProjectHandlerRejectsNilAndTypedNilService(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewProjectHandler(nil, log); !errors.Is(err, ErrMissingProjectService) {
		t.Fatalf("nil error = %v", err)
	}
	var typedNil *projectServiceStub
	if _, err := NewProjectHandler(typedNil, log); !errors.Is(err, ErrMissingProjectService) {
		t.Fatalf("typed nil error = %v", err)
	}
	if _, err := NewProjectHandler(&projectServiceStub{}, nil); !errors.Is(err, ErrMissingProjectLogger) {
		t.Fatalf("nil logger error = %v", err)
	}
}

func TestProjectHandlerCreateUsesExplicitSafeDTOAndLocation(t *testing.T) {
	project := validProject()
	service := &projectServiceStub{createProject: func(_ context.Context, request app.CreateProjectRequest) (*store.Project, error) {
		if request.Name != " Payments " || request.Slug != "payments" {
			t.Fatalf("request = %#v", request)
		}
		return project, nil
	}}
	response, payload := projectRequest(t, projectHandlerTestApp(t, service), http.MethodPost, "/projects", `{"name":" Payments ","slug":"payments"}`)
	if response.StatusCode != fiber.StatusCreated || response.Header.Get(fiber.HeaderLocation) != "/api/v1/projects/payments" {
		t.Fatalf("status/location = %d/%q", response.StatusCode, response.Header.Get(fiber.HeaderLocation))
	}
	for _, forbidden := range []string{"network", "finalizer_state", "reconcile_error_message", "private-network", "private-diagnostic"} {
		if bytes.Contains(payload, []byte(forbidden)) {
			t.Fatalf("response leaked %q: %s", forbidden, payload)
		}
	}
	var got ProjectResponse
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != project.ID || got.Slug != project.Slug || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("response = %#v", got)
	}
}

func TestProjectHandlerAcceptsMaximumUnicodeProjectName(t *testing.T) {
	name := strings.Repeat("界", maximumProjectNameLength)
	project := validProject()
	project.Name = name
	project.Slug = "unicode"
	service := &projectServiceStub{createProject: func(_ context.Context, request app.CreateProjectRequest) (*store.Project, error) {
		if request.Name != name || request.Slug != "unicode" {
			t.Fatalf("request = %#v", request)
		}
		return project, nil
	}}
	response, payload := projectRequest(t, projectHandlerTestApp(t, service), http.MethodPost, "/projects", `{"name":"`+name+`","slug":"unicode"}`)
	if response.StatusCode != fiber.StatusCreated {
		t.Fatalf("status = %d: %s", response.StatusCode, payload)
	}
}

func TestProjectHandlerCollectionsAreNeverNullAndRejectCorruptState(t *testing.T) {
	service := &projectServiceStub{}
	server := projectHandlerTestApp(t, service)
	for _, path := range []string{"/projects", "/projects/source/apps", "/projects/source/links"} {
		response, payload := projectRequest(t, server, http.MethodGet, path, "")
		if response.StatusCode != fiber.StatusOK || bytes.Contains(payload, []byte("null")) || !bytes.Contains(payload, []byte("\"total\":0")) {
			t.Fatalf("%s response = %d %s", path, response.StatusCode, payload)
		}
	}

	corrupt := &projectServiceStub{getProject: func(context.Context, string) (*store.Project, error) {
		project := validProject()
		project.ObservedState = "invented"
		return project, nil
	}}
	response, payload := projectRequest(t, projectHandlerTestApp(t, corrupt), http.MethodGet, "/projects/payments", "")
	body := decodeProjectError(t, payload)
	if response.StatusCode != 500 || body.Error.Code != "internal_error" {
		t.Fatalf("corrupt response = %d %#v", response.StatusCode, body)
	}
}

func TestProjectHandlerApplicationSummaryRedactsSensitiveState(t *testing.T) {
	now := time.Now()
	application := &store.Application{
		ID: uuid.NewString(), Name: "api", Status: store.AppStatusRunning, DesiredRunState: store.DesiredRunStateRunning,
		Replicas: 2, DesiredGeneration: 3, ObservedGeneration: 2, ObservedState: store.ObservedStateReconciling,
		EnvVars: `{"SECRET":"value"}`, Volumes: `/private:/data`, Image: "registry.example/private:tag",
		ReconcileErrorMessage: "credential=private", CreatedAt: now, UpdatedAt: now,
	}
	service := &projectServiceStub{listProjectApps: func(context.Context, string) ([]*store.Application, error) {
		return []*store.Application{application}, nil
	}}
	response, payload := projectRequest(t, projectHandlerTestApp(t, service), http.MethodGet, "/projects/payments/apps", "")
	if response.StatusCode != 200 {
		t.Fatalf("status = %d: %s", response.StatusCode, payload)
	}
	for _, forbidden := range []string{"SECRET", "value", "/private", "registry.example", "credential", "env_vars", "volumes", "image", "reconcile_error"} {
		if bytes.Contains(payload, []byte(forbidden)) {
			t.Fatalf("application summary leaked %q: %s", forbidden, payload)
		}
	}
	var collection ProjectApplicationCollectionResponse
	if err := json.Unmarshal(payload, &collection); err != nil || collection.Total != 1 || collection.Applications[0].ObservedGeneration != 2 {
		t.Fatalf("collection = %#v, err = %v", collection, err)
	}
}

func TestProjectHandlerLinkLifecycleUsesConvergenceStatusAndAcceptedDeletion(t *testing.T) {
	link := validProjectLink()
	service := &projectServiceStub{
		createProjectLink: func(_ context.Context, slug, target, alias string) (*app.ProjectLinkDetails, error) {
			if slug != "source" || target != "api" || alias != "target.api" {
				t.Fatalf("arguments = %q/%q/%q", slug, target, alias)
			}
			return link, nil
		},
		listProjectLinks: func(context.Context, string) ([]*app.ProjectLinkDetails, error) {
			ready := validProjectLink()
			ready.ProjectLink.ObservedGeneration = ready.ProjectLink.DesiredGeneration
			return []*app.ProjectLinkDetails{link, ready}, nil
		},
		deleteProjectLink: func(_ context.Context, slug, linkID string) error {
			if slug != "source" || linkID != link.ID {
				t.Fatalf("delete arguments = %q/%q", slug, linkID)
			}
			return nil
		},
	}
	server := projectHandlerTestApp(t, service)
	created, createdPayload := projectRequest(t, server, http.MethodPost, "/projects/source/links", `{"target_app_name":"api","alias":"target.api"}`)
	if created.StatusCode != 201 || created.Header.Get(fiber.HeaderLocation) != "/api/v1/projects/source/links/"+link.ID {
		t.Fatalf("create = %d/%q/%s", created.StatusCode, created.Header.Get(fiber.HeaderLocation), createdPayload)
	}
	var createdBody ProjectLinkResponse
	if err := json.Unmarshal(createdPayload, &createdBody); err != nil || createdBody.Status != ProjectLinkPending {
		t.Fatalf("create body = %#v/%v", createdBody, err)
	}

	listed, listedPayload := projectRequest(t, server, http.MethodGet, "/projects/source/links", "")
	var collection ProjectLinkCollectionResponse
	if err := json.Unmarshal(listedPayload, &collection); err != nil || listed.StatusCode != 200 || collection.Total != 2 || collection.Links[1].Status != ProjectLinkReady {
		t.Fatalf("list = %d/%#v/%v", listed.StatusCode, collection, err)
	}

	deleted, deletedPayload := projectRequest(t, server, http.MethodDelete, "/projects/source/links/"+link.ID, "")
	var operation AcceptedOperationResponse
	if err := json.Unmarshal(deletedPayload, &operation); err != nil || deleted.StatusCode != 202 || operation.Status != "accepted" || operation.Resource != link.ID {
		t.Fatalf("delete = %d/%#v/%v", deleted.StatusCode, operation, err)
	}
}

func TestProjectHandlerDeleteProjectReturnsAcceptedIntent(t *testing.T) {
	service := &projectServiceStub{deleteProject: func(_ context.Context, slug string) error {
		if slug != "payments" {
			t.Fatalf("slug = %q", slug)
		}
		return nil
	}}
	response, payload := projectRequest(t, projectHandlerTestApp(t, service), http.MethodDelete, "/projects/payments", "")
	var operation AcceptedOperationResponse
	if err := json.Unmarshal(payload, &operation); err != nil || response.StatusCode != 202 || operation.Operation != "delete_project" || operation.Resource != "payments" {
		t.Fatalf("response = %d/%#v/%v", response.StatusCode, operation, err)
	}
}

func TestProjectHandlerRejectsInvalidBodiesAndPathsBeforeService(t *testing.T) {
	var calls atomic.Int32
	service := &projectServiceStub{
		createProject: func(context.Context, app.CreateProjectRequest) (*store.Project, error) {
			calls.Add(1)
			return validProject(), nil
		},
		getProject: func(context.Context, string) (*store.Project, error) { calls.Add(1); return validProject(), nil },
		createProjectLink: func(context.Context, string, string, string) (*app.ProjectLinkDetails, error) {
			calls.Add(1)
			return validProjectLink(), nil
		},
		deleteProjectLink: func(context.Context, string, string) error { calls.Add(1); return nil },
	}
	server := projectHandlerTestApp(t, service)
	tests := []struct {
		method string
		path   string
		body   string
		status int
		field  string
	}{
		{method: http.MethodPost, path: "/projects", body: `{}`, status: 422, field: "name"},
		{method: http.MethodPost, path: "/projects", body: `{"name":" \n\t "}`, status: 422, field: "name"},
		{method: http.MethodPost, path: "/projects", body: `{"name":"` + strings.Repeat("a", maximumProjectNameLength+1) + `","slug":"project"}`, status: 422, field: "name"},
		{method: http.MethodPost, path: "/projects", body: `{"name":" ` + strings.Repeat("a", maximumProjectNameLength) + `","slug":"project"}`, status: 422, field: "name"},
		{method: http.MethodPost, path: "/projects", body: `{"name":"project","unknown":true}`, status: 400},
		{method: http.MethodGet, path: "/projects/UPPER", status: 400, field: "slug"},
		{method: http.MethodGet, path: "/projects/" + strings.Repeat("a", 49), status: 400, field: "slug"},
		{method: http.MethodPost, path: "/projects/source/links", body: `{}`, status: 422, field: "target_app_name"},
		{method: http.MethodPost, path: "/projects/source/links", body: `{"target_app_name":"Bad_Name"}`, status: 422, field: "target_app_name"},
		{method: http.MethodPost, path: "/projects/source/links", body: `{"target_app_name":"` + strings.Repeat("a", 64) + `"}`, status: 422, field: "target_app_name"},
		{method: http.MethodDelete, path: "/projects/source/links/not-a-uuid", status: 400, field: "link_id"},
		{method: http.MethodDelete, path: "/projects/source/links/AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", status: 400, field: "link_id"},
	}
	for _, test := range tests {
		response, payload := projectRequest(t, server, test.method, test.path, test.body)
		body := decodeProjectError(t, payload)
		if response.StatusCode != test.status || (test.field != "" && body.Error.Fields[test.field] == "") {
			t.Fatalf("%s %s = %d/%#v", test.method, test.path, response.StatusCode, body)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("service calls = %d, want 0", calls.Load())
	}
}

func TestProjectHandlerMapsTypedErrorsWithoutLeakingDetails(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		status    int
		code      string
		retryable bool
	}{
		{name: "not found", err: store.ErrNotFound, status: 404, code: "not_found"},
		{name: "conflict", err: store.ErrConflict, status: 409, code: "conflict"},
		{name: "generation", err: store.ErrGenerationConflict, status: 409, code: "conflict"},
		{name: "transition", err: store.ErrInvalidTransition, status: 409, code: "conflict"},
		{name: "validation", err: app.ErrValidation, status: 422, code: "validation_failed"},
		{name: "cancelled", err: context.Canceled, status: 503, code: "dependency_unavailable", retryable: true},
		{name: "deadline", err: context.DeadlineExceeded, status: 503, code: "dependency_unavailable", retryable: true},
		{name: "internal", err: errors.New("database password=private"), status: 500, code: "internal_error"},
		{name: "invalid data", err: store.ErrInvalidData, status: 500, code: "internal_error"},
		{name: "inconsistent", err: app.ErrInconsistent, status: 500, code: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &projectServiceStub{getProject: func(context.Context, string) (*store.Project, error) {
				return nil, errors.Join(errors.New("private-value"), test.err)
			}}
			response, payload := projectRequest(t, projectHandlerTestApp(t, service), http.MethodGet, "/projects/payments", "")
			body := decodeProjectError(t, payload)
			if response.StatusCode != test.status || body.Error.Code != test.code || body.Error.Retryable != test.retryable || body.Error.RequestID == "" {
				t.Fatalf("response = %d/%#v", response.StatusCode, body)
			}
			if bytes.Contains(payload, []byte("private")) || bytes.Contains(payload, []byte("password")) {
				t.Fatalf("private detail leaked: %s", payload)
			}
		})
	}
}

func TestProjectHandlerLogsInternalClassificationWithoutErrorDetails(t *testing.T) {
	var logs bytes.Buffer
	service := &projectServiceStub{getProject: func(context.Context, string) (*store.Project, error) {
		return nil, errors.New("password=private-value")
	}}
	handler, err := NewProjectHandler(service, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	server := fiber.New()
	server.Get("/projects/:slug", handler.Get)
	response, err := server.Test(httptest.NewRequest(http.MethodGet, "/projects/payments", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 500 || !strings.Contains(logs.String(), "operation=get_project") || !strings.Contains(logs.String(), "status=500") || !strings.Contains(logs.String(), "request_id=") {
		t.Fatalf("response/logs = %d/%q", response.StatusCode, logs.String())
	}
	if strings.Contains(logs.String(), "password") || strings.Contains(logs.String(), "private-value") {
		t.Fatalf("internal error detail leaked: %q", logs.String())
	}
}

func TestProjectDTOMappersRejectInvalidInternalModels(t *testing.T) {
	project := validProject()
	invalidProjects := []*store.Project{nil, {ID: "bad", Name: "x", Slug: "x", ObservedState: store.ObservedStateReady, CreatedAt: time.Now(), UpdatedAt: time.Now()}, func() *store.Project { p := *project; p.ReconcileErrorCode = "BAD CODE"; return &p }()}
	for _, invalid := range invalidProjects {
		if _, err := mapProjectResponse(invalid); !errors.Is(err, errInvalidPublicState) {
			t.Fatalf("project %#v error = %v", invalid, err)
		}
	}
	badSlug := *project
	badSlug.Slug = "Bad-Slug"
	if _, err := mapProjectResponse(&badSlug); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("invalid project slug error = %v", err)
	}
	longName := *project
	longName.Name = strings.Repeat("a", maximumProjectNameLength+1)
	if _, err := mapProjectResponse(&longName); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("long project name error = %v", err)
	}
	invalidUTF8Name := *project
	invalidUTF8Name.Name = string([]byte{0xff})
	if _, err := mapProjectResponse(&invalidUTF8Name); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("invalid UTF-8 project name error = %v", err)
	}
	if _, err := mapProjectCollection(make([]*store.Project, maximumPublicProjects+1)); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("project collection limit error = %v", err)
	}

	application := &store.Application{ID: uuid.NewString(), Name: "app", Status: store.AppStatusRunning, DesiredRunState: store.DesiredRunStateRunning, Replicas: 1, DesiredGeneration: 1, ObservedState: store.ObservedStateRunning, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	application.ObservedGeneration = 2
	if _, err := mapProjectApplications([]*store.Application{application}); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("application error = %v", err)
	}
	if _, err := mapProjectApplications(make([]*store.Application, maximumPublicApplications+1)); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("application collection limit error = %v", err)
	}
	application.ObservedGeneration = 0
	application.DesiredGeneration = 1
	application.Name = "Bad_Name"
	if _, err := mapProjectApplications([]*store.Application{application}); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("application name error = %v", err)
	}

	link := validProjectLink()
	link.ObservedGeneration = 2
	if _, err := mapProjectLinkResponse(link); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("link error = %v", err)
	}
	badAlias := validProjectLink()
	badAlias.Alias = "Bad_Alias"
	if _, err := mapProjectLinkResponse(badAlias); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("link alias error = %v", err)
	}
	if _, err := mapProjectLinkCollection(make([]*app.ProjectLinkDetails, maximumPublicProjectLinks+1)); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("link collection limit error = %v", err)
	}
}

func TestProjectHandlersMapServiceAndRepresentationFailures(t *testing.T) {
	internalFailure := errors.New("private storage failure")
	corruptProject := validProject()
	corruptProject.ID = "invalid"
	corruptApplication := &store.Application{ID: "invalid"}
	corruptLink := validProjectLink()
	corruptLink.ID = "invalid"

	tests := []struct {
		name    string
		service *projectServiceStub
		method  string
		path    string
		body    string
	}{
		{name: "create service", service: &projectServiceStub{createProject: func(context.Context, app.CreateProjectRequest) (*store.Project, error) { return nil, internalFailure }}, method: http.MethodPost, path: "/projects", body: `{"name":"x"}`},
		{name: "create representation", service: &projectServiceStub{createProject: func(context.Context, app.CreateProjectRequest) (*store.Project, error) { return corruptProject, nil }}, method: http.MethodPost, path: "/projects", body: `{"name":"x"}`},
		{name: "list service", service: &projectServiceStub{listProjects: func(context.Context) ([]*store.Project, error) { return nil, internalFailure }}, method: http.MethodGet, path: "/projects"},
		{name: "list representation", service: &projectServiceStub{listProjects: func(context.Context) ([]*store.Project, error) { return []*store.Project{corruptProject}, nil }}, method: http.MethodGet, path: "/projects"},
		{name: "delete service", service: &projectServiceStub{deleteProject: func(context.Context, string) error { return internalFailure }}, method: http.MethodDelete, path: "/projects/x"},
		{name: "apps service", service: &projectServiceStub{listProjectApps: func(context.Context, string) ([]*store.Application, error) { return nil, internalFailure }}, method: http.MethodGet, path: "/projects/x/apps"},
		{name: "apps representation", service: &projectServiceStub{listProjectApps: func(context.Context, string) ([]*store.Application, error) {
			return []*store.Application{corruptApplication}, nil
		}}, method: http.MethodGet, path: "/projects/x/apps"},
		{name: "create link service", service: &projectServiceStub{createProjectLink: func(context.Context, string, string, string) (*app.ProjectLinkDetails, error) {
			return nil, internalFailure
		}}, method: http.MethodPost, path: "/projects/x/links", body: `{"target_app_name":"app"}`},
		{name: "create link representation", service: &projectServiceStub{createProjectLink: func(context.Context, string, string, string) (*app.ProjectLinkDetails, error) {
			return corruptLink, nil
		}}, method: http.MethodPost, path: "/projects/x/links", body: `{"target_app_name":"app"}`},
		{name: "list links service", service: &projectServiceStub{listProjectLinks: func(context.Context, string) ([]*app.ProjectLinkDetails, error) { return nil, internalFailure }}, method: http.MethodGet, path: "/projects/x/links"},
		{name: "list links representation", service: &projectServiceStub{listProjectLinks: func(context.Context, string) ([]*app.ProjectLinkDetails, error) {
			return []*app.ProjectLinkDetails{corruptLink}, nil
		}}, method: http.MethodGet, path: "/projects/x/links"},
		{name: "delete link service", service: &projectServiceStub{deleteProjectLink: func(context.Context, string, string) error { return internalFailure }}, method: http.MethodDelete, path: "/projects/x/links/" + uuid.NewString()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, payload := projectRequest(t, projectHandlerTestApp(t, test.service), test.method, test.path, test.body)
			body := decodeProjectError(t, payload)
			if response.StatusCode != 500 || body.Error.Code != "internal_error" || bytes.Contains(payload, []byte("private")) {
				t.Fatalf("response = %d/%#v/%s", response.StatusCode, body, payload)
			}
		})
	}
}

func TestProjectDTOsCoverDeletionTimeAndAllApplicationStatuses(t *testing.T) {
	deletion := time.Date(2026, 10, 8, 10, 0, 0, 0, time.FixedZone("offset", -3*60*60))
	project := validProject()
	project.DeletionTimestamp = &deletion
	collection, err := mapProjectCollection([]*store.Project{project})
	if err != nil || collection.Total != 1 || collection.Projects[0].DeletionRequestedAt == nil || collection.Projects[0].DeletionRequestedAt.Location() != time.UTC {
		t.Fatalf("collection = %#v, err = %v", collection, err)
	}

	statuses := []store.AppStatus{store.AppStatusCreated, store.AppStatusBuilding, store.AppStatusRunning, store.AppStatusStopped, store.AppStatusFailed, store.AppStatusUpdating}
	for _, status := range statuses {
		application := &store.Application{
			ID: uuid.NewString(), Name: string(status), Status: status, DesiredRunState: store.DesiredRunStateStopped,
			DesiredGeneration: 1, ObservedState: store.ObservedStateStopped, CreatedAt: time.Now(), UpdatedAt: time.Now(), DeletionTimestamp: &deletion,
		}
		mapped, err := mapProjectApplications([]*store.Application{application})
		if err != nil || mapped.Applications[0].Status != status || mapped.Applications[0].DeletionRequestedAt == nil {
			t.Fatalf("status %q mapped to %#v, err = %v", status, mapped, err)
		}
	}
}
