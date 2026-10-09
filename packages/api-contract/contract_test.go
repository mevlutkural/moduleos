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
		"POST /apps":                              {"201", "400", "401", "404", "409", "413", "415", "422", "500", "503"},
		"GET /apps":                               {"200", "401", "500", "503"},
		"GET /apps/{name}":                        {"200", "400", "401", "404", "500", "503"},
		"PATCH /apps/{name}":                      {"202", "400", "401", "404", "409", "412", "413", "415", "422", "428", "500", "503"},
		"DELETE /apps/{name}":                     {"202", "400", "401", "404", "409", "412", "428", "500", "503"},
		"POST /apps/{name}/start":                 {"202", "400", "401", "404", "409", "412", "428", "500", "503"},
		"POST /apps/{name}/stop":                  {"202", "400", "401", "404", "409", "412", "428", "500", "503"},
		"POST /apps/{name}/scale":                 {"202", "400", "401", "404", "409", "412", "413", "415", "422", "428", "500", "503"},
		"GET /live":                               {"200", "500"},
		"GET /ready":                              {"200", "500", "503"},
		"GET /version":                            {"200", "500"},
		"POST /projects":                          {"201", "400", "401", "409", "413", "415", "422", "500", "503"},
		"GET /projects":                           {"200", "401", "500", "503"},
		"GET /projects/{slug}":                    {"200", "400", "401", "404", "500", "503"},
		"DELETE /projects/{slug}":                 {"202", "400", "401", "404", "409", "500", "503"},
		"GET /projects/{slug}/apps":               {"200", "400", "401", "404", "500", "503"},
		"POST /projects/{slug}/links":             {"201", "400", "401", "404", "409", "413", "415", "422", "500", "503"},
		"GET /projects/{slug}/links":              {"200", "400", "401", "404", "500", "503"},
		"DELETE /projects/{slug}/links/{link_id}": {"202", "400", "401", "404", "500", "503"},
	}
	systemOperations := map[string]bool{"GET /live": true, "GET /ready": true, "GET /version": true}
	bodyOperations := map[string]bool{"POST /apps": true, "PATCH /apps/{name}": true, "POST /apps/{name}/scale": true, "POST /projects": true, "POST /projects/{slug}/links": true}
	createOperations := map[string]bool{"POST /apps": true, "POST /projects": true, "POST /projects/{slug}/links": true}
	var operations []string
	operationIDs := make(map[string]struct{})
	for _, path := range document.Paths.InMatchingOrder() {
		item := document.Paths.Find(path)
		requestIDFound := false
		for _, parameter := range item.Parameters {
			if parameter.Value != nil && parameter.Value.Name == "X-Request-ID" {
				requestIDFound = true
				if parameter.Value.In != openapi3.ParameterInHeader || parameter.Value.Required {
					t.Fatalf("%s request ID parameter = %#v", path, parameter.Value)
				}
			}
		}
		if !requestIDFound {
			t.Fatalf("%s does not declare the request ID parameter", path)
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
			if systemOperations[operationKey] {
				if operation.Security == nil || len(*operation.Security) != 0 {
					t.Fatalf("system operation %s must explicitly disable authentication", operationKey)
				}
			} else if operation.Security != nil {
				t.Fatalf("protected operation %s must inherit global bearer authentication", operationKey)
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
				if status == "401" && response.Value.Headers["WWW-Authenticate"] == nil {
					t.Fatalf("%s response 401 is missing WWW-Authenticate", operationKey)
				}
			}
			if bodyOperations[operationKey] {
				if operation.RequestBody == nil || operation.RequestBody.Value == nil || operation.RequestBody.Value.Content.Get("application/json") == nil {
					t.Fatalf("%s is missing its JSON request body", operationKey)
				}
			}
			if createOperations[operationKey] {
				if operation.Responses.Value("201").Value.Headers["Location"] == nil {
					t.Fatalf("%s response 201 is missing Location", operationKey)
				}
			}
			if operationKey == "GET /ready" && operation.Responses.Value("503").Value.Headers["Retry-After"] == nil {
				t.Fatal("GET /ready response 503 is missing optional Retry-After")
			}
		}
	}
	sort.Strings(operations)
	want := []string{
		"DELETE /apps/{name}",
		"DELETE /projects/{slug}",
		"DELETE /projects/{slug}/links/{link_id}",
		"GET /apps",
		"GET /apps/{name}",
		"GET /live",
		"GET /projects",
		"GET /projects/{slug}",
		"GET /projects/{slug}/apps",
		"GET /projects/{slug}/links",
		"GET /ready",
		"GET /version",
		"PATCH /apps/{name}",
		"POST /apps",
		"POST /apps/{name}/scale",
		"POST /apps/{name}/start",
		"POST /apps/{name}/stop",
		"POST /projects",
		"POST /projects/{slug}/links",
	}
	if strings.Join(operations, "\n") != strings.Join(want, "\n") {
		t.Fatalf("operations:\n%s\nwant:\n%s", strings.Join(operations, "\n"), strings.Join(want, "\n"))
	}
}

func TestProjectSchemasKeepPersistenceAndSecretFieldsPrivate(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		schema    string
		forbidden []string
	}{
		{schema: "Project", forbidden: []string{"network", "finalizer_state", "reconcile_error_message"}},
		{schema: "ProjectApplicationSummary", forbidden: []string{"project_id", "image", "observed_image", "env_vars", "ports", "volumes", "domain", "reconcile_error_message", "finalizer_state"}},
		{schema: "ProjectLink", forbidden: []string{"source_project_id", "target_app_id", "deletion_timestamp"}},
		{schema: "Application", forbidden: []string{"reconcile_error_message", "reconcile_attempt", "resume_replicas", "domain", "finalizer_state"}},
		{schema: "ApplicationVolume", forbidden: []string{"source"}},
	}
	for _, test := range tests {
		schema := document.Components.Schemas[test.schema]
		if schema == nil || schema.Value == nil {
			t.Fatalf("schema %s is unresolved", test.schema)
		}
		if schema.Value.AdditionalProperties.Has == nil || *schema.Value.AdditionalProperties.Has {
			t.Fatalf("schema %s must reject additional properties", test.schema)
		}
		for _, property := range test.forbidden {
			if _, exists := schema.Value.Properties[property]; exists {
				t.Errorf("schema %s exposes forbidden property %s", test.schema, property)
			}
		}
	}
}

func TestProjectRequestSchemasEnforceRuntimeBoundaries(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}

	createProject := document.Components.Schemas["CreateProjectRequest"]
	if createProject == nil || createProject.Value == nil {
		t.Fatal("CreateProjectRequest schema is unresolved")
	}
	if err := createProject.Value.VisitJSON(map[string]any{"name": strings.Repeat("界", 120), "slug": "unicode"}); err != nil {
		t.Fatalf("120-character project name rejected: %v", err)
	}
	if err := createProject.Value.VisitJSON(map[string]any{"name": " " + strings.Repeat("a", 120), "slug": "project"}); err == nil {
		t.Fatal("121-character raw project name was accepted")
	}

	linkID := document.Components.Parameters["ProjectLinkID"]
	if linkID == nil || linkID.Value == nil || linkID.Value.Schema == nil || linkID.Value.Schema.Value == nil {
		t.Fatal("ProjectLinkID parameter schema is unresolved")
	}
	if err := linkID.Value.Schema.Value.VisitJSON("30000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatalf("canonical project-link UUID rejected: %v", err)
	}
	if err := linkID.Value.Schema.Value.VisitJSON("AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"); err == nil {
		t.Fatal("uppercase non-canonical project-link UUID was accepted")
	}
}

func TestApplicationSchemasEnforceRuntimeBoundaries(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	create := document.Components.Schemas["CreateApplicationRequest"]
	update := document.Components.Schemas["UpdateApplicationRequest"]
	scale := document.Components.Schemas["ScaleApplicationRequest"]
	if create == nil || create.Value == nil || update == nil || update.Value == nil || scale == nil || scale.Value == nil {
		t.Fatal("application request schemas are unresolved")
	}
	if err := create.Value.VisitJSON(map[string]any{"name": "api", "image": "nginx:1.27"}); err != nil {
		t.Fatalf("minimal create rejected: %v", err)
	}
	if err := create.Value.VisitJSON(map[string]any{"name": "api", "image": "nginx:1.27", "replicas": 1000}); err != nil {
		t.Fatalf("global replica ceiling rejected: %v", err)
	}
	for name, value := range map[string]any{
		"zero replicas":    map[string]any{"name": "api", "image": "nginx:1.27", "replicas": 0},
		"replica overflow": map[string]any{"name": "api", "image": "nginx:1.27", "replicas": 1001},
		"uppercase name":   map[string]any{"name": "API", "image": "nginx:1.27"},
		"null environment": map[string]any{"name": "api", "image": "nginx:1.27", "env_vars": nil},
	} {
		if err := create.Value.VisitJSON(value); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := update.Value.VisitJSON(map[string]any{"env_vars": map[string]any{}}); err != nil {
		t.Fatalf("empty replacement rejected: %v", err)
	}
	for name, value := range map[string]any{
		"empty patch": map[string]any{},
		"image patch": map[string]any{"image": "nginx:2"},
		"null patch":  map[string]any{"env_vars": nil},
	} {
		if err := update.Value.VisitJSON(value); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := scale.Value.VisitJSON(map[string]any{"replicas": 0}); err != nil {
		t.Fatalf("scale-to-zero rejected: %v", err)
	}
	if err := scale.Value.VisitJSON(map[string]any{"replicas": 1000}); err != nil {
		t.Fatalf("global scale ceiling rejected: %v", err)
	}
	if err := scale.Value.VisitJSON(map[string]any{"replicas": -1}); err == nil {
		t.Fatal("negative scale was accepted")
	}
	if err := scale.Value.VisitJSON(map[string]any{"replicas": 1001}); err == nil {
		t.Fatal("scale above global ceiling was accepted")
	}

	ifMatch := document.Components.Parameters["IfMatch"]
	if ifMatch == nil || ifMatch.Value == nil || ifMatch.Value.Schema == nil || ifMatch.Value.Schema.Value == nil {
		t.Fatal("IfMatch parameter is unresolved")
	}
	if err := ifMatch.Value.Schema.Value.VisitJSON(`"12"`); err != nil {
		t.Fatalf("canonical If-Match rejected: %v", err)
	}
	for _, value := range []string{`12`, `W/"12"`, `"01"`, `*`, `"1", "2"`} {
		if err := ifMatch.Value.Schema.Value.VisitJSON(value); err == nil {
			t.Errorf("invalid If-Match %q was accepted", value)
		}
	}
}
