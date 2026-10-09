package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/testkit/swarmfake"
)

type recordingQueue struct {
	mu    sync.Mutex
	names []string
}

func (q *recordingQueue) Enqueue(name string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.names = append(q.names, name)
}

func (q *recordingQueue) snapshot() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.names...)
}

func (q *recordingQueue) reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.names = nil
}

func newEdgeService(t *testing.T) (*Service, *store.SQLiteStore, *swarmfake.Client, *recordingQueue) {
	t.Helper()
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mock := swarmfake.New()
	queue := &recordingQueue{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewService(st, mock, "moduleos.local", logger).WithReconcileQueue(queue)
	return service, st, mock, queue
}

func mustCreateProject(t *testing.T, service *Service, name, slug string) *store.Project {
	t.Helper()
	project, err := service.CreateProject(t.Context(), CreateProjectRequest{Name: name, Slug: slug})
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func mustCreateApp(t *testing.T, service *Service, request CreateAppRequest) *store.Application {
	t.Helper()
	application, err := service.CreateApp(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	return application
}

type lookupErrorStore struct {
	store.Store
	applicationErr error
	projectErr     error
}

func (s *lookupErrorStore) GetApplication(ctx context.Context, name string) (*store.Application, error) {
	if s.applicationErr != nil {
		return nil, s.applicationErr
	}
	return s.Store.GetApplication(ctx, name)
}

func (s *lookupErrorStore) GetProject(ctx context.Context, slug string) (*store.Project, error) {
	if s.projectErr != nil {
		return nil, s.projectErr
	}
	return s.Store.GetProject(ctx, slug)
}

type admissionProbeStore struct {
	store.Store
	active    atomic.Int32
	maxActive atomic.Int32
}

func (s *admissionProbeStore) GetApplication(ctx context.Context, name string) (*store.Application, error) {
	active := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		current := s.maxActive.Load()
		if active <= current || s.maxActive.CompareAndSwap(current, active) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	return s.Store.GetApplication(ctx, name)
}

type projectLinkLimitStore struct {
	store.Store
	links int
}

func (s *projectLinkLimitStore) ListProjectLinksByProject(context.Context, string) ([]*store.ProjectLink, error) {
	return make([]*store.ProjectLink, s.links), nil
}

type logErrorClient struct {
	swarm.Client
	err error
}

func (c *logErrorClient) GetServiceLogs(context.Context, string, string, bool) (io.ReadCloser, error) {
	return nil, c.err
}

func TestServiceConfigurationAndAccessors(t *testing.T) {
	service, st, _, _ := newEdgeService(t)
	if service.Store() != st {
		t.Fatal("Store did not return the configured persistence boundary")
	}
	if service.IngressNetwork() != "moduleos-ingress" {
		t.Fatalf("default ingress network = %q", service.IngressNetwork())
	}

	roots := []string{"/srv/moduleos"}
	service.WithRuntimeLimits(3, roots, 2*time.Second).WithIngressNetwork("custom-ingress")
	roots[0] = "/mutated"
	if service.maxReplicas != 3 || service.deploymentTimeout != 2*time.Second ||
		len(service.allowedMountRoots) != 1 || service.allowedMountRoots[0] != "/srv/moduleos" ||
		service.IngressNetwork() != "custom-ingress" {
		t.Fatalf("runtime configuration was not applied defensively: %#v", service)
	}
	service.WithRuntimeLimits(0, nil, 0).WithResourceLimits(0, 0).WithIngressNetwork("")
	if service.maxReplicas != 3 || service.deploymentTimeout != 2*time.Second || service.IngressNetwork() != "custom-ingress" {
		t.Fatal("non-positive or empty overrides changed existing limits")
	}
	serviceWithDefaultLogger := NewService(st, swarmfake.New(), "moduleos.local", nil)
	if serviceWithDefaultLogger.log == nil {
		t.Fatal("nil logger was not replaced with a safe default")
	}
}

func TestCreateAppValidationDefaultsAndQueue(t *testing.T) {
	service, st, _, queue := newEdgeService(t)
	service.WithRuntimeLimits(2, []string{"/srv/moduleos"}, time.Minute)

	tests := []struct {
		name    string
		request CreateAppRequest
	}{
		{name: "empty image", request: CreateAppRequest{Name: "empty-image"}},
		{name: "negative replicas", request: CreateAppRequest{Name: "negative", Image: "nginx:1.27", Replicas: -1}},
		{name: "replica maximum", request: CreateAppRequest{Name: "too-many", Image: "nginx:1.27", Replicas: 3}},
		{name: "unsupported source", request: CreateAppRequest{Name: "tar-source", Image: "bundle.tar", SourceType: store.SourceTypeTar}},
		{name: "exposure without port", request: CreateAppRequest{Name: "bad-expose", Image: "nginx:1.27", Expose: true}},
		{name: "ingress port overflow", request: CreateAppRequest{Name: "bad-ingress-port", Image: "nginx:1.27", IngressContainerPort: 65536}},
		{name: "environment NUL", request: CreateAppRequest{Name: "bad-environment", Image: "nginx:1.27", EnvVars: map[string]string{"TOKEN": "a\x00b"}}},
		{name: "relative mount", request: CreateAppRequest{Name: "relative-mount", Image: "nginx:1.27", Volumes: []swarm.VolumeConfig{{Source: "relative", Target: "/data"}}}},
		{name: "noncanonical mount source", request: CreateAppRequest{Name: "noncanonical-source", Image: "nginx:1.27", Volumes: []swarm.VolumeConfig{{Source: "/srv/moduleos/data/../data", Target: "/data"}}}},
		{name: "noncanonical mount target", request: CreateAppRequest{Name: "noncanonical-target", Image: "nginx:1.27", Volumes: []swarm.VolumeConfig{{Source: "/srv/moduleos/data", Target: "/data/"}}}},
		{name: "root mount target", request: CreateAppRequest{Name: "root-target", Image: "nginx:1.27", Volumes: []swarm.VolumeConfig{{Source: "/srv/moduleos/data", Target: "/"}}}},
		{name: "mount path NUL", request: CreateAppRequest{Name: "nul-mount", Image: "nginx:1.27", Volumes: []swarm.VolumeConfig{{Source: "/srv/moduleos/data", Target: "/data\x00suffix"}}}},
		{name: "outside allowed root", request: CreateAppRequest{Name: "outside-root", Image: "nginx:1.27", Volumes: []swarm.VolumeConfig{{Source: "/srv/other", Target: "/data"}}}},
		{name: "sensitive mount", request: CreateAppRequest{Name: "docker-socket", Image: "nginx:1.27", Volumes: []swarm.VolumeConfig{{Source: "/var/run/docker.sock", Target: "/socket"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.CreateApp(t.Context(), test.request); !errors.Is(err, store.ErrInvalidData) {
				t.Fatalf("error = %v, want ErrInvalidData", err)
			}
		})
	}

	created := mustCreateApp(t, service, CreateAppRequest{
		Name:     "valid-app",
		Image:    "nginx:1.27",
		EnvVars:  map[string]string{"Z": "last", "A": "first"},
		Volumes:  []swarm.VolumeConfig{{Source: "/srv/moduleos/data", Target: "/data"}},
		Replicas: 0,
	})
	if created.Replicas != 1 || created.ResumeReplicas != 1 || created.SourceType != store.SourceTypeImage ||
		created.DesiredGeneration != 1 || created.EnvVars != `{"A":"first","Z":"last"}` {
		t.Fatalf("unexpected create defaults: %#v", created)
	}
	if names := queue.snapshot(); len(names) != 1 || names[0] != created.Name {
		t.Fatalf("create queue events = %#v", names)
	}
	if _, err := st.GetApplication(t.Context(), created.Name); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAppCollectionLimits(t *testing.T) {
	service, _, _, _ := newEdgeService(t)
	environment := make(map[string]string, maxEnvironmentEntries+1)
	for index := 0; index <= maxEnvironmentEntries; index++ {
		environment[fmt.Sprintf("KEY_%d", index)] = "value"
	}
	ports := make([]swarm.PortConfig, maxPortEntries+1)
	volumes := make([]swarm.VolumeConfig, maxVolumeEntries+1)
	for index := range ports {
		ports[index] = swarm.PortConfig{ContainerPort: uint32(index + 1)}
	}
	for index := range volumes {
		volumes[index] = swarm.VolumeConfig{Source: fmt.Sprintf("/srv/data/%d", index), Target: fmt.Sprintf("/data/%d", index)}
	}

	requests := []CreateAppRequest{
		{Name: "env-limit", Image: "nginx:1.27", EnvVars: environment},
		{Name: "port-limit", Image: "nginx:1.27", Ports: ports},
		{Name: "volume-limit", Image: "nginx:1.27", Volumes: volumes},
	}
	for _, request := range requests {
		if _, err := service.CreateApp(t.Context(), request); !errors.Is(err, store.ErrInvalidData) {
			t.Fatalf("CreateApp(%s) error = %v, want ErrInvalidData", request.Name, err)
		}
	}
}

func TestMountSymlinksCannotEscapeAllowedRoots(t *testing.T) {
	service, st, _, queue := newEdgeService(t)
	allowedRoot := t.TempDir()
	inside := filepath.Join(allowedRoot, "inside")
	outside := t.TempDir()
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	insideLink := filepath.Join(allowedRoot, "inside-link")
	escapeLink := filepath.Join(allowedRoot, "escape-link")
	if err := os.Symlink(inside, insideLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, escapeLink); err != nil {
		t.Fatal(err)
	}
	service.WithRuntimeLimits(20, []string{allowedRoot}, time.Minute)

	for _, test := range []struct {
		name   string
		source string
	}{
		{name: "existing destination", source: escapeLink},
		{name: "missing suffix", source: filepath.Join(escapeLink, "future")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.CreateApp(t.Context(), CreateAppRequest{
				Name: "escape-create", Image: "nginx:1.27",
				Volumes: []swarm.VolumeConfig{{Source: test.source, Target: "/data"}},
			}); !errors.Is(err, store.ErrInvalidData) {
				t.Fatalf("symlink escape create error = %v, want ErrInvalidData", err)
			}
			if _, err := st.GetApplication(t.Context(), "escape-create"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("rejected symlink escape was persisted: %v", err)
			}
		})
	}

	requestVolumes := []swarm.VolumeConfig{{Source: insideLink, Target: "/data"}}
	created := mustCreateApp(t, service, CreateAppRequest{
		Name: "inside-link", Image: "nginx:1.27", Volumes: requestVolumes,
	})
	if requestVolumes[0].Source != insideLink {
		t.Fatalf("caller's mount input was mutated: %#v", requestVolumes)
	}
	resolvedInside, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	persistedVolumes, err := decodeVolumes(created.Volumes)
	if err != nil {
		t.Fatal(err)
	}
	if len(persistedVolumes) != 1 || persistedVolumes[0].Source != resolvedInside {
		t.Fatalf("persisted mount source = %#v, want resolved source %q", persistedVolumes, resolvedInside)
	}

	queue.reset()
	generation := created.DesiredGeneration
	escapeVolumes := []swarm.VolumeConfig{{Source: escapeLink, Target: "/data"}}
	if _, err := service.UpdateApp(t.Context(), UpdateAppRequest{
		AppName: created.Name, ExpectedGeneration: generation, Volumes: &escapeVolumes,
	}); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("symlink escape update error = %v, want ErrInvalidData", err)
	}
	unchanged, err := st.GetApplication(t.Context(), created.Name)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.DesiredGeneration != generation || len(queue.snapshot()) != 0 {
		t.Fatalf("rejected update mutated intent: app=%#v queue=%#v", unchanged, queue.snapshot())
	}

	unchanged.Volumes = encodeVolumes(escapeVolumes)
	if err := st.UpdateApplication(t.Context(), unchanged); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BuildDesiredServiceSpec(t.Context(), unchanged); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("persisted symlink escape desired spec error = %v, want ErrInvalidData", err)
	}
}

func TestCreateAdmissionIsSerializedAndLimitCannotBeExceeded(t *testing.T) {
	baseService, st, mock, _ := newEdgeService(t)
	probe := &admissionProbeStore{Store: st}
	service := NewService(probe, mock, "moduleos.local", baseService.log).WithResourceLimits(1, 20)

	start := make(chan struct{})
	errorsByRequest := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		go func() {
			<-start
			_, err := service.CreateApp(context.Background(), CreateAppRequest{Name: name, Image: "nginx:1.27"})
			errorsByRequest <- err
		}()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-errorsByRequest
		switch {
		case err == nil:
			successes++
		case errors.Is(err, store.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent create error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	if probe.maxActive.Load() != 1 {
		t.Fatalf("admission checks overlapped: max active = %d", probe.maxActive.Load())
	}
	applications, err := st.ListApplications(t.Context())
	if err != nil || len(applications) != 1 {
		t.Fatalf("persisted applications = %d, err=%v", len(applications), err)
	}
}

func TestConcurrentRelationshipMutationsPreserveDeletionInvariants(t *testing.T) {
	service, st, _, _ := newEdgeService(t)
	service.WithResourceLimits(1000, 1000)

	for index := 0; index < 20; index++ {
		slug := fmt.Sprintf("project-race-%d", index)
		name := fmt.Sprintf("project-race-app-%d", index)
		mustCreateProject(t, service, "Project Race", slug)
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			results <- service.DeleteProject(context.Background(), slug)
		}()
		go func() {
			<-start
			_, err := service.CreateApp(context.Background(), CreateAppRequest{Name: name, ProjectSlug: slug, Image: "nginx:1.27"})
			results <- err
		}()
		close(start)
		first, second := <-results, <-results
		if (first == nil) == (second == nil) {
			t.Fatalf("project/app race %d results = %v, %v; want one winner", index, first, second)
		}
		loser := first
		if loser == nil {
			loser = second
		}
		if !errors.Is(loser, store.ErrConflict) {
			t.Fatalf("project/app race %d loser error = %v", index, loser)
		}
	}

	for index := 0; index < 20; index++ {
		targetSlug := fmt.Sprintf("link-target-%d", index)
		sourceSlug := fmt.Sprintf("link-source-%d", index)
		appName := fmt.Sprintf("link-race-app-%d", index)
		mustCreateProject(t, service, "Link Target", targetSlug)
		mustCreateProject(t, service, "Link Source", sourceSlug)
		application := mustCreateApp(t, service, CreateAppRequest{Name: appName, ProjectSlug: targetSlug, Image: "nginx:1.27"})
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, err := service.DeleteAppIntent(context.Background(), appName, application.DesiredGeneration)
			results <- err
		}()
		go func() {
			<-start
			_, err := service.CreateProjectLink(context.Background(), sourceSlug, appName, "target.app")
			results <- err
		}()
		close(start)
		first, second := <-results, <-results
		if (first == nil) == (second == nil) {
			t.Fatalf("link/delete race %d results = %v, %v; want one winner", index, first, second)
		}
		loser := first
		if loser == nil {
			loser = second
		}
		if !errors.Is(loser, store.ErrConflict) {
			t.Fatalf("link/delete race %d loser error = %v", index, loser)
		}
		persisted, err := st.GetApplication(t.Context(), application.Name)
		if err != nil {
			t.Fatal(err)
		}
		links, err := st.ListProjectLinksByApp(t.Context(), application.ID)
		if err != nil {
			t.Fatal(err)
		}
		if (persisted.DeletionTimestamp != nil) == (len(links) > 0) {
			t.Fatalf("link/delete race %d inconsistent state: deleting=%v links=%d", index, persisted.DeletionTimestamp != nil, len(links))
		}
	}
}

func TestAvailabilityLookupsFailClosed(t *testing.T) {
	baseService, st, mock, _ := newEdgeService(t)
	backendErr := errors.New("database unavailable")
	logger := baseService.log

	applicationService := NewService(&lookupErrorStore{Store: st, applicationErr: backendErr}, mock, "moduleos.local", logger)
	if _, err := applicationService.CreateApp(t.Context(), CreateAppRequest{Name: "app", Image: "nginx:1.27"}); !errors.Is(err, backendErr) {
		t.Fatalf("application lookup error = %v", err)
	}
	projectService := NewService(&lookupErrorStore{Store: st, projectErr: backendErr}, mock, "moduleos.local", logger)
	if _, err := projectService.CreateProject(t.Context(), CreateProjectRequest{Name: "Project", Slug: "project"}); !errors.Is(err, backendErr) {
		t.Fatalf("project lookup error = %v", err)
	}
}

func TestUpdateAppIsIdempotentAndCASProtected(t *testing.T) {
	service, st, _, queue := newEdgeService(t)
	created := mustCreateApp(t, service, CreateAppRequest{Name: "patch-app", Image: "nginx:1.27"})
	queue.reset()
	if created.EnvVars != "{}" {
		t.Fatalf("omitted environment persisted as %q", created.EnvVars)
	}

	emptyEnvironment := map[string]string{}
	replayed, err := service.UpdateApp(t.Context(), UpdateAppRequest{
		AppName: created.Name, ExpectedGeneration: created.DesiredGeneration, EnvVars: emptyEnvironment,
	})
	if err != nil || replayed.DesiredGeneration != created.DesiredGeneration || len(queue.snapshot()) != 0 {
		t.Fatalf("empty environment replay = %#v, %v; queue=%#v", replayed, err, queue.snapshot())
	}

	sameImage := created.Image
	if result, err := service.UpdateApp(t.Context(), UpdateAppRequest{
		AppName: created.Name, ExpectedGeneration: 0, Image: &sameImage,
	}); !errors.Is(err, store.ErrGenerationConflict) || result != nil || len(queue.snapshot()) != 0 {
		t.Fatalf("zero generation no-op = %#v, %v; queue=%#v", result, err, queue.snapshot())
	}
	replayed, err = service.UpdateApp(t.Context(), UpdateAppRequest{
		AppName: created.Name, ExpectedGeneration: created.DesiredGeneration, Image: &sameImage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.DesiredGeneration != created.DesiredGeneration || len(queue.snapshot()) != 0 {
		t.Fatalf("no-op patch changed state or queued work: app=%#v queue=%#v", replayed, queue.snapshot())
	}

	environment := map[string]string{"MODE": "production"}
	updated, err := service.UpdateApp(t.Context(), UpdateAppRequest{
		AppName: created.Name, ExpectedGeneration: created.DesiredGeneration, EnvVars: environment,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DesiredGeneration != created.DesiredGeneration+1 || len(queue.snapshot()) != 1 {
		t.Fatalf("patch did not advance exactly once: app=%#v queue=%#v", updated, queue.snapshot())
	}

	replayed, err = service.UpdateApp(t.Context(), UpdateAppRequest{
		AppName: created.Name, ExpectedGeneration: created.DesiredGeneration, EnvVars: environment,
	})
	if !errors.Is(err, store.ErrGenerationConflict) || replayed != nil {
		t.Fatalf("stale no-op patch = %#v, %v; want generation conflict", replayed, err)
	}
	if len(queue.snapshot()) != 1 {
		t.Fatalf("stale no-op patch queued work: queue=%#v", queue.snapshot())
	}

	replayed, err = service.UpdateApp(t.Context(), UpdateAppRequest{
		AppName: created.Name, ExpectedGeneration: updated.DesiredGeneration, EnvVars: environment,
	})
	if err != nil || replayed.DesiredGeneration != updated.DesiredGeneration || len(queue.snapshot()) != 1 {
		t.Fatalf("current-generation no-op patch = %#v, %v; queue=%#v", replayed, err, queue.snapshot())
	}

	newImage := "nginx:2.0"
	if _, err := service.UpdateApp(t.Context(), UpdateAppRequest{
		AppName: created.Name, ExpectedGeneration: created.DesiredGeneration, Image: &newImage,
	}); !errors.Is(err, store.ErrGenerationConflict) {
		t.Fatalf("stale patch error = %v, want generation conflict", err)
	}
	persisted, err := st.GetApplication(t.Context(), created.Name)
	if err != nil || persisted.Image != created.Image || persisted.DesiredGeneration != updated.DesiredGeneration {
		t.Fatalf("stale patch partially persisted: app=%#v err=%v", persisted, err)
	}
}

func TestUpdateAppRejectsInvalidPatches(t *testing.T) {
	service, st, _, queue := newEdgeService(t)
	service.WithRuntimeLimits(20, []string{"/srv/moduleos"}, time.Minute)
	created := mustCreateApp(t, service, CreateAppRequest{Name: "validation-app", Image: "nginx:1.27"})

	emptyImage := "   "
	outsideVolumes := []swarm.VolumeConfig{{Source: "/srv/outside", Target: "/data"}}
	noncanonicalSource := []swarm.VolumeConfig{{Source: "/srv/moduleos/data/../data", Target: "/data"}}
	noncanonicalTarget := []swarm.VolumeConfig{{Source: "/srv/moduleos/data", Target: "/data/"}}
	rootTarget := []swarm.VolumeConfig{{Source: "/srv/moduleos/data", Target: "/"}}
	nulTarget := []swarm.VolumeConfig{{Source: "/srv/moduleos/data", Target: "/data\x00suffix"}}
	nulEnvironment := map[string]string{"TOKEN": "a\x00b"}
	badPorts := []swarm.PortConfig{{ContainerPort: 0}}
	ingressPortOverflow := uint32(65536)
	expose := true
	requests := []UpdateAppRequest{
		{AppName: created.Name, ExpectedGeneration: -1},
		{AppName: created.Name, ExpectedGeneration: -1, Image: &emptyImage},
		{AppName: created.Name, ExpectedGeneration: -1, Volumes: &outsideVolumes},
		{AppName: created.Name, ExpectedGeneration: -1, Volumes: &noncanonicalSource},
		{AppName: created.Name, ExpectedGeneration: -1, Volumes: &noncanonicalTarget},
		{AppName: created.Name, ExpectedGeneration: -1, Volumes: &rootTarget},
		{AppName: created.Name, ExpectedGeneration: -1, Volumes: &nulTarget},
		{AppName: created.Name, ExpectedGeneration: -1, EnvVars: nulEnvironment},
		{AppName: created.Name, ExpectedGeneration: -1, Ports: &badPorts},
		{AppName: created.Name, ExpectedGeneration: -1, IngressContainerPort: &ingressPortOverflow},
		{AppName: created.Name, ExpectedGeneration: -1, Expose: &expose},
	}
	for index, request := range requests {
		if _, err := service.UpdateApp(t.Context(), request); !errors.Is(err, store.ErrInvalidData) {
			t.Fatalf("invalid patch %d error = %v, want ErrInvalidData", index, err)
		}
	}
	persisted, err := st.GetApplication(t.Context(), created.Name)
	if err != nil || persisted.DesiredGeneration != created.DesiredGeneration || persisted.Volumes != created.Volumes || len(queue.snapshot()) != 1 {
		t.Fatalf("invalid patches mutated state: app=%#v err=%v queue=%#v", persisted, err, queue.snapshot())
	}
	newImage := "nginx:2.0"
	if _, err := service.UpdateApp(t.Context(), UpdateAppRequest{AppName: "missing", ExpectedGeneration: -1, Image: &newImage}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing application patch error = %v", err)
	}
}

func TestDesiredSpecRevalidatesPersistedMountPolicy(t *testing.T) {
	service, st, _, _ := newEdgeService(t)
	service.WithRuntimeLimits(20, []string{"/srv/moduleos"}, time.Minute)
	created := mustCreateApp(t, service, CreateAppRequest{
		Name: "mount-policy", Image: "nginx:1.27",
		Volumes: []swarm.VolumeConfig{{Source: "/srv/moduleos/data", Target: "/data"}},
	})

	created.Volumes = `[{"source":"/srv/outside","target":"/data","read_only":false}]`
	if err := st.UpdateApplication(t.Context(), created); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BuildDesiredServiceSpec(t.Context(), created); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("desired spec error = %v, want ErrInvalidData", err)
	}
	if _, err := service.DeployApp(t.Context(), DeployAppRequest{AppName: created.Name, Image: "nginx:2.0"}); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("deploy with persisted unsafe mount error = %v, want ErrInvalidData", err)
	}
	persisted, err := st.GetApplication(t.Context(), created.Name)
	if err != nil {
		t.Fatal(err)
	}
	deployments, err := st.ListDeployments(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Image != created.Image || persisted.DesiredGeneration != created.DesiredGeneration || len(deployments) != 0 {
		t.Fatalf("rejected desired spec partially persisted: app=%#v deployments=%#v", persisted, deployments)
	}
}

func TestRunStateScaleAndDeletionIntentSemantics(t *testing.T) {
	service, st, _, queue := newEdgeService(t)
	created := mustCreateApp(t, service, CreateAppRequest{Name: "lifecycle-app", Image: "nginx:1.27", Replicas: 2})
	queue.reset()

	stopped, err := service.SetRunState(t.Context(), created.Name, store.DesiredRunStateStopped, created.DesiredGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Replicas != 0 || stopped.ResumeReplicas != 2 || len(queue.snapshot()) != 1 {
		t.Fatalf("stop intent = %#v queue=%#v", stopped, queue.snapshot())
	}
	replayedStop, err := service.SetRunState(t.Context(), created.Name, store.DesiredRunStateStopped, created.DesiredGeneration)
	if !errors.Is(err, store.ErrGenerationConflict) || replayedStop != nil || len(queue.snapshot()) != 1 {
		t.Fatalf("stale stop replay = %#v err=%v queue=%#v", replayedStop, err, queue.snapshot())
	}
	replayedStop, err = service.SetRunState(t.Context(), created.Name, store.DesiredRunStateStopped, stopped.DesiredGeneration)
	if err != nil || replayedStop.DesiredGeneration != stopped.DesiredGeneration || len(queue.snapshot()) != 1 {
		t.Fatalf("current-generation stop replay = %#v err=%v queue=%#v", replayedStop, err, queue.snapshot())
	}

	running, err := service.SetRunState(t.Context(), created.Name, store.DesiredRunStateRunning, stopped.DesiredGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if running.Replicas != 2 || running.DesiredGeneration != stopped.DesiredGeneration+1 {
		t.Fatalf("start intent = %#v", running)
	}
	scaled, err := service.ScaleAppIntent(t.Context(), created.Name, 3, running.DesiredGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if scaled.Replicas != 3 || scaled.DesiredGeneration != running.DesiredGeneration+1 {
		t.Fatalf("scale intent = %#v", scaled)
	}
	queueAfterScale := len(queue.snapshot())
	replayedScale, err := service.ScaleAppIntent(t.Context(), created.Name, 3, running.DesiredGeneration)
	if !errors.Is(err, store.ErrGenerationConflict) || replayedScale != nil || len(queue.snapshot()) != queueAfterScale {
		t.Fatalf("stale scale replay = %#v err=%v queue=%#v", replayedScale, err, queue.snapshot())
	}
	replayedScale, err = service.ScaleAppIntent(t.Context(), created.Name, 3, scaled.DesiredGeneration)
	if err != nil || replayedScale.DesiredGeneration != scaled.DesiredGeneration || len(queue.snapshot()) != queueAfterScale {
		t.Fatalf("current-generation scale replay = %#v err=%v queue=%#v", replayedScale, err, queue.snapshot())
	}
	if _, err := service.ScaleAppIntent(t.Context(), created.Name, -1, -1); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("negative scale error = %v", err)
	}
	if _, err := service.ScaleAppIntent(t.Context(), created.Name, service.maxReplicas+1, -1); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("oversized scale error = %v", err)
	}
	if _, err := service.SetRunState(t.Context(), created.Name, store.DesiredRunState("paused"), scaled.DesiredGeneration); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("invalid run state error = %v", err)
	}

	deleting, err := service.DeleteAppIntent(t.Context(), created.Name, scaled.DesiredGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if deleting.DeletionTimestamp == nil || deleting.DesiredGeneration != scaled.DesiredGeneration+1 {
		t.Fatalf("deletion intent = %#v", deleting)
	}
	queue.reset()
	staleDelete, err := service.DeleteAppIntent(t.Context(), created.Name, scaled.DesiredGeneration)
	if !errors.Is(err, store.ErrGenerationConflict) || staleDelete != nil || len(queue.snapshot()) != 0 {
		t.Fatalf("stale delete replay = %#v err=%v queue=%#v", staleDelete, err, queue.snapshot())
	}
	replayedDelete, err := service.DeleteAppIntent(t.Context(), created.Name, deleting.DesiredGeneration)
	if err != nil || replayedDelete.DesiredGeneration != deleting.DesiredGeneration || replayedDelete.DeletionTimestamp == nil ||
		!replayedDelete.DeletionTimestamp.Equal(*deleting.DeletionTimestamp) || len(queue.snapshot()) != 0 {
		t.Fatalf("current-generation delete replay = %#v err=%v queue=%#v", replayedDelete, err, queue.snapshot())
	}
	persisted, err := st.GetApplication(t.Context(), created.Name)
	if err != nil || persisted.DeletionTimestamp == nil {
		t.Fatalf("persisted deletion = %#v err=%v", persisted, err)
	}
}

func TestRollbackValidationAndSuccess(t *testing.T) {
	service, st, _, queue := newEdgeService(t)
	application := mustCreateApp(t, service, CreateAppRequest{Name: "rollback-app", Image: "nginx:1.0"})
	target, err := service.DeployApp(t.Context(), DeployAppRequest{AppName: application.Name, Image: "nginx:2.0"})
	if err != nil {
		t.Fatal(err)
	}
	resolvedTarget := "docker.io/library/nginx:2.0@sha256:" + strings.Repeat("a", 64)
	if err := st.MarkDeploymentSucceeded(t.Context(), target.ID, target.TargetGeneration, resolvedTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeployApp(t.Context(), DeployAppRequest{AppName: application.Name, Image: "nginx:3.0"}); err != nil {
		t.Fatal(err)
	}
	queue.reset()

	rollback, err := service.RollbackApp(t.Context(), application.Name, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rollback.Image != resolvedTarget || rollback.RollbackSourceDeploymentID == nil || *rollback.RollbackSourceDeploymentID != target.ID ||
		rollback.Status != store.DeploymentStatusPending || len(queue.snapshot()) != 1 {
		t.Fatalf("rollback intent = %#v queue=%#v", rollback, queue.snapshot())
	}

	pending := &store.Deployment{
		ID: uuid.NewString(), AppID: application.ID, SourceType: store.SourceTypeImage,
		Image: "nginx:pending", Status: store.DeploymentStatusPending,
		TriggeredBy: store.TriggeredByAPI, CreatedAt: time.Now().UTC(), TargetGeneration: rollback.TargetGeneration,
	}
	if err := st.CreateDeployment(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RollbackApp(t.Context(), application.Name, pending.ID); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("pending rollback error = %v", err)
	}
	emptyImage := &store.Deployment{
		ID: uuid.NewString(), AppID: application.ID, SourceType: store.SourceTypeImage,
		Status: store.DeploymentStatusSucceeded, TriggeredBy: store.TriggeredByAPI,
		CreatedAt: time.Now().UTC(), TargetGeneration: rollback.TargetGeneration,
	}
	if err := st.CreateDeployment(t.Context(), emptyImage); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RollbackApp(t.Context(), application.Name, emptyImage.ID); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("unresolved-image rollback error = %v", err)
	}

	other := mustCreateApp(t, service, CreateAppRequest{Name: "other-app", Image: "redis:7"})
	if _, err := service.RollbackApp(t.Context(), other.Name, target.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-application rollback error = %v", err)
	}
	if _, err := service.RollbackApp(t.Context(), application.Name, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing deployment rollback error = %v", err)
	}
	if _, err := service.RollbackApp(t.Context(), "missing", target.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing application rollback error = %v", err)
	}
}

func TestProjectLifecycleQueriesAndDeletionGuards(t *testing.T) {
	service, st, _, _ := newEdgeService(t)
	project, err := service.CreateProject(t.Context(), CreateProjectRequest{Name: "  Billing__API  "})
	if err != nil {
		t.Fatal(err)
	}
	if project.Name != "Billing__API" || project.Slug != "billing-api" || project.Network != "moduleos-billing-api-net" {
		t.Fatalf("derived project identity = %#v", project)
	}
	bySlug, err := service.GetProject(t.Context(), project.Slug)
	if err != nil || bySlug.ID != project.ID {
		t.Fatalf("GetProject = %#v, %v", bySlug, err)
	}
	byID, err := service.GetProjectByID(t.Context(), project.ID)
	if err != nil || byID.Slug != project.Slug {
		t.Fatalf("GetProjectByID = %#v, %v", byID, err)
	}
	projects, err := service.ListProjects(t.Context())
	if err != nil || len(projects) != 2 {
		t.Fatalf("ListProjects count=%d err=%v", len(projects), err)
	}
	application := mustCreateApp(t, service, CreateAppRequest{Name: "billing", ProjectSlug: project.Slug, Image: "nginx:1.27"})
	apps, err := service.ListProjectApps(t.Context(), project.Slug)
	if err != nil || len(apps) != 1 || apps[0].ID != application.ID {
		t.Fatalf("ListProjectApps = %#v, %v", apps, err)
	}
	if err := service.DeleteProject(t.Context(), project.Slug); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("project with application deletion error = %v", err)
	}
	if err := st.DeleteApplication(t.Context(), application.Name); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteProject(t.Context(), project.Slug); err != nil {
		t.Fatal(err)
	}
	deleting, err := st.GetProject(t.Context(), project.Slug)
	if err != nil || deleting.DeletionTimestamp == nil {
		t.Fatalf("project deletion intent = %#v, %v", deleting, err)
	}
	if _, err := service.CreateApp(t.Context(), CreateAppRequest{
		Name: "late-app", ProjectSlug: project.Slug, Image: "nginx:1.27",
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("application creation in deleting project error = %v", err)
	}

	invalid := []CreateProjectRequest{
		{Name: "", Slug: "explicit"},
		{Name: "Root", Slug: "root"},
		{Name: "Bad", Slug: "Bad_Slug"},
		{Name: "Long", Slug: strings.Repeat("a", 49)},
	}
	for _, request := range invalid {
		if _, err := service.CreateProject(t.Context(), request); err == nil {
			t.Fatalf("CreateProject(%#v) unexpectedly succeeded", request)
		}
	}
	if _, err := service.GetProject(t.Context(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing project error = %v", err)
	}
	if _, err := service.GetProjectByID(t.Context(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing project by ID error = %v", err)
	}
	if _, err := service.ListProjectApps(t.Context(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing project applications error = %v", err)
	}
	if err := service.DeleteProject(t.Context(), "root"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("root deletion error = %v", err)
	}
}

func TestProjectLinkLifecycleDetailsOwnershipAndQueue(t *testing.T) {
	service, st, _, queue := newEdgeService(t)
	targetProject := mustCreateProject(t, service, "Target", "target")
	targetApp := mustCreateApp(t, service, CreateAppRequest{Name: "database", ProjectSlug: targetProject.Slug, Image: "postgres:17"})
	sourceProject := mustCreateProject(t, service, "Source", "source")
	otherProject := mustCreateProject(t, service, "Other", "other")
	queue.reset()

	link, err := service.CreateProjectLink(t.Context(), sourceProject.Slug, targetApp.Name, "")
	if err != nil {
		t.Fatal(err)
	}
	if link.Alias != "target.database" || link.SourceProjectSlug != sourceProject.Slug ||
		link.TargetAppName != targetApp.Name || link.TargetProjectSlug != targetProject.Slug {
		t.Fatalf("link details = %#v", link)
	}
	if names := queue.snapshot(); len(names) != 1 || names[0] != targetApp.Name {
		t.Fatalf("create link queue = %#v", names)
	}
	details, err := service.ListProjectLinks(t.Context(), sourceProject.Slug)
	if err != nil || len(details) != 1 || details[0].ID != link.ID || details[0].TargetProjectSlug != targetProject.Slug {
		t.Fatalf("listed link details = %#v, %v", details, err)
	}
	if _, err := service.DeleteAppIntent(t.Context(), targetApp.Name, targetApp.DesiredGeneration); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("application deletion with active link error = %v", err)
	}
	if err := service.DeleteProjectLink(t.Context(), otherProject.Slug, link.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong-owner link deletion error = %v", err)
	}
	if _, err := st.GetProjectLink(t.Context(), link.ID); err != nil {
		t.Fatal("wrong-owner deletion removed the link")
	}
	if err := service.DeleteProjectLink(t.Context(), sourceProject.Slug, link.ID); err != nil {
		t.Fatal(err)
	}
	if names := queue.snapshot(); len(names) != 2 || names[1] != targetApp.Name {
		t.Fatalf("delete link queue = %#v", names)
	}
	if _, err := st.GetProjectLink(t.Context(), link.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted link lookup error = %v", err)
	}
	if err := service.DeleteProjectLink(t.Context(), sourceProject.Slug, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing link deletion error = %v", err)
	}
	deletingApp, err := service.DeleteAppIntent(t.Context(), targetApp.Name, targetApp.DesiredGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateProjectLink(t.Context(), sourceProject.Slug, deletingApp.Name, "target.database"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("link to deleting application error = %v", err)
	}
}

func TestProjectLinkValidationConflictsAndLimit(t *testing.T) {
	service, st, mock, _ := newEdgeService(t)
	target := mustCreateProject(t, service, "Target", "target")
	mustCreateApp(t, service, CreateAppRequest{Name: "target-app", ProjectSlug: target.Slug, Image: "nginx:1.27"})
	source := mustCreateProject(t, service, "Source", "source")
	mustCreateApp(t, service, CreateAppRequest{Name: "local-app", ProjectSlug: source.Slug, Image: "nginx:1.27"})

	tests := []struct {
		name        string
		projectSlug string
		targetApp   string
		alias       string
		want        error
	}{
		{name: "missing project", projectSlug: "missing", targetApp: "target-app", alias: "target.app", want: store.ErrNotFound},
		{name: "missing application", projectSlug: source.Slug, targetApp: "missing", alias: "target.app", want: store.ErrNotFound},
		{name: "same project", projectSlug: target.Slug, targetApp: "target-app", alias: "target.app", want: store.ErrConflict},
		{name: "invalid alias", projectSlug: source.Slug, targetApp: "target-app", alias: "Bad_Alias", want: ErrValidation},
		{name: "local name conflict", projectSlug: source.Slug, targetApp: "target-app", alias: "local-app", want: store.ErrConflict},
		{name: "qualified local conflict", projectSlug: source.Slug, targetApp: "target-app", alias: "source.local-app", want: store.ErrConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.CreateProjectLink(t.Context(), test.projectSlug, test.targetApp, test.alias); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	limited := NewService(&projectLinkLimitStore{Store: st, links: maxProjectLinks}, mock, "moduleos.local", service.log)
	if _, err := limited.CreateProjectLink(t.Context(), source.Slug, "target-app", "target.app"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("link limit error = %v", err)
	}
	if _, err := service.ListProjectLinks(t.Context(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing project link listing error = %v", err)
	}
}

func TestProjectDeletionRejectsActiveLinks(t *testing.T) {
	service, _, _, _ := newEdgeService(t)
	target := mustCreateProject(t, service, "Target", "target")
	mustCreateApp(t, service, CreateAppRequest{Name: "target-app", ProjectSlug: target.Slug, Image: "nginx:1.27"})
	source := mustCreateProject(t, service, "Source", "source")
	if _, err := service.CreateProjectLink(t.Context(), source.Slug, "target-app", "target.app"); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteProject(t.Context(), source.Slug); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("active-link project deletion error = %v", err)
	}
}

func TestProjectLinkRejectsDeletingProjects(t *testing.T) {
	service, st, _, _ := newEdgeService(t)
	target := mustCreateProject(t, service, "Target", "target")
	targetApp := mustCreateApp(t, service, CreateAppRequest{Name: "target-app", ProjectSlug: target.Slug, Image: "nginx:1.27"})
	source := mustCreateProject(t, service, "Source", "source")
	if err := service.DeleteProject(t.Context(), source.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateProjectLink(t.Context(), source.Slug, targetApp.Name, "target.app"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("link from deleting source project error = %v", err)
	}

	activeSource := mustCreateProject(t, service, "Active Source", "active-source")
	if _, err := st.CreateProjectDeletionIntent(t.Context(), target.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateProjectLink(t.Context(), activeSource.Slug, targetApp.Name, "target.app"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("link to deleting target project error = %v", err)
	}
}

func TestApplicationLogsRequireRuntimeOwnership(t *testing.T) {
	service, st, mock, _ := newEdgeService(t)
	application := mustCreateApp(t, service, CreateAppRequest{Name: "logs-app", Image: "nginx:1.27"})
	serviceName := swarm.ServiceName(application.Name)
	mock.Services[serviceName] = &swarm.ServiceInfo{
		ID: "service-id", Name: serviceName,
		Labels: map[string]string{swarm.LabelManagedBy: "true", swarm.LabelAppID: application.ID},
	}
	stream, err := service.GetApplicationLogs(t.Context(), application, "100", false)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(stream)
	closeErr := stream.Close()
	if err != nil || closeErr != nil || !strings.Contains(string(payload), "mock log line") {
		t.Fatalf("log payload=%q readErr=%v closeErr=%v", payload, err, closeErr)
	}

	mock.Services[serviceName].Labels[swarm.LabelAppID] = uuid.NewString()
	if _, err := service.GetApplicationLogs(t.Context(), application, "100", false); !errors.Is(err, swarm.ErrOwnershipConflict) {
		t.Fatalf("foreign service log error = %v", err)
	}
	delete(mock.Services, serviceName)
	if _, err := service.GetApplicationLogs(t.Context(), application, "100", false); err == nil {
		t.Fatal("missing runtime service unexpectedly returned logs")
	}

	logErr := errors.New("log backend unavailable")
	faultClient := &logErrorClient{Client: mock, err: logErr}
	faultService := NewService(st, faultClient, "moduleos.local", service.log)
	if _, err := faultService.GetLogs(t.Context(), "service-id", "all", true); !errors.Is(err, logErr) {
		t.Fatalf("log backend error = %v", err)
	}
}

func TestUpdateStatusAndDeploymentListing(t *testing.T) {
	service, st, _, _ := newEdgeService(t)
	application := mustCreateApp(t, service, CreateAppRequest{Name: "status-app", Image: "nginx:1.27"})
	if err := service.UpdateAppStatus(t.Context(), application.Name, store.AppStatusRunning); err != nil {
		t.Fatal(err)
	}
	persisted, err := st.GetApplication(t.Context(), application.Name)
	if err != nil || persisted.Status != store.AppStatusRunning {
		t.Fatalf("persisted status = %#v, %v", persisted, err)
	}
	if err := service.UpdateAppStatus(t.Context(), "missing", store.AppStatusFailed); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing status update error = %v", err)
	}
	if _, err := service.ListDeployments(t.Context(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing deployment history error = %v", err)
	}
	if _, err := service.DeployApp(t.Context(), DeployAppRequest{AppName: application.Name, Image: "nginx:2.0"}); err != nil {
		t.Fatal(err)
	}
	deployments, err := service.ListDeployments(t.Context(), application.Name)
	if err != nil || len(deployments) != 1 {
		t.Fatalf("deployments = %#v, %v", deployments, err)
	}
}

func TestCancelledMutationsPersistNothing(t *testing.T) {
	service, st, _, queue := newEdgeService(t)
	application := mustCreateApp(t, service, CreateAppRequest{Name: "cancel-app", Image: "nginx:1.27"})
	queue.reset()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	newImage := "nginx:2.0"
	if _, err := service.UpdateApp(cancelled, UpdateAppRequest{AppName: application.Name, ExpectedGeneration: -1, Image: &newImage}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled update error = %v", err)
	}
	if _, err := service.DeployApp(cancelled, DeployAppRequest{AppName: application.Name, Image: newImage}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled deploy error = %v", err)
	}
	persisted, err := st.GetApplication(t.Context(), application.Name)
	if err != nil || persisted.Image != application.Image || persisted.DesiredGeneration != application.DesiredGeneration || len(queue.snapshot()) != 0 {
		t.Fatalf("cancelled mutation changed state: app=%#v err=%v queue=%#v", persisted, err, queue.snapshot())
	}
}

func TestBuildDesiredSpecIncludesCanonicalNetworksAndRouting(t *testing.T) {
	service, st, _, _ := newEdgeService(t)
	service.WithRuntimeLimits(5, []string{"/srv/moduleos"}, time.Minute).WithIngressNetwork("edge-ingress")

	targetProject := mustCreateProject(t, service, "Target", "target")
	sourceProject := mustCreateProject(t, service, "Source", "source")
	created := mustCreateApp(t, service, CreateAppRequest{
		Name:                 "api",
		ProjectSlug:          targetProject.Slug,
		Image:                "nginx:1.27",
		Replicas:             2,
		EnvVars:              map[string]string{"B": "2", "A": "1"},
		Ports:                []swarm.PortConfig{{ContainerPort: 8080, PublishedPort: 18080}},
		Volumes:              []swarm.VolumeConfig{{Source: "/srv/moduleos/api", Target: "/data", ReadOnly: true}},
		Expose:               true,
		IngressContainerPort: 8080,
	})
	if _, err := service.CreateProjectLink(t.Context(), sourceProject.Slug, created.Name, "shared.api"); err != nil {
		t.Fatal(err)
	}

	persisted, err := st.GetApplication(t.Context(), created.Name)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := service.BuildDesiredServiceSpec(t.Context(), persisted)
	if err != nil {
		t.Fatal(err)
	}

	if spec.Name != created.Name || spec.Image != created.Image || spec.Replicas != 2 {
		t.Fatalf("identity or scale mismatch: %#v", spec)
	}
	if len(spec.EnvVars) != 2 || spec.EnvVars[0] != "A=1" || spec.EnvVars[1] != "B=2" {
		t.Fatalf("environment is not canonical: %#v", spec.EnvVars)
	}
	if len(spec.Ports) != 1 || spec.Ports[0].ContainerPort != 8080 || spec.Ports[0].PublishedPort != 18080 {
		t.Fatalf("ports mismatch: %#v", spec.Ports)
	}
	if len(spec.Volumes) != 1 || spec.Volumes[0].Source != "/srv/moduleos/api" ||
		spec.Volumes[0].Target != "/data" || !spec.Volumes[0].ReadOnly {
		t.Fatalf("volumes mismatch: %#v", spec.Volumes)
	}

	wantLabels := map[string]string{
		swarm.LabelManagedBy:    "true",
		swarm.LabelAppName:      created.Name,
		swarm.LabelAppID:        created.ID,
		swarm.LabelProjectID:    targetProject.ID,
		swarm.LabelProject:      targetProject.Slug,
		swarm.LabelGeneration:   "1",
		"traefik.enable":        "true",
		"traefik.swarm.network": "edge-ingress",
	}
	for key, want := range wantLabels {
		if got := spec.Labels[key]; got != want {
			t.Errorf("label %q = %q, want %q", key, got, want)
		}
	}
	hasRouterRule := false
	hasServicePort := false
	for key, value := range spec.Labels {
		if strings.HasSuffix(key, ".rule") && value == "Host(`api.moduleos.local`)" {
			hasRouterRule = true
		}
		if strings.HasSuffix(key, ".loadbalancer.server.port") && value == "8080" {
			hasServicePort = true
		}
	}
	if !hasRouterRule || !hasServicePort {
		t.Fatalf("routing labels incomplete: %#v", spec.Labels)
	}

	wantNetworks := map[string][]string{
		targetProject.Network: {"api", "target.api"},
		sourceProject.Network: {"shared.api"},
		"edge-ingress":        nil,
	}
	if len(spec.Networks) != len(wantNetworks) {
		t.Fatalf("networks = %#v", spec.Networks)
	}
	for _, network := range spec.Networks {
		wantAliases, ok := wantNetworks[network.Network]
		if !ok {
			t.Errorf("unexpected network: %#v", network)
			continue
		}
		if strings.Join(network.Aliases, ",") != strings.Join(wantAliases, ",") {
			t.Errorf("aliases for %q = %#v, want %#v", network.Network, network.Aliases, wantAliases)
		}
		delete(wantNetworks, network.Network)
	}
	if len(wantNetworks) != 0 {
		t.Fatalf("missing networks: %#v", wantNetworks)
	}
}

func TestValidationAndEncodingHelpers(t *testing.T) {
	if got := slugify("  My__Project---API  "); got != "my-project-api" {
		t.Fatalf("slugify = %q", got)
	}
	for _, slug := range []string{"", "Upper", strings.Repeat("a", 49), "bad--slug", "-bad"} {
		if err := validateSlug(slug); !errors.Is(err, ErrValidation) {
			t.Fatalf("validateSlug(%q) = %v", slug, err)
		}
	}
	for _, name := range []string{"", "-bad", "bad-", "Bad", strings.Repeat("a", 64)} {
		if err := validateName(name); !errors.Is(err, store.ErrInvalidData) {
			t.Fatalf("validateName(%q) = %v", name, err)
		}
	}

	ports := []swarm.PortConfig{{ContainerPort: 8080, PublishedPort: 18080}, {ContainerPort: 80, PublishedPort: 10080}}
	encodedPorts := encodePorts(ports)
	decodedPorts, err := decodePorts(encodedPorts)
	if err != nil || len(decodedPorts) != 2 || decodedPorts[0].ContainerPort != 80 || decodedPorts[0].Protocol != "tcp" || decodedPorts[0].PublishMode != "ingress" ||
		ports[0].ContainerPort != 8080 || ports[0].Protocol != "" || ports[0].PublishMode != "" {
		t.Fatalf("ports round trip=%#v err=%v original=%#v", decodedPorts, err, ports)
	}
	if _, err := decodePorts("not-json"); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("invalid ports error = %v", err)
	}
	volumes := []swarm.VolumeConfig{{Source: "/srv/z", Target: "/z"}, {Source: "/srv/a", Target: "/a"}}
	encodedVolumes := encodeVolumes(volumes)
	decodedVolumes, err := decodeVolumes(encodedVolumes)
	if err != nil || len(decodedVolumes) != 2 || decodedVolumes[0].Target != "/a" || volumes[0].Target != "/z" {
		t.Fatalf("volumes round trip=%#v err=%v original=%#v", decodedVolumes, err, volumes)
	}
	if _, err := decodeVolumes("not-json"); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("invalid volumes error = %v", err)
	}
	if decoded := decodeEnvVars("B=2\nA=1"); len(decoded) != 2 || decoded[0] != "B=2" {
		t.Fatalf("legacy environment decode = %#v", decoded)
	}
	if decoded := decodeEnvVars(`{"B":"2","A":"1"}`); len(decoded) != 2 || decoded[0] != "A=1" || decoded[1] != "B=2" {
		t.Fatalf("JSON environment decode = %#v", decoded)
	}
}

func TestMutationPreviewAndConfigEquality(t *testing.T) {
	application := &store.Application{Image: "old", EnvVars: "{}", Ports: "[]", Volumes: "[]"}
	image := "new"
	environment := `{"A":"1"}`
	ports := `[{"container_port":80}]`
	volumes := `[{"source":"/srv/data","target":"/data"}]`
	expose := true
	ingressPort := uint32(80)
	copy := *application
	applyMutationPreview(&copy, store.ApplicationMutation{
		Image: &image, EnvVars: &environment, Ports: &ports, Volumes: &volumes,
		Expose: &expose, IngressContainerPort: &ingressPort,
	})
	if copy.Image != image || copy.EnvVars != environment || copy.Ports != ports || copy.Volumes != volumes ||
		!copy.Expose || copy.IngressContainerPort != ingressPort || applicationConfigEqual(application, &copy) {
		t.Fatalf("mutation preview = %#v", copy)
	}
	if !applicationConfigEqual(&copy, &copy) {
		t.Fatal("identical application configs were not equal")
	}
}

func TestValidateAliasBoundaries(t *testing.T) {
	valid253 := strings.Join([]string{strings.Repeat("a", 63), strings.Repeat("b", 63), strings.Repeat("c", 63), strings.Repeat("d", 61)}, ".")
	if len(valid253) != 253 {
		t.Fatalf("test alias length = %d", len(valid253))
	}
	if err := ValidateAlias(valid253); err != nil {
		t.Fatalf("253-byte alias rejected: %v", err)
	}
	if err := ValidateAlias(valid253 + "a"); !errors.Is(err, ErrValidation) {
		t.Fatalf("254-byte alias error = %v", err)
	}
}
