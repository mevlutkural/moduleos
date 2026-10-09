package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/middleware"
)

func TestNoStoreAppliesToSuccessfulResourceResponses(t *testing.T) {
	server := fiber.New()
	server.Use(middleware.NoStore())
	server.Get("/", func(c fiber.Ctx) error { return c.JSON(fiber.Map{"ok": true}) })
	response, err := server.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.Header.Get(fiber.HeaderCacheControl) != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header.Get(fiber.HeaderCacheControl))
	}
}
