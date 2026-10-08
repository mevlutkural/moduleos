package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/middleware"
)

func TestRecoveryConvertsPanicWithoutLeakingValue(t *testing.T) {
	var output bytes.Buffer
	log := slog.New(slog.NewTextHandler(&output, nil))
	server := fiber.New(fiber.Config{ErrorHandler: apiresponse.NewErrorHandler(log)})
	server.Use(middleware.RequestID())
	server.Use(middleware.Recovery(log))
	server.Get("/apps/:name", func(fiber.Ctx) error { panic("secret-panic-value") })
	response, err := server.Test(httptest.NewRequest(http.MethodGet, "/apps/private-app-name", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var body apiresponse.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusInternalServerError || body.Error.Code != "internal_error" {
		t.Fatalf("response = %d/%#v", response.StatusCode, body)
	}
	if strings.Contains(output.String(), "secret-panic-value") || strings.Contains(output.String(), "private-app-name") || strings.Contains(body.Error.Message, "secret-panic-value") {
		t.Fatalf("panic value leaked: log=%q body=%#v", output.String(), body)
	}
	if !strings.Contains(output.String(), "HTTP panic recovered") || !strings.Contains(output.String(), "route=/apps/:name") || !strings.Contains(output.String(), "stack=") {
		t.Fatalf("panic diagnostics missing: %q", output.String())
	}
}

func TestRecoveryPassesThroughNormalResponse(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	server := fiber.New()
	server.Use(middleware.Recovery(log))
	server.Get("/", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
	response, err := server.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != fiber.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
}
