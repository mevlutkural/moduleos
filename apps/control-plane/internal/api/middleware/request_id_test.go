package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/middleware"
)

func TestRequestIDMiddleware(t *testing.T) {
	server := fiber.New()
	server.Use(middleware.RequestID())
	server.Get("/", func(c fiber.Ctx) error { return c.SendString(apiresponse.RequestID(c)) })

	want := uuid.NewString()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(apiresponse.RequestIDHeader, want)
	response, err := server.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if got := response.Header.Get(apiresponse.RequestIDHeader); got != want {
		t.Fatalf("request ID = %q, want %q", got, want)
	}

	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(apiresponse.RequestIDHeader, strings.Repeat("x", 512))
	response, err = server.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if _, err := uuid.Parse(response.Header.Get(apiresponse.RequestIDHeader)); err != nil {
		t.Fatalf("replacement request ID is invalid: %v", err)
	}
}
