package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/middleware"
)

func TestSecurityHeaders(t *testing.T) {
	server := fiber.New()
	server.Use(middleware.SecurityHeaders())
	server.Get("/", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
	response, err := server.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	want := map[string]string{
		fiber.HeaderXContentTypeOptions: "nosniff",
		"Content-Security-Policy":       "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'",
		"Permissions-Policy":            "camera=(), geolocation=(), microphone=()",
		"Referrer-Policy":               "no-referrer",
		"X-Frame-Options":               "DENY",
	}
	for header, value := range want {
		if got := response.Header.Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
}
