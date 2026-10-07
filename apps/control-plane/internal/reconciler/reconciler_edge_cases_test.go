package reconciler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/testkit/swarmfake"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/watcher"
)

func newInternalReconciler(t *testing.T) (*Reconciler, *app.Service, *store.SQLiteStore, *swarmfake.Client) {
	t.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "reconciler.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mock := swarmfake.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := app.NewService(st, mock, "moduleos.local", logger)
	reconciler := New(mock, service, logger).WithStabilizationWindow(0)
	return reconciler, service, st, mock
}

func mustCreateInternalApp(t *testing.T, service *app.Service, request app.CreateAppRequest) *store.Application {
	t.Helper()
	created, err := service.CreateApp(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func mustCreateInternalProject(t *testing.T, service *app.Service, name, slug string) *store.Project {
	t.Helper()
	project, err := service.CreateProject(t.Context(), app.CreateProjectRequest{Name: name, Slug: slug})
	if err != nil {
		t.Fatal(err)
	}
	return project
}

type faultStore struct {
	store.Store
	listProjectsErr    error
	listDeploymentsErr error
	diagnosticsErr     error
	finalizeAppErr     error
}

func (s *faultStore) FinalizeApplicationDeletion(ctx context.Context, appID string, generation int64) error {
	if s.finalizeAppErr != nil {
		return s.finalizeAppErr
	}
	return s.Store.FinalizeApplicationDeletion(ctx, appID, generation)
}

func (s *faultStore) ListProjects(ctx context.Context) ([]*store.Project, error) {
	if s.listProjectsErr != nil {
		return nil, s.listProjectsErr
	}
	return s.Store.ListProjects(ctx)
}

func (s *faultStore) ListDeployments(ctx context.Context, appID string) ([]*store.Deployment, error) {
	if s.listDeploymentsErr != nil {
		return nil, s.listDeploymentsErr
	}
	return s.Store.ListDeployments(ctx, appID)
}

func (s *faultStore) PersistReconcileDiagnostics(ctx context.Context, appID string, generation int64, code, message string, retryable bool, attempt int) error {
	if s.diagnosticsErr != nil {
		return s.diagnosticsErr
	}
	return s.Store.PersistReconcileDiagnostics(ctx, appID, generation, code, message, retryable, attempt)
}

type panicCreateClient struct{ *swarmfake.Client }

func (c *panicCreateClient) CreateService(context.Context, swarm.ServiceSpec) error {
	panic("synthetic create panic")
}

type controlledEventClient struct {
	*swarmfake.Client
	events chan swarm.SwarmEvent
	errs   chan error
}

func (c *controlledEventClient) WatchEvents(context.Context) (<-chan swarm.SwarmEvent, <-chan error) {
	return c.events, c.errs
}

type orphanListFailureClient struct {
	swarm.Client
	serviceErr error
	networkErr error
}

type projectNetworkFaultClient struct {
	swarm.Client
	getErr    error
	removeErr error
}

func (c *projectNetworkFaultClient) GetNetwork(ctx context.Context, name string) (*swarm.NetworkInfo, error) {
	if c.getErr != nil {
		return nil, c.getErr
	}
	return c.Client.GetNetwork(ctx, name)
}

func (c *projectNetworkFaultClient) RemoveNetwork(ctx context.Context, name string) error {
	if c.removeErr != nil {
		return c.removeErr
	}
	return c.Client.RemoveNetwork(ctx, name)
}

func (c *orphanListFailureClient) ListServices(ctx context.Context) ([]swarm.ServiceInfo, error) {
	if c.serviceErr != nil {
		return nil, c.serviceErr
	}
	return c.Client.ListServices(ctx)
}

func (c *orphanListFailureClient) ListNetworks(ctx context.Context) ([]swarm.NetworkInfo, error) {
	if c.networkErr != nil {
		return nil, c.networkErr
	}
	return c.Client.ListNetworks(ctx)
}

type linkSnapshotRaceStore struct {
	store.Store
	base       *store.SQLiteStore
	mu         sync.Mutex
	calls      int
	injectLink *store.ProjectLink
}

func (s *linkSnapshotRaceStore) ListProjectLinksByApp(ctx context.Context, appID string) ([]*store.ProjectLink, error) {
	links, err := s.Store.ListProjectLinksByApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 2 {
		if err := s.base.CreateProjectLink(ctx, s.injectLink); err != nil {
			return nil, err
		}
	}
	return links, nil
}

func TestConfigurationQueueAndLockBoundaries(t *testing.T) {
	reconciler, _, _, _ := newInternalReconciler(t)
	reconciler.log = nil
	reconciler = New(reconciler.swarm, reconciler.appSvc, nil)
	if reconciler.log == nil {
		t.Fatal("nil logger was not replaced")
	}

	interval := reconciler.interval
	workers := reconciler.workers
	maximum := reconciler.maxBackoff
	reconciler.WithInterval(0).WithWorkers(0).WithMaxBackoff(0).WithStabilizationWindow(-1)
	if reconciler.interval != interval || reconciler.workers != workers || reconciler.maxBackoff != maximum || reconciler.stabilization != 0 {
		t.Fatal("invalid configuration changed reconciler defaults")
	}
	reconciler.WithInterval(time.Second).WithWorkers(2).WithMaxBackoff(2 * time.Second).WithStabilizationWindow(time.Second)
	if reconciler.interval != time.Second || reconciler.workers != 2 || reconciler.maxBackoff != 2*time.Second || reconciler.stabilization != time.Second {
		t.Fatal("valid configuration was not applied")
	}

	reconciler.Enqueue("")
	reconciler.Enqueue("api")
	reconciler.Enqueue("api")
	if len(reconciler.queue) != 1 || len(reconciler.pending) != 1 {
		t.Fatalf("queue was not coalesced: queued=%d pending=%d", len(reconciler.queue), len(reconciler.pending))
	}
	reconciler.stopping.Store(true)
	reconciler.Enqueue("ignored")
	if len(reconciler.queue) != 1 {
		t.Fatal("enqueue accepted work while stopping")
	}
	if reconciler.applicationLock("api") != reconciler.applicationLock("api") {
		t.Fatal("application lock is not stable")
	}

	bounded := New(reconciler.swarm, reconciler.appSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for index := 0; index < cap(bounded.queue); index++ {
		bounded.Enqueue("queued-" + strconv.Itoa(index))
	}
	bounded.Enqueue("overflow")
	if len(bounded.queue) != cap(bounded.queue) {
		t.Fatalf("queue length = %d, capacity = %d", len(bounded.queue), cap(bounded.queue))
	}
	if _, exists := bounded.pending["overflow"]; exists {
		t.Fatal("dropped queue item remained marked pending")
	}
}

func TestRetrySchedulingIsBoundedAndCancellable(t *testing.T) {
	reconciler, _, _, _ := newInternalReconciler(t)
	reconciler.WithMaxBackoff(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reconciler.scheduleRetry(ctx, "api")
	reconciler.scheduleRetry(ctx, "api")
	reconciler.attemptMu.Lock()
	attempts := reconciler.attempts["api"]
	scheduled := len(reconciler.retrying)
	reconciler.attemptMu.Unlock()
	if attempts != 2 || scheduled != 1 {
		t.Fatalf("retry state: attempts=%d scheduled=%d", attempts, scheduled)
	}
	reconciler.resetBackoff("api")
	reconciler.attemptMu.Lock()
	if len(reconciler.attempts) != 0 || len(reconciler.retrying) != 0 {
		t.Fatalf("retry state survived reset: attempts=%#v retrying=%#v", reconciler.attempts, reconciler.retrying)
	}
	reconciler.attemptMu.Unlock()

	reconciler.scheduleRetry(ctx, "one")
	reconciler.scheduleRetry(ctx, "two")
	reconciler.cancelRetries()
	reconciler.attemptMu.Lock()
	defer reconciler.attemptMu.Unlock()
	if len(reconciler.attempts) != 0 || len(reconciler.retrying) != 0 {
		t.Fatal("shutdown did not clear retry state")
	}
}

func TestReconcileSafelyConvertsPanicToRetryableError(t *testing.T) {
	_, _, st, mock := newInternalReconciler(t)
	client := &panicCreateClient{Client: mock}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := app.NewService(st, client, "moduleos.local", logger)
	created := mustCreateInternalApp(t, service, app.CreateAppRequest{Name: "panic-app", Image: "nginx:1.27"})
	reconciler := New(client, service, logger).WithStabilizationWindow(0)
	err := reconciler.reconcileSafely(t.Context(), created.Name)
	if err == nil || !isRetryable(err) || !strings.Contains(err.Error(), "synthetic create panic") {
		t.Fatalf("panic classification = %v", err)
	}
}

func TestFullScanContinuesWhenProjectListingFails(t *testing.T) {
	_, _, st, mock := newInternalReconciler(t)
	failure := errors.New("project listing unavailable")
	wrapped := &faultStore{Store: st, listProjectsErr: failure}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := app.NewService(wrapped, mock, "moduleos.local", logger)
	created := mustCreateInternalApp(t, service, app.CreateAppRequest{Name: "independent-scan", Image: "nginx:1.27"})
	reconciler := New(mock, service, logger).WithStabilizationWindow(0)
	reconciler.Reconcile(t.Context())
	if _, err := mock.GetService(t.Context(), swarm.ServiceName(created.Name)); err != nil {
		t.Fatalf("application scan was skipped after project-list failure: %v", err)
	}
}

func TestStoreFailuresPreventRuntimeMutation(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*faultStore, error)
	}{
		{name: "diagnostic clear", configure: func(s *faultStore, err error) { s.diagnosticsErr = err }},
		{name: "deployment read", configure: func(s *faultStore, err error) { s.listDeploymentsErr = err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, st, mock := newInternalReconciler(t)
			failure := errors.New("store unavailable")
			wrapped := &faultStore{Store: st}
			test.configure(wrapped, failure)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			service := app.NewService(wrapped, mock, "moduleos.local", logger)
			created := mustCreateInternalApp(t, service, app.CreateAppRequest{Name: "fail-closed", Image: "nginx:1.27"})
			reconciler := New(mock, service, logger).WithStabilizationWindow(0)
			err := reconciler.ReconcileApplication(t.Context(), created.Name)
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v, want wrapped store failure", err)
			}
			if mock.CreateCalls != 0 || len(mock.Services) != 0 {
				t.Fatal("runtime was mutated after persistence failure")
			}
		})
	}
}

func TestDeletionBypassesInvalidRuntimeSpecAndMissingIngress(t *testing.T) {
	reconciler, service, st, _ := newInternalReconciler(t)
	created := mustCreateInternalApp(t, service, app.CreateAppRequest{
		Name: "delete-invalid", Image: "nginx:1.27", Expose: true, IngressContainerPort: 80,
	})
	deleting, err := service.DeleteAppIntent(t.Context(), created.Name, created.DesiredGeneration)
	if err != nil {
		t.Fatal(err)
	}
	deleting.Volumes = `[{"source":"/etc","target":"/host-etc"}]`
	if err := st.UpdateApplication(t.Context(), deleting); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileApplication(t.Context(), created.Name); err != nil {
		t.Fatalf("deletion was blocked by irrelevant runtime validation: %v", err)
	}
	if _, err := st.GetApplication(t.Context(), created.Name); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("application was not finalized: %v", err)
	}
}

func TestDeletionFinalizationFailureIsRetryable(t *testing.T) {
	_, service, st, mock := newInternalReconciler(t)
	created := mustCreateInternalApp(t, service, app.CreateAppRequest{Name: "finalize-retry", Image: "nginx:1.27"})
	if _, err := service.DeleteAppIntent(t.Context(), created.Name, created.DesiredGeneration); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("finalization unavailable")
	wrapped := &faultStore{Store: st, finalizeAppErr: failure}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	wrappedService := app.NewService(wrapped, mock, "moduleos.local", logger)
	reconciler := New(mock, wrappedService, logger).WithStabilizationWindow(0)
	err := reconciler.ReconcileApplication(t.Context(), created.Name)
	if !errors.Is(err, failure) || !isRetryable(err) {
		t.Fatalf("finalization error = %v", err)
	}
	if _, err := st.GetApplication(t.Context(), created.Name); err != nil {
		t.Fatalf("failed finalization removed durable intent: %v", err)
	}
	wrapped.finalizeAppErr = nil
	if err := reconciler.ReconcileApplication(t.Context(), created.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetApplication(t.Context(), created.Name); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("retry did not finalize application: %v", err)
	}
}

func TestMissingIngressFailsBeforeServiceCreation(t *testing.T) {
	reconciler, service, st, mock := newInternalReconciler(t)
	created := mustCreateInternalApp(t, service, app.CreateAppRequest{
		Name: "missing-ingress", Image: "nginx:1.27", Expose: true, IngressContainerPort: 80,
	})
	err := reconciler.ReconcileApplication(t.Context(), created.Name)
	if err == nil || !isRetryable(err) {
		t.Fatalf("missing ingress error = %v", err)
	}
	persisted, getErr := st.GetApplication(t.Context(), created.Name)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.ReconcileErrorCode != "ingress_network_missing" || !persisted.ReconcileRetryable {
		t.Fatalf("ingress diagnostic = %#v", persisted)
	}
	if mock.CreateCalls != 0 {
		t.Fatal("service was created without the required ingress network")
	}
}

func TestApplicationReconcileProvisionsProjectNetworkOnDemand(t *testing.T) {
	reconciler, service, st, mock := newInternalReconciler(t)
	created := mustCreateInternalApp(t, service, app.CreateAppRequest{Name: "network-on-demand", Image: "nginx:1.27"})
	project, err := st.GetProjectByID(t.Context(), created.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mock.GetNetwork(t.Context(), project.Network); !errdefs.IsNotFound(err) {
		t.Fatalf("project network unexpectedly existed before reconciliation: %v", err)
	}

	if err := reconciler.ReconcileApplication(t.Context(), created.Name); err != nil {
		t.Fatalf("application reconcile waited for a full project scan: %v", err)
	}
	if _, err := mock.GetNetwork(t.Context(), project.Network); err != nil {
		t.Fatalf("project network was not provisioned on demand: %v", err)
	}
	if _, err := mock.GetService(t.Context(), swarm.ServiceName(created.Name)); err != nil {
		t.Fatalf("application service was not created after provisioning its network: %v", err)
	}
	persistedProject, err := st.GetProjectByID(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedProject.ObservedState != store.ObservedStateReady {
		t.Fatalf("project state = %q, want ready", persistedProject.ObservedState)
	}
}

func TestNetworkDisappearingAfterPreflightIsRetryable(t *testing.T) {
	reconciler, service, st, mock := newInternalReconciler(t)
	created := mustCreateInternalApp(t, service, app.CreateAppRequest{Name: "network-race", Image: "nginx:1.27"})
	mock.CreateError = errors.Join(errors.New("network disappeared before service creation"), errdefs.ErrNotFound)

	err := reconciler.ReconcileApplication(t.Context(), created.Name)
	if err == nil || !isRetryable(err) {
		t.Fatalf("create race error = %v, want retryable", err)
	}
	persisted, getErr := st.GetApplication(t.Context(), created.Name)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.ReconcileErrorCode != "docker_dependency_missing" || !persisted.ReconcileRetryable {
		t.Fatalf("create race diagnostic = %#v", persisted)
	}

	mock.CreateError = nil
	if err := reconciler.ReconcileApplication(t.Context(), created.Name); err != nil {
		t.Fatalf("direct retry did not recover without a full scan: %v", err)
	}
	if _, err := mock.GetService(t.Context(), swarm.ServiceName(created.Name)); err != nil {
		t.Fatalf("service missing after direct retry: %v", err)
	}
}

func TestStabilizationStateIsBoundedAndResetsOnFailure(t *testing.T) {
	reconciler, service, st, mock := newInternalReconciler(t)
	reconciler.WithStabilizationWindow(time.Hour)
	created := mustCreateInternalApp(t, service, app.CreateAppRequest{Name: "stable", Image: "nginx:1.27"})
	if err := reconciler.ReconcileApplication(t.Context(), created.Name); err == nil {
		t.Fatal("expected stabilization retry")
	}
	if len(reconciler.stableSince) != 1 || reconciler.stableSince[created.ID].generation != 1 {
		t.Fatalf("initial stability state = %#v", reconciler.stableSince)
	}
	current, err := st.GetApplication(t.Context(), created.Name)
	if err != nil {
		t.Fatal(err)
	}
	image := "nginx:1.28"
	if _, err := service.UpdateApp(t.Context(), app.UpdateAppRequest{AppName: created.Name, Image: &image, ExpectedGeneration: current.DesiredGeneration}); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileApplication(t.Context(), created.Name); err == nil {
		t.Fatal("expected new generation to stabilize")
	}
	if len(reconciler.stableSince) != 1 || reconciler.stableSince[created.ID].generation != 2 {
		t.Fatalf("stability state grew across generations: %#v", reconciler.stableSince)
	}
	runtimeService := mock.Services[swarm.ServiceName(created.Name)]
	runtimeService.Running = 0
	if err := reconciler.ReconcileApplication(t.Context(), created.Name); err == nil {
		t.Fatal("expected convergence failure")
	}
	if len(reconciler.stableSince) != 0 {
		t.Fatalf("failed convergence retained stability state: %#v", reconciler.stableSince)
	}
}

func TestLinkCreatedDuringObservationIsNotFalselyMarkedObserved(t *testing.T) {
	reconciler, service, st, mock := newInternalReconciler(t)
	target := mustCreateInternalProject(t, service, "Target", "target")
	sourceOne := mustCreateInternalProject(t, service, "Source One", "source-one")
	sourceTwo := mustCreateInternalProject(t, service, "Source Two", "source-two")
	created := mustCreateInternalApp(t, service, app.CreateAppRequest{Name: "linked", ProjectSlug: target.Slug, Image: "nginx:1.27"})
	if _, err := service.CreateProjectLink(t.Context(), sourceOne.Slug, created.Name, "one.linked"); err != nil {
		t.Fatal(err)
	}
	reconciler.Reconcile(t.Context())

	newLink := &store.ProjectLink{
		ID:              uuid.NewString(),
		SourceProjectID: sourceTwo.ID,
		TargetAppID:     created.ID,
		Alias:           "two.linked",
		CreatedAt:       time.Now().UTC(),
	}
	wrapped := &linkSnapshotRaceStore{Store: st, base: st, injectLink: newLink}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	raceService := app.NewService(wrapped, mock, "moduleos.local", logger)
	raceReconciler := New(mock, raceService, logger).WithStabilizationWindow(0)
	if err := raceReconciler.ReconcileApplication(t.Context(), created.Name); err != nil {
		t.Fatal(err)
	}
	persisted, err := st.GetProjectLink(t.Context(), newLink.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ObservedGeneration != 0 {
		t.Fatalf("new link was falsely marked observed: %#v", persisted)
	}
	observed, err := mock.GetService(t.Context(), swarm.ServiceName(created.Name))
	if err != nil {
		t.Fatal(err)
	}
	for _, network := range observed.Spec.Networks {
		if network.Network == sourceTwo.Network {
			t.Fatal("new link unexpectedly appeared in the pre-race runtime snapshot")
		}
	}

	if err := raceReconciler.ReconcileApplication(t.Context(), created.Name); err != nil {
		t.Fatal(err)
	}
	persisted, err = st.GetProjectLink(t.Context(), newLink.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ObservedGeneration != persisted.DesiredGeneration {
		t.Fatalf("new link did not converge on retry: %#v", persisted)
	}
}

func TestOrphanDiagnosticsSurvivePartialRuntimeReadFailure(t *testing.T) {
	reconciler, _, _, mock := newInternalReconciler(t)
	mock.Services["moduleos_orphan"] = &swarm.ServiceInfo{
		ID: "orphan", Name: "moduleos_orphan", Labels: map[string]string{swarm.LabelManagedBy: "true"},
	}
	mock.NetworkResources["moduleos-orphan-net"] = &swarm.NetworkInfo{
		ID: "orphan-net", Name: "moduleos-orphan-net",
		Labels: map[string]string{swarm.LabelManagedBy: "true", swarm.LabelResourceKind: "project-network"},
	}
	reconciler.detectOrphans(t.Context(), nil, nil)
	delete(mock.NetworkResources, "moduleos-orphan-net")
	reconciler.swarm = &orphanListFailureClient{Client: mock, serviceErr: errors.New("service list unavailable")}
	reconciler.detectOrphans(t.Context(), nil, nil)
	diagnostics := reconciler.Diagnostics()
	if len(diagnostics.OrphanServices) != 1 || diagnostics.OrphanServices[0] != "moduleos_orphan" {
		t.Fatalf("service diagnostics were erased on read failure: %#v", diagnostics)
	}
	if len(diagnostics.OrphanNetworks) != 0 {
		t.Fatalf("successful network refresh did not replace diagnostics: %#v", diagnostics)
	}
}

func TestProjectNetworkFailuresPersistDiagnosticsAndRecover(t *testing.T) {
	_, service, st, mock := newInternalReconciler(t)
	project := mustCreateInternalProject(t, service, "Degraded", "degraded")
	mock.EnsureNetworkError = errors.New("network create unavailable")
	reconciler := New(mock, service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	reconciler.Reconcile(t.Context())
	persisted, err := st.GetProject(t.Context(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ObservedState != store.ObservedStateDegraded || persisted.ReconcileErrorCode != "network_reconcile_failed" {
		t.Fatalf("network reconcile diagnostic = %#v", persisted)
	}
	mock.EnsureNetworkError = nil
	reconciler.Reconcile(t.Context())
	persisted, err = st.GetProject(t.Context(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ObservedState != store.ObservedStateReady || persisted.ReconcileErrorCode != "" {
		t.Fatalf("project did not recover: %#v", persisted)
	}
}

func TestProjectDeletionNetworkFailuresAreRetryableByFullScan(t *testing.T) {
	_, service, st, mock := newInternalReconciler(t)
	project := mustCreateInternalProject(t, service, "Remove Retry", "remove-retry")
	baseReconciler := New(mock, service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	baseReconciler.Reconcile(t.Context())
	if err := service.DeleteProject(t.Context(), project.Slug); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("network remove unavailable")
	client := &projectNetworkFaultClient{Client: mock, removeErr: failure}
	reconciler := New(client, service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	reconciler.Reconcile(t.Context())
	persisted, err := st.GetProject(t.Context(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ReconcileErrorCode != "network_remove_failed" || persisted.ObservedState != store.ObservedStateDegraded {
		t.Fatalf("network removal diagnostic = %#v", persisted)
	}
	client.removeErr = nil
	reconciler.Reconcile(t.Context())
	if _, err := st.GetProject(t.Context(), project.Slug); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("project deletion did not recover: %v", err)
	}
}

func TestProjectDeletionFinalizesWhenNetworkAlreadyMissing(t *testing.T) {
	reconciler, service, st, _ := newInternalReconciler(t)
	project := mustCreateInternalProject(t, service, "Already Missing", "already-missing")
	if err := service.DeleteProject(t.Context(), project.Slug); err != nil {
		t.Fatal(err)
	}
	reconciler.Reconcile(t.Context())
	if _, err := st.GetProject(t.Context(), project.Slug); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("project with missing network was not finalized: %v", err)
	}
}

func TestRunPerformsInitialScanCanRestartAndStopsCleanly(t *testing.T) {
	reconciler, service, _, mock := newInternalReconciler(t)
	created := mustCreateInternalApp(t, service, app.CreateAppRequest{Name: "run-loop", Image: "nginx:1.27"})
	reconciler.WithInterval(time.Hour).WithWorkers(1).WithMaxBackoff(time.Millisecond)

	runOnce := func(run int) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			reconciler.Run(ctx)
			close(done)
		}()
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, err := mock.GetService(t.Context(), swarm.ServiceName(created.Name)); err == nil {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				<-done
				persisted, err := service.GetApp(t.Context(), created.Name)
				t.Fatalf("run %d full scan did not converge: app=%#v app_err=%v create_calls=%d queued=%d pending=%d running=%v stopping=%v", run, persisted, err, mock.CreateCalls, len(reconciler.queue), len(reconciler.pending), reconciler.running.Load(), reconciler.stopping.Load())
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("reconciler did not stop")
		}
	}

	runOnce(1)
	delete(mock.Services, swarm.ServiceName(created.Name))
	runOnce(2)
	if reconciler.running.Load() || !reconciler.stopping.Load() {
		t.Fatalf("unexpected lifecycle flags: running=%v stopping=%v", reconciler.running.Load(), reconciler.stopping.Load())
	}
	if len(reconciler.queue) != 0 || len(reconciler.pending) != 0 {
		t.Fatalf("reconciler retained queued work after shutdown: queued=%d pending=%d", len(reconciler.queue), len(reconciler.pending))
	}
	reconciler.attemptMu.Lock()
	defer reconciler.attemptMu.Unlock()
	if len(reconciler.attempts) != 0 || len(reconciler.retrying) != 0 {
		t.Fatal("reconciler retained retry state after shutdown")
	}
}

func TestWatcherEventsAndFullScanShareTheReconcileQueue(t *testing.T) {
	_, service, _, mock := newInternalReconciler(t)
	client := &controlledEventClient{
		Client: mock,
		events: make(chan swarm.SwarmEvent, 32),
		errs:   make(chan error),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reconciler := New(client, service, logger).WithInterval(time.Hour).WithWorkers(2).WithStabilizationWindow(0)
	eventWatcher := watcher.New(client, reconciler, logger).WithBackoff(time.Millisecond, 4*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	var processes sync.WaitGroup
	processes.Add(2)
	go func() {
		defer processes.Done()
		reconciler.Run(ctx)
	}()
	go func() {
		defer processes.Done()
		eventWatcher.Run(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := mock.GetNetwork(t.Context(), "moduleos-root-net"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			processes.Wait()
			t.Fatal("initial project scan did not complete")
		}
		time.Sleep(time.Millisecond)
	}

	fast := mustCreateInternalApp(t, service, app.CreateAppRequest{Name: "event-fast", Image: "nginx:1.27"})
	for range 20 {
		client.events <- swarm.SwarmEvent{Type: "task", Action: "update", Target: swarm.ServiceName(fast.Name)}
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		if _, err := mock.GetService(t.Context(), swarm.ServiceName(fast.Name)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			processes.Wait()
			t.Fatal("event-triggered application did not converge")
		}
		time.Sleep(time.Millisecond)
	}

	missed := mustCreateInternalApp(t, service, app.CreateAppRequest{Name: "missed-event", Image: "nginx:1.27"})
	if _, err := mock.GetService(t.Context(), swarm.ServiceName(missed.Name)); !errdefs.IsNotFound(err) {
		t.Fatalf("application converged before the recovery scan: %v", err)
	}
	reconciler.enqueueFullScan(ctx)
	deadline = time.Now().Add(2 * time.Second)
	for {
		if _, err := mock.GetService(t.Context(), swarm.ServiceName(missed.Name)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			processes.Wait()
			t.Fatal("full scan did not recover a missed event")
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	done := make(chan struct{})
	go func() {
		processes.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher and reconciler did not stop together")
	}
	if mock.CreateCalls != 2 {
		t.Fatalf("service creation calls = %d, want two", mock.CreateCalls)
	}
}

func TestShortDeterministicDriftRepairSoak(t *testing.T) {
	reconciler, service, _, mock := newInternalReconciler(t)
	applications := make([]*store.Application, 8)
	for index := range applications {
		applications[index] = mustCreateInternalApp(t, service, app.CreateAppRequest{
			Name: "soak-" + string(rune('a'+index)), Image: "nginx:1.27",
		})
	}
	reconciler.Reconcile(t.Context())
	const iterations = 256
	for index := range iterations {
		application := applications[index%len(applications)]
		runtimeService := mock.Services[swarm.ServiceName(application.Name)]
		runtimeService.Image = "redis:7"
		runtimeService.Spec.Image = "redis:7"
		if err := reconciler.ReconcileApplication(t.Context(), application.Name); err != nil {
			t.Fatalf("iteration %d: %v", index, err)
		}
	}
	if mock.UpdateCalls < iterations {
		t.Fatalf("drift repairs = %d, want at least %d", mock.UpdateCalls, iterations)
	}
	if len(reconciler.stableSince) != 0 {
		t.Fatalf("soak retained stability state: %#v", reconciler.stableSince)
	}
	reconciler.attemptMu.Lock()
	defer reconciler.attemptMu.Unlock()
	if len(reconciler.attempts) != 0 || len(reconciler.retrying) != 0 {
		t.Fatal("soak retained retry state")
	}
}

func TestErrorClassificationAndDiagnosticRedaction(t *testing.T) {
	if !isRetryable(retryableError("temporary", errors.New("failure"))) ||
		!isRetryable(fmtError(errdefs.ErrUnavailable)) ||
		isRetryable(errors.New("permanent")) {
		t.Fatal("retry classification mismatch")
	}
	if !isRetryableMutation(fmtError(errdefs.ErrNotFound)) {
		t.Fatal("a Docker mutation dependency race was not retryable")
	}
	cases := map[string]string{
		"https://user:password@example.test/path": "https://[redacted]@example.test/path",
		"authorization=top-secret":                "authorization=[redacted]",
		"Bearer abc.def.ghi":                      "Bearer [redacted]",
	}
	for input, want := range cases {
		if got := safeDiagnostic(input); got != want {
			t.Fatalf("safeDiagnostic(%q) = %q, want %q", input, got, want)
		}
	}
	if code := classifyDockerError(errors.New("port is already allocated")); code != "published_port_conflict" {
		t.Fatalf("port conflict code = %q", code)
	}
	if code := classifyDockerError(errors.New("invalid reference format")); code != "invalid_image" {
		t.Fatalf("invalid image code = %q", code)
	}
	if code := classifyDockerError(fmtError(errdefs.ErrConflict)); code != "docker_conflict" {
		t.Fatalf("Docker conflict code = %q", code)
	}
	if code := classifyDockerError(fmtError(errdefs.ErrUnavailable)); code != "docker_unavailable" {
		t.Fatalf("Docker unavailable code = %q", code)
	}
	if code := classifyDockerError(fmtError(errdefs.ErrNotFound)); code != "docker_dependency_missing" {
		t.Fatalf("Docker missing dependency code = %q", code)
	}
	if code := classifyDockerError(errors.New("unexpected mutation failure")); code != "docker_mutation_failed" {
		t.Fatalf("fallback mutation code = %q", code)
	}
}

func fmtError(err error) error {
	return errors.Join(errors.New("wrapped"), err)
}
