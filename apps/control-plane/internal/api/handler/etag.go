package handler

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

var (
	errMissingIfMatch   = errors.New("If-Match header is required")
	errMalformedIfMatch = errors.New("If-Match header is malformed")
)

func expectedApplicationETag(c fiber.Ctx) (string, int64, error) {
	values := c.Request().Header.PeekAll(fiber.HeaderIfMatch)
	if len(values) == 0 {
		return "", 0, errMissingIfMatch
	}
	if len(values) != 1 {
		return "", 0, errMalformedIfMatch
	}
	value := string(values[0])
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || strings.ContainsAny(value, "\\,") {
		return "", 0, errMalformedIfMatch
	}
	parts := strings.Split(value[1:len(value)-1], ":")
	if len(parts) != 3 || parts[0] != "app" {
		return "", 0, errMalformedIfMatch
	}
	id, err := uuid.Parse(parts[1])
	if err != nil || id.String() != parts[1] {
		return "", 0, errMalformedIfMatch
	}
	digits := parts[2]
	if digits == "" || digits[0] == '0' {
		return "", 0, errMalformedIfMatch
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return "", 0, errMalformedIfMatch
		}
	}
	generation, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || generation < 1 {
		return "", 0, errMalformedIfMatch
	}
	return parts[1], generation, nil
}

func generationETag(id string, generation int64) string {
	return `"app:` + id + `:` + strconv.FormatInt(generation, 10) + `"`
}
