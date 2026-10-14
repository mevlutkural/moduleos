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
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

type deploymentServiceStub struct {
	accept func(context.Context, app.DeploymentMutationRequest) (*store.DeploymentAcceptance, error)
	list   func(context.Context, string, int, string) (*app.DeploymentPage, error)
	get    func(context.Context, string, string) (*store.Deployment, error)
}

func (s *deploymentServiceStub) AcceptDeployment(ctx context.Context, request app.DeploymentMutationRequest) (*store.DeploymentAcceptance, error) {
	if s.accept != nil {
		return s.accept(ctx, request)
	}
	return &store.DeploymentAcceptance{AppID: request.ExpectedAppID, DeploymentID: uuid.NewString(), TargetGeneration: request.ExpectedGeneration + 1}, nil
}

func (s *deploymentServiceStub) ListDeploymentPage(ctx context.Context, name string, limit int, cursor string) (*app.DeploymentPage, error) {
	if s.list != nil {
		return s.list(ctx, name, limit, cursor)
	}
	return &app.DeploymentPage{Deployments: []*store.Deployment{}}, nil
}

func (s *deploymentServiceStub) GetDeploymentForApp(ctx context.Context, name, id string) (*store.Deployment, error) {
	if s.get != nil {
		return s.get(ctx, name, id)
	}
	return &store.Deployment{ID: id, AppID: uuid.NewString(), SourceType: store.SourceTypeImage,
		Image: "nginx:1.27", Status: store.DeploymentStatusPending,
		TriggeredBy: store.TriggeredByAPI, TargetGeneration: 2, CreatedAt: time.Now().UTC()}, nil
}

func newDeploymentTestServer(t *testing.T, service DeploymentService, logOutput io.Writer) *fiber.App {
	t.Helper()
	if logOutput == nil {
		logOutput = io.Discard
	}
	logger := slog.New(slog.NewTextHandler(logOutput, nil))
	handler, err := NewDeploymentHandler(service, logger, func() time.Time {
		return time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	server := fiber.New(fiber.Config{ErrorHandler: apiresponse.NewErrorHandler(logger), StrictRouting: true})
	server.Post("/apps/:name/deploy", handler.Deploy)
	server.Get("/apps/:name/deployments", handler.List)
	server.Get("/apps/:name/deployments/:id", handler.Get)
	server.Post("/apps/:name/deployments/:id/rollback", handler.Rollback)
	return server
}

func deploymentRequest(t *testing.T, server *fiber.App, method, path, body, etag, key string) (*http.Response, []byte) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	if etag != "" {
		request.Header.Set(fiber.HeaderIfMatch, etag)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := server.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, payload
}

func TestNewDeploymentHandlerRejectsMissingDependencies(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	clock := time.Now
	if _, err := NewDeploymentHandler(nil, logger, clock); !errors.Is(err, ErrMissingDeploymentService) {
		t.Fatalf("nil service = %v", err)
	}
	var typedNil *deploymentServiceStub
	if _, err := NewDeploymentHandler(typedNil, logger, clock); !errors.Is(err, ErrMissingDeploymentService) {
		t.Fatalf("typed nil = %v", err)
	}
	if _, err := NewDeploymentHandler(&deploymentServiceStub{}, nil, clock); !errors.Is(err, ErrMissingDeploymentLogger) {
		t.Fatalf("nil logger = %v", err)
	}
	if _, err := NewDeploymentHandler(&deploymentServiceStub{}, logger, nil); err == nil {
		t.Fatal("nil clock accepted")
	}
}

func TestDeploymentInputValidationPrecedesServiceCall(t *testing.T) {
	id := uuid.NewString()
	tag := generationETag(id, 1)
	target := uuid.NewString()
	validKey := "01234567-89ab-cdef-0123-456789abcdef"
	called := 0
	service := &deploymentServiceStub{accept: func(context.Context, app.DeploymentMutationRequest) (*store.DeploymentAcceptance, error) {
		called++
		return nil, store.ErrNotFound
	}}
	server := newDeploymentTestServer(t, service, nil)
	tests := []struct {
		name, method, path, body, etag, key string
		want                                int
	}{
		{"bad app", http.MethodPost, "/apps/Bad/deploy", `{"image":"nginx:1.27"}`, tag, validKey, 400},
		{"missing precondition", http.MethodPost, "/apps/api/deploy", `{"image":"nginx:1.27"}`, "", validKey, 428},
		{"weak precondition", http.MethodPost, "/apps/api/deploy", `{"image":"nginx:1.27"}`, "W/" + tag, validKey, 400},
		{"missing key", http.MethodPost, "/apps/api/deploy", `{"image":"nginx:1.27"}`, tag, "", 400},
		{"short key", http.MethodPost, "/apps/api/deploy", `{"image":"nginx:1.27"}`, tag, "short", 400},
		{"comma key", http.MethodPost, "/apps/api/deploy", `{"image":"nginx:1.27"}`, tag, validKey + ",other", 400},
		{"unknown query", http.MethodPost, "/apps/api/deploy?other=1", `{"image":"nginx:1.27"}`, tag, validKey, 400},
		{"invalid JSON", http.MethodPost, "/apps/api/deploy", `{"image":`, tag, validKey, 400},
		{"duplicate JSON", http.MethodPost, "/apps/api/deploy", `{"image":"nginx","image":"busybox"}`, tag, validKey, 400},
		{"case-folded JSON", http.MethodPost, "/apps/api/deploy", `{"IMAGE":"nginx"}`, tag, validKey, 400},
		{"case-folded duplicate JSON", http.MethodPost, "/apps/api/deploy", `{"image":"nginx","IMAGE":"busybox"}`, tag, validKey, 400},
		{"unknown JSON", http.MethodPost, "/apps/api/deploy", `{"image":"nginx","secret":"x"}`, tag, validKey, 400},
		{"missing image", http.MethodPost, "/apps/api/deploy", `{}`, tag, validKey, 422},
		{"null image", http.MethodPost, "/apps/api/deploy", `{"image":null}`, tag, validKey, 422},
		{"invalid image", http.MethodPost, "/apps/api/deploy", `{"image":"bad image"}`, tag, validKey, 422},
		{"rollback bad ID", http.MethodPost, "/apps/api/deployments/Bad/rollback", "", tag, validKey, 400},
		{"rollback body", http.MethodPost, "/apps/api/deployments/" + target + "/rollback", `{}`, tag, validKey, 400},
		{"rollback query", http.MethodPost, "/apps/api/deployments/" + target + "/rollback?foo=1", "", tag, validKey, 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, payload := deploymentRequest(t, server, test.method, test.path, test.body, test.etag, test.key)
			if response.StatusCode != test.want || !bytes.Contains(payload, []byte("request_id")) {
				t.Fatalf("status/body = %d/%s, want %d", response.StatusCode, payload, test.want)
			}
		})
	}
	if called != 0 {
		t.Fatalf("invalid input called service %d times", called)
	}
}

func TestDeploymentAcceptanceAndErrorMapping(t *testing.T) {
	id := uuid.NewString()
	tag := generationETag(id, 1)
	key := "01234567-89ab-cdef-0123-456789abcdef"
	target := uuid.NewString()
	fixedDeploymentID := uuid.NewString()
	var seen app.DeploymentMutationRequest
	service := &deploymentServiceStub{accept: func(_ context.Context, request app.DeploymentMutationRequest) (*store.DeploymentAcceptance, error) {
		seen = request
		return &store.DeploymentAcceptance{AppID: id, DeploymentID: fixedDeploymentID, TargetGeneration: 2}, nil
	}}
	server := newDeploymentTestServer(t, service, nil)
	for _, test := range []struct {
		method, path, body string
		operation          store.DeploymentOperation
	}{
		{http.MethodPost, "/apps/api/deploy", `{"image":"nginx:1.27"}`, store.DeploymentOperationDeploy},
		{http.MethodPost, "/apps/api/deployments/" + target + "/rollback", "", store.DeploymentOperationRollback},
	} {
		response, payload := deploymentRequest(t, server, test.method, test.path, test.body, tag, key)
		if response.StatusCode != 202 || response.Header.Get(fiber.HeaderETag) != generationETag(id, 2) ||
			response.Header.Get(fiber.HeaderLocation) != "/api/v1/apps/api/deployments/"+fixedDeploymentID ||
			seen.Operation != test.operation || seen.IdempotencyKey != key || seen.ExpectedAppID != id {
			t.Fatalf("accept = %d/%#v request=%#v body=%s", response.StatusCode, response.Header, seen, payload)
		}
		var accepted DeploymentAcceptedResponse
		if err := json.Unmarshal(payload, &accepted); err != nil || accepted.DeploymentID != fixedDeploymentID || accepted.TargetGeneration != 2 {
			t.Fatalf("accepted = %#v/%v", accepted, err)
		}
	}

	tests := []struct {
		name     string
		err      error
		accepted *store.DeploymentAcceptance
		want     int
	}{
		{"missing", store.ErrNotFound, nil, 404},
		{"stale", store.ErrGenerationConflict, nil, 412},
		{"key conflict", store.ErrIdempotencyConflict, nil, 409},
		{"state conflict", store.ErrConflict, nil, 409},
		{"capacity", store.ErrIdempotencyCapacity, &store.DeploymentAcceptance{EarliestExpiryUTC: time.Date(2026, time.October, 14, 12, 0, 3, 0, time.UTC)}, 429},
		{"cancelled", context.Canceled, nil, 503},
		{"commit unknown", store.ErrCommitOutcomeUnknown, nil, 503},
		{"corrupt", store.ErrInvalidData, nil, 500},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			service.accept = func(context.Context, app.DeploymentMutationRequest) (*store.DeploymentAcceptance, error) {
				return test.accepted, test.err
			}
			server := newDeploymentTestServer(t, service, &logs)
			response, payload := deploymentRequest(t, server, http.MethodPost, "/apps/api/deploy", `{"image":"nginx:1.27"}`, tag, key)
			if response.StatusCode != test.want || bytes.Contains(payload, []byte(key)) || strings.Contains(logs.String(), key) {
				t.Fatalf("error = %d/%s logs=%s, want %d", response.StatusCode, payload, logs.String(), test.want)
			}
			if test.want == 429 && response.Header.Get(fiber.HeaderRetryAfter) != "3" {
				t.Fatalf("Retry-After = %q", response.Header.Get(fiber.HeaderRetryAfter))
			}
		})
	}
}

func TestDeploymentReadValidationAndPublicDTO(t *testing.T) {
	id := uuid.NewString()
	appID := uuid.NewString()
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	deployment := &store.Deployment{
		ID: id, AppID: appID, SourceType: store.SourceTypeTar, Image: "/private/build.tar",
		Status: store.DeploymentStatusFailed, TriggeredBy: store.TriggeredByAPI,
		TargetGeneration: 0, CreatedAt: now, ErrorMessage: "secret diagnostic",
		ErrorCode: "private_error_code", PreviousObservedImage: "private.registry/image",
	}
	service := &deploymentServiceStub{
		get: func(context.Context, string, string) (*store.Deployment, error) { return deployment, nil },
		list: func(context.Context, string, int, string) (*app.DeploymentPage, error) {
			return &app.DeploymentPage{Deployments: []*store.Deployment{deployment}}, nil
		},
	}
	server := newDeploymentTestServer(t, service, nil)
	for _, path := range []string{"/apps/api/deployments/" + id, "/apps/api/deployments?limit=1"} {
		response, payload := deploymentRequest(t, server, http.MethodGet, path, "", "", "")
		if response.StatusCode != 200 || bytes.Contains(payload, []byte("/private")) ||
			bytes.Contains(payload, []byte("secret")) || bytes.Contains(payload, []byte("app_id")) ||
			!bytes.Contains(payload, []byte("deployment_failed")) {
			t.Fatalf("read = %d/%s", response.StatusCode, payload)
		}
	}
	for _, path := range []string{
		"/apps/api/deployments?limit=0", "/apps/api/deployments?limit=101",
		"/apps/api/deployments?limit=+1", "/apps/api/deployments?limit=1&limit=2",
		"/apps/api/deployments?cursor=", "/apps/api/deployments?other=1",
		"/apps/api/deployments?cursor=%GG", "/apps/api/deployments/" + strings.ToUpper(id),
		"/apps/api/deployments/" + id + "?other=1",
	} {
		response, _ := deploymentRequest(t, server, http.MethodGet, path, "", "", "")
		if response.StatusCode != 400 {
			t.Errorf("%s status = %d, want 400", path, response.StatusCode)
		}
	}
	service.list = func(context.Context, string, int, string) (*app.DeploymentPage, error) {
		return nil, app.ErrInvalidDeploymentCursor
	}
	response, _ := deploymentRequest(t, server, http.MethodGet, "/apps/api/deployments?cursor=bad", "", "", "")
	if response.StatusCode != 400 {
		t.Fatalf("invalid cursor status = %d", response.StatusCode)
	}
	deployment.Status = "private_status"
	response, payload := deploymentRequest(t, server, http.MethodGet, "/apps/api/deployments/"+id, "", "", "")
	if response.StatusCode != 500 || bytes.Contains(payload, []byte("private_status")) {
		t.Fatalf("corrupt status = %d/%s", response.StatusCode, payload)
	}
}

func TestDeploymentDTORejectsCorruptionAndBoundsRetryAfter(t *testing.T) {
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	valid := &store.Deployment{
		ID: uuid.NewString(), AppID: uuid.NewString(), SourceType: store.SourceTypeImage,
		Image: "nginx:1.27", Status: store.DeploymentStatusSucceeded,
		TriggeredBy: store.TriggeredByAPI, TargetGeneration: 2, CreatedAt: now,
		ResolvedImage: "nginx@sha256:" + strings.Repeat("a", 64),
	}
	if _, err := mapDeploymentResponse(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*store.Deployment){
		func(d *store.Deployment) { d.ID = "not-a-uuid" },
		func(d *store.Deployment) { d.AppID = "not-a-uuid" },
		func(d *store.Deployment) { d.SourceType = "other" },
		func(d *store.Deployment) { d.Image = "/private/path" },
		func(d *store.Deployment) { d.Status = "unknown" },
		func(d *store.Deployment) { d.TriggeredBy = "unknown" },
		func(d *store.Deployment) { d.TargetGeneration = -1 },
		func(d *store.Deployment) { d.CreatedAt = time.Time{} },
		func(d *store.Deployment) { d.ResolvedImage = "nginx:mutable" },
		func(d *store.Deployment) { value := "invalid"; d.RollbackSourceDeploymentID = &value },
		func(d *store.Deployment) { value := now.Add(-time.Second); d.FinishedAt = &value },
	} {
		candidate := *valid
		mutate(&candidate)
		if _, err := mapDeploymentResponse(&candidate); !errors.Is(err, errInvalidPublicState) {
			t.Errorf("corrupt candidate %#v accepted: %v", candidate, err)
		}
	}
	if _, err := mapDeploymentPage([]*store.Deployment{valid, valid}, 1, ""); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("oversized page = %v", err)
	}
	if _, err := mapDeploymentPage([]*store.Deployment{valid}, 1, "not-a-cursor"); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("corrupt next cursor = %v", err)
	}
	other := *valid
	other.AppID = uuid.NewString()
	if _, err := mapDeploymentPage([]*store.Deployment{valid, &other}, 2, ""); !errors.Is(err, errInvalidPublicState) {
		t.Fatalf("mixed application page = %v", err)
	}
	for _, test := range []struct {
		expiry time.Time
		want   int
	}{
		{time.Time{}, 1}, {now.Add(-time.Second), 1},
		{now.Add(time.Millisecond), 1}, {now.Add(1500 * time.Millisecond), 2},
		{now.Add(48 * time.Hour), 86400},
	} {
		if got := deploymentRetryAfter(now, test.expiry); got != test.want {
			t.Errorf("Retry-After = %d, want %d", got, test.want)
		}
	}
}
