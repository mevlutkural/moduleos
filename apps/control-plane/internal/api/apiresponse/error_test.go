package apiresponse_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
)

func decodeError(t *testing.T, response *http.Response) apiresponse.ErrorResponse {
	t.Helper()
	defer func() { _ = response.Body.Close() }()
	var body apiresponse.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return body
}

func TestEnsureRequestIDPreservesCanonicalUUID(t *testing.T) {
	server := fiber.New()
	server.Get("/", func(c fiber.Ctx) error {
		first := apiresponse.EnsureRequestID(c)
		second := apiresponse.RequestID(c)
		if first != second {
			t.Fatalf("request ID changed from %q to %q", first, second)
		}
		return c.SendString(first)
	})
	want := uuid.NewString()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(apiresponse.RequestIDHeader, strings.ToUpper(want))
	response, err := server.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if got := response.Header.Get(apiresponse.RequestIDHeader); got != want {
		t.Fatalf("request ID = %q, want %q", got, want)
	}
}

func TestEnsureRequestIDReplacesInvalidOrNonCanonicalValues(t *testing.T) {
	values := []string{"", "not-a-uuid", strings.Repeat("a", 512), strings.ReplaceAll(uuid.NewString(), "-", "")}
	for _, value := range values {
		t.Run(value[:min(len(value), 16)], func(t *testing.T) {
			server := fiber.New()
			server.Get("/", func(c fiber.Ctx) error {
				return c.SendString(apiresponse.EnsureRequestID(c))
			})
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set(apiresponse.RequestIDHeader, value)
			response, err := server.Test(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			generated := response.Header.Get(apiresponse.RequestIDHeader)
			if _, err := uuid.Parse(generated); err != nil || len(generated) != 36 {
				t.Fatalf("generated request ID = %q, error = %v", generated, err)
			}
			if value != "" && generated == value {
				t.Fatalf("invalid request ID was preserved")
			}
		})
	}
}

func TestEnsureRequestIDReplacesDuplicateValues(t *testing.T) {
	server := fiber.New()
	server.Get("/", func(c fiber.Ctx) error {
		return c.SendString(apiresponse.EnsureRequestID(c))
	})
	first := uuid.NewString()
	second := uuid.NewString()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Add(apiresponse.RequestIDHeader, first)
	request.Header.Add(apiresponse.RequestIDHeader, second)
	response, err := server.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	generated := response.Header.Get(apiresponse.RequestIDHeader)
	if _, err := uuid.Parse(generated); err != nil || len(generated) != 36 {
		t.Fatalf("generated request ID = %q, error = %v", generated, err)
	}
	if generated == first || generated == second {
		t.Fatalf("duplicate request ID was preserved: %q", generated)
	}
}

func TestBeginRequestRecordsOneStableStartTime(t *testing.T) {
	server := fiber.New()
	server.Get("/", func(c fiber.Ctx) error {
		if duration := apiresponse.RequestDuration(c); duration != 0 {
			t.Fatalf("duration before request start = %s, want 0", duration)
		}
		apiresponse.BeginRequest(c)
		time.Sleep(time.Millisecond)
		first := apiresponse.RequestDuration(c)
		apiresponse.BeginRequest(c)
		second := apiresponse.RequestDuration(c)
		if first <= 0 || second < first {
			t.Fatalf("request start was reset: first=%s second=%s", first, second)
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	response, err := server.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestWriteErrorUsesStableEnvelopeAndHeaders(t *testing.T) {
	server := fiber.New()
	server.Get("/", func(c fiber.Ctx) error {
		return apiresponse.WriteError(c, fiber.StatusUnauthorized, "unauthorized", "authentication is required", apiresponse.ErrorOptions{
			Retryable: true,
			Fields:    map[string]string{"token": "missing"},
		})
	})
	response, err := server.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	body := decodeError(t, response)
	if response.StatusCode != fiber.StatusUnauthorized || response.Header.Get(fiber.HeaderWWWAuthenticate) != "Bearer" || response.Header.Get(fiber.HeaderCacheControl) != "no-store" {
		t.Fatalf("status/headers = %d/%q/%q", response.StatusCode, response.Header.Get(fiber.HeaderWWWAuthenticate), response.Header.Get(fiber.HeaderCacheControl))
	}
	if response.Header.Get(fiber.HeaderXContentTypeOptions) != "nosniff" ||
		response.Header.Get("Content-Security-Policy") == "" ||
		response.Header.Get("Permissions-Policy") == "" ||
		response.Header.Get("Referrer-Policy") != "no-referrer" ||
		response.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers = %#v", response.Header)
	}
	if body.Error.Code != "unauthorized" || body.Error.Message != "authentication is required" || !body.Error.Retryable || body.Error.Fields["token"] != "missing" {
		t.Fatalf("error body = %#v", body)
	}
	if _, err := uuid.Parse(body.Error.RequestID); err != nil {
		t.Fatalf("request ID = %q: %v", body.Error.RequestID, err)
	}
}

func TestErrorHandlerMapsFrameworkErrorsWithoutLeakingDetails(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		retryable  bool
	}{
		{name: "bad request", err: fiber.ErrBadRequest, wantStatus: 400, wantCode: "invalid_request"},
		{name: "unauthorized", err: fiber.ErrUnauthorized, wantStatus: 401, wantCode: "unauthorized"},
		{name: "forbidden", err: fiber.ErrForbidden, wantStatus: 403, wantCode: "forbidden"},
		{name: "not found", err: fiber.ErrNotFound, wantStatus: 404, wantCode: "not_found"},
		{name: "method", err: fiber.ErrMethodNotAllowed, wantStatus: 405, wantCode: "method_not_allowed"},
		{name: "conflict", err: fiber.ErrConflict, wantStatus: 409, wantCode: "conflict"},
		{name: "stale", err: fiber.ErrPreconditionFailed, wantStatus: 412, wantCode: "precondition_failed"},
		{name: "timeout", err: fiber.ErrRequestTimeout, wantStatus: 408, wantCode: "request_timeout", retryable: true},
		{name: "payload", err: fiber.ErrRequestEntityTooLarge, wantStatus: 413, wantCode: "payload_too_large"},
		{name: "URI", err: fiber.ErrRequestURITooLong, wantStatus: 414, wantCode: "uri_too_long"},
		{name: "media", err: fiber.ErrUnsupportedMediaType, wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "validation", err: fiber.ErrUnprocessableEntity, wantStatus: 422, wantCode: "validation_failed"},
		{name: "precondition", err: fiber.ErrPreconditionRequired, wantStatus: 428, wantCode: "precondition_required"},
		{name: "limited", err: fiber.ErrTooManyRequests, wantStatus: 429, wantCode: "rate_limited", retryable: true},
		{name: "headers", err: fiber.ErrRequestHeaderFieldsTooLarge, wantStatus: 431, wantCode: "headers_too_large"},
		{name: "upstream", err: fiber.ErrBadGateway, wantStatus: 502, wantCode: "bad_gateway", retryable: true},
		{name: "unavailable", err: fiber.ErrServiceUnavailable, wantStatus: 503, wantCode: "dependency_unavailable", retryable: true},
		{name: "other client", err: fiber.NewError(418, "sensitive detail"), wantStatus: 418, wantCode: "request_rejected"},
		{name: "internal", err: errors.New("database password is secret-value"), wantStatus: 500, wantCode: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			log := slog.New(slog.NewTextHandler(&logs, nil))
			server := fiber.New(fiber.Config{ErrorHandler: apiresponse.NewErrorHandler(log)})
			server.Get("/", func(fiber.Ctx) error { return test.err })
			response, err := server.Test(httptest.NewRequest(http.MethodGet, "/", nil))
			if err != nil {
				t.Fatal(err)
			}
			body := decodeError(t, response)
			if response.StatusCode != test.wantStatus || body.Error.Code != test.wantCode || body.Error.Retryable != test.retryable {
				t.Fatalf("status/body = %d/%#v", response.StatusCode, body)
			}
			if strings.Contains(body.Error.Message, "sensitive") || strings.Contains(body.Error.Message, "password") || strings.Contains(logs.String(), "secret-value") {
				t.Fatalf("sensitive error detail leaked: body=%#v logs=%q", body, logs.String())
			}
			if test.wantStatus >= 500 && !strings.Contains(logs.String(), "HTTP request failed") {
				t.Fatalf("server failure was not logged: %q", logs.String())
			}
		})
	}
}

func TestErrorHandlerLogsRouteTemplateWithoutPathValues(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	server := fiber.New(fiber.Config{ErrorHandler: apiresponse.NewErrorHandler(log)})
	server.Get("/apps/:name", func(fiber.Ctx) error { return errors.New("internal failure") })
	response, err := server.Test(httptest.NewRequest(http.MethodGet, "/apps/private-app-name", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = decodeError(t, response)
	if strings.Contains(logs.String(), "private-app-name") || !strings.Contains(logs.String(), "route=/apps/:name") {
		t.Fatalf("error log did not preserve the route boundary: %q", logs.String())
	}
}

func TestErrorHandlerLogsUnmatchedRouteWithoutRawPath(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	server := fiber.New(fiber.Config{ErrorHandler: apiresponse.NewErrorHandler(log)})
	response, err := server.Test(httptest.NewRequest(http.MethodGet, "/private-missing-path", nil))
	if err != nil {
		t.Fatal(err)
	}
	body := decodeError(t, response)
	if response.StatusCode != fiber.StatusNotFound || body.Error.Code != "not_found" {
		t.Fatalf("status/body = %d/%#v", response.StatusCode, body)
	}
	if !strings.Contains(logs.String(), "route=<unmatched>") || !strings.Contains(logs.String(), "status=404") || strings.Contains(logs.String(), "private-missing-path") {
		t.Fatalf("unmatched request log crossed the route boundary: %q", logs.String())
	}
}

func TestStatusForErrorDefaultsToInternalServerError(t *testing.T) {
	if got := apiresponse.StatusForError(errors.New("test")); got != fiber.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", got)
	}
	if got := apiresponse.StatusForError(fiber.ErrConflict); got != fiber.StatusConflict {
		t.Fatalf("status = %d, want 409", got)
	}
}
