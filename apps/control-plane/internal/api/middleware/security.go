package middleware

import (
	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
)

func SecurityHeaders() fiber.Handler {
	return func(c fiber.Ctx) error {
		apiresponse.SetSecurityHeaders(c)
		return c.Next()
	}
}
