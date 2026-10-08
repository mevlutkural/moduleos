package middleware

import (
	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
)

func RequestID() fiber.Handler {
	return func(c fiber.Ctx) error {
		apiresponse.BeginRequest(c)
		apiresponse.EnsureRequestID(c)
		return c.Next()
	}
}
