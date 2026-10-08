package middleware_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/middleware"
)

func TestBearerAuthRejectsEmptyKey(t *testing.T) {
	if _, err := middleware.BearerAuth(""); !errors.Is(err, middleware.ErrEmptyAPIKey) {
		t.Fatalf("error = %v, want ErrEmptyAPIKey", err)
	}
}

func TestBearerAuthRejectsKeyContainingWhitespace(t *testing.T) {
	for _, apiKey := range []string{" leading", "trailing ", "embedded space", "embedded\ttab", "embedded\nnewline"} {
		if _, err := middleware.BearerAuth(apiKey); !errors.Is(err, middleware.ErrInvalidAPIKey) {
			t.Fatalf("API key %q error = %v, want ErrInvalidAPIKey", apiKey, err)
		}
	}
}

func TestBearerAuth(t *testing.T) {
	auth, err := middleware.BearerAuth("correct-secret")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		authorization string
		query         string
		wantStatus    int
	}{
		{name: "valid", authorization: "Bearer correct-secret", wantStatus: 204},
		{name: "case insensitive scheme", authorization: "bearer correct-secret", wantStatus: 204},
		{name: "missing", wantStatus: 401},
		{name: "wrong scheme", authorization: "Token correct-secret", wantStatus: 401},
		{name: "missing credential", authorization: "Bearer ", wantStatus: 401},
		{name: "extra space", authorization: "Bearer  correct-secret", wantStatus: 401},
		{name: "tab separator", authorization: "Bearer\tcorrect-secret", wantStatus: 401},
		{name: "wrong credential", authorization: "Bearer wrong-secret", wantStatus: 401},
		{name: "query token rejected", query: "?api_key=correct-secret", wantStatus: 401},
		{name: "header wins over ignored query", authorization: "Bearer correct-secret", query: "?api_key=wrong", wantStatus: 204},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := fiber.New()
			server.Use(middleware.RequestID())
			server.Use(auth)
			server.Get("/", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
			request := httptest.NewRequest(http.MethodGet, "/"+test.query, nil)
			if test.authorization != "" {
				request.Header.Set(fiber.HeaderAuthorization, test.authorization)
			}
			response, err := server.Test(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
			if test.wantStatus == 401 {
				var body apiresponse.ErrorResponse
				if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Error.Code != "unauthorized" || body.Error.Message != "authentication is required" || response.Header.Get(fiber.HeaderWWWAuthenticate) != "Bearer" {
					t.Fatalf("unauthorized response = %#v, challenge=%q", body, response.Header.Get(fiber.HeaderWWWAuthenticate))
				}
			}
		})
	}
}

func TestBearerAuthRejectsDuplicateAuthorizationHeaders(t *testing.T) {
	auth, err := middleware.BearerAuth("correct-secret")
	if err != nil {
		t.Fatal(err)
	}
	server := fiber.New()
	server.Use(middleware.RequestID())
	server.Use(auth)
	server.Get("/", func(c fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Add(fiber.HeaderAuthorization, "Bearer correct-secret")
	request.Header.Add(fiber.HeaderAuthorization, "Bearer second-secret")
	response, err := server.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
}
