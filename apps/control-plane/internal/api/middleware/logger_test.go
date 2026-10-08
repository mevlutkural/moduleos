package middleware_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/middleware"
)

func TestRequestLoggerUsesRouteTemplateAndExcludesSecrets(t *testing.T) {
	var output bytes.Buffer
	log := slog.New(slog.NewTextHandler(&output, nil))
	server := fiber.New()
	server.Use(middleware.RequestLogger(log))
	server.Get("/apps/:name", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/apps/private-name?token=query-secret", nil)
	request.Header.Set(fiber.HeaderAuthorization, "Bearer header-secret")
	response, err := server.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	logged := output.String()
	for _, want := range []string{"HTTP request", "method=GET", "route=/apps/:name", "status=204"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log %q does not contain %q", logged, want)
		}
	}
	for _, forbidden := range []string{"private-name", "query-secret", "header-secret", "Authorization"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("log leaked %q: %q", forbidden, logged)
		}
	}
}

func TestRequestLoggerRecordsReturnedErrorStatus(t *testing.T) {
	var output bytes.Buffer
	log := slog.New(slog.NewTextHandler(&output, nil))
	server := fiber.New()
	server.Use(middleware.RequestLogger(log))
	server.Get("/", func(fiber.Ctx) error { return fiber.ErrServiceUnavailable })
	response, err := server.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !strings.Contains(output.String(), "status=503") {
		t.Fatalf("log = %q, want status 503", output.String())
	}
}

func TestRequestLoggerRecordsMiddlewareRejection(t *testing.T) {
	var output bytes.Buffer
	log := slog.New(slog.NewTextHandler(&output, nil))
	server := fiber.New()
	server.Use(middleware.RequestLogger(log))
	server.Use(func(c fiber.Ctx) error {
		return apiresponse.WriteError(c, fiber.StatusServiceUnavailable, "server_busy", "server is busy", apiresponse.ErrorOptions{Retryable: true})
	})
	server.Get("/ready", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
	response, err := server.Test(httptest.NewRequest(http.MethodGet, "/ready", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	logged := output.String()
	for _, want := range []string{"HTTP request", "route=<unmatched>", "status=503"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log %q does not contain %q", logged, want)
		}
	}
}
