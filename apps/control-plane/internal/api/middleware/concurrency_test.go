package middleware_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/middleware"
)

func TestConcurrencyRejectsInvalidLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if _, err := middleware.Concurrency(limit); !errors.Is(err, middleware.ErrInvalidConcurrencyLimit) {
			t.Fatalf("limit %d error = %v", limit, err)
		}
	}
}

func TestConcurrencyRejectsAndThenReleasesCapacity(t *testing.T) {
	guard, err := middleware.Concurrency(1)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var blockFirst sync.Once
	server := fiber.New()
	server.Use(middleware.RequestID())
	server.Use(guard)
	server.Get("/", func(c fiber.Ctx) error {
		blockFirst.Do(func() {
			close(entered)
			<-release
		})
		return c.SendStatus(fiber.StatusNoContent)
	})

	firstDone := make(chan *http.Response, 1)
	go func() {
		response, _ := server.Test(httptest.NewRequest(http.MethodGet, "/", nil), fiber.TestConfig{Timeout: 0})
		firstDone <- response
	}()
	<-entered

	second, err := server.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	var body apiresponse.ErrorResponse
	if err := json.NewDecoder(second.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	_ = second.Body.Close()
	if second.StatusCode != fiber.StatusServiceUnavailable || second.Header.Get(fiber.HeaderRetryAfter) != "1" || body.Error.Code != "server_busy" || !body.Error.Retryable {
		t.Fatalf("second response = %d/%q/%#v", second.StatusCode, second.Header.Get(fiber.HeaderRetryAfter), body)
	}

	close(release)
	first := <-firstDone
	if first == nil || first.StatusCode != fiber.StatusNoContent {
		t.Fatalf("first response = %#v", first)
	}
	_ = first.Body.Close()

	third, err := server.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = third.Body.Close()
	if third.StatusCode != fiber.StatusNoContent {
		t.Fatalf("capacity was not released, status = %d", third.StatusCode)
	}
}
