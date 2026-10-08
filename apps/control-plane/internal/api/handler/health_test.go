package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/handler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/buildinfo"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

type healthStoreStub struct {
	pingErr      error
	listErr      error
	applications []*store.Application
	waitForPing  bool
	pingStarted  chan<- struct{}
	pingRelease  <-chan struct{}
	pingCalls    int
	listCalls    int
}

func (s *healthStoreStub) Ping(ctx context.Context) error {
	s.pingCalls++
	if s.pingStarted != nil {
		s.pingStarted <- struct{}{}
	}
	if s.pingRelease != nil {
		select {
		case <-s.pingRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.waitForPing {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.pingErr
}

func (s *healthStoreStub) ListApplications(context.Context) ([]*store.Application, error) {
	s.listCalls++
	return s.applications, s.listErr
}

type healthRuntimeStub struct {
	healthErr    error
	networkErr   error
	networkInfo  *swarm.NetworkInfo
	nilNetwork   bool
	healthCalls  int
	networkCalls int
	lastNetwork  string
}

func (r *healthRuntimeStub) Health(context.Context) error {
	r.healthCalls++
	return r.healthErr
}

func (r *healthRuntimeStub) GetNetwork(_ context.Context, network string) (*swarm.NetworkInfo, error) {
	r.networkCalls++
	r.lastNetwork = network
	if r.networkErr != nil {
		return nil, r.networkErr
	}
	if r.nilNetwork {
		return nil, nil
	}
	if r.networkInfo != nil {
		return r.networkInfo, nil
	}
	return &swarm.NetworkInfo{Name: network, Driver: "overlay", Attachable: true}, nil
}

func newHealthServer(t *testing.T, storeStub handler.HealthStore, runtimeStub handler.HealthRuntime, timeout time.Duration) (*fiber.App, *handler.HealthHandler) {
	t.Helper()
	health, err := handler.NewHealthHandler(storeStub, runtimeStub, "moduleos-ingress", timeout)
	if err != nil {
		t.Fatalf("new health handler: %v", err)
	}
	server := fiber.New()
	server.Get("/live", health.Live)
	server.Get("/ready", health.Ready)
	server.Get("/version", health.Version)
	return server, health
}

func TestNewHealthHandlerRejectsInvalidDependencies(t *testing.T) {
	storeStub := &healthStoreStub{}
	runtimeStub := &healthRuntimeStub{}
	var typedNilStore *healthStoreStub
	var typedNilRuntime *healthRuntimeStub
	tests := []struct {
		name    string
		store   handler.HealthStore
		runtime handler.HealthRuntime
		network string
		timeout time.Duration
		want    error
	}{
		{name: "nil store", runtime: runtimeStub, network: "ingress", timeout: time.Second, want: handler.ErrMissingHealthStore},
		{name: "typed nil store", store: typedNilStore, runtime: runtimeStub, network: "ingress", timeout: time.Second, want: handler.ErrMissingHealthStore},
		{name: "nil runtime", store: storeStub, network: "ingress", timeout: time.Second, want: handler.ErrMissingHealthRuntime},
		{name: "typed nil runtime", store: storeStub, runtime: typedNilRuntime, network: "ingress", timeout: time.Second, want: handler.ErrMissingHealthRuntime},
		{name: "empty network", store: storeStub, runtime: runtimeStub, timeout: time.Second, want: handler.ErrInvalidHealthOptions},
		{name: "zero timeout", store: storeStub, runtime: runtimeStub, network: "ingress", want: handler.ErrInvalidHealthOptions},
		{name: "negative timeout", store: storeStub, runtime: runtimeStub, network: "ingress", timeout: -time.Second, want: handler.ErrInvalidHealthOptions},
		{name: "excessive timeout", store: storeStub, runtime: runtimeStub, network: "ingress", timeout: handler.MaximumReadinessTimeout + time.Nanosecond, want: handler.ErrInvalidHealthOptions},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := handler.NewHealthHandler(test.store, test.runtime, test.network, test.timeout)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestHealthReadyWithoutExposedApplications(t *testing.T) {
	storeStub := &healthStoreStub{}
	runtimeStub := &healthRuntimeStub{}
	server, _ := newHealthServer(t, storeStub, runtimeStub, time.Second)
	response := testRequest(t, server, httptest.NewRequest("GET", "/ready", nil))
	if response.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	body := decodeResponse[handler.ReadinessResponse](t, response)
	want := handler.ReadinessComponents{
		Database: handler.ComponentReady,
		Docker:   handler.ComponentReady,
		Swarm:    handler.ComponentReady,
		Ingress:  handler.ComponentNotRequired,
	}
	if body.Status != "ready" || body.Code != "" || body.Components != want {
		t.Fatalf("response = %#v, want ready with %#v", body, want)
	}
	if storeStub.pingCalls != 1 || storeStub.listCalls != 1 || runtimeStub.healthCalls != 1 || runtimeStub.networkCalls != 0 {
		t.Fatalf("probe calls store=%d/%d runtime=%d/%d", storeStub.pingCalls, storeStub.listCalls, runtimeStub.healthCalls, runtimeStub.networkCalls)
	}
}

func TestHealthReadyChecksIngressForExposedApplication(t *testing.T) {
	storeStub := &healthStoreStub{applications: []*store.Application{nil, {Expose: false}, {Expose: true}}}
	runtimeStub := &healthRuntimeStub{}
	server, _ := newHealthServer(t, storeStub, runtimeStub, time.Second)
	response := testRequest(t, server, httptest.NewRequest("GET", "/ready", nil))
	body := decodeResponse[handler.ReadinessResponse](t, response)
	if response.StatusCode != fiber.StatusOK || body.Components.Ingress != handler.ComponentReady {
		t.Fatalf("status/body = %d/%#v", response.StatusCode, body)
	}
	if runtimeStub.networkCalls != 1 || runtimeStub.lastNetwork != "moduleos-ingress" {
		t.Fatalf("network probe = %d/%q", runtimeStub.networkCalls, runtimeStub.lastNetwork)
	}
}

func TestHealthReadyDoesNotSucceedAfterDrainStarts(t *testing.T) {
	pingStarted := make(chan struct{})
	pingRelease := make(chan struct{})
	storeStub := &healthStoreStub{pingStarted: pingStarted, pingRelease: pingRelease}
	runtimeStub := &healthRuntimeStub{}
	server, health := newHealthServer(t, storeStub, runtimeStub, time.Second)
	result := make(chan *http.Response, 1)
	go func() {
		response, _ := server.Test(httptest.NewRequest("GET", "/ready", nil), fiber.TestConfig{Timeout: 0})
		result <- response
	}()
	<-pingStarted
	health.SetDraining()
	close(pingRelease)
	response := <-result
	t.Cleanup(func() { _ = response.Body.Close() })
	body := decodeResponse[handler.ReadinessResponse](t, response)
	if response.StatusCode != fiber.StatusServiceUnavailable || body.Status != "not_ready" || body.Code != "shutting_down" {
		t.Fatalf("status/body after drain transition = %d/%#v", response.StatusCode, body)
	}
}

func TestHealthReadinessFailures(t *testing.T) {
	dependencyErr := errors.New("dependency failed")
	tests := []struct {
		name     string
		store    *healthStoreStub
		runtime  *healthRuntimeStub
		wantCode string
		want     handler.ReadinessComponents
	}{
		{
			name: "database ping", store: &healthStoreStub{pingErr: dependencyErr}, runtime: &healthRuntimeStub{}, wantCode: "database_unavailable",
			want: handler.ReadinessComponents{Database: handler.ComponentUnavailable, Docker: handler.ComponentNotChecked, Swarm: handler.ComponentNotChecked, Ingress: handler.ComponentNotChecked},
		},
		{
			name: "docker", store: &healthStoreStub{}, runtime: &healthRuntimeStub{healthErr: dependencyErr}, wantCode: "docker_unavailable",
			want: handler.ReadinessComponents{Database: handler.ComponentReady, Docker: handler.ComponentUnavailable, Swarm: handler.ComponentNotChecked, Ingress: handler.ComponentNotChecked},
		},
		{
			name: "inactive swarm", store: &healthStoreStub{}, runtime: &healthRuntimeStub{healthErr: swarm.ErrSwarmInactive}, wantCode: "swarm_unavailable",
			want: handler.ReadinessComponents{Database: handler.ComponentReady, Docker: handler.ComponentReady, Swarm: handler.ComponentUnavailable, Ingress: handler.ComponentNotChecked},
		},
		{
			name: "worker node", store: &healthStoreStub{}, runtime: &healthRuntimeStub{healthErr: swarm.ErrSwarmManagerRequired}, wantCode: "swarm_unavailable",
			want: handler.ReadinessComponents{Database: handler.ComponentReady, Docker: handler.ComponentReady, Swarm: handler.ComponentUnavailable, Ingress: handler.ComponentNotChecked},
		},
		{
			name: "application scan", store: &healthStoreStub{listErr: dependencyErr}, runtime: &healthRuntimeStub{}, wantCode: "database_unavailable",
			want: handler.ReadinessComponents{Database: handler.ComponentUnavailable, Docker: handler.ComponentReady, Swarm: handler.ComponentReady, Ingress: handler.ComponentNotChecked},
		},
		{
			name: "ingress", store: &healthStoreStub{applications: []*store.Application{{Expose: true}}}, runtime: &healthRuntimeStub{networkErr: dependencyErr}, wantCode: "ingress_network_unavailable",
			want: handler.ReadinessComponents{Database: handler.ComponentReady, Docker: handler.ComponentReady, Swarm: handler.ComponentReady, Ingress: handler.ComponentUnavailable},
		},
		{
			name: "nil ingress network", store: &healthStoreStub{applications: []*store.Application{{Expose: true}}}, runtime: &healthRuntimeStub{nilNetwork: true}, wantCode: "ingress_network_unavailable",
			want: handler.ReadinessComponents{Database: handler.ComponentReady, Docker: handler.ComponentReady, Swarm: handler.ComponentReady, Ingress: handler.ComponentUnavailable},
		},
		{
			name: "wrong ingress driver", store: &healthStoreStub{applications: []*store.Application{{Expose: true}}}, runtime: &healthRuntimeStub{networkInfo: &swarm.NetworkInfo{Name: "moduleos-ingress", Driver: "bridge", Attachable: true}}, wantCode: "ingress_network_unavailable",
			want: handler.ReadinessComponents{Database: handler.ComponentReady, Docker: handler.ComponentReady, Swarm: handler.ComponentReady, Ingress: handler.ComponentUnavailable},
		},
		{
			name: "non-attachable ingress", store: &healthStoreStub{applications: []*store.Application{{Expose: true}}}, runtime: &healthRuntimeStub{networkInfo: &swarm.NetworkInfo{Name: "moduleos-ingress", Driver: "overlay"}}, wantCode: "ingress_network_unavailable",
			want: handler.ReadinessComponents{Database: handler.ComponentReady, Docker: handler.ComponentReady, Swarm: handler.ComponentReady, Ingress: handler.ComponentUnavailable},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newHealthServer(t, test.store, test.runtime, time.Second)
			response := testRequest(t, server, httptest.NewRequest("GET", "/ready", nil))
			body := decodeResponse[handler.ReadinessResponse](t, response)
			if response.StatusCode != fiber.StatusServiceUnavailable || body.Status != "not_ready" || body.Code != test.wantCode || body.Components != test.want {
				t.Fatalf("status/body = %d/%#v, want 503/%q/%#v", response.StatusCode, body, test.wantCode, test.want)
			}
		})
	}
}

func TestHealthDrainingSkipsDependenciesAndKeepsLiveness(t *testing.T) {
	storeStub := &healthStoreStub{}
	runtimeStub := &healthRuntimeStub{}
	server, health := newHealthServer(t, storeStub, runtimeStub, time.Second)
	health.SetDraining()

	ready := testRequest(t, server, httptest.NewRequest("GET", "/ready", nil))
	readyBody := decodeResponse[handler.ReadinessResponse](t, ready)
	if ready.StatusCode != fiber.StatusServiceUnavailable || readyBody.Code != "shutting_down" || readyBody.Components != (handler.ReadinessComponents{
		Database: handler.ComponentNotChecked, Docker: handler.ComponentNotChecked, Swarm: handler.ComponentNotChecked, Ingress: handler.ComponentNotChecked,
	}) {
		t.Fatalf("readiness = %d/%#v", ready.StatusCode, readyBody)
	}
	if storeStub.pingCalls != 0 || runtimeStub.healthCalls != 0 {
		t.Fatalf("dependencies were probed while draining")
	}

	live := testRequest(t, server, httptest.NewRequest("GET", "/live", nil))
	liveBody := decodeResponse[handler.LivenessResponse](t, live)
	if live.StatusCode != fiber.StatusOK || liveBody.Status != "live" {
		t.Fatalf("liveness = %d/%#v", live.StatusCode, liveBody)
	}
	if live.Header.Get("Cache-Control") != "no-store" || ready.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("health responses must disable caching")
	}
}

func TestHealthReadinessEnforcesTimeout(t *testing.T) {
	storeStub := &healthStoreStub{waitForPing: true}
	server, _ := newHealthServer(t, storeStub, &healthRuntimeStub{}, 10*time.Millisecond)
	started := time.Now()
	response := testRequest(t, server, httptest.NewRequest("GET", "/ready", nil))
	body := decodeResponse[handler.ReadinessResponse](t, response)
	if response.StatusCode != fiber.StatusServiceUnavailable || body.Code != "database_unavailable" {
		t.Fatalf("status/body = %d/%#v", response.StatusCode, body)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("readiness timeout took %v", elapsed)
	}
}

func TestVersionUsesBuildMetadataDTO(t *testing.T) {
	originalVersion, originalCommit, originalBuildDate := buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate
	t.Cleanup(func() {
		buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate = originalVersion, originalCommit, originalBuildDate
	})
	buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate = "v0.1.0", "abc123", "2026-10-08T00:00:00Z"
	server, _ := newHealthServer(t, &healthStoreStub{}, &healthRuntimeStub{}, time.Second)
	response := testRequest(t, server, httptest.NewRequest("GET", "/version", nil))
	body := decodeResponse[handler.VersionResponse](t, response)
	if response.StatusCode != fiber.StatusOK || body != (handler.VersionResponse{Version: "v0.1.0", Commit: "abc123", BuildDate: "2026-10-08T00:00:00Z"}) {
		t.Fatalf("status/body = %d/%#v", response.StatusCode, body)
	}
}
