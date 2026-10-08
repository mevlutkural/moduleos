package apiresponse

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

const (
	RequestIDHeader     = "X-Request-ID"
	UnmatchedRoute      = "<unmatched>"
	requestIDLocal      = "request_id"
	requestStartedLocal = "request_started_at"
)

type ErrorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	RequestID string            `json:"request_id"`
	Retryable bool              `json:"retryable"`
	Fields    map[string]string `json:"fields,omitempty"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorOptions struct {
	Retryable bool
	Fields    map[string]string
}

type errorDefinition struct {
	code      string
	message   string
	retryable bool
}

func EnsureRequestID(c fiber.Ctx) string {
	if requestID, ok := c.Locals(requestIDLocal).(string); ok && requestID != "" {
		return requestID
	}

	requestIDValues := c.Request().Header.PeekAll(RequestIDHeader)
	requestID := ""
	if len(requestIDValues) == 1 {
		requestID = strings.TrimSpace(string(requestIDValues[0]))
	}
	parsed, err := uuid.Parse(requestID)
	if err != nil || len(requestID) != len(uuid.Nil.String()) {
		requestID = uuid.NewString()
	} else {
		requestID = parsed.String()
	}

	c.Locals(requestIDLocal, requestID)
	c.Set(RequestIDHeader, requestID)
	return requestID
}

func RequestID(c fiber.Ctx) string {
	return EnsureRequestID(c)
}

func BeginRequest(c fiber.Ctx) {
	if _, ok := c.Locals(requestStartedLocal).(time.Time); !ok {
		c.Locals(requestStartedLocal, time.Now())
	}
}

func RequestDuration(c fiber.Ctx) time.Duration {
	if started, ok := c.Locals(requestStartedLocal).(time.Time); ok {
		return time.Since(started)
	}
	return 0
}

func RouteTemplate(c fiber.Ctx) string {
	if !c.Matched() {
		return UnmatchedRoute
	}
	if route := c.Route(); route != nil && route.Path != "" {
		return route.Path
	}
	return UnmatchedRoute
}

func WriteError(c fiber.Ctx, status int, code, message string, options ErrorOptions) error {
	SetSecurityHeaders(c)
	if status == fiber.StatusUnauthorized {
		c.Set(fiber.HeaderWWWAuthenticate, "Bearer")
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Status(status).JSON(ErrorResponse{Error: ErrorBody{
		Code:      code,
		Message:   message,
		RequestID: EnsureRequestID(c),
		Retryable: options.Retryable,
		Fields:    options.Fields,
	}})
}

func SetSecurityHeaders(c fiber.Ctx) {
	c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
	c.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	c.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
	c.Set("Referrer-Policy", "no-referrer")
	c.Set("X-Frame-Options", "DENY")
}

func StatusForError(err error) int {
	var fiberError *fiber.Error
	if errors.As(err, &fiberError) {
		return fiberError.Code
	}
	return fiber.StatusInternalServerError
}

func NewErrorHandler(log *slog.Logger) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		status := StatusForError(err)
		definition := definitionForStatus(status)
		route := RouteTemplate(c)
		if status >= fiber.StatusInternalServerError || route == UnmatchedRoute {
			log.Error("HTTP request failed",
				"request_id", EnsureRequestID(c),
				"method", c.Method(),
				"route", route,
				"status", status,
				"duration", RequestDuration(c),
				"error_type", fmt.Sprintf("%T", err),
			)
		}
		return WriteError(c, status, definition.code, definition.message, ErrorOptions{Retryable: definition.retryable})
	}
}

func definitionForStatus(status int) errorDefinition {
	switch status {
	case fiber.StatusBadRequest:
		return errorDefinition{code: "invalid_request", message: "request is malformed"}
	case fiber.StatusUnauthorized:
		return errorDefinition{code: "unauthorized", message: "authentication is required"}
	case fiber.StatusForbidden:
		return errorDefinition{code: "forbidden", message: "request is not permitted"}
	case fiber.StatusNotFound:
		return errorDefinition{code: "not_found", message: "route not found"}
	case fiber.StatusMethodNotAllowed:
		return errorDefinition{code: "method_not_allowed", message: "method is not allowed for this route"}
	case fiber.StatusConflict:
		return errorDefinition{code: "conflict", message: "request conflicts with current resource state"}
	case fiber.StatusPreconditionFailed:
		return errorDefinition{code: "precondition_failed", message: "request precondition does not match current resource state"}
	case fiber.StatusRequestTimeout:
		return errorDefinition{code: "request_timeout", message: "request timed out", retryable: true}
	case fiber.StatusRequestEntityTooLarge:
		return errorDefinition{code: "payload_too_large", message: "request body exceeds the configured limit"}
	case fiber.StatusRequestURITooLong:
		return errorDefinition{code: "uri_too_long", message: "request URI exceeds the configured limit"}
	case fiber.StatusUnsupportedMediaType:
		return errorDefinition{code: "unsupported_media_type", message: "request media type is unsupported"}
	case fiber.StatusUnprocessableEntity:
		return errorDefinition{code: "validation_failed", message: "request validation failed"}
	case fiber.StatusPreconditionRequired:
		return errorDefinition{code: "precondition_required", message: "a request precondition is required"}
	case fiber.StatusTooManyRequests:
		return errorDefinition{code: "rate_limited", message: "request limit reached", retryable: true}
	case fiber.StatusRequestHeaderFieldsTooLarge:
		return errorDefinition{code: "headers_too_large", message: "request headers exceed the configured limit"}
	case fiber.StatusBadGateway:
		return errorDefinition{code: "bad_gateway", message: "an upstream dependency returned an invalid response", retryable: true}
	case fiber.StatusServiceUnavailable:
		return errorDefinition{code: "dependency_unavailable", message: "service is temporarily unavailable", retryable: true}
	default:
		if status >= 400 && status < 500 {
			return errorDefinition{code: "request_rejected", message: "request was rejected"}
		}
		return errorDefinition{code: "internal_error", message: "internal server error"}
	}
}
