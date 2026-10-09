package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/testkit/swarmfake"
)

type observedLocker struct {
	mutex     mutationLocker
	attempted chan struct{}
	acquired  chan struct{}
}

func newObservedLocker() *observedLocker {
	return &observedLocker{
		mutex:     newContextMutex(),
		attempted: make(chan struct{}, 4),
		acquired:  make(chan struct{}, 4),
	}
}

func (l *observedLocker) Lock(ctx context.Context) error {
	l.attempted <- struct{}{}
	if err := l.mutex.Lock(ctx); err != nil {
		return err
	}
	l.acquired <- struct{}{}
	return nil
}

func (l *observedLocker) Unlock() {
	l.mutex.Unlock()
}

type lockBarrierStore struct {
	store.Store
	entered           chan string
	updateRelease     <-chan struct{}
	deploymentRelease <-chan struct{}
}

func (s *lockBarrierStore) UpdateApplicationIntent(ctx context.Context, name string, expectedGeneration int64, mutation store.ApplicationMutation) (*store.Application, error) {
	if err := s.wait(ctx, "update", s.updateRelease); err != nil {
		return nil, err
	}
	return s.Store.UpdateApplicationIntent(ctx, name, expectedGeneration, mutation)
}

func (s *lockBarrierStore) CreateDeploymentIntent(ctx context.Context, name string, deployment *store.Deployment) (*store.Application, error) {
	if err := s.wait(ctx, "deploy", s.deploymentRelease); err != nil {
		return nil, err
	}
	return s.Store.CreateDeploymentIntent(ctx, name, deployment)
}

func (s *lockBarrierStore) wait(ctx context.Context, operation string, release <-chan struct{}) error {
	select {
	case s.entered <- operation:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type failOnceUpdateStore struct {
	store.Store
	once sync.Once
	err  error
}

func (s *failOnceUpdateStore) UpdateApplicationIntent(ctx context.Context, name string, expectedGeneration int64, mutation store.ApplicationMutation) (*store.Application, error) {
	failed := false
	s.once.Do(func() { failed = true })
	if failed {
		return nil, s.err
	}
	return s.Store.UpdateApplicationIntent(ctx, name, expectedGeneration, mutation)
}

func TestRollbackAndLifecycleMutationShareAdmissionLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mock := swarmfake.New()
	baseService := NewService(st, mock, "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	application, err := baseService.CreateApp(ctx, CreateAppRequest{Name: "rollback-lock", Image: "nginx:1.0"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := baseService.DeployApp(ctx, DeployAppRequest{AppName: application.Name, Image: "nginx:1.1"})
	if err != nil {
		t.Fatal(err)
	}
	resolvedTarget := "nginx@sha256:" + strings.Repeat("a", 64)
	if err := st.MarkDeploymentSucceeded(ctx, target.ID, target.TargetGeneration, resolvedTarget); err != nil {
		t.Fatal(err)
	}
	current, err := st.GetApplication(ctx, application.Name)
	if err != nil {
		t.Fatal(err)
	}

	updateRelease := make(chan struct{})
	deploymentRelease := make(chan struct{})
	barrier := &lockBarrierStore{
		Store:             st,
		entered:           make(chan string, 2),
		updateRelease:     updateRelease,
		deploymentRelease: deploymentRelease,
	}
	service := NewService(barrier, mock, "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	locker := newObservedLocker()
	service.resourceMu = locker

	scaleResult := make(chan error, 1)
	go func() {
		_, scaleErr := service.ScaleAppIntent(ctx, application.Name, 2, current.DesiredGeneration)
		scaleResult <- scaleErr
	}()
	requireLockSignal(t, ctx, locker.attempted, "lifecycle lock attempt")
	requireLockSignal(t, ctx, locker.acquired, "lifecycle lock acquisition")
	requireOperationSignal(t, ctx, barrier.entered, "update")

	rollbackResult := make(chan error, 1)
	go func() {
		_, rollbackErr := service.RollbackApp(ctx, application.Name, target.ID)
		rollbackResult <- rollbackErr
	}()
	requireLockSignal(t, ctx, locker.attempted, "rollback lock attempt")

	close(updateRelease)
	if err := <-scaleResult; err != nil {
		t.Fatalf("scale intent: %v", err)
	}
	requireLockSignal(t, ctx, locker.acquired, "rollback lock acquisition")
	requireOperationSignal(t, ctx, barrier.entered, "deploy")
	close(deploymentRelease)
	if err := <-rollbackResult; err != nil {
		t.Fatalf("rollback intent: %v", err)
	}

	persisted, err := st.GetApplication(ctx, application.Name)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Replicas != 2 || persisted.Image != resolvedTarget || persisted.DesiredGeneration != current.DesiredGeneration+2 {
		t.Fatalf("serialized rollback state = %#v", persisted)
	}
}

func TestApplicationMutationLockIsReleasedAfterDependencyError(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mock := swarmfake.New()
	baseService := NewService(st, mock, "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	application, err := baseService.CreateApp(ctx, CreateAppRequest{Name: "dependency-error-lock", Image: "nginx:1.0"})
	if err != nil {
		t.Fatal(err)
	}

	dependencyErr := errors.New("injected update failure")
	service := NewService(&failOnceUpdateStore{Store: st, err: dependencyErr}, mock, "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	locker := newObservedLocker()
	service.resourceMu = locker

	if _, err := service.ScaleAppIntent(ctx, application.Name, 2, application.DesiredGeneration); !errors.Is(err, dependencyErr) {
		t.Fatalf("scale error = %v, want injected dependency failure", err)
	}
	requireLockSignal(t, ctx, locker.attempted, "failed mutation lock attempt")
	requireLockSignal(t, ctx, locker.acquired, "failed mutation lock acquisition")

	deployResult := make(chan error, 1)
	go func() {
		_, deployErr := service.DeployApp(ctx, DeployAppRequest{AppName: application.Name, Image: "nginx:2.0"})
		deployResult <- deployErr
	}()
	requireLockSignal(t, ctx, locker.attempted, "post-error deploy lock attempt")
	requireLockSignal(t, ctx, locker.acquired, "post-error deploy lock acquisition")
	if err := <-deployResult; err != nil {
		t.Fatalf("deploy after dependency error: %v", err)
	}

	persisted, err := st.GetApplication(ctx, application.Name)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Replicas != application.Replicas || persisted.Image != "nginx:2.0" || persisted.DesiredGeneration != application.DesiredGeneration+1 {
		t.Fatalf("dependency failure leaked partial state: %#v", persisted)
	}
}

func TestWaitingApplicationMutationHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mock := swarmfake.New()
	baseService := NewService(st, mock, "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	application, err := baseService.CreateApp(ctx, CreateAppRequest{Name: "cancelled-waiter", Image: "nginx:1.0"})
	if err != nil {
		t.Fatal(err)
	}

	updateRelease := make(chan struct{})
	deploymentRelease := make(chan struct{})
	barrier := &lockBarrierStore{
		Store:             st,
		entered:           make(chan string, 2),
		updateRelease:     updateRelease,
		deploymentRelease: deploymentRelease,
	}
	service := NewService(barrier, mock, "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	locker := newObservedLocker()
	service.resourceMu = locker

	scaleResult := make(chan error, 1)
	go func() {
		_, scaleErr := service.ScaleAppIntent(ctx, application.Name, 2, application.DesiredGeneration)
		scaleResult <- scaleErr
	}()
	requireLockSignal(t, ctx, locker.attempted, "holder lock attempt")
	requireLockSignal(t, ctx, locker.acquired, "holder lock acquisition")
	requireOperationSignal(t, ctx, barrier.entered, "update")

	waiterContext, cancelWaiter := context.WithCancel(ctx)
	deployResult := make(chan error, 1)
	go func() {
		_, deployErr := service.DeployApp(waiterContext, DeployAppRequest{AppName: application.Name, Image: "nginx:2.0"})
		deployResult <- deployErr
	}()
	requireLockSignal(t, ctx, locker.attempted, "waiting deploy lock attempt")
	cancelWaiter()
	select {
	case err := <-deployResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting deploy error = %v, want context cancellation", err)
		}
	case <-ctx.Done():
		t.Fatalf("waiting deploy did not honor cancellation: %v", ctx.Err())
	}
	select {
	case <-locker.acquired:
		t.Fatal("canceled waiter acquired the application mutation lock")
	default:
	}

	close(updateRelease)
	if err := <-scaleResult; err != nil {
		t.Fatalf("lock holder scale intent: %v", err)
	}

	persisted, err := st.GetApplication(ctx, application.Name)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Replicas != 2 || persisted.Image != application.Image || persisted.DesiredGeneration != application.DesiredGeneration+1 {
		t.Fatalf("canceled waiter changed application state: %#v", persisted)
	}

	close(deploymentRelease)
	if _, err := service.DeployApp(ctx, DeployAppRequest{AppName: application.Name, Image: "nginx:2.0"}); err != nil {
		t.Fatalf("mutation after canceled waiter: %v", err)
	}
	persisted, err = st.GetApplication(ctx, application.Name)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Replicas != 2 || persisted.Image != "nginx:2.0" || persisted.DesiredGeneration != application.DesiredGeneration+2 {
		t.Fatalf("canceled waiter leaked or consumed admission token: %#v", persisted)
	}
}

func TestApplicationAndProjectMutationsRejectPreCanceledAdmission(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mock := swarmfake.New()
	service := NewService(st, mock, "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))

	targetProject, err := service.CreateProject(t.Context(), CreateProjectRequest{Name: "Target", Slug: "target"})
	if err != nil {
		t.Fatal(err)
	}
	application, err := service.CreateApp(t.Context(), CreateAppRequest{Name: "target-app", ProjectSlug: targetProject.Slug, Image: "nginx:1.0"})
	if err != nil {
		t.Fatal(err)
	}
	sourceProject, err := service.CreateProject(t.Context(), CreateProjectRequest{Name: "Source", Slug: "source"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := service.CreateProjectLink(t.Context(), sourceProject.Slug, application.Name, "target.api")
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	newImage := "nginx:2.0"
	operations := []struct {
		name string
		run  func() error
	}{
		{name: "create application", run: func() error {
			_, err := service.CreateApp(canceled, CreateAppRequest{Name: "cancelled-app", Image: "nginx:1.0"})
			return err
		}},
		{name: "delete application", run: func() error {
			_, err := service.DeleteAppIntent(canceled, application.Name, application.DesiredGeneration)
			return err
		}},
		{name: "set run state", run: func() error {
			_, err := service.SetRunState(canceled, application.Name, store.DesiredRunStateStopped, application.DesiredGeneration)
			return err
		}},
		{name: "scale application", run: func() error {
			_, err := service.ScaleAppIntent(canceled, application.Name, 2, application.DesiredGeneration)
			return err
		}},
		{name: "update application", run: func() error {
			_, err := service.UpdateApp(canceled, UpdateAppRequest{AppName: application.Name, ExpectedGeneration: application.DesiredGeneration, Image: &newImage})
			return err
		}},
		{name: "deploy application", run: func() error {
			_, err := service.DeployApp(canceled, DeployAppRequest{AppName: application.Name, Image: newImage})
			return err
		}},
		{name: "create project", run: func() error {
			_, err := service.CreateProject(canceled, CreateProjectRequest{Name: "Cancelled", Slug: "cancelled"})
			return err
		}},
		{name: "delete project", run: func() error {
			return service.DeleteProject(canceled, sourceProject.Slug)
		}},
		{name: "create project link", run: func() error {
			_, err := service.CreateProjectLink(canceled, sourceProject.Slug, application.Name, "other.api")
			return err
		}},
		{name: "delete project link", run: func() error {
			return service.DeleteProjectLink(canceled, sourceProject.Slug, link.ID)
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context cancellation", err)
			}
		})
	}

	persisted, err := st.GetApplication(t.Context(), application.Name)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.DesiredGeneration != application.DesiredGeneration || persisted.Image != application.Image || persisted.DeletionTimestamp != nil {
		t.Fatalf("pre-canceled admission changed application: %#v", persisted)
	}
	projects, err := st.ListProjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	links, err := st.ListProjectLinksByProject(t.Context(), sourceProject.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 3 || len(links) != 1 || links[0].ID != link.ID {
		t.Fatalf("pre-canceled admission changed project resources: projects=%#v links=%#v", projects, links)
	}

	scaled, err := service.ScaleAppIntent(t.Context(), application.Name, 2, application.DesiredGeneration)
	if err != nil {
		t.Fatalf("fresh mutation after pre-canceled admission: %v", err)
	}
	if scaled.Replicas != 2 || scaled.DesiredGeneration != application.DesiredGeneration+1 {
		t.Fatalf("fresh mutation after pre-canceled admission = %#v", scaled)
	}
}

func requireLockSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("%s: %v", description, ctx.Err())
	}
}

func requireOperationSignal(t *testing.T, ctx context.Context, entered <-chan string, expected string) {
	t.Helper()
	select {
	case operation := <-entered:
		if operation != expected {
			t.Fatalf("operation = %q, want %q", operation, expected)
		}
	case <-ctx.Done():
		t.Fatalf("%s operation did not reach persistence: %v", expected, ctx.Err())
	}
}

var _ mutationLocker = (*observedLocker)(nil)
var _ store.Store = (*lockBarrierStore)(nil)
var _ store.Store = (*failOnceUpdateStore)(nil)
