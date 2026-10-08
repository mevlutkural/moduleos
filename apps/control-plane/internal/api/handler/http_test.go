package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func testRequest(t testing.TB, server *fiber.App, request *http.Request, config ...fiber.TestConfig) *http.Response {
	t.Helper()
	response, err := server.Test(request, config...)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func decodeResponse[T any](t testing.TB, response *http.Response) T {
	t.Helper()
	var value T
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return value
}
