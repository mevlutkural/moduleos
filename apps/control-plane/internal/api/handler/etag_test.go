package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

const testApplicationID = "20000000-0000-4000-8000-000000000001"

func TestExpectedApplicationETagAcceptsOnlyCanonicalStrongETag(t *testing.T) {
	tests := []struct {
		value string
		want  int64
		ok    bool
	}{
		{value: `"app:20000000-0000-4000-8000-000000000001:1"`, want: 1, ok: true},
		{value: `"app:20000000-0000-4000-8000-000000000001:9223372036854775807"`, want: 9223372036854775807, ok: true},
		{value: "", ok: false},
		{value: `"app:20000000-0000-4000-8000-000000000001:0"`, ok: false},
		{value: `"app:20000000-0000-4000-8000-000000000001:01"`, ok: false},
		{value: `W/"app:20000000-0000-4000-8000-000000000001:1"`, ok: false},
		{value: `*`, ok: false},
		{value: `"app:20000000-0000-4000-8000-000000000001:1", "app:20000000-0000-4000-8000-000000000001:2"`, ok: false},
		{value: `"app:20000000-0000-4000-8000-000000000001:+1"`, ok: false},
		{value: `"app:20000000-0000-4000-8000-000000000001:-1"`, ok: false},
		{value: `"app:20000000-0000-4000-8000-000000000001:9223372036854775808"`, ok: false},
		{value: `"app:20000000-0000-4000-8000-000000000001:1:extra"`, ok: false},
		{value: `"app:20000000000040008000000000000001:1"`, ok: false},
		{value: `"1"`, ok: false},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			server := fiber.New()
			server.Get("/", func(c fiber.Ctx) error {
				id, generation, err := expectedApplicationETag(c)
				if (err == nil) != test.ok || (test.ok && (id != testApplicationID || generation != test.want)) {
					t.Errorf("id = %s, generation = %d, err = %v", id, generation, err)
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

func TestExpectedApplicationETagRejectsRepeatedHeader(t *testing.T) {
	server := fiber.New()
	server.Get("/", func(c fiber.Ctx) error {
		if _, _, err := expectedApplicationETag(c); err == nil {
			t.Error("repeated header accepted")
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Add(fiber.HeaderIfMatch, generationETag(testApplicationID, 1))
	request.Header.Add(fiber.HeaderIfMatch, generationETag(testApplicationID, 2))
	if _, err := server.Test(request); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationETagBindsApplicationIncarnation(t *testing.T) {
	first := generationETag(testApplicationID, 42)
	second := generationETag("20000000-0000-4000-8000-000000000002", 42)
	if first != `"app:20000000-0000-4000-8000-000000000001:42"` || first == second {
		t.Fatalf("ETags do not bind application identity: %q and %q", first, second)
	}
}

func FuzzExpectedApplicationETag(f *testing.F) {
	for _, value := range []string{generationETag(testApplicationID, 1), `"1"`, `W/"app:20000000-0000-4000-8000-000000000001:1"`, `*`, `"app:20000000-0000-4000-8000-000000000001:01"`} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		server := fiber.New()
		server.Get("/", func(c fiber.Ctx) error {
			id, generation, err := expectedApplicationETag(c)
			if err == nil && (generation < 1 || generationETag(id, generation) != value) {
				t.Fatalf("accepted non-canonical value %q as %s/%d", value, id, generation)
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
