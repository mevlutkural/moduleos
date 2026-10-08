package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
)

var (
	ErrEmptyAPIKey   = errors.New("bearer authentication requires a non-empty API key")
	ErrInvalidAPIKey = errors.New("bearer authentication API key cannot contain whitespace")
)

func BearerAuth(apiKey string) (fiber.Handler, error) {
	if apiKey == "" {
		return nil, ErrEmptyAPIKey
	}
	if strings.ContainsAny(apiKey, " \t\r\n") {
		return nil, ErrInvalidAPIKey
	}
	expectedHash := sha256.Sum256([]byte(apiKey))

	return func(c fiber.Ctx) error {
		authorizationValues := c.Request().Header.PeekAll(fiber.HeaderAuthorization)
		if len(authorizationValues) != 1 {
			return unauthorized(c)
		}
		scheme, credential, ok := strings.Cut(string(authorizationValues[0]), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || credential == "" || strings.ContainsAny(credential, " \t\r\n") {
			return unauthorized(c)
		}

		providedHash := sha256.Sum256([]byte(credential))
		if subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) != 1 {
			return unauthorized(c)
		}
		return c.Next()
	}, nil
}

func unauthorized(c fiber.Ctx) error {
	return apiresponse.WriteError(c, fiber.StatusUnauthorized, "unauthorized", "authentication is required", apiresponse.ErrorOptions{})
}
