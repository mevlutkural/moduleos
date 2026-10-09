package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestExpectedGenerationAcceptsOnlyCanonicalStrongETag(t *testing.T) {
	tests := []struct {
		value string
		want  int64
		ok    bool
	}{
		{value: `"1"`, want: 1, ok: true},
		{value: `"9223372036854775807"`, want: 9223372036854775807, ok: true},
		{value: "", ok: false},
		{value: `"0"`, ok: false},
		{value: `"01"`, ok: false},
		{value: `1`, ok: false},
		{value: `W/"1"`, ok: false},
		{value: `*`, ok: false},
		{value: `"1", "2"`, ok: false},
		{value: `"+1"`, ok: false},
		{value: `"-1"`, ok: false},
		{value: `"9223372036854775808"`, ok: false},
		{value: `"1\\"`, ok: false},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			server := fiber.New()
			server.Get("/", func(c fiber.Ctx) error {
				got, err := expectedGeneration(c)
				if (err == nil) != test.ok || got != test.want {
					t.Errorf("generation = %d, err = %v", got, err)
				}
				return c.SendStatus(fiber.StatusNoContent)
			})
			request := httptest.NewRequest("GET", "/", nil)
			if test.value != "" {
				request.Header.Set(fiber.HeaderIfMatch, test.value)
			}
			if _, err := server.Test(request); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExpectedGenerationRejectsRepeatedHeader(t *testing.T) {
	server := fiber.New()
	server.Get("/", func(c fiber.Ctx) error {
		if _, err := expectedGeneration(c); err == nil {
			t.Error("repeated header accepted")
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Add(fiber.HeaderIfMatch, `"1"`)
	request.Header.Add(fiber.HeaderIfMatch, `"2"`)
	if _, err := server.Test(request); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationETag(t *testing.T) {
	if got := generationETag(42); got != `"42"` {
		t.Fatalf("ETag = %q", got)
	}
}

func FuzzExpectedGeneration(f *testing.F) {
	for _, value := range []string{`"1"`, `"01"`, `W/"1"`, `*`, `"1", "2"`, `"9223372036854775808"`} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		server := fiber.New()
		server.Get("/", func(c fiber.Ctx) error {
			generation, err := expectedGeneration(c)
			if err == nil && (generation < 1 || generationETag(generation) != value) {
				t.Fatalf("accepted non-canonical value %q as %d", value, generation)
			}
			return c.SendStatus(fiber.StatusNoContent)
		})
		request := httptest.NewRequest("GET", "/", nil)
		request.Header.Set(fiber.HeaderIfMatch, value)
		if _, err := server.Test(request); err != nil {
			t.Fatal(err)
		}
	})
}
