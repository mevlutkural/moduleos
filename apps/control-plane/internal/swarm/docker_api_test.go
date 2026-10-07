package swarm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dockerswarm "github.com/moby/moby/api/types/swarm"
	dockerclient "github.com/moby/moby/client"
)

const testDockerAPIVersion = "1.52"

func newDockerAPITestClient(t *testing.T, handler http.HandlerFunc) *DockerClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := dockerclient.New(
		dockerclient.WithHost(server.URL),
		dockerclient.WithAPIVersion(testDockerAPIVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &DockerClient{docker: client}
}

func dockerAPIPath(request *http.Request) string {
	return strings.TrimPrefix(request.URL.Path, "/v"+testDockerAPIVersion)
}

func writeDockerJSON(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Errorf("encode Docker response: %v", err)
	}
}

func TestDockerClientHealth(t *testing.T) {
	tests := []struct {
		name       string
		pingStatus int
		infoStatus int
		state      string
		manager    bool
		wantError  string
	}{
		{name: "active manager", pingStatus: http.StatusOK, infoStatus: http.StatusOK, state: "active", manager: true},
		{name: "ping failure", pingStatus: http.StatusNotFound, infoStatus: http.StatusOK, state: "active", manager: true, wantError: "docker_unavailable"},
		{name: "info failure", pingStatus: http.StatusOK, infoStatus: http.StatusInternalServerError, state: "active", manager: true, wantError: "docker_info_unavailable"},
		{name: "inactive swarm", pingStatus: http.StatusOK, infoStatus: http.StatusOK, state: "inactive", manager: true, wantError: "swarm_inactive"},
		{name: "worker node", pingStatus: http.StatusOK, infoStatus: http.StatusOK, state: "active", manager: false, wantError: "swarm_manager_required"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newDockerAPITestClient(t, func(response http.ResponseWriter, request *http.Request) {
				switch dockerAPIPath(request) {
				case "/_ping":
					response.WriteHeader(test.pingStatus)
				case "/info":
					response.WriteHeader(test.infoStatus)
					if test.infoStatus == http.StatusOK {
						writeDockerJSON(t, response, map[string]any{"Swarm": map[string]any{
							"LocalNodeState":   test.state,
							"ControlAvailable": test.manager,
						}})
					}
				default:
					http.NotFound(response, request)
				}
			})

			err := client.Health(t.Context())
			if test.wantError == "" && err != nil {
				t.Fatalf("Health() error = %v", err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("Health() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestDockerClientEnsureNetwork(t *testing.T) {
	t.Run("creates missing project network with ownership", func(t *testing.T) {
		var createRequest struct {
			Name       string            `json:"Name"`
			Driver     string            `json:"Driver"`
			Attachable bool              `json:"Attachable"`
			Labels     map[string]string `json:"Labels"`
		}
		client := newDockerAPITestClient(t, func(response http.ResponseWriter, request *http.Request) {
			switch dockerAPIPath(request) {
			case "/networks":
				writeDockerJSON(t, response, []any{})
			case "/networks/create":
				if err := json.NewDecoder(request.Body).Decode(&createRequest); err != nil {
					t.Errorf("decode network create request: %v", err)
					return
				}
				writeDockerJSON(t, response, map[string]any{"Id": "network-id"})
			default:
				http.NotFound(response, request)
			}
		})
		if err := client.EnsureProjectNetwork(t.Context(), "moduleos-payments-net", "project-id", "payments"); err != nil {
			t.Fatal(err)
		}
		if createRequest.Name != "moduleos-payments-net" || createRequest.Driver != "overlay" || !createRequest.Attachable {
			t.Fatalf("unexpected create request: %#v", createRequest)
		}
		if createRequest.Labels[LabelProjectID] != "project-id" || createRequest.Labels[LabelProject] != "payments" {
			t.Fatalf("ownership labels missing: %#v", createRequest.Labels)
		}
	})

	tests := []struct {
		name       string
		driver     string
		attachable bool
		labels     map[string]string
		wantError  string
	}{
		{name: "existing compatible network", driver: "overlay", attachable: true, labels: map[string]string{LabelProjectID: "project-id"}},
		{name: "incompatible driver", driver: "bridge", attachable: true, labels: map[string]string{LabelProjectID: "project-id"}, wantError: ErrInvalidSpec.Error()},
		{name: "ownership conflict", driver: "overlay", attachable: true, labels: map[string]string{LabelProjectID: "other"}, wantError: ErrOwnershipConflict.Error()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newDockerAPITestClient(t, func(response http.ResponseWriter, request *http.Request) {
				writeDockerJSON(t, response, []map[string]any{{
					"Id": "network-id", "Name": "moduleos-project-net", "Driver": test.driver,
					"Attachable": test.attachable, "Labels": test.labels,
				}})
			})
			err := client.ensureNetwork(t.Context(), "moduleos-project-net", map[string]string{LabelProjectID: "project-id"})
			if test.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("ensureNetwork() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestDockerClientServiceLifecycle(t *testing.T) {
	currentSpec := buildSwarmSpec(canonicalServiceSpec())
	currentSpec.Labels["external.owner"] = "platform"
	currentSpec.Labels[LabelGeneration] = "1"
	var created, updated, removed bool
	var updatedSpec dockerswarm.ServiceSpec

	client := newDockerAPITestClient(t, func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && dockerAPIPath(request) == "/services/create":
			created = true
			writeDockerJSON(t, response, map[string]any{"ID": "service-id"})
		case request.Method == http.MethodGet && dockerAPIPath(request) == "/services/service-id":
			writeDockerJSON(t, response, dockerswarm.Service{
				ID:   "service-id",
				Meta: dockerswarm.Meta{Version: dockerswarm.Version{Index: 4}},
				Spec: currentSpec,
			})
		case request.Method == http.MethodPost && dockerAPIPath(request) == "/services/service-id/update":
			updated = true
			if err := json.NewDecoder(request.Body).Decode(&updatedSpec); err != nil {
				t.Errorf("decode service update request: %v", err)
				return
			}
			writeDockerJSON(t, response, map[string]any{"Warnings": []string{}})
		case request.Method == http.MethodDelete && dockerAPIPath(request) == "/services/service-id":
			removed = true
			response.WriteHeader(http.StatusOK)
		default:
			http.NotFound(response, request)
		}
	})

	if err := client.CreateService(t.Context(), canonicalServiceSpec()); err != nil {
		t.Fatal(err)
	}
	desired := canonicalServiceSpec()
	desired.Labels = map[string]string{LabelGeneration: "2"}
	if err := client.UpdateService(t.Context(), "service-id", desired); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveService(t.Context(), "service-id"); err != nil {
		t.Fatal(err)
	}
	if !created || !updated || !removed {
		t.Fatalf("lifecycle requests missing: create=%v update=%v remove=%v", created, updated, removed)
	}
	if updatedSpec.Labels["external.owner"] != "platform" || updatedSpec.Labels[LabelGeneration] != "2" {
		t.Fatalf("service update did not preserve external labels: %#v", updatedSpec.Labels)
	}
}

func TestDockerClientServiceObservation(t *testing.T) {
	spec := buildSwarmSpec(canonicalServiceSpec())
	oldTaskSpec := spec.TaskTemplate
	oldContainerSpec := *oldTaskSpec.ContainerSpec
	oldContainerSpec.Labels = map[string]string{LabelTaskTemplate: "previous-template"}
	oldTaskSpec.ContainerSpec = &oldContainerSpec
	client := newDockerAPITestClient(t, func(response http.ResponseWriter, request *http.Request) {
		switch dockerAPIPath(request) {
		case "/services/service-id":
			writeDockerJSON(t, response, dockerswarm.Service{ID: "service-id", Spec: spec})
		case "/networks/moduleos-root-net":
			writeDockerJSON(t, response, map[string]any{"Id": "network-id", "Name": "moduleos-root-net"})
		case "/tasks":
			writeDockerJSON(t, response, []dockerswarm.Task{
				{Spec: oldTaskSpec, Status: dockerswarm.TaskStatus{State: dockerswarm.TaskStateRunning}},
				{Spec: spec.TaskTemplate, Status: dockerswarm.TaskStatus{State: dockerswarm.TaskStateRunning}},
				{Spec: spec.TaskTemplate, Status: dockerswarm.TaskStatus{State: dockerswarm.TaskStateFailed, Err: "exit 1"}},
			})
		case "/services":
			writeDockerJSON(t, response, []dockerswarm.Service{{ID: "service-id", Spec: spec}})
		default:
			http.NotFound(response, request)
		}
	})

	observed, err := client.GetService(t.Context(), "service-id")
	if err != nil {
		t.Fatal(err)
	}
	if observed.Running != 1 || len(observed.TaskErrors) != 1 || observed.TaskErrors[0] != "exit 1" {
		t.Fatalf("task observation lost: %#v", observed)
	}
	services, err := client.ListServices(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].ID != "service-id" {
		t.Fatalf("ListServices() = %#v", services)
	}
}

func TestDockerClientServiceObservationFailsWhenTasksAreUnknown(t *testing.T) {
	client := newDockerAPITestClient(t, func(response http.ResponseWriter, request *http.Request) {
		switch dockerAPIPath(request) {
		case "/services/service-id":
			writeDockerJSON(t, response, dockerswarm.Service{
				ID: "service-id",
				Spec: dockerswarm.ServiceSpec{TaskTemplate: dockerswarm.TaskSpec{
					ContainerSpec: &dockerswarm.ContainerSpec{Image: "nginx:1.27"},
				}},
			})
		case "/tasks":
			http.Error(response, "task inventory unavailable", http.StatusInternalServerError)
		default:
			http.NotFound(response, request)
		}
	})

	if _, err := client.GetService(t.Context(), "service-id"); err == nil || !strings.Contains(err.Error(), "failed to list tasks") {
		t.Fatalf("GetService task inventory error = %v", err)
	}
}

func TestDockerClientServiceObservationHandlesNetworkInspectionFailures(t *testing.T) {
	service := dockerswarm.Service{
		ID: "service-id",
		Spec: dockerswarm.ServiceSpec{
			TaskTemplate: dockerswarm.TaskSpec{
				ContainerSpec: &dockerswarm.ContainerSpec{Image: "nginx:1.27"},
				Networks:      []dockerswarm.NetworkAttachmentConfig{{Target: "network-id"}},
			},
		},
	}

	t.Run("transient failure rejects incomplete observation", func(t *testing.T) {
		taskListCalls := 0
		client := newDockerAPITestClient(t, func(response http.ResponseWriter, request *http.Request) {
			switch dockerAPIPath(request) {
			case "/services/service-id":
				writeDockerJSON(t, response, service)
			case "/networks/network-id":
				http.Error(response, "network inventory unavailable", http.StatusInternalServerError)
			case "/tasks":
				taskListCalls++
				writeDockerJSON(t, response, []dockerswarm.Task{})
			default:
				http.NotFound(response, request)
			}
		})

		if _, err := client.GetService(t.Context(), "service-id"); err == nil || !strings.Contains(err.Error(), "failed to inspect network") {
			t.Fatalf("GetService network inventory error = %v", err)
		}
		if taskListCalls != 0 {
			t.Fatal("task state was read after network observation became incomplete")
		}
	})

	t.Run("missing attachment remains repairable drift", func(t *testing.T) {
		client := newDockerAPITestClient(t, func(response http.ResponseWriter, request *http.Request) {
			switch dockerAPIPath(request) {
			case "/services/service-id":
				writeDockerJSON(t, response, service)
			case "/networks/network-id":
				http.NotFound(response, request)
			case "/tasks":
				writeDockerJSON(t, response, []dockerswarm.Task{})
			default:
				http.NotFound(response, request)
			}
		})

		observed, err := client.GetService(t.Context(), "service-id")
		if err != nil {
			t.Fatal(err)
		}
		if len(observed.Spec.Networks) != 1 || observed.Spec.Networks[0].Network != "network-id" {
			t.Fatalf("missing attachment was not preserved as drift: %#v", observed.Spec.Networks)
		}
	})
}

func TestDockerClientNetworkAndLogOperations(t *testing.T) {
	client := newDockerAPITestClient(t, func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && dockerAPIPath(request) == "/networks/network-id":
			writeDockerJSON(t, response, map[string]any{
				"Id": "network-id", "Name": "moduleos-root-net", "Driver": "overlay", "Attachable": true,
				"Labels": map[string]string{LabelManagedBy: "true"},
			})
		case request.Method == http.MethodGet && dockerAPIPath(request) == "/networks":
			writeDockerJSON(t, response, []map[string]any{{
				"Id": "network-id", "Name": "moduleos-root-net", "Driver": "overlay", "Attachable": true,
			}})
		case request.Method == http.MethodDelete && dockerAPIPath(request) == "/networks/network-id":
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && dockerAPIPath(request) == "/services/service-id/logs":
			_, _ = io.WriteString(response, "logs")
		default:
			http.NotFound(response, request)
		}
	})

	network, err := client.GetNetwork(t.Context(), "network-id")
	if err != nil {
		t.Fatal(err)
	}
	if network.Name != "moduleos-root-net" || !network.Attachable {
		t.Fatalf("GetNetwork() = %#v", network)
	}
	networks, err := client.ListNetworks(t.Context())
	if err != nil || len(networks) != 1 {
		t.Fatalf("ListNetworks() = %#v, %v", networks, err)
	}
	if err := client.RemoveNetwork(t.Context(), "network-id"); err != nil {
		t.Fatal(err)
	}
	logs, err := client.GetServiceLogs(t.Context(), "service-id", "25", true)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	data, err := io.ReadAll(logs)
	if err != nil || string(data) != "logs" {
		t.Fatalf("logs = %q, %v", data, err)
	}
}

func TestDockerClientNetworkMutationResolvesNames(t *testing.T) {
	current := buildSwarmSpec(canonicalServiceSpec())
	current.TaskTemplate.Networks = nil
	var updates []dockerswarm.ServiceSpec
	client := newDockerAPITestClient(t, func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.HasPrefix(dockerAPIPath(request), "/networks/"):
			writeDockerJSON(t, response, map[string]any{"Id": "network-id", "Name": "project-net"})
		case request.Method == http.MethodGet && dockerAPIPath(request) == "/services/service-id":
			writeDockerJSON(t, response, dockerswarm.Service{
				ID: "service-id", Meta: dockerswarm.Meta{Version: dockerswarm.Version{Index: uint64(len(updates) + 1)}}, Spec: current,
			})
		case request.Method == http.MethodPost && dockerAPIPath(request) == "/services/service-id/update":
			var spec dockerswarm.ServiceSpec
			if err := json.NewDecoder(request.Body).Decode(&spec); err != nil {
				t.Errorf("decode network mutation request: %v", err)
				return
			}
			updates = append(updates, spec)
			current = spec
			writeDockerJSON(t, response, map[string]any{"Warnings": []string{}})
		default:
			http.NotFound(response, request)
		}
	})

	if err := client.AttachServiceNetwork(t.Context(), "service-id", NetworkAttachment{Network: "project-net", Aliases: []string{"db"}}); err != nil {
		t.Fatal(err)
	}
	if err := client.DetachServiceNetwork(t.Context(), "service-id", "project-net"); err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 || len(updates[0].TaskTemplate.Networks) != 1 || len(updates[1].TaskTemplate.Networks) != 0 {
		t.Fatalf("unexpected network mutations: %#v", updates)
	}
}

func TestDockerClientWatchEvents(t *testing.T) {
	client := newDockerAPITestClient(t, func(response http.ResponseWriter, request *http.Request) {
		if dockerAPIPath(request) != "/events" {
			http.NotFound(response, request)
			return
		}
		writeDockerJSON(t, response, map[string]any{
			"Type": "service", "Action": "update",
			"Actor": map[string]any{"Attributes": map[string]string{"name": "moduleos_api"}},
		})
	})

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	events, errs := client.WatchEvents(ctx)
	select {
	case event := <-events:
		if event.Type != "service" || event.Action != "update" || event.Target != "moduleos_api" {
			t.Fatalf("unexpected event: %#v", event)
		}
	case err := <-errs:
		t.Fatalf("event stream failed: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for Docker event")
	}
}
