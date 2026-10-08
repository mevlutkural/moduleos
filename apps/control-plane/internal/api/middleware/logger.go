package middleware

import (
	"log/slog"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
)

func RequestLogger(log *slog.Logger) fiber.Handler {
	return func(c fiber.Ctx) error {
		apiresponse.BeginRequest(c)
		err := c.Next()
		route := apiresponse.RouteTemplate(c)
		status := c.Response().StatusCode()
		if err != nil {
			status = apiresponse.StatusForError(err)
		}
		if route == apiresponse.UnmatchedRoute && (err != nil || status < fiber.StatusBadRequest) {
			return err
		}
		log.Info("HTTP request",
			"request_id", apiresponse.RequestID(c),
			"method", c.Method(),
			"route", route,
			"status", status,
			"duration", apiresponse.RequestDuration(c),
		)
		return err
	}
}
