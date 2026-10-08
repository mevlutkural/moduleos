package middleware

import (
	"errors"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
)

var ErrInvalidConcurrencyLimit = errors.New("request concurrency limit must be positive")

func Concurrency(limit int) (fiber.Handler, error) {
	if limit < 1 {
		return nil, ErrInvalidConcurrencyLimit
	}
	semaphore := make(chan struct{}, limit)
	return func(c fiber.Ctx) error {
		select {
		case semaphore <- struct{}{}:
			defer func() { <-semaphore }()
			return c.Next()
		default:
			c.Set(fiber.HeaderRetryAfter, "1")
			return apiresponse.WriteError(c, fiber.StatusServiceUnavailable, "server_busy", "server is at its request concurrency limit", apiresponse.ErrorOptions{Retryable: true})
		}
	}, nil
}
