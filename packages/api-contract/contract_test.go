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
	volume := document.Components.Schemas["ApplicationVolumeInput"]
	image := document.Components.Schemas["ContainerImageReference"]
	persistedImage := document.Components.Schemas["PersistedContainerImageReference"]
	if create == nil || create.Value == nil || update == nil || update.Value == nil || scale == nil || scale.Value == nil || volume == nil || volume.Value == nil || image == nil || image.Value == nil || persistedImage == nil || persistedImage.Value == nil {
		t.Fatal("application request schemas are unresolved")
	}
	for name, schema := range map[string]*openapi3.SchemaRef{"create": create, "update": update} {
		environment := schema.Value.Properties["env_vars"]
		if environment == nil || environment.Value == nil || environment.Value.PropertyNames == nil || environment.Value.PropertyNames.Value == nil {
			t.Fatalf("%s environment property-name schema is unresolved", name)
		}
		if err := environment.Value.VisitJSON(map[string]any{"MODE": "production", "_TOKEN": "value"}, openapi3.EnableJSONSchema2020()); err != nil {
			t.Errorf("%s environment property-name schema rejected valid keys: %v", name, err)
		}
		for _, invalid := range []string{"", "1MODE", "BAD-KEY"} {
			if err := environment.Value.VisitJSON(map[string]any{invalid: "value"}, openapi3.EnableJSONSchema2020()); err == nil {
				t.Errorf("%s environment property-name schema accepted invalid key %q", name, invalid)
			}
		}
	}
	validate := func(schema *openapi3.SchemaRef, value any) error {
		return schema.Value.VisitJSON(value, openapi3.EnableJSONSchema2020())
	}
	longNamePrefix := "registry.example.com/"
	longName := longNamePrefix + strings.Repeat("g", 255-len(longNamePrefix))
	longObserved := longName + "@sha256:" + strings.Repeat("a", 64)
	for _, value := range []string{"nginx", "ghcr.io/mevlutkural/moduleos:v0.1.0", "registry.example.com:5000/team/api@sha256:" + strings.Repeat("a", 64), longName} {
		if err := validate(image, value); err != nil {
			t.Errorf("valid image reference %q rejected: %v", value, err)
		}
	}
	if err := validate(image, longObserved); err == nil {
		t.Error("runtime-expanded image reference accepted as desired input")
	}
	if err := validate(persistedImage, longObserved); err != nil {
		t.Errorf("runtime-expanded persisted image reference rejected: %v", err)
	}
	if err := validate(persistedImage, longName+":x@sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Error("observed image beyond generated-reference ceiling accepted")
	}
	for _, value := range []string{"bad image", "example.com/UPPERCASE", "nginx:", "nginx@sha256:short", "nginx@sha256:" + strings.Repeat("a", 32), "nginx@sha512:" + strings.Repeat("a", 128), strings.Repeat("a", 64), strings.Repeat("repo", 64)} {
		if err := validate(image, value); err == nil {
			t.Errorf("invalid image reference %q accepted", value)
		}
	}
	for name, value := range map[string]any{
		"root source":           map[string]any{"source": "/", "target": "/data"},
		"dot source":            map[string]any{"source": "/srv/./data", "target": "/data"},
		"parent source":         map[string]any{"source": "/srv/data/../data", "target": "/data"},
		"duplicate separator":   map[string]any{"source": "/srv//data", "target": "/data"},
		"trailing target slash": map[string]any{"source": "/srv/data", "target": "/data/"},
	} {
		if err := validate(volume, value); err == nil {
			t.Errorf("%s mount was accepted", name)
		}
	}
	if err := validate(volume, map[string]any{"source": "/srv/.state/...", "target": "/data/.cache"}); err != nil {
		t.Fatalf("canonical hidden mount path rejected: %v", err)
	}
	if err := validate(create, map[string]any{"name": "api", "image": "nginx:1.27"}); err != nil {
		t.Fatalf("minimal create rejected: %v", err)
	}
	if err := validate(create, map[string]any{"name": "api", "image": "nginx:1.27", "replicas": 1000}); err != nil {
		t.Fatalf("global replica ceiling rejected: %v", err)
	}
	if err := validate(create, map[string]any{"name": "api", "image": "nginx:1.27", "expose": true, "ingress_container_port": 1}); err != nil {
		t.Fatalf("valid exposed application rejected: %v", err)
	}
	for name, value := range map[string]any{
		"zero replicas":            map[string]any{"name": "api", "image": "nginx:1.27", "replicas": 0},
		"replica overflow":         map[string]any{"name": "api", "image": "nginx:1.27", "replicas": 1001},
		"uppercase name":           map[string]any{"name": "API", "image": "nginx:1.27"},
		"invalid image":            map[string]any{"name": "api", "image": "bad image"},
		"null project":             map[string]any{"name": "api", "image": "nginx:1.27", "project_slug": nil},
		"null environment":         map[string]any{"name": "api", "image": "nginx:1.27", "env_vars": nil},
		"null environment value":   map[string]any{"name": "api", "image": "nginx:1.27", "env_vars": map[string]any{"TOKEN": nil}},
		"environment NUL":          map[string]any{"name": "api", "image": "nginx:1.27", "env_vars": map[string]any{"TOKEN": "a\x00b"}},
		"expose without ingress":   map[string]any{"name": "api", "image": "nginx:1.27", "expose": true},
		"expose with zero ingress": map[string]any{"name": "api", "image": "nginx:1.27", "expose": true, "ingress_container_port": 0},
		"root mount target": map[string]any{"name": "api", "image": "nginx:1.27", "volumes": []any{
			map[string]any{"source": "/srv/data", "target": "/"},
		}},
		"mount NUL": map[string]any{"name": "api", "image": "nginx:1.27", "volumes": []any{
			map[string]any{"source": "/srv/data", "target": "/data\x00suffix"},
		}},
	} {
		if err := validate(create, value); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := validate(update, map[string]any{"env_vars": map[string]any{}}); err != nil {
		t.Fatalf("empty replacement rejected: %v", err)
	}
	if err := validate(update, map[string]any{"expose": true, "ingress_container_port": 1}); err != nil {
		t.Fatalf("valid exposed update rejected: %v", err)
	}
	for name, value := range map[string]any{
		"empty patch":              map[string]any{},
		"image patch":              map[string]any{"image": "nginx:2"},
		"null patch":               map[string]any{"env_vars": nil},
		"exposed update zero port": map[string]any{"expose": true, "ingress_container_port": 0},
	} {
		if err := validate(update, value); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := validate(scale, map[string]any{"replicas": 0}); err != nil {
		t.Fatalf("scale-to-zero rejected: %v", err)
	}
	if err := validate(scale, map[string]any{"replicas": 1000}); err != nil {
		t.Fatalf("global scale ceiling rejected: %v", err)
	}
	if err := validate(scale, map[string]any{"replicas": -1}); err == nil {
		t.Fatal("negative scale was accepted")
	}
	if err := validate(scale, map[string]any{"replicas": 1001}); err == nil {
		t.Fatal("scale above global ceiling was accepted")
	}

	ifMatch := document.Components.Parameters["IfMatch"]
	if ifMatch == nil || ifMatch.Value == nil || ifMatch.Value.Schema == nil || ifMatch.Value.Schema.Value == nil {
		t.Fatal("IfMatch parameter is unresolved")
	}
	for _, value := range []string{`"1"`, `"999999999999999999"`, `"1000000000000000000"`, `"9000000000000000000"`, `"9223372036854775799"`, `"9223372036854775807"`} {
		if err := ifMatch.Value.Schema.Value.VisitJSON(value); err != nil {
			t.Fatalf("canonical If-Match %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{`12`, `W/"12"`, `"01"`, `*`, `"1", "2"`, `"9223372036854775808"`, `"9999999999999999999"`, `"10000000000000000000"`} {
		if err := ifMatch.Value.Schema.Value.VisitJSON(value); err == nil {
			t.Errorf("invalid If-Match %q was accepted", value)
		}
	}
}
