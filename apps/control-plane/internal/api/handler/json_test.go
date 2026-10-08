package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

type strictJSONFixture struct {
	Name   string `json:"name"`
	Nested struct {
		Value string `json:"value"`
	} `json:"nested,omitempty"`
}

func strictJSONTestApp() *fiber.App {
	server := fiber.New()
	server.Post("/", func(c fiber.Ctx) error {
		var body strictJSONFixture
		if err := decodeStrictJSON(c, &body); err != nil {
			switch {
			case errors.Is(err, ErrUnsupportedMedia):
				return c.SendStatus(fiber.StatusUnsupportedMediaType)
			default:
				return c.SendStatus(fiber.StatusBadRequest)
			}
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	return server
}

func TestDecodeStrictJSONAcceptsOneObjectAndUTF8Charset(t *testing.T) {
	for _, contentType := range []string{"application/json", "Application/JSON", "application/json; charset=utf-8", "application/json; charset=UTF-8"} {
		t.Run(contentType, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"project","nested":{"value":"ok"}}`))
			request.Header.Set(fiber.HeaderContentType, contentType)
			response, err := strictJSONTestApp().Test(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != fiber.StatusNoContent {
				t.Fatalf("status = %d", response.StatusCode)
			}
		})
	}
}

func TestDecodeStrictJSONRejectsMalformedAmbiguousAndUnsupportedBodies(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType []string
		wantStatus  int
	}{
		{name: "missing content type", body: `{}`, wantStatus: 415},
		{name: "wrong content type", body: `{}`, contentType: []string{"text/plain"}, wantStatus: 415},
		{name: "malformed content type", body: `{}`, contentType: []string{`application/json; charset="`}, wantStatus: 415},
		{name: "unsupported charset", body: `{}`, contentType: []string{"application/json; charset=iso-8859-1"}, wantStatus: 415},
		{name: "unsupported parameter", body: `{}`, contentType: []string{"application/json; profile=test"}, wantStatus: 415},
		{name: "empty", body: "", contentType: []string{"application/json"}, wantStatus: 400},
		{name: "whitespace", body: " \n\t", contentType: []string{"application/json"}, wantStatus: 400},
		{name: "null", body: `null`, contentType: []string{"application/json"}, wantStatus: 400},
		{name: "array", body: `[]`, contentType: []string{"application/json"}, wantStatus: 400},
		{name: "scalar", body: `true`, contentType: []string{"application/json"}, wantStatus: 400},
		{name: "malformed", body: `{"name":`, contentType: []string{"application/json"}, wantStatus: 400},
		{name: "invalid UTF-8", body: "{\"name\":\"\xff\"}", contentType: []string{"application/json"}, wantStatus: 400},
		{name: "unknown", body: `{"unknown":true}`, contentType: []string{"application/json"}, wantStatus: 400},
		{name: "duplicate top level", body: `{"name":"one","name":"two"}`, contentType: []string{"application/json"}, wantStatus: 400},
		{name: "duplicate nested", body: `{"name":"one","nested":{"value":"one","value":"two"}}`, contentType: []string{"application/json"}, wantStatus: 400},
		{name: "trailing value", body: `{"name":"one"} {"name":"two"}`, contentType: []string{"application/json"}, wantStatus: 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			for _, contentType := range test.contentType {
				request.Header.Add(fiber.HeaderContentType, contentType)
			}
			response, err := strictJSONTestApp().Test(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
		})
	}
}

func TestDecodeStrictJSONRejectsDuplicateContentType(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"project"}`))
	request.Header.Set(fiber.HeaderContentType, "application/json, application/json")
	response, err := strictJSONTestApp().Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != fiber.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", response.StatusCode)
	}
}

func FuzzValidateJSONKeysNeverPanics(f *testing.F) {
	for _, seed := range []string{`{}`, `{"a":1}`, `{"a":1,"a":2}`, `{"a":[{"b":true}]}`, `null`, `[]`, `{"a":`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_ = validateJSONKeys([]byte(input))
	})
}
