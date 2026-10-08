package apicontract_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestOpenAPIContractIsValidAndScopedToMergedRoutes(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q, want 3.1.0", document.OpenAPI)
	}
	if document.Info == nil || document.Info.Version != "0.1.0" {
		t.Fatalf("API version = %#v, want 0.1.0", document.Info)
	}
	if err := document.Validate(context.Background(), openapi3.EnableExamplesValidation(), openapi3.EnableSchemaFormatValidation()); err != nil {
		t.Fatalf("validate contract: %v", err)
	}

	wantResponses := map[string][]string{
		"GET /live":    {"200", "500"},
		"GET /ready":   {"200", "500", "503"},
		"GET /version": {"200", "500"},
	}
	var operations []string
	operationIDs := make(map[string]struct{})
	for _, path := range document.Paths.InMatchingOrder() {
		item := document.Paths.Find(path)
		if len(item.Parameters) != 1 || item.Parameters[0].Value == nil {
			t.Fatalf("%s must declare one resolved request parameter", path)
		}
		requestID := item.Parameters[0].Value
		if requestID.Name != "X-Request-ID" || requestID.In != openapi3.ParameterInHeader || requestID.Required {
			t.Fatalf("%s request ID parameter = %#v", path, requestID)
		}
		for method, operation := range item.Operations() {
			operationKey := strings.ToUpper(method) + " " + path
			operations = append(operations, operationKey)
			if operation.OperationID == "" {
				t.Fatalf("%s %s has no operationId", method, path)
			}
			if _, exists := operationIDs[operation.OperationID]; exists {
				t.Fatalf("duplicate operationId %q", operation.OperationID)
			}
			operationIDs[operation.OperationID] = struct{}{}
			if operation.Security == nil || len(*operation.Security) != 0 {
				t.Fatalf("system operation %s %s must explicitly disable authentication", method, path)
			}
			statuses := operation.Responses.Keys()
			sort.Strings(statuses)
			if strings.Join(statuses, ",") != strings.Join(wantResponses[operationKey], ",") {
				t.Fatalf("%s responses = %v, want %v", operationKey, statuses, wantResponses[operationKey])
			}
			for _, status := range statuses {
				response := operation.Responses.Value(status)
				if response == nil || response.Value == nil {
					t.Fatalf("%s response %s is unresolved", operationKey, status)
				}
				for _, header := range []string{"X-Request-ID", "Cache-Control"} {
					if response.Value.Headers[header] == nil {
						t.Fatalf("%s response %s is missing %s", operationKey, status, header)
					}
				}
				if response.Value.Content.Get("application/json") == nil {
					t.Fatalf("%s response %s is missing application/json content", operationKey, status)
				}
			}
			if operationKey == "GET /ready" && operation.Responses.Value("503").Value.Headers["Retry-After"] == nil {
				t.Fatal("GET /ready response 503 is missing optional Retry-After")
			}
		}
	}
	sort.Strings(operations)
	want := []string{"GET /live", "GET /ready", "GET /version"}
	if strings.Join(operations, "\n") != strings.Join(want, "\n") {
		t.Fatalf("operations:\n%s\nwant:\n%s", strings.Join(operations, "\n"), strings.Join(want, "\n"))
	}
}
