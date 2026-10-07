package reconciler_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/reconciler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/testkit/swarmfake"
)

func setup(t testing.TB) (*reconciler.Reconciler, *store.SQLiteStore, *swarmfake.Client) {
	t.Helper()

	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mock := swarmfake.New()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	appSvc := app.NewService(st, mock, "moduleos.local", log)
	rec := reconciler.New(mock, appSvc, log)

	return rec, st, mock
}

type firstUpdateBarrierClient struct {
	swarm.Client
	entered chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (c *firstUpdateBarrierClient) UpdateService(ctx context.Context, serviceID string, spec swarm.ServiceSpec) error {
	shouldWait := false
	c.once.Do(func() {
		shouldWait = true
		close(c.entered)
	})
	if shouldWait {
		select {
		case <-c.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.Client.UpdateService(ctx, serviceID, spec)
}

func TestReconcile_AppMissingInSwarmIsRecreated(t *testing.T) {
	rec, st, mock := setup(t)
	ctx := context.Background()

	appSvc := appSvcFromStore(t, st, mock)
	mustCreateApp(t, appSvc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:latest"})
	if err := rec.ReconcileApplication(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}

	a := mustGetApp(t, st, ctx, "my-api")
	a.Status = store.AppStatusRunning
	mustUpdateApp(t, st, ctx, a)

	delete(mock.Services, "moduleos_my-api")

	rec.Reconcile(ctx)

	// Desired state is the source of truth: the service is recreated and observed.
	fromDB := mustGetApp(t, st, ctx, "my-api")
	if fromDB.Status != store.AppStatusRunning || fromDB.ObservedGeneration != fromDB.DesiredGeneration {
		t.Errorf("application did not reconverge: %#v", fromDB)
	}
	if _, exists := mock.Services["moduleos_my-api"]; !exists {
		t.Error("missing service was not recreated")
	}
}

func TestReconcile_StoppedAppNotTouched(t *testing.T) {
	rec, st, mock := setup(t)
	ctx := context.Background()

	appSvc := appSvcFromStore(t, st, mock)
	mustCreateApp(t, appSvc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:latest"})
	if err := rec.ReconcileApplication(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}

	a := mustGetApp(t, st, ctx, "my-api")
	stopped := store.DesiredRunStateStopped
	if _, err := st.UpdateApplicationIntent(ctx, a.Name, a.DesiredGeneration, store.ApplicationMutation{DesiredRunState: &stopped}); err != nil {
		t.Fatal(err)
	}

	delete(mock.Services, "moduleos_my-api")

	rec.Reconcile(ctx)

	fromDB := mustGetApp(t, st, ctx, "my-api")
	if fromDB.ObservedState != store.ObservedStateStopped {
		t.Errorf("stopped desired state did not converge: %#v", fromDB)
	}
	if mock.Services["moduleos_my-api"].Replicas != 0 {
		t.Errorf("stopped service replicas = %d", mock.Services["moduleos_my-api"].Replicas)
	}
}

func TestReconcile_InSyncNoChange(t *testing.T) {
	rec, st, mock := setup(t)
	ctx := context.Background()

	appSvc := appSvcFromStore(t, st, mock)
	mustCreateApp(t, appSvc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:latest"})
	if err := rec.ReconcileApplication(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}

	a := mustGetApp(t, st, ctx, "my-api")
	a.Status = store.AppStatusRunning
	mustUpdateApp(t, st, ctx, a)

	rec.Reconcile(ctx)

	fromDB := mustGetApp(t, st, ctx, "my-api")
	if fromDB.Status != store.AppStatusRunning {
		t.Errorf("in-sync app must not be changed, status: %s", fromDB.Status)
	}
}

func TestReconcile_RunStopsOnContextCancel(t *testing.T) {
	rec, st, mock := setup(t)
	_ = st
	_ = mock

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		rec.WithInterval(10 * time.Millisecond).Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("reconciler did not stop after context cancellation")
	}
}

func TestReconcileRepairsCanonicalSpecDrift(t *testing.T) {
	rec, st, mock := setup(t)
	ctx := context.Background()
	appSvc := appSvcFromStore(t, st, mock)
	created, err := appSvc.CreateApp(ctx, app.CreateAppRequest{
		Name: "drifted", Image: "nginx:1.27", EnvVars: map[string]string{"A": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.ReconcileApplication(ctx, created.Name); err != nil {
		t.Fatal(err)
	}
	service := mock.Services[swarm.ServiceName(created.Name)]
	service.Spec.Image = "redis:7"
	service.Image = "redis:7"
	service.Spec.EnvVars = []string{"A=changed"}
	service.Spec.Labels[swarm.LabelGeneration] = "stale"
	before := mock.UpdateCalls

	if err := rec.ReconcileApplication(ctx, created.Name); err != nil {
		t.Fatal(err)
	}
	if mock.UpdateCalls != before+1 {
		t.Fatalf("update calls = %d, want %d", mock.UpdateCalls, before+1)
	}
	observed := mustGetRuntimeService(t, mock, ctx, swarm.ServiceName(created.Name))
	desired := mustBuildDesiredSpec(t, appSvc, ctx, created)
	if fields := swarm.DiffServiceSpec(desired, observed.Spec); len(fields) != 0 {
		t.Fatalf("service still drifted: %v", fields)
	}
	fromDB := mustGetApp(t, st, ctx, created.Name)
	if fromDB.ObservedGeneration != fromDB.DesiredGeneration || fromDB.ObservedState != store.ObservedStateRunning {
		t.Fatalf("observed state not committed: %#v", fromDB)
	}
}

func TestReconcileOwnershipConflictFailsClosed(t *testing.T) {
	rec, st, mock := setup(t)
	ctx := context.Background()
	appSvc := appSvcFromStore(t, st, mock)
	created, err := appSvc.CreateApp(ctx, app.CreateAppRequest{Name: "owned", Image: "nginx:1.27"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.ReconcileApplication(ctx, created.Name); err != nil {
		t.Fatal(err)
	}
	service := mock.Services[swarm.ServiceName(created.Name)]
	service.Labels[swarm.LabelAppID] = "different-app"
	service.Spec.Labels[swarm.LabelAppID] = "different-app"
	before := mock.UpdateCalls

	err = rec.ReconcileApplication(ctx, created.Name)
	if !errors.Is(err, swarm.ErrOwnershipConflict) {
		t.Fatalf("error = %v", err)
	}
	if mock.UpdateCalls != before {
		t.Fatal("ownership conflict mutated the foreign service")
	}
	fromDB := mustGetApp(t, st, ctx, created.Name)
	if fromDB.ReconcileErrorCode != "ownership_conflict" || fromDB.ReconcileRetryable {
		t.Fatalf("ownership diagnostic = %#v", fromDB)
	}
}

func TestReconcileDeletionFinalizerSurvivesRestartBoundary(t *testing.T) {
	rec, st, mock := setup(t)
	ctx := context.Background()
	appSvc := appSvcFromStore(t, st, mock)
	created, err := appSvc.CreateApp(ctx, app.CreateAppRequest{Name: "delete-me", Image: "nginx:1.27"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.ReconcileApplication(ctx, created.Name); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateDeletionIntent(ctx, created.Name, created.DesiredGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if intent.DeletionTimestamp == nil {
		t.Fatal("deletion intent was not persisted")
	}
	if err := rec.ReconcileApplication(ctx, created.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetApplication(ctx, created.Name); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("application was not finalized: %v", err)
	}
	if _, exists := mock.Services[swarm.ServiceName(created.Name)]; exists {
		t.Fatal("service was not removed")
	}
}

func TestProjectDeletionFinalizerRemovesOwnedNetwork(t *testing.T) {
	rec, st, mock := setup(t)
	ctx := context.Background()
	appSvc := appSvcFromStore(t, st, mock)
	project, err := appSvc.CreateProject(ctx, app.CreateProjectRequest{Name: "Temporary", Slug: "temporary"})
	if err != nil {
		t.Fatal(err)
	}
	rec.Reconcile(ctx)
	if _, err := mock.GetNetwork(ctx, project.Network); err != nil {
		t.Fatalf("project network was not created: %v", err)
	}
	if err := appSvc.DeleteProject(ctx, project.Slug); err != nil {
		t.Fatal(err)
	}
	rec.Reconcile(ctx)
	if _, err := st.GetProject(ctx, project.Slug); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("project was not finalized: %v", err)
	}
	if _, err := mock.GetNetwork(ctx, project.Network); err == nil {
		t.Fatal("owned project network was not removed")
	}
}

func TestProjectDeletionFinalizerProtectsForeignNetwork(t *testing.T) {
	rec, st, mock := setup(t)
	ctx := context.Background()
	appSvc := appSvcFromStore(t, st, mock)
	project, err := appSvc.CreateProject(ctx, app.CreateProjectRequest{Name: "Foreign", Slug: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	mock.NetworkResources[project.Network] = &swarm.NetworkInfo{ID: "foreign-id", Name: project.Network, Labels: map[string]string{swarm.LabelManagedBy: "false"}}
	if err := appSvc.DeleteProject(ctx, project.Slug); err != nil {
		t.Fatal(err)
	}
	rec.Reconcile(ctx)
	persisted, err := st.GetProject(ctx, project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ReconcileErrorCode != "ownership_conflict" || persisted.ObservedState != store.ObservedStateFailed {
		t.Fatalf("unexpected project diagnostic: %#v", persisted)
	}
	if _, err := mock.GetNetwork(ctx, project.Network); err != nil {
		t.Fatalf("foreign network was mutated: %v", err)
	}
}

func TestProjectNetworkDeletionIsRecovered(t *testing.T) {
	rec, st, mock := setup(t)
	svc := appSvcFromStore(t, st, mock)
	project, err := svc.CreateProject(t.Context(), app.CreateProjectRequest{Name: "Recover", Slug: "recover"})
	if err != nil {
		t.Fatal(err)
	}
	rec.Reconcile(t.Context())
	delete(mock.NetworkResources, project.Network)
	rec.Reconcile(t.Context())
	if _, err := mock.GetNetwork(t.Context(), project.Network); err != nil {
		t.Fatalf("project network was not recreated: %v", err)
	}
	persisted, err := st.GetProject(t.Context(), project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ObservedState != store.ObservedStateReady {
		t.Fatalf("project observed state = %q, want ready", persisted.ObservedState)
	}
}

func TestProjectLinkAndIngressAttachmentDriftIsRecovered(t *testing.T) {
	rec, st, mock := setup(t)
	svc := appSvcFromStore(t, st, mock).WithIngressNetwork("moduleos-ingress")
	mock.NetworkResources["moduleos-ingress"] = &swarm.NetworkInfo{ID: "ingress-id", Name: "moduleos-ingress", Driver: "overlay", Attachable: true}
	targetProject := mustCreateProject(t, svc, t.Context(), app.CreateProjectRequest{Name: "Target", Slug: "target"})
	sourceProject := mustCreateProject(t, svc, t.Context(), app.CreateProjectRequest{Name: "Source", Slug: "source"})
	created, err := svc.CreateApp(t.Context(), app.CreateAppRequest{Name: "linked", ProjectSlug: targetProject.Slug, Image: "nginx:1.27", Expose: true, IngressContainerPort: 80})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateProjectLink(t.Context(), sourceProject.Slug, created.Name, "target.linked"); err != nil {
		t.Fatal(err)
	}
	rec.Reconcile(t.Context())
	runtimeService := mock.Services[swarm.ServiceName(created.Name)]
	runtimeService.Spec.Networks = []swarm.NetworkAttachment{{Network: targetProject.Network}}
	before := mock.UpdateCalls
	if err := rec.ReconcileApplication(t.Context(), created.Name); err != nil {
		t.Fatal(err)
	}
	if mock.UpdateCalls != before+1 {
		t.Fatalf("network drift update calls = %d, want 1", mock.UpdateCalls-before)
	}
	desired := mustBuildDesiredSpec(t, svc, t.Context(), created)
	observed := mustGetRuntimeService(t, mock, t.Context(), swarm.ServiceName(created.Name))
	if fields := swarm.DiffServiceSpec(desired, observed.Spec); len(fields) != 0 {
		t.Fatalf("link/ingress drift remains: %v", fields)
	}
}

func TestPersistedIntentConvergesWithoutQueueDelivery(t *testing.T) {
	rec, st, mock := setup(t)
	svc := appSvcFromStore(t, st, mock)
	created, err := svc.CreateApp(t.Context(), app.CreateAppRequest{Name: "crash-boundary", Image: "nginx:1.27"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.Services) != 0 {
		t.Fatal("request path mutated Swarm before reconciliation")
	}
	rec.Reconcile(t.Context())
	if _, err := mock.GetService(t.Context(), swarm.ServiceName(created.Name)); err != nil {
		t.Fatalf("full scan did not recover persisted intent: %v", err)
	}
}

func TestVersionConflictIsRetryableAndRecovers(t *testing.T) {
	rec, st, mock := setup(t)
	svc := appSvcFromStore(t, st, mock)
	created, err := svc.CreateApp(t.Context(), app.CreateAppRequest{Name: "versioned", Image: "nginx:1.27"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.ReconcileApplication(t.Context(), created.Name); err != nil {
		t.Fatal(err)
	}
	image := "nginx:1.28"
	current := mustGetApp(t, st, t.Context(), created.Name)
	if _, err := svc.UpdateApp(t.Context(), app.UpdateAppRequest{AppName: created.Name, Image: &image, ExpectedGeneration: current.DesiredGeneration}); err != nil {
		t.Fatal(err)
	}
	mock.UpdateError = fmt.Errorf("%w: update out of sequence", errdefs.ErrConflict)
	if err := rec.ReconcileApplication(t.Context(), created.Name); err == nil {
		t.Fatal("expected retryable version conflict")
	}
	degraded := mustGetApp(t, st, t.Context(), created.Name)
	if !degraded.ReconcileRetryable {
		t.Fatalf("version conflict was not persisted as retryable: %#v", degraded)
	}
	mock.UpdateError = nil
	if err := rec.ReconcileApplication(t.Context(), created.Name); err != nil {
		t.Fatal(err)
	}
	recovered := mustGetApp(t, st, t.Context(), created.Name)
	if recovered.ObservedGeneration != recovered.DesiredGeneration || recovered.ReconcileErrorCode != "" {
		t.Fatalf("version conflict did not recover: %#v", recovered)
	}
}

func TestConcurrentReconcileForSameApplicationIsSerialized(t *testing.T) {
	rec, st, mock := setup(t)
	ctx := context.Background()
	appSvc := appSvcFromStore(t, st, mock)
	created, err := appSvc.CreateApp(ctx, app.CreateAppRequest{Name: "serial", Image: "nginx:1.27"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.ReconcileApplication(ctx, created.Name); err != nil {
		t.Fatal(err)
	}
	mock.Services[swarm.ServiceName(created.Name)].Spec.Image = "redis:7"
	before := mock.UpdateCalls
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = rec.ReconcileApplication(ctx, created.Name)
		}()
	}
	wg.Wait()
	if mock.UpdateCalls != before+1 {
		t.Fatalf("concurrent update calls = %d, want one", mock.UpdateCalls-before)
	}
}

func TestEventTriggeredReconcileConcurrentWithAPIMutationPreservesNewGeneration(t *testing.T) {
	rec, st, mock := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	apiService := appSvcFromStore(t, st, mock)
	created, err := apiService.CreateApp(ctx, app.CreateAppRequest{Name: "event-api-race", Image: "nginx:1.27", Replicas: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.ReconcileApplication(ctx, created.Name); err != nil {
		t.Fatal(err)
	}

	// Force an event-worthy runtime drift. The event reconcile reads generation
	// one, then pauses at the Docker update while the API persists generation two.
	runtimeService := mock.Services[swarm.ServiceName(created.Name)]
	runtimeService.Image = "redis:7"
	runtimeService.Spec.Image = "redis:7"
	entered := make(chan struct{})
	release := make(chan struct{})
	barrierClient := &firstUpdateBarrierClient{Client: mock, entered: entered, release: release}
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	eventService := app.NewService(st, barrierClient, "moduleos.local", log)
	eventReconciler := reconciler.New(barrierClient, eventService, log).WithStabilizationWindow(0)
	reconcileResult := make(chan error, 1)
	go func() {
		reconcileResult <- eventReconciler.ReconcileApplication(ctx, created.Name)
	}()

	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatalf("event reconcile did not reach Docker update barrier: %v", ctx.Err())
	}
	if _, err := apiService.ScaleAppIntent(ctx, created.Name, 2, created.DesiredGeneration); err != nil {
		t.Fatalf("API mutation during event reconcile failed: %v", err)
	}
	close(release)
	if err := <-reconcileResult; err != nil {
		t.Fatalf("stale event reconcile returned an unexpected error: %v", err)
	}

	afterRace, err := st.GetApplication(ctx, created.Name)
	if err != nil {
		t.Fatal(err)
	}
	if afterRace.DesiredGeneration != 2 || afterRace.ObservedGeneration != 1 || afterRace.Replicas != 2 {
		t.Fatalf("stale event reconcile overwrote the API generation: %#v", afterRace)
	}
	if err := eventReconciler.ReconcileApplication(ctx, created.Name); err != nil {
		t.Fatalf("new generation did not reconcile after event/API race: %v", err)
	}
	converged, err := st.GetApplication(ctx, created.Name)
	if err != nil {
		t.Fatal(err)
	}
	if converged.ObservedGeneration != converged.DesiredGeneration || converged.Replicas != 2 {
		t.Fatalf("application did not converge after event/API race: %#v", converged)
	}
	observed, err := mock.GetService(ctx, swarm.ServiceName(created.Name))
	if err != nil {
		t.Fatal(err)
	}
	if observed.Spec.Replicas != 2 || observed.Spec.Labels[swarm.LabelGeneration] != "2" {
		t.Fatalf("runtime did not converge to the API mutation: %#v", observed.Spec)
	}
}

func TestReconcileReportsOrphansWithoutDeleting(t *testing.T) {
	rec, _, mock := setup(t)
	ctx := context.Background()
	mock.Services["moduleos_orphan"] = &swarm.ServiceInfo{
		ID: "orphan", Name: "moduleos_orphan", Labels: map[string]string{swarm.LabelManagedBy: "true"},
	}
	mock.NetworkResources["moduleos-orphan-net"] = &swarm.NetworkInfo{
		ID: "orphan-net", Name: "moduleos-orphan-net",
		Labels: map[string]string{swarm.LabelManagedBy: "true", swarm.LabelResourceKind: "project-network"},
	}
	rec.Reconcile(ctx)
	diagnostics := rec.Diagnostics()
	if len(diagnostics.OrphanServices) != 1 || len(diagnostics.OrphanNetworks) != 1 {
		t.Fatalf("orphan diagnostics = %#v", diagnostics)
	}
	if _, exists := mock.Services["moduleos_orphan"]; !exists {
		t.Fatal("orphan service was deleted automatically")
	}
}

func TestDeploymentSucceedsOnlyAfterReconcileConvergence(t *testing.T) {
	rec, st, mock := setup(t)
	ctx := context.Background()
	svc := appSvcFromStore(t, st, mock)
	created, err := svc.CreateApp(ctx, app.CreateAppRequest{Name: "deploy-state", Image: "nginx:1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.ReconcileApplication(ctx, created.Name); err != nil {
		t.Fatal(err)
	}
	deployment, err := svc.DeployApp(ctx, app.DeployAppRequest{AppName: created.Name, Image: "nginx:2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Status != store.DeploymentStatusPending {
		t.Fatalf("initial=%s", deployment.Status)
	}
	if err := rec.ReconcileApplication(ctx, created.Name); err != nil {
		t.Fatal(err)
	}
	observed, err := st.GetDeployment(ctx, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Status != store.DeploymentStatusSucceeded || observed.FinishedAt == nil {
		t.Fatalf("deployment=%#v", observed)
	}
}

func TestRejectedTaskFailsDeploymentWithoutAdvancingObservedImage(t *testing.T) {
	rec, st, mock := setup(t)
	ctx := context.Background()
	svc := appSvcFromStore(t, st, mock)
	created := mustCreateApp(t, svc, ctx, app.CreateAppRequest{Name: "reject", Image: "nginx:1.0"})
	if err := rec.ReconcileApplication(ctx, created.Name); err != nil {
		t.Fatal(err)
	}
	deployment := mustDeployApp(t, svc, ctx, app.DeployAppRequest{AppName: created.Name, Image: "missing.invalid/image:nope"})
	service := mock.Services[swarm.ServiceName(created.Name)]
	service.Running = 0
	service.TaskErrors = []string{"image pull rejected"}
	err := rec.ReconcileApplication(ctx, created.Name)
	if err == nil {
		t.Fatal("expected terminal convergence error")
	}
	failed := mustGetDeployment(t, st, ctx, deployment.ID)
	if failed.Status != store.DeploymentStatusFailed || failed.ErrorCode != "task_rejected" {
		t.Fatalf("deployment=%#v", failed)
	}
	application := mustGetApp(t, st, ctx, created.Name)
	if application.ObservedImage != "nginx:1.0" {
		t.Fatalf("observed image advanced to %q", application.ObservedImage)
	}
}

func mustCreateApp(t testing.TB, service *app.Service, ctx context.Context, request app.CreateAppRequest) *store.Application {
	t.Helper()
	created, err := service.CreateApp(ctx, request)
	if err != nil {
		t.Fatalf("CreateApp(%q): %v", request.Name, err)
	}
	return created
}

func mustGetApp(t testing.TB, st store.Store, ctx context.Context, name string) *store.Application {
	t.Helper()
	application, err := st.GetApplication(ctx, name)
	if err != nil {
		t.Fatalf("GetApplication(%q): %v", name, err)
	}
	return application
}

func mustUpdateApp(t testing.TB, st store.Store, ctx context.Context, application *store.Application) {
	t.Helper()
	if err := st.UpdateApplication(ctx, application); err != nil {
		t.Fatalf("UpdateApplication(%q): %v", application.Name, err)
	}
}

func mustGetRuntimeService(t testing.TB, client swarm.Client, ctx context.Context, name string) *swarm.ServiceInfo {
	t.Helper()
	service, err := client.GetService(ctx, name)
	if err != nil {
		t.Fatalf("GetService(%q): %v", name, err)
	}
	return service
}

func mustBuildDesiredSpec(t testing.TB, service *app.Service, ctx context.Context, application *store.Application) swarm.ServiceSpec {
	t.Helper()
	spec, err := service.BuildDesiredServiceSpec(ctx, application)
	if err != nil {
		t.Fatalf("BuildDesiredServiceSpec(%q): %v", application.Name, err)
	}
	return spec
}

func mustCreateProject(t testing.TB, service *app.Service, ctx context.Context, request app.CreateProjectRequest) *store.Project {
	t.Helper()
	project, err := service.CreateProject(ctx, request)
	if err != nil {
		t.Fatalf("CreateProject(%q): %v", request.Slug, err)
	}
	return project
}

func mustDeployApp(t testing.TB, service *app.Service, ctx context.Context, request app.DeployAppRequest) *store.Deployment {
	t.Helper()
	deployment, err := service.DeployApp(ctx, request)
	if err != nil {
		t.Fatalf("DeployApp(%q): %v", request.AppName, err)
	}
	return deployment
}

func mustGetDeployment(t testing.TB, st store.Store, ctx context.Context, id string) *store.Deployment {
	t.Helper()
	deployment, err := st.GetDeployment(ctx, id)
	if err != nil {
		t.Fatalf("GetDeployment(%q): %v", id, err)
	}
	return deployment
}

// appSvcFromStore constructs an app.Service from the store and mock created in setup.
func appSvcFromStore(t testing.TB, st *store.SQLiteStore, mock *swarmfake.Client) *app.Service {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	return app.NewService(st, mock, "moduleos.local", log)
}
