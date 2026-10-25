//go:build darwin || linux

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	moduleosswarm "github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	dockernetwork "github.com/moby/moby/api/types/network"
	dockerswarm "github.com/moby/moby/api/types/swarm"
)

var dockerVersionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

type infoBarrier struct {
	entered     chan struct{}
	release     chan struct{}
	canceled    chan struct{}
	releaseOnce sync.Once
	cancelOnce  sync.Once
}

type fakeDocker struct {
	t          *testing.T
	socketPath string
	listener   net.Listener
	server     *http.Server
	done       chan struct{}

	mu         sync.Mutex
	requests   map[string]int
	unexpected []string
	barrier    *infoBarrier
	service    *dockerswarm.Service
	handlers   sync.WaitGroup

	eventOpened    chan struct{}
	eventClosed    chan struct{}
	logOpened      chan struct{}
	logClosed      chan struct{}
	eventOpenOnce  sync.Once
	eventCloseOnce sync.Once
	logOpenOnce    sync.Once
	logCloseOnce   sync.Once
}

func startFakeDocker(t *testing.T, socketPath string) *fakeDocker {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("create fake Docker directory: %v", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen on fake Docker socket: %v", err)
	}
	fake := &fakeDocker{
		t: t, socketPath: socketPath, listener: listener, done: make(chan struct{}),
		requests: make(map[string]int), eventOpened: make(chan struct{}), eventClosed: make(chan struct{}),
		logOpened: make(chan struct{}), logClosed: make(chan struct{}),
	}
	fake.server = &http.Server{Handler: http.HandlerFunc(fake.serveHTTP), ReadHeaderTimeout: time.Second}
	go func() {
		defer close(fake.done)
		if serveErr := fake.server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			fake.recordUnexpected("serve: " + serveErr.Error())
		}
	}()
	t.Cleanup(fake.close)
	return fake
}

func (f *fakeDocker) close() {
	if f.server == nil {
		return
	}
	_ = f.server.Close()
	_ = f.listener.Close()
	select {
	case <-f.done:
	case <-time.After(processCleanupDeadline):
		f.t.Errorf("fake Docker server did not stop")
	}
	handlersDone := make(chan struct{})
	go func() {
		f.handlers.Wait()
		close(handlersDone)
	}()
	select {
	case <-handlersDone:
	case <-time.After(processCleanupDeadline):
		f.t.Errorf("fake Docker handlers did not stop")
		return
	}
	_ = os.Remove(f.socketPath)
	f.server = nil
}

func (f *fakeDocker) assertNoUnexpected() {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.unexpected) != 0 {
		f.t.Fatalf("unexpected fake Docker requests: %v", f.unexpected)
	}
}

func (f *fakeDocker) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, value := range f.requests {
		count += value
	}
	return count
}

func (f *fakeDocker) recordUnexpected(value string) {
	f.mu.Lock()
	f.unexpected = append(f.unexpected, value)
	f.mu.Unlock()
}

func (f *fakeDocker) armInfoBarrier() (<-chan struct{}, <-chan struct{}, func()) {
	barrier := &infoBarrier{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	f.mu.Lock()
	f.barrier = barrier
	f.mu.Unlock()
	return barrier.entered, barrier.canceled, func() {
		barrier.releaseOnce.Do(func() { close(barrier.release) })
	}
}

func (f *fakeDocker) setApplication(application *store.Application) {
	desired, err := moduleosswarm.BuildDesiredServiceSpec(moduleosswarm.DesiredServiceInput{
		ApplicationID: application.ID, ProjectID: store.RootProjectID, ProjectSlug: "root",
		Name: application.Name, Image: application.Image,
		DesiredRunning: application.DesiredRunState != store.DesiredRunStateStopped,
		Replicas:       application.Replicas, Generation: application.DesiredGeneration,
		ProjectNetwork: moduleosswarm.NetworkAttachment{Network: "moduleos-root-net", Aliases: []string{application.Name, "root." + application.Name}},
		Expose:         false, IngressNetwork: "moduleos-ingress", BaseDomain: "moduleos.test",
	})
	if err != nil {
		f.t.Fatalf("build fake desired service: %v", err)
	}
	replicas := desired.Replicas
	restartDelay := desired.Restart.Delay
	f.mu.Lock()
	f.service = &dockerswarm.Service{
		ID: "service-id", Meta: dockerswarm.Meta{Version: dockerswarm.Version{Index: 1}},
		Spec: dockerswarm.ServiceSpec{
			Annotations: dockerswarm.Annotations{Name: moduleosswarm.ServiceName(application.Name), Labels: desired.Labels},
			TaskTemplate: dockerswarm.TaskSpec{
				ContainerSpec: &dockerswarm.ContainerSpec{Image: desired.Image, Labels: map[string]string{moduleosswarm.LabelTaskTemplate: desired.TaskTemplateHash}},
				Networks:      []dockerswarm.NetworkAttachmentConfig{{Target: "moduleos-root-net", Aliases: []string{application.Name, "root." + application.Name}}},
				RestartPolicy: &dockerswarm.RestartPolicy{Condition: dockerswarm.RestartPolicyCondition(desired.Restart.Condition), Delay: &restartDelay},
			},
			Mode:           dockerswarm.ServiceMode{Replicated: &dockerswarm.ReplicatedService{Replicas: &replicas}},
			EndpointSpec:   &dockerswarm.EndpointSpec{Mode: dockerswarm.ResolutionMode(desired.EndpointMode)},
			UpdateConfig:   &dockerswarm.UpdateConfig{Parallelism: desired.Update.Parallelism, Delay: desired.Update.Delay, Monitor: desired.Update.Monitor, MaxFailureRatio: desired.Update.MaxFailureRatio, FailureAction: dockerswarm.FailureAction(desired.Update.FailureAction), Order: dockerswarm.UpdateOrder(desired.Update.Order)},
			RollbackConfig: &dockerswarm.UpdateConfig{Parallelism: desired.Rollback.Parallelism, Delay: desired.Rollback.Delay, Monitor: desired.Rollback.Monitor, MaxFailureRatio: desired.Rollback.MaxFailureRatio, FailureAction: dockerswarm.FailureAction(desired.Rollback.FailureAction), Order: dockerswarm.UpdateOrder(desired.Rollback.Order)},
		},
	}
	f.mu.Unlock()
}

func (f *fakeDocker) serveHTTP(response http.ResponseWriter, request *http.Request) {
	f.handlers.Add(1)
	defer f.handlers.Done()

	path := normalizeDockerPath(request.URL.Path)
	key := request.Method + " " + path
	f.mu.Lock()
	f.requests[key]++
	f.mu.Unlock()

	switch {
	case request.Method == http.MethodHead && path == "/_ping":
		response.Header().Set("API-Version", "1.52")
		response.WriteHeader(http.StatusOK)
	case request.Method == http.MethodGet && path == "/_ping":
		response.Header().Set("API-Version", "1.52")
		_, _ = io.WriteString(response, "OK")
	case request.Method == http.MethodGet && path == "/info":
		f.handleInfo(response, request)
	case request.Method == http.MethodGet && path == "/events":
		f.handleLongRequest(response, request, f.eventOpened, f.eventClosed, &f.eventOpenOnce, &f.eventCloseOnce)
	case request.Method == http.MethodGet && path == "/networks":
		writeFakeJSON(response, []map[string]any{f.rootNetwork()})
	case request.Method == http.MethodGet && strings.HasPrefix(path, "/networks/"):
		writeFakeJSON(response, f.rootNetwork())
	case request.Method == http.MethodGet && path == "/services":
		f.mu.Lock()
		var services []dockerswarm.Service
		if f.service != nil {
			services = append(services, *f.service)
		}
		f.mu.Unlock()
		writeFakeJSON(response, services)
	case request.Method == http.MethodGet && strings.HasSuffix(path, "/logs") && strings.HasPrefix(path, "/services/"):
		f.handleLogs(response, request)
	case request.Method == http.MethodGet && strings.HasPrefix(path, "/services/"):
		f.mu.Lock()
		service := f.service
		f.mu.Unlock()
		if service == nil {
			http.NotFound(response, request)
			return
		}
		writeFakeJSON(response, service)
	case request.Method == http.MethodPost && strings.HasSuffix(path, "/update") && strings.HasPrefix(path, "/services/"):
		f.handleServiceUpdate(response, request)
	case request.Method == http.MethodGet && path == "/tasks":
		writeFakeJSON(response, []dockerswarm.Task{})
	default:
		f.recordUnexpected(key + "?" + request.URL.RawQuery)
		http.NotFound(response, request)
	}
}

func (f *fakeDocker) handleInfo(response http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	barrier := f.barrier
	if barrier != nil {
		f.barrier = nil
	}
	f.mu.Unlock()
	if barrier != nil {
		close(barrier.entered)
		select {
		case <-barrier.release:
		case <-request.Context().Done():
			barrier.cancelOnce.Do(func() { close(barrier.canceled) })
			return
		}
	}
	writeFakeJSON(response, map[string]any{"Swarm": map[string]any{"LocalNodeState": "active", "ControlAvailable": true}})
}

func (f *fakeDocker) handleLongRequest(response http.ResponseWriter, request *http.Request, opened, closed chan struct{}, openOnce, closeOnce *sync.Once) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	if flusher, ok := response.(http.Flusher); ok {
		flusher.Flush()
	}
	openOnce.Do(func() { close(opened) })
	<-request.Context().Done()
	closeOnce.Do(func() { close(closed) })
}

func (f *fakeDocker) handleLogs(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	response.WriteHeader(http.StatusOK)
	payload := []byte("phase7d log line\n")
	header := make([]byte, 8)
	header[0] = 1
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	_, _ = response.Write(header)
	_, _ = response.Write(payload)
	if flusher, ok := response.(http.Flusher); ok {
		flusher.Flush()
	}
	f.logOpenOnce.Do(func() { close(f.logOpened) })
	<-request.Context().Done()
	f.logCloseOnce.Do(func() { close(f.logClosed) })
}

func (f *fakeDocker) handleServiceUpdate(response http.ResponseWriter, request *http.Request) {
	var spec dockerswarm.ServiceSpec
	if err := json.NewDecoder(request.Body).Decode(&spec); err != nil {
		f.recordUnexpected("decode service update: " + err.Error())
		http.Error(response, "invalid update", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	if f.service != nil {
		f.service.Spec = spec
		f.service.Version.Index++
	}
	f.mu.Unlock()
	writeFakeJSON(response, map[string]any{"Warnings": []string{}})
}

func (f *fakeDocker) rootNetwork() map[string]any {
	return map[string]any{
		"Id": "root-network-id", "Name": "moduleos-root-net", "Driver": "overlay", "Attachable": true,
		"Scope": "swarm", "Labels": map[string]string{
			moduleosswarm.LabelManagedBy: "true", moduleosswarm.LabelResourceKind: "project-network",
			moduleosswarm.LabelProjectID: store.RootProjectID, moduleosswarm.LabelProject: "root", moduleosswarm.LabelSchema: moduleosswarm.SchemaVersion,
		},
		"IPAM": dockernetwork.IPAM{},
	}
}

func normalizeDockerPath(path string) string {
	normalized := dockerVersionPrefix.ReplaceAllString(path, "")
	if normalized == "" {
		return "/"
	}
	return normalized
}

func writeFakeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		panic(fmt.Sprintf("encode fake Docker response: %v", err))
	}
}

func waitForChannel(t *testing.T, channel <-chan struct{}, timeout time.Duration, description string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case <-channel:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s", description)
	}
}
