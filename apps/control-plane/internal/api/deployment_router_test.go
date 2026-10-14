package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/handler"
	controlapp "github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/testkit/swarmfake"
)

func deploymentRouterFixture(t *testing.T) (*Router, *store.SQLiteStore, *controlapp.Service, *routerRecordingQueue) {
	return deploymentRouterFixtureAtPath(t, ":memory:")
}

func deploymentRouterFixtureAtPath(t *testing.T, databasePath string) (*Router, *store.SQLiteStore, *controlapp.Service, *routerRecordingQueue) {
	t.Helper()
	st, err := store.NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	queue := &routerRecordingQueue{}
	service := controlapp.NewService(st, swarmfake.New(), "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithReconcileQueue(queue).WithRuntimeLimits(20, nil, time.Minute)
	options := validRouterOptions()
	options.Store, options.Projects, options.Applications, options.Deployments = st, service, service, service
	options.APIKey = "test-key"
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	return router, st, service, queue
}

func TestRouterDeploymentReplaySurvivesSQLiteReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moduleos.db")
	router, st, service, _ := deploymentRouterFixtureAtPath(t, path)
	created, err := service.CreateApp(t.Context(), controlapp.CreateAppRequest{Name: "api", Image: "nginx:1.27"})
	if err != nil {
		t.Fatal(err)
	}
	tag, key := applicationTag(created.ID, created.DesiredGeneration), "01234567-89ab-cdef-0123-456789abcdef"
	first, firstBody := deploymentRouterRequest(t, router, http.MethodPost, "/api/v1/apps/api/deploy",
		"{\"image\":\"nginx:2.0\"}", tag, key, true)
	if first.StatusCode != 202 {
		t.Fatalf("initial acceptance = %d/%s", first.StatusCode, firstBody)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedRouter, reopenedStore, _, queue := deploymentRouterFixtureAtPath(t, path)
	replay, replayBody := deploymentRouterRequest(t, reopenedRouter, http.MethodPost, "/api/v1/apps/api/deploy",
		"{\"image\":\"nginx:2.0\"}", tag, key, true)
	if replay.StatusCode != 202 || string(replayBody) != string(firstBody) ||
		replay.Header.Get(fiber.HeaderLocation) != first.Header.Get(fiber.HeaderLocation) ||
		replay.Header.Get(fiber.HeaderETag) != first.Header.Get(fiber.HeaderETag) || queue.count() != 0 {
		t.Fatalf("restart replay = %d/%#v/%s queue=%d", replay.StatusCode, replay.Header, replayBody, queue.count())
	}
	current, err := reopenedStore.GetApplication(t.Context(), created.Name)
	if err != nil || current.DesiredGeneration != created.DesiredGeneration+1 {
		t.Fatalf("restart state = %#v/%v", current, err)
	}
}

func deploymentRouterRequest(t *testing.T, router *Router, method, path, body, etag, key string, authorized bool) (*http.Response, []byte) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorized {
		request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
	}
	if body != "" {
		request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	if etag != "" {
		request.Header.Set(fiber.HeaderIfMatch, etag)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := sendRequest(t, router.App, request)
	payload := validateOpenAPIResponse(t, contractRouterForTest(t), request, response)
	return response, payload
}

func TestRouterDeploymentLifecycleMatchesOpenAPIAndPersistsIntent(t *testing.T) {
	router, st, service, queue := deploymentRouterFixture(t)
	created, err := service.CreateApp(t.Context(), controlapp.CreateAppRequest{Name: "api", Image: "nginx:1.27"})
	if err != nil {
		t.Fatal(err)
	}
	initialQueue := queue.count()
	tag := applicationTag(created.ID, created.DesiredGeneration)
	key := "01234567-89ab-cdef-0123-456789abcdef"
	first, firstBody := deploymentRouterRequest(t, router, http.MethodPost, "/api/v1/apps/api/deploy",
		"{\"image\":\"nginx:2.0\"}", tag, key, true)
	var accepted handler.DeploymentAcceptedResponse
	if err := json.Unmarshal(firstBody, &accepted); err != nil {
		t.Fatal(err)
	}
	location, acceptedTag := first.Header.Get(fiber.HeaderLocation), first.Header.Get(fiber.HeaderETag)
	if first.StatusCode != 202 || accepted.Status != "accepted" ||
		accepted.TargetGeneration != created.DesiredGeneration+1 ||
		location != "/api/v1/apps/api/deployments/"+accepted.DeploymentID ||
		acceptedTag != applicationTag(created.ID, accepted.TargetGeneration) ||
		queue.count() != initialQueue+1 {
		t.Fatalf("acceptance = %d/%#v/%s queue=%d", first.StatusCode, first.Header, firstBody, queue.count())
	}
	detail, detailBody := deploymentRouterRequest(t, router, http.MethodGet, location, "", "", "", true)
	if detail.StatusCode != 200 || !bytes.Contains(detailBody, []byte(accepted.DeploymentID)) {
		t.Fatalf("poll = %d/%s", detail.StatusCode, detailBody)
	}
	resolved := "nginx@sha256:" + strings.Repeat("a", 64)
	if err := st.MarkDeploymentSucceeded(t.Context(), accepted.DeploymentID, accepted.TargetGeneration, resolved); err != nil {
		t.Fatal(err)
	}
	second, secondBody := deploymentRouterRequest(t, router, http.MethodPost, "/api/v1/apps/api/deploy",
		"{\"image\":\"nginx:2.0\"}", acceptedTag, "11234567-89ab-cdef-0123-456789abcdef", true)
	var secondAccepted handler.DeploymentAcceptedResponse
	if err := json.Unmarshal(secondBody, &secondAccepted); err != nil || second.StatusCode != 202 ||
		secondAccepted.TargetGeneration != accepted.TargetGeneration+1 ||
		secondAccepted.DeploymentID == accepted.DeploymentID {
		t.Fatalf("second = %d/%s/%v", second.StatusCode, secondBody, err)
	}
	replay, replayBody := deploymentRouterRequest(t, router, http.MethodPost, "/api/v1/apps/api/deploy",
		"{ \"image\" : \"nginx:2.0\" }", tag, key, true)
	if replay.StatusCode != 202 || string(replayBody) != string(firstBody) ||
		replay.Header.Get(fiber.HeaderLocation) != location ||
		replay.Header.Get(fiber.HeaderETag) != acceptedTag ||
		replay.Header.Get(apiresponse.RequestIDHeader) == first.Header.Get(apiresponse.RequestIDHeader) ||
		queue.count() != initialQueue+2 {
		t.Fatalf("replay = %d/%#v/%s queue=%d", replay.StatusCode, replay.Header, replayBody, queue.count())
	}
	conflict, _ := deploymentRouterRequest(t, router, http.MethodPost, "/api/v1/apps/api/deploy",
		"{\"image\":\"nginx:3.0\"}", tag, key, true)
	stale, _ := deploymentRouterRequest(t, router, http.MethodPost, "/api/v1/apps/api/deploy",
		"{\"image\":\"nginx:3.0\"}", tag, "21234567-89ab-cdef-0123-456789abcdef", true)
	if conflict.StatusCode != 409 || stale.StatusCode != 412 {
		t.Fatalf("conflict/stale = %d/%d", conflict.StatusCode, stale.StatusCode)
	}
	history, historyBody := deploymentRouterRequest(t, router, http.MethodGet, "/api/v1/apps/api/deployments?limit=1", "", "", "", true)
	var page handler.DeploymentCollectionResponse
	if err := json.Unmarshal(historyBody, &page); err != nil || history.StatusCode != 200 ||
		len(page.Deployments) != 1 || page.NextCursor == nil || page.Limit != 1 {
		t.Fatalf("history = %d/%s/%v", history.StatusCode, historyBody, err)
	}
	next, nextBody := deploymentRouterRequest(t, router, http.MethodGet,
		"/api/v1/apps/api/deployments?limit=1&cursor="+*page.NextCursor, "", "", "", true)
	var nextPage handler.DeploymentCollectionResponse
	if err := json.Unmarshal(nextBody, &nextPage); err != nil || next.StatusCode != 200 ||
		len(nextPage.Deployments) != 1 || nextPage.Deployments[0].ID == page.Deployments[0].ID {
		t.Fatalf("next = %d/%s/%v", next.StatusCode, nextBody, err)
	}
	if _, err := service.CreateApp(t.Context(), controlapp.CreateAppRequest{Name: "other", Image: "nginx:1.27"}); err != nil {
		t.Fatal(err)
	}
	foreign, _ := deploymentRouterRequest(t, router, http.MethodGet,
		"/api/v1/apps/other/deployments/"+accepted.DeploymentID, "", "", "", true)
	foreignCursor, _ := deploymentRouterRequest(t, router, http.MethodGet,
		"/api/v1/apps/other/deployments?cursor="+*page.NextCursor, "", "", "", true)
	if foreign.StatusCode != 404 || foreignCursor.StatusCode != 400 {
		t.Fatalf("foreign detail/cursor = %d/%d", foreign.StatusCode, foreignCursor.StatusCode)
	}
	rollbackKey := "31234567-89ab-cdef-0123-456789abcdef"
	rollback, rollbackBody := deploymentRouterRequest(t, router, http.MethodPost,
		"/api/v1/apps/api/deployments/"+accepted.DeploymentID+"/rollback", "",
		second.Header.Get(fiber.HeaderETag), rollbackKey, true)
	var rollbackAccepted handler.DeploymentAcceptedResponse
	if err := json.Unmarshal(rollbackBody, &rollbackAccepted); err != nil ||
		rollback.StatusCode != 202 || rollbackAccepted.TargetGeneration != secondAccepted.TargetGeneration+1 {
		t.Fatalf("rollback = %d/%s/%v", rollback.StatusCode, rollbackBody, err)
	}
	persisted, err := st.GetDeploymentForApp(t.Context(), created.ID, rollbackAccepted.DeploymentID)
	if err != nil || persisted.Image != resolved || persisted.RollbackSourceDeploymentID == nil ||
		*persisted.RollbackSourceDeploymentID != accepted.DeploymentID {
		t.Fatalf("rollback persistence = %#v/%v", persisted, err)
	}
	rollbackReplay, replayBody := deploymentRouterRequest(t, router, http.MethodPost,
		"/api/v1/apps/api/deployments/"+accepted.DeploymentID+"/rollback", "",
		second.Header.Get(fiber.HeaderETag), rollbackKey, true)
	if rollbackReplay.StatusCode != 202 || string(replayBody) != string(rollbackBody) ||
		rollbackReplay.Header.Get(fiber.HeaderETag) != rollback.Header.Get(fiber.HeaderETag) {
		t.Fatalf("rollback replay = %d/%s", rollbackReplay.StatusCode, replayBody)
	}
}

func TestRouterDeploymentFailureResponsesMatchOpenAPI(t *testing.T) {
	router, _, service, _ := deploymentRouterFixture(t)
	created, err := service.CreateApp(t.Context(), controlapp.CreateAppRequest{Name: "api", Image: "nginx:1.27"})
	if err != nil {
		t.Fatal(err)
	}
	tag := applicationTag(created.ID, created.DesiredGeneration)
	key := "01234567-89ab-cdef-0123-456789abcdef"
	tests := []struct {
		name, method, path, body, etag, key string
		authorized                          bool
		want                                int
	}{
		{"auth", http.MethodGet, "/api/v1/apps/api/deployments", "", "", "", false, 401},
		{"missing ETag", http.MethodPost, "/api/v1/apps/api/deploy", "{\"image\":\"nginx\"}", "", key, true, 428},
		{"bad ETag", http.MethodPost, "/api/v1/apps/api/deploy", "{\"image\":\"nginx\"}", "W/" + tag, key, true, 400},
		{"bad key", http.MethodPost, "/api/v1/apps/api/deploy", "{\"image\":\"nginx\"}", tag, "short", true, 400},
		{"bad image", http.MethodPost, "/api/v1/apps/api/deploy", "{\"image\":\"bad image\"}", tag, key, true, 422},
		{"bad name", http.MethodGet, "/api/v1/apps/Bad/deployments", "", "", "", true, 400},
		{"missing app", http.MethodGet, "/api/v1/apps/missing/deployments", "", "", "", true, 404},
		{"invalid cursor", http.MethodGet, "/api/v1/apps/api/deployments?cursor=bad", "", "", "", true, 400},
		{"missing target", http.MethodPost, "/api/v1/apps/api/deployments/" + uuid.NewString() + "/rollback", "", tag, key, true, 404},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, body := deploymentRouterRequest(t, router, test.method, test.path, test.body, test.etag, test.key, test.authorized)
			if response.StatusCode != test.want {
				t.Fatalf("response = %d/%s, want %d", response.StatusCode, body, test.want)
			}
			if response.Header.Get(fiber.HeaderCacheControl) != "no-store" ||
				response.Header.Get("Content-Security-Policy") == "" ||
				response.Header.Get(apiresponse.RequestIDHeader) == "" {
				t.Fatalf("missing hardening headers: %#v", response.Header)
			}
		})
	}
}

func TestRouterDeploymentCORSAndHeaderBoundaries(t *testing.T) {
	options := validRouterOptions()
	options.APIKey = "test-key"
	options.CORSAllowedOrigins = []string{"https://console.example.com"}
	router, err := NewRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	preflight := httptest.NewRequest(http.MethodOptions, "/api/v1/apps/api/deploy", nil)
	preflight.Header.Set(fiber.HeaderOrigin, "https://console.example.com")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	preflight.Header.Set("Access-Control-Request-Headers", "if-match,idempotency-key,content-type,authorization")
	preflightResponse := sendRequest(t, router.App, preflight)
	allowed := strings.ToLower(preflightResponse.Header.Get("Access-Control-Allow-Headers"))
	if preflightResponse.StatusCode != 204 ||
		preflightResponse.Header.Get("Access-Control-Allow-Origin") != "https://console.example.com" ||
		!strings.Contains(allowed, "idempotency-key") || !strings.Contains(allowed, "if-match") {
		t.Fatalf("preflight = %d/%#v", preflightResponse.StatusCode, preflightResponse.Header)
	}
	tag := applicationTag(uuid.NewString(), 1)
	for _, test := range []struct {
		name string
		set  func(*http.Request)
		want int
	}{
		{"duplicate key", func(r *http.Request) {
			r.Header.Add("Idempotency-Key", "11234567-89ab-cdef-0123-456789abcdef")
		}, 400},
		{"wrong media", func(r *http.Request) {
			r.Header.Set(fiber.HeaderContentType, fiber.MIMETextPlain)
		}, 415},
		{"invalid UTF-8", func(r *http.Request) {
			body := []byte{'{', '"', 'i', 'm', 'a', 'g', 'e', '"', ':', '"', 0xff, '"', '}'}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		}, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/apps/api/deploy", strings.NewReader("{\"image\":\"nginx\"}"))
			request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
			request.Header.Set(fiber.HeaderIfMatch, tag)
			request.Header.Set("Idempotency-Key", "01234567-89ab-cdef-0123-456789abcdef")
			request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
			test.set(request)
			response := sendRequest(t, router.App, request)
			payload := validateOpenAPIResponse(t, contractRouterForTest(t), request, response)
			if response.StatusCode != test.want {
				t.Fatalf("response = %d/%s, want %d", response.StatusCode, payload, test.want)
			}
		})
	}
}

func TestRouterDeploymentOversizedBodyReturnsDocumented413(t *testing.T) {
	router, _, _, _ := deploymentRouterFixture(t)
	baseURL := startRealHTTPServer(t, router.App)
	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/apps/api/deploy",
		strings.NewReader("{\"image\":\""+strings.Repeat("x", 1100)+"\"}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
	request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	request.Header.Set(fiber.HeaderIfMatch, applicationTag(uuid.NewString(), 1))
	request.Header.Set("Idempotency-Key", "01234567-89ab-cdef-0123-456789abcdef")
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	contractRequest := httptest.NewRequest(http.MethodPost, "/api/v1/apps/api/deploy", nil)
	payload := validateOpenAPIResponse(t, contractRouterForTest(t), contractRequest, response)
	if response.StatusCode != 413 || !bytes.Contains(payload, []byte("payload_too_large")) {
		t.Fatalf("oversized request = %d/%s", response.StatusCode, payload)
	}
}

func TestRouterConcurrentDeploymentRetryCreatesOneIntent(t *testing.T) {
	router, st, service, queue := deploymentRouterFixture(t)
	created, err := service.CreateApp(t.Context(), controlapp.CreateAppRequest{Name: "api", Image: "nginx:1.27"})
	if err != nil {
		t.Fatal(err)
	}
	initialQueue := queue.count()
	tag, key := applicationTag(created.ID, created.DesiredGeneration), "01234567-89ab-cdef-0123-456789abcdef"
	start := make(chan struct{})
	results := make(chan handler.DeploymentAcceptedResponse, 2)
	errs := make(chan error, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			request := httptest.NewRequest(http.MethodPost, "/api/v1/apps/api/deploy", strings.NewReader("{\"image\":\"nginx:2.0\"}"))
			request.Header.Set(fiber.HeaderAuthorization, "Bearer test-key")
			request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
			request.Header.Set(fiber.HeaderIfMatch, tag)
			request.Header.Set("Idempotency-Key", key)
			response, err := router.App.Test(request)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = response.Body.Close() }()
			var accepted handler.DeploymentAcceptedResponse
			if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
				errs <- err
				return
			}
			if response.StatusCode != 202 {
				errs <- store.ErrConflict
				return
			}
			results <- accepted
		}()
	}
	close(start)
	workers.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Fatal(err)
	}
	var firstID string
	for accepted := range results {
		if firstID != "" && accepted.DeploymentID != firstID {
			t.Fatalf("concurrent retries returned different IDs: %s and %s", firstID, accepted.DeploymentID)
		}
		firstID = accepted.DeploymentID
	}
	current, err := st.GetApplication(t.Context(), created.Name)
	if err != nil || current.DesiredGeneration != created.DesiredGeneration+1 || queue.count() != initialQueue+1 {
		t.Fatalf("concurrent state = %#v/%v queue=%d", current, err, queue.count())
	}
}

func applicationTag(id string, generation int64) string {
	return "\"app:" + id + ":" + strconv.FormatInt(generation, 10) + "\""
}
