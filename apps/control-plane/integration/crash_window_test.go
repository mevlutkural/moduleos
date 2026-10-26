//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/reconciler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	moduleosswarm "github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

type crashPoint string

const (
	crashAfterServiceUpdate crashPoint = "after_service_update"
	crashAfterServiceRemove crashPoint = "after_service_remove"
)

type crashSentinel struct {
	point crashPoint
}

type crashWindowClient struct {
	moduleosswarm.Client
	point    crashPoint
	sentinel *crashSentinel
}

func (c *crashWindowClient) UpdateService(ctx context.Context, serviceID string, spec moduleosswarm.ServiceSpec) error {
	if err := c.Client.UpdateService(ctx, serviceID, spec); err != nil {
		return err
	}
	if c.point == crashAfterServiceUpdate {
		panic(c.sentinel)
	}
	return nil
}

func (c *crashWindowClient) RemoveService(ctx context.Context, serviceID string) error {
	if err := c.Client.RemoveService(ctx, serviceID); err != nil {
		return err
	}
	if c.point == crashAfterServiceRemove {
		panic(c.sentinel)
	}
	return nil
}

func TestCrashWindowPersistedIntentSurvivesRestartWithoutQueueDelivery(t *testing.T) {
	f := newIntegrationFixture(t)
	project := f.createProject("Persisted intent", f.unique("persisted"))
	f.reconcileProject(project)
	application := f.createApplication(project, f.unique("pending-app"), qualificationImage, 1)
	if err := requireServiceAbsent(f.ctx, f.docker, application.Name); err != nil {
		t.Fatalf("runtime was created before queue delivery: %v", err)
	}
	before, err := f.store.GetApplication(f.ctx, application.Name)
	if err != nil {
		t.Fatalf("read persisted application before restart: %v", err)
	}
	if before.DesiredGeneration <= before.ObservedGeneration {
		t.Fatalf("intent was already observed before restart: desired=%d observed=%d", before.DesiredGeneration, before.ObservedGeneration)
	}

	f.reopenStore(f.docker)
	after, err := f.store.GetApplication(f.ctx, application.Name)
	if err != nil {
		t.Fatalf("read persisted application after restart: %v", err)
	}
	if after.ID != application.ID || after.DesiredGeneration != before.DesiredGeneration {
		t.Fatalf("persisted identity/generation changed across restart: before=%#v after=%#v", before, after)
	}
	converged := f.reconcileApplicationUntilConverged(application.Name)
	if converged.ObservedGeneration != converged.DesiredGeneration || converged.ObservedState != store.ObservedStateRunning {
		t.Fatalf("startup reconciliation did not recover persisted intent: %#v", converged)
	}
}

func TestCrashWindowRuntimeUpdateSurvivesRestartBeforeObservedWrite(t *testing.T) {
	f := newIntegrationFixture(t)
	project := f.createProject("Update crash", f.unique("update-crash"))
	f.reconcileProject(project)
	application := f.createApplication(project, f.unique("update-app"), qualificationImage, 1)
	converged := f.reconcileApplicationUntilConverged(application.Name)
	runtimeService := f.registerApplicationService(application)

	updated, err := f.service.ScaleAppIntentIfMatch(f.ctx, application.Name, 2, application.ID, converged.DesiredGeneration)
	if err != nil {
		t.Fatalf("persist scale intent: %v", err)
	}
	sentinel := &crashSentinel{point: crashAfterServiceUpdate}
	crashingClient := &crashWindowClient{Client: f.docker, point: crashAfterServiceUpdate, sentinel: sentinel}
	crashingService, crashingReconciler := crashRuntime(f, crashingClient)
	_ = crashingService
	requireExactCrash(t, sentinel, func() {
		_ = crashingReconciler.ReconcileApplication(f.ctx, application.Name)
	})

	runtimeAfterCrash, err := f.docker.GetService(f.ctx, runtimeService.ID)
	if err != nil {
		t.Fatalf("inspect service after crash-window update: %v", err)
	}
	if runtimeAfterCrash.Spec.Replicas != 2 {
		t.Fatalf("runtime update was not applied before crash: replicas=%d", runtimeAfterCrash.Spec.Replicas)
	}
	stale, err := f.store.GetApplication(f.ctx, application.Name)
	if err != nil {
		t.Fatalf("read stale observation after crash: %v", err)
	}
	if stale.ObservedGeneration != converged.ObservedGeneration || stale.ObservedGeneration == updated.DesiredGeneration {
		t.Fatalf("observation advanced despite crash: before=%d after=%d desired=%d", converged.ObservedGeneration, stale.ObservedGeneration, updated.DesiredGeneration)
	}

	f.reopenStore(f.docker)
	recovered := f.reconcileApplicationUntilConverged(application.Name)
	if recovered.ObservedGeneration != updated.DesiredGeneration || recovered.Replicas != 2 {
		t.Fatalf("restart did not adopt already-applied runtime update: %#v", recovered)
	}
	desired, err := f.service.BuildDesiredServiceSpec(f.ctx, recovered)
	if err != nil {
		t.Fatalf("build recovered desired spec: %v", err)
	}
	runtimeAfterRecovery, err := f.docker.GetService(f.ctx, runtimeService.ID)
	if err != nil {
		t.Fatalf("inspect recovered service: %v", err)
	}
	if fields := moduleosswarm.DiffServiceSpec(desired, runtimeAfterRecovery.Spec); len(fields) != 0 {
		t.Fatalf("runtime drift remains after update crash recovery: %v", fields)
	}
}

func TestCrashWindowRuntimeRemovalSurvivesRestartBeforeFinalizer(t *testing.T) {
	f := newIntegrationFixture(t)
	project := f.createProject("Removal crash", f.unique("remove-crash"))
	f.reconcileProject(project)
	application := f.createApplication(project, f.unique("remove-app"), qualificationImage, 1)
	converged := f.reconcileApplicationUntilConverged(application.Name)
	f.registerApplicationService(application)

	deleting, err := f.service.DeleteAppIntentIfMatch(f.ctx, application.Name, application.ID, converged.DesiredGeneration)
	if err != nil {
		t.Fatalf("persist deletion intent: %v", err)
	}
	sentinel := &crashSentinel{point: crashAfterServiceRemove}
	crashingClient := &crashWindowClient{Client: f.docker, point: crashAfterServiceRemove, sentinel: sentinel}
	_, crashingReconciler := crashRuntime(f, crashingClient)
	requireExactCrash(t, sentinel, func() {
		_ = crashingReconciler.ReconcileApplication(f.ctx, application.Name)
	})
	if err := requireServiceAbsent(f.ctx, f.docker, application.Name); err != nil {
		t.Fatalf("service removal did not complete before crash: %v", err)
	}
	pending, err := f.store.GetApplication(f.ctx, application.Name)
	if err != nil {
		t.Fatalf("application was finalized despite crash: %v", err)
	}
	if pending.DeletionTimestamp == nil || pending.DesiredGeneration != deleting.DesiredGeneration {
		t.Fatalf("deletion intent was not preserved: %#v", pending)
	}

	f.reopenStore(f.docker)
	recoveryCtx, recoveryCancel := context.WithTimeout(f.ctx, convergenceBudget)
	defer recoveryCancel()
	err = pollUntil(recoveryCtx, func() (bool, error) {
		reconcileErr := f.rec.ReconcileApplication(recoveryCtx, application.Name)
		_, readErr := f.store.GetApplication(recoveryCtx, application.Name)
		if errors.Is(readErr, store.ErrNotFound) {
			return true, nil
		}
		if readErr != nil {
			return false, readErr
		}
		return false, reconcileErr
	})
	if err != nil {
		t.Fatalf("deletion finalizer did not recover after restart: %v", err)
	}
}

func crashRuntime(f *integrationFixture, runtime moduleosswarm.Client) (*app.Service, *reconciler.Reconciler) {
	service := app.NewService(f.store, runtime, "moduleos.local", f.log).
		WithRuntimeLimits(10, nil, 90*time.Second).
		WithResourceLimits(100, 20).
		WithIngressNetwork("moduleos-integration-ingress").
		WithDockerEndpoint(integrationSuite.endpoint)
	rec := reconciler.New(runtime, service, f.log).
		WithWorkers(1).
		WithMaxBackoff(time.Second).
		WithStabilizationWindow(0)
	service.WithReconcileQueue(rec)
	return service, rec
}

func requireExactCrash(t *testing.T, sentinel *crashSentinel, operation func()) {
	t.Helper()
	recovered := false
	func() {
		defer func() {
			value := recover()
			if value == nil {
				return
			}
			if value != sentinel {
				panic(value)
			}
			recovered = true
		}()
		operation()
	}()
	if !recovered {
		t.Fatalf("expected exact simulated crash at %s", sentinel.point)
	}
}

func requireNotFound(t *testing.T, err error) {
	t.Helper()
	if !errdefs.IsNotFound(err) {
		t.Fatalf("error = %v, want Docker not found", err)
	}
}

func (c *crashSentinel) Error() string {
	return fmt.Sprintf("simulated process crash at %s", c.point)
}
