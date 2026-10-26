//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/reconciler"
	moduleosswarm "github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/watcher"
	dockerswarm "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

func TestOwnershipConflictFailsClosedAgainstSwarm(t *testing.T) {
	f := newIntegrationFixture(t)
	project := f.createProject("Ownership", f.unique("owner"))
	f.reconcileProject(project)
	application := f.createApplication(project, f.unique("conflict"), qualificationImage, 1)
	serviceName := moduleosswarm.ServiceName(application.Name)
	foreignID := "foreign-" + integrationSuite.runID
	directID := createDirectService(f.ctx, f, serviceName, qualificationImage, 0, nil, map[string]string{
		moduleosswarm.LabelManagedBy: "true",
		moduleosswarm.LabelAppID:     foreignID,
	}, []string{"sleep", "3600"})
	before, err := f.raw.ServiceInspect(f.ctx, directID, client.ServiceInspectOptions{})
	if err != nil {
		t.Fatalf("inspect foreign service before reconciliation: %v", err)
	}

	err = f.rec.ReconcileApplication(f.ctx, application.Name)
	if err == nil {
		t.Fatal("ownership-conflicting service reconciliation unexpectedly succeeded")
	}
	persisted, err := f.store.GetApplication(f.ctx, application.Name)
	if err != nil {
		t.Fatalf("read application diagnostics: %v", err)
	}
	if persisted.ReconcileErrorCode != "ownership_conflict" || persisted.ReconcileRetryable {
		t.Fatalf("ownership diagnostics = code %q retryable %v", persisted.ReconcileErrorCode, persisted.ReconcileRetryable)
	}
	after, err := f.raw.ServiceInspect(f.ctx, directID, client.ServiceInspectOptions{})
	if err != nil {
		t.Fatalf("foreign service was removed: %v", err)
	}
	if before.Service.ID != after.Service.ID || before.Service.Version.Index != after.Service.Version.Index {
		t.Fatalf("foreign service changed: before version=%d after=%d", before.Service.Version.Index, after.Service.Version.Index)
	}
	if after.Service.Spec.Annotations.Labels[moduleosswarm.LabelAppID] != foreignID {
		t.Fatalf("foreign service was relabeled: %#v", after.Service.Spec.Annotations.Labels)
	}
}

func TestOrphanDiagnosticsPreserveRuntimeResources(t *testing.T) {
	f := newIntegrationFixture(t)
	managedServiceName := f.unique("moduleos-orphan")
	managedServiceID := createDirectService(f.ctx, f, managedServiceName, qualificationImage, 0, nil, map[string]string{
		moduleosswarm.LabelManagedBy:    "true",
		moduleosswarm.LabelAppID:        "orphan-app-" + integrationSuite.runID,
		moduleosswarm.LabelResourceKind: "application-service",
	}, []string{"sleep", "3600"})
	managedNetworkName := f.unique("orphan-network")
	managedNetworkID := createDirectNetwork(f.ctx, f, managedNetworkName, map[string]string{
		moduleosswarm.LabelManagedBy:    "true",
		moduleosswarm.LabelProjectID:    "orphan-project-" + integrationSuite.runID,
		moduleosswarm.LabelResourceKind: "project-network",
	})
	foreignServiceName := f.unique("foreign")
	foreignServiceID := createDirectService(f.ctx, f, foreignServiceName, qualificationImage, 0, nil, nil, []string{"sleep", "3600"})
	foreignNetworkName := f.unique("foreign-network")
	foreignNetworkID := createDirectNetwork(f.ctx, f, foreignNetworkName, nil)

	f.rec.Reconcile(f.ctx)
	first := f.rec.Diagnostics()
	f.rec.Reconcile(f.ctx)
	second := f.rec.Diagnostics()
	if !slices.Equal(first.OrphanServices, second.OrphanServices) || !slices.Equal(first.OrphanNetworks, second.OrphanNetworks) {
		t.Fatalf("orphan diagnostics were not stable: first=%#v second=%#v", first, second)
	}
	if !slices.Contains(first.OrphanServices, managedServiceName) || !slices.Contains(first.OrphanNetworks, managedNetworkName) {
		t.Fatalf("managed orphan resources missing from diagnostics: %#v", first)
	}
	if slices.Contains(first.OrphanServices, foreignServiceName) || slices.Contains(first.OrphanNetworks, foreignNetworkName) {
		t.Fatalf("foreign resources were misclassified as orphans: %#v", first)
	}
	for _, check := range []struct {
		kind resourceKind
		id   string
	}{
		{serviceResource, managedServiceID}, {serviceResource, foreignServiceID},
		{networkResource, managedNetworkID}, {networkResource, foreignNetworkID},
	} {
		if check.kind == serviceResource {
			if _, err := f.raw.ServiceInspect(f.ctx, check.id, client.ServiceInspectOptions{}); err != nil {
				t.Fatalf("orphan/foreign service %s was not preserved: %v", shortID(check.id), err)
			}
		} else if _, err := f.raw.NetworkInspect(f.ctx, check.id, client.NetworkInspectOptions{}); err != nil {
			t.Fatalf("orphan/foreign network %s was not preserved: %v", shortID(check.id), err)
		}
	}
}

type observedEventClient struct {
	moduleosswarm.Client
	once       sync.Once
	subscribed chan struct{}
}

func (c *observedEventClient) WatchEvents(ctx context.Context) (<-chan moduleosswarm.SwarmEvent, <-chan error) {
	events, errs := c.Client.WatchEvents(ctx)
	c.once.Do(func() { close(c.subscribed) })
	return events, errs
}

type observedQueue struct {
	delegate *reconciler.Reconciler
	names    chan string
}

func (q *observedQueue) Enqueue(name string) {
	select {
	case q.names <- name:
	default:
	}
	q.delegate.Enqueue(name)
}

func TestEventTriggeredRepairAgainstRealSwarm(t *testing.T) {
	f := newIntegrationFixture(t)
	project := f.createProject("Event", f.unique("event-project"))
	f.reconcileProject(project)
	application := f.createApplication(project, f.unique("event-app"), qualificationImage, 1)
	f.reconcileApplicationUntilConverged(application.Name)
	runtimeService := f.registerApplicationService(application)

	observedClient := &observedEventClient{Client: f.docker, subscribed: make(chan struct{})}
	service := app.NewService(f.store, observedClient, "moduleos.local", f.log).
		WithRuntimeLimits(10, nil, 90*time.Second).
		WithResourceLimits(100, 20).
		WithIngressNetwork("moduleos-integration-ingress").
		WithDockerEndpoint(integrationSuite.endpoint)
	rec := reconciler.New(observedClient, service, f.log).
		WithWorkers(1).
		WithInterval(time.Hour).
		WithMaxBackoff(time.Second).
		WithStabilizationWindow(0)
	queue := &observedQueue{delegate: rec, names: make(chan string, 32)}
	service.WithReconcileQueue(rec)
	eventWatcher := watcher.New(observedClient, queue, f.log).WithBackoff(100*time.Millisecond, time.Second)

	before, err := f.store.GetApplication(f.ctx, application.Name)
	if err != nil {
		t.Fatal(err)
	}
	f.startWorker(rec.Run)
	initialCtx, initialCancel := context.WithTimeout(f.ctx, 15*time.Second)
	err = pollUntil(initialCtx, func() (bool, error) {
		current, readErr := f.store.GetApplication(initialCtx, application.Name)
		if readErr != nil {
			return false, readErr
		}
		return current.LastReconciledAt != nil && (before.LastReconciledAt == nil || current.LastReconciledAt.After(*before.LastReconciledAt)), nil
	})
	initialCancel()
	if err != nil {
		t.Fatalf("initial asynchronous full scan did not complete: %v", err)
	}
	f.startWorker(eventWatcher.Run)
	select {
	case <-observedClient.subscribed:
	case <-time.After(5 * time.Second):
		t.Fatal("real Docker event subscription was not established")
	}

	introduceReplicaDrift(t, f.ctx, f.raw, runtimeService.ID, 0)
	eventCtx, eventCancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer eventCancel()
	seen := false
	for !seen {
		select {
		case name := <-queue.names:
			seen = name == application.Name
		case <-eventCtx.Done():
			t.Fatalf("watcher did not enqueue application from a real Docker event: %v", eventCtx.Err())
		}
	}
	if err := waitForDesiredRuntime(eventCtx, f, application.Name); err != nil {
		t.Fatalf("event-triggered repair did not converge: %v", err)
	}
}

func TestPeriodicRecoveryRepairsMissedEventDrift(t *testing.T) {
	f := newIntegrationFixture(t)
	project := f.createProject("Periodic", f.unique("periodic-project"))
	f.reconcileProject(project)
	application := f.createApplication(project, f.unique("periodic-app"), qualificationImage, 1)
	f.reconcileApplicationUntilConverged(application.Name)
	runtimeService := f.registerApplicationService(application)

	service := app.NewService(f.store, f.docker, "moduleos.local", f.log).
		WithRuntimeLimits(10, nil, 90*time.Second).
		WithResourceLimits(100, 20).
		WithIngressNetwork("moduleos-integration-ingress").
		WithDockerEndpoint(integrationSuite.endpoint)
	rec := reconciler.New(f.docker, service, f.log).
		WithWorkers(1).
		WithInterval(250 * time.Millisecond).
		WithMaxBackoff(time.Second).
		WithStabilizationWindow(0)
	service.WithReconcileQueue(rec)
	before, err := f.store.GetApplication(f.ctx, application.Name)
	if err != nil {
		t.Fatal(err)
	}
	f.startWorker(rec.Run)
	initialCtx, initialCancel := context.WithTimeout(f.ctx, 15*time.Second)
	err = pollUntil(initialCtx, func() (bool, error) {
		current, readErr := f.store.GetApplication(initialCtx, application.Name)
		if readErr != nil {
			return false, readErr
		}
		return current.LastReconciledAt != nil && (before.LastReconciledAt == nil || current.LastReconciledAt.After(*before.LastReconciledAt)), nil
	})
	initialCancel()
	if err != nil {
		t.Fatalf("initial periodic full scan did not complete: %v", err)
	}

	introduceReplicaDrift(t, f.ctx, f.raw, runtimeService.ID, 0)
	recoveryCtx, recoveryCancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer recoveryCancel()
	if err := waitForDesiredRuntime(recoveryCtx, f, application.Name); err != nil {
		t.Fatalf("periodic recovery did not converge without watcher/manual enqueue: %v", err)
	}
}

func introduceReplicaDrift(t *testing.T, ctx context.Context, dockerClient *client.Client, serviceID string, replicas uint64) {
	t.Helper()
	result, err := dockerClient.ServiceInspect(ctx, serviceID, client.ServiceInspectOptions{})
	if err != nil {
		t.Fatalf("inspect service before drift: %v", err)
	}
	if result.Service.Spec.Mode.Replicated == nil {
		t.Fatal("service is not replicated")
	}
	result.Service.Spec.Mode.Replicated.Replicas = &replicas
	if _, err := dockerClient.ServiceUpdate(ctx, serviceID, client.ServiceUpdateOptions{Version: result.Service.Version, Spec: result.Service.Spec}); err != nil {
		t.Fatalf("introduce direct replica drift: %v", err)
	}
}

func waitForDesiredRuntime(ctx context.Context, f *integrationFixture, appName string) error {
	return pollUntil(ctx, func() (bool, error) {
		application, err := f.store.GetApplication(ctx, appName)
		if err != nil {
			return false, err
		}
		runtimeService, err := f.docker.GetService(ctx, moduleosswarm.ServiceName(appName))
		if err != nil {
			return false, err
		}
		desired, err := f.service.BuildDesiredServiceSpec(ctx, application)
		if err != nil {
			return false, err
		}
		if fields := moduleosswarm.DiffServiceSpec(desired, runtimeService.Spec); len(fields) != 0 {
			return false, fmt.Errorf("runtime drift remains: %v", fields)
		}
		return application.ObservedGeneration == application.DesiredGeneration, nil
	})
}

func serviceNetworksByID(spec dockerswarm.ServiceSpec) []string {
	return sortedNetworkTargets(spec.TaskTemplate.Networks)
}
