package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"unicode"
)

const (
	minimumAPIKeyBytes     = 32
	maximumAPIKeyFileBytes = 4096
)

type apiKeyFileReader func(string) (string, error)

type apiKeyFileHandle interface {
	io.Reader
	Stat() (os.FileInfo, error)
	Close() error
}

type apiKeyFileInspector func(string) (os.FileInfo, error)
type apiKeyFileOpener func(string) (apiKeyFileHandle, error)

func validateAPIKeySourceConflict(environment map[string]environmentValue) error {
	if environment["API_KEY"].set && environment["API_KEY_FILE"].set {
		return errors.New("API_KEY and API_KEY_FILE cannot both be configured")
	}
	return nil
}

func resolveAPIKey(cfg *Config, environment map[string]environmentValue, readFile apiKeyFileReader) error {
	if cfg == nil || readFile == nil {
		return loadFailure(ReasonAPIKeySourceInvalid, "", errors.New("API key configuration cannot be resolved"))
	}

	direct := environment["API_KEY"]
	file := environment["API_KEY_FILE"]
	if cfg.Env == "production" {
		if direct.set {
			return loadFailure(ReasonAPIKeySourceInvalid, "API_KEY", errors.New("API_KEY is not supported in production; configure API_KEY_FILE"))
		}
		if !file.set {
			return loadFailure(ReasonAPIKeySourceInvalid, "API_KEY_FILE", errors.New("API_KEY_FILE must be explicitly configured in production"))
		}
	}
	if !file.set {
		return nil
	}

	value, err := readFile(file.value)
	if err != nil {
		return loadFailure(ReasonAPIKeyFileInvalid, "API_KEY_FILE", err)
	}
	cfg.APIKey = value
	return nil
}

func readAPIKeyFile(path string) (string, error) {
	return readAPIKeyFileWith(path, os.Lstat, openAPIKeyFile)
}

func readAPIKeyFileWith(path string, inspect apiKeyFileInspector, open apiKeyFileOpener) (value string, resultErr error) {
	if inspect == nil || open == nil {
		return "", errors.New("API_KEY_FILE cannot be read")
	}

	before, err := inspect(path)
	if err != nil {
		return "", errors.New("API_KEY_FILE cannot be inspected")
	}
	if !before.Mode().IsRegular() {
		return "", errors.New("API_KEY_FILE must reference a regular file")
	}
	if before.Size() > maximumAPIKeyFileBytes {
		return "", errors.New("API_KEY_FILE exceeds the maximum size")
	}

	file, err := open(path)
	if err != nil {
		return "", errors.New("API_KEY_FILE cannot be opened")
	}
	defer func() {
		if err := file.Close(); err != nil && resultErr == nil {
			value = ""
			resultErr = errors.New("API_KEY_FILE cannot be closed")
		}
	}()

	after, err := file.Stat()
	if err != nil {
		return "", errors.New("API_KEY_FILE cannot be inspected after opening")
	}
	if !after.Mode().IsRegular() {
		return "", errors.New("API_KEY_FILE must reference a regular file")
	}
	if !os.SameFile(before, after) {
		return "", errors.New("API_KEY_FILE changed while opening")
	}
	if after.Size() > maximumAPIKeyFileBytes {
		return "", errors.New("API_KEY_FILE exceeds the maximum size")
	}

	content, err := io.ReadAll(io.LimitReader(file, maximumAPIKeyFileBytes+1))
	if err != nil {
		return "", errors.New("API_KEY_FILE cannot be read")
	}
	if len(content) > maximumAPIKeyFileBytes {
		return "", errors.New("API_KEY_FILE exceeds the maximum size")
	}
	return normalizeAPIKeyFileContent(content)
}

func normalizeAPIKeyFileContent(content []byte) (string, error) {
	switch {
	case bytes.HasSuffix(content, []byte("\r\n")):
		content = content[:len(content)-2]
	case bytes.HasSuffix(content, []byte("\n")):
		content = content[:len(content)-1]
	}
	if len(content) == 0 {
		return "", errors.New("API_KEY_FILE content cannot be empty")
	}
	if len(content) < minimumAPIKeyBytes {
		return "", errors.New("API_KEY_FILE content must contain at least 32 bytes")
	}
	value := string(content)
	if err := validateAPIKeyContent(value); err != nil {
		return "", errors.New("API_KEY_FILE content cannot contain whitespace or control characters")
	}
	return value, nil
}

func validateAPIKeyContent(value string) error {
	if value != "" && strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) >= 0 {
		return errors.New("cannot contain whitespace or control characters")
	}
	return nil
}
