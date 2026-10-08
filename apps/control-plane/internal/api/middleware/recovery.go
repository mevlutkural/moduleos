package middleware

import (
	"errors"
	"log/slog"
	"runtime/debug"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
)

var ErrRecoveredPanic = errors.New("recovered panic")

func Recovery(log *slog.Logger) fiber.Handler {
	return func(c fiber.Ctx) (err error) {
		defer func() {
			if recover() != nil {
				log.Error("HTTP panic recovered",
					"request_id", apiresponse.RequestID(c),
					"method", c.Method(),
					"route", apiresponse.RouteTemplate(c),
					"stack", string(debug.Stack()),
				)
				err = ErrRecoveredPanic
			}
		}()
		return c.Next()
	}
}
