package handler

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
)

var (
	errMissingIfMatch   = errors.New("If-Match header is required")
	errMalformedIfMatch = errors.New("If-Match header is malformed")
)

func expectedGeneration(c fiber.Ctx) (int64, error) {
	values := c.Request().Header.PeekAll(fiber.HeaderIfMatch)
	if len(values) == 0 {
		return 0, errMissingIfMatch
	}
	if len(values) != 1 {
		return 0, errMalformedIfMatch
	}
	value := string(values[0])
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || strings.ContainsAny(value, "\\,") {
		return 0, errMalformedIfMatch
	}
	digits := value[1 : len(value)-1]
	if digits == "" || digits[0] == '0' {
		return 0, errMalformedIfMatch
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, errMalformedIfMatch
		}
	}
	generation, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || generation < 1 {
		return 0, errMalformedIfMatch
	}
	return generation, nil
}

func generationETag(generation int64) string {
	return `"` + strconv.FormatInt(generation, 10) + `"`
}
