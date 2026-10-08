package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
)

var (
	ErrInvalidJSON         = errors.New("invalid JSON body")
	ErrDuplicateJSONKey    = errors.New("duplicate JSON key")
	ErrUnsupportedJSONType = errors.New("JSON body must be an object")
	ErrUnsupportedMedia    = errors.New("content type must be application/json")
)

func decodeStrictJSON(c fiber.Ctx, destination any) error {
	contentTypes := c.Request().Header.PeekAll(fiber.HeaderContentType)
	if len(contentTypes) != 1 {
		return ErrUnsupportedMedia
	}
	mediaType, parameters, err := mime.ParseMediaType(string(contentTypes[0]))
	if err != nil || !strings.EqualFold(mediaType, fiber.MIMEApplicationJSON) {
		return ErrUnsupportedMedia
	}
	for name, value := range parameters {
		if !strings.EqualFold(name, "charset") || !strings.EqualFold(value, "utf-8") {
			return ErrUnsupportedMedia
		}
	}

	body := bytes.TrimSpace(c.Body())
	if len(body) == 0 {
		return fmt.Errorf("%w: body is empty", ErrInvalidJSON)
	}
	if !utf8.Valid(body) {
		return fmt.Errorf("%w: body is not valid UTF-8", ErrInvalidJSON)
	}
	if body[0] != '{' {
		return ErrUnsupportedJSONType
	}
	if err := validateJSONKeys(body); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func validateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidJSON, err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalidJSON
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("%w: %s", ErrDuplicateJSONKey, key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			if err == nil {
				err = ErrInvalidJSON
			}
			return fmt.Errorf("%w: %v", ErrInvalidJSON, err)
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			if err == nil {
				err = ErrInvalidJSON
			}
			return fmt.Errorf("%w: %v", ErrInvalidJSON, err)
		}
	default:
		return ErrInvalidJSON
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: body contains more than one JSON value", ErrInvalidJSON)
		}
		return fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	return nil
}
