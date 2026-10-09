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
	mutex     sync.Mutex
	attempted chan struct{}
	acquired  chan struct{}
}

func newObservedLocker() *observedLocker {
	return &observedLocker{
		attempted: make(chan struct{}, 4),
		acquired:  make(chan struct{}, 4),
	}
}

func (l *observedLocker) Lock() {
	l.attempted <- struct{}{}
	l.mutex.Lock()
	l.acquired <- struct{}{}
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

var _ sync.Locker = (*observedLocker)(nil)
var _ store.Store = (*lockBarrierStore)(nil)
var _ store.Store = (*failOnceUpdateStore)(nil)
