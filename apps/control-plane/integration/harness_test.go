//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/reconciler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	moduleosswarm "github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

const (
	pollInterval      = 100 * time.Millisecond
	convergenceBudget = 90 * time.Second
	cleanupBudget     = 45 * time.Second
	workerJoinBudget  = 10 * time.Second
	forcedJoinBudget  = 5 * time.Second
)

type resourceKind string

const (
	serviceResource resourceKind = "service"
	networkResource resourceKind = "network"
)

type ownedResource struct {
	kind   resourceKind
	id     string
	name   string
	labels map[string]string
}

type workerHandle struct {
	cancel context.CancelFunc
	done   <-chan struct{}
}

type integrationFixture struct {
	t       *testing.T
	ctx     context.Context
	cancel  context.CancelFunc
	docker  *moduleosswarm.DockerClient
	raw     *client.Client
	store   *store.SQLiteStore
	dbPath  string
	service *app.Service
	rec     *reconciler.Reconciler
	log     *slog.Logger
	mu      sync.Mutex
	owned   []ownedResource
	workers []workerHandle
	closed  bool
}

func newIntegrationFixture(t *testing.T) *integrationFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	dockerClient, err := moduleosswarm.NewDockerClientWithEndpoint(integrationSuite.endpoint)
	if err != nil {
		cancel()
		t.Fatalf("construct real Docker client: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "moduleos.db")
	st, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		cancel()
		_ = dockerClient.Close()
		t.Fatalf("open integration SQLite store: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := app.NewService(st, dockerClient, "moduleos.local", logger).
		WithRuntimeLimits(10, []string{t.TempDir()}, 90*time.Second).
		WithResourceLimits(100, 20).
		WithIngressNetwork("moduleos-integration-ingress").
		WithDockerEndpoint(integrationSuite.endpoint)
	rec := reconciler.New(dockerClient, service, logger).
		WithWorkers(1).
		WithMaxBackoff(time.Second).
		WithStabilizationWindow(0)
	service.WithReconcileQueue(rec)
	fixture := &integrationFixture{
		t:       t,
		ctx:     ctx,
		cancel:  cancel,
		docker:  dockerClient,
		raw:     dockerClient.Client(),
		store:   st,
		dbPath:  dbPath,
		service: service,
		rec:     rec,
		log:     logger,
	}
	t.Cleanup(fixture.cleanup)
	return fixture
}

func (f *integrationFixture) unique(prefix string) string {
	cleanPrefix := strings.NewReplacer("/", "-", "_", "-").Replace(strings.ToLower(prefix))
	cleanPrefix = strings.Trim(cleanPrefix, "-")
	if len(cleanPrefix) > 20 {
		cleanPrefix = cleanPrefix[:20]
	}
	testDigest := sha256.Sum256([]byte(f.t.Name()))
	return fmt.Sprintf("%s-%s-%x", cleanPrefix, integrationSuite.runID, testDigest[:2])
}

func (f *integrationFixture) register(resource ownedResource) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index, existing := range f.owned {
		if existing.kind != resource.kind {
			continue
		}
		sameID := existing.id != "" && resource.id != "" && existing.id == resource.id
		sameName := existing.name != "" && resource.name != "" && existing.name == resource.name
		if sameID || sameName {
			if existing.id == "" && resource.id != "" {
				resource.labels = cloneLabels(resource.labels)
				f.owned[index] = resource
			}
			return
		}
	}
	resource.labels = cloneLabels(resource.labels)
	f.owned = append(f.owned, resource)
}

func (f *integrationFixture) registerManagedService(info *moduleosswarm.ServiceInfo, applicationID string) {
	f.t.Helper()
	f.register(ownedResource{kind: serviceResource, id: info.ID, name: info.Name, labels: map[string]string{
		moduleosswarm.LabelManagedBy:    "true",
		moduleosswarm.LabelResourceKind: "application-service",
		moduleosswarm.LabelAppID:        applicationID,
	}})
}

func (f *integrationFixture) registerManagedNetwork(info *moduleosswarm.NetworkInfo, projectID string) {
	f.t.Helper()
	f.register(ownedResource{kind: networkResource, id: info.ID, name: info.Name, labels: map[string]string{
		moduleosswarm.LabelManagedBy:    "true",
		moduleosswarm.LabelResourceKind: "project-network",
		moduleosswarm.LabelProjectID:    projectID,
	}})
}

func (f *integrationFixture) registerDirectService(id, name string) {
	f.register(ownedResource{kind: serviceResource, id: id, name: name, labels: map[string]string{integrationRunLabel: integrationSuite.runID}})
}

func (f *integrationFixture) registerDirectNetwork(id, name string) {
	f.register(ownedResource{kind: networkResource, id: id, name: name, labels: map[string]string{integrationRunLabel: integrationSuite.runID}})
}

func (f *integrationFixture) cleanup() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	f.mu.Unlock()

	for index := len(f.workers) - 1; index >= 0; index-- {
		f.workers[index].cancel()
	}
	f.cancel()
	if !f.stopWorkers() {
		if f.store != nil {
			if err := f.store.Close(); err != nil {
				f.t.Errorf("close integration SQLite store after worker join failure: %v", err)
			}
			f.store = nil
		}
		return
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	if err := f.discoverManagedRuntimeResources(cleanupCtx); err != nil {
		f.t.Errorf("discover managed resources for cleanup: %v", err)
	}
	f.mu.Lock()
	resources := append([]ownedResource(nil), f.owned...)
	f.mu.Unlock()

	for index := len(resources) - 1; index >= 0; index-- {
		if resources[index].kind != serviceResource {
			continue
		}
		if err := f.removeOwnedService(cleanupCtx, resources[index]); err != nil {
			f.t.Errorf("cleanup service %q: %v", resources[index].name, err)
		}
	}
	for index := len(resources) - 1; index >= 0; index-- {
		if resources[index].kind != networkResource {
			continue
		}
		if err := f.removeOwnedNetwork(cleanupCtx, resources[index]); err != nil {
			f.t.Errorf("cleanup network %q: %v", resources[index].name, err)
		}
	}
	if f.store != nil {
		if err := f.store.Close(); err != nil {
			f.t.Errorf("close integration SQLite store: %v", err)
		}
		f.store = nil
	}
	if err := f.docker.Close(); err != nil {
		f.t.Errorf("close integration Docker client: %v", err)
	}
}

func (f *integrationFixture) discoverManagedRuntimeResources(ctx context.Context) error {
	if f.raw != nil {
		if err := f.discoverRunOwnedRuntimeResources(ctx); err != nil {
			return err
		}
	}
	if f.store == nil {
		return nil
	}
	projects, err := f.store.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("list durable projects: %w", err)
	}
	for _, project := range projects {
		network, getErr := f.docker.GetNetwork(ctx, project.Network)
		if errdefs.IsNotFound(getErr) {
			continue
		}
		if getErr != nil {
			return fmt.Errorf("inspect project network %q: %w", project.Network, getErr)
		}
		if network.Labels[integrationRunLabel] == integrationSuite.runID {
			continue
		}
		if network.Labels[moduleosswarm.LabelManagedBy] != "true" || network.Labels[moduleosswarm.LabelProjectID] != project.ID {
			return fmt.Errorf("project network %q has unexpected ownership", project.Network)
		}
		f.registerManagedNetwork(network, project.ID)
	}

	applications, err := f.store.ListApplications(ctx)
	if err != nil {
		return fmt.Errorf("list durable applications: %w", err)
	}
	for _, application := range applications {
		info, getErr := f.docker.GetService(ctx, moduleosswarm.ServiceName(application.Name))
		if errdefs.IsNotFound(getErr) {
			continue
		}
		if getErr != nil {
			return fmt.Errorf("inspect application service for cleanup registration: %w", getErr)
		}
		if info.Labels[integrationRunLabel] == integrationSuite.runID {
			continue
		}
		if info.Labels[moduleosswarm.LabelManagedBy] != "true" || info.Labels[moduleosswarm.LabelAppID] != application.ID {
			return fmt.Errorf("application service %q has unexpected ownership", info.Name)
		}
		f.registerManagedService(info, application.ID)
	}
	return nil
}

func (f *integrationFixture) discoverRunOwnedRuntimeResources(ctx context.Context) error {
	filters := make(client.Filters)
	filters.Add("label", integrationRunLabel+"="+integrationSuite.runID)
	services, err := f.raw.ServiceList(ctx, client.ServiceListOptions{Filters: filters})
	if err != nil {
		return fmt.Errorf("list run-owned services for cleanup: %w", err)
	}
	for _, service := range services.Items {
		if service.Spec.Annotations.Labels[integrationRunLabel] != integrationSuite.runID {
			return fmt.Errorf("run-owned service query returned mismatched ownership")
		}
		if _, baseline := integrationSuite.baseline.services[service.ID]; baseline {
			continue
		}
		f.registerDirectService(service.ID, service.Spec.Annotations.Name)
	}
	networks, err := f.raw.NetworkList(ctx, client.NetworkListOptions{Filters: filters})
	if err != nil {
		return fmt.Errorf("list run-owned networks for cleanup: %w", err)
	}
	for _, network := range networks.Items {
		if network.Labels[integrationRunLabel] != integrationSuite.runID {
			return fmt.Errorf("run-owned network query returned mismatched ownership")
		}
		if _, baseline := integrationSuite.baseline.networks[network.ID]; baseline {
			continue
		}
		f.registerDirectNetwork(network.ID, network.Name)
	}
	return nil
}

func (f *integrationFixture) removeOwnedService(ctx context.Context, resource ownedResource) error {
	if resource.id == "" {
		return fmt.Errorf("refusing deletion of service %q without an immutable Docker ID", resource.name)
	}
	result, err := f.raw.ServiceInspect(ctx, resource.id, client.ServiceInspectOptions{})
	if isDockerNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect owned service %q: %w", resource.name, err)
	}
	if result.Service.ID != resource.id {
		return fmt.Errorf("refusing deletion because immutable service identity changed")
	}
	if result.Service.Spec.Annotations.Name != resource.name || !labelsContain(result.Service.Spec.Annotations.Labels, resource.labels) {
		return fmt.Errorf("refusing deletion because immutable identity/ownership changed")
	}
	serviceID := result.Service.ID
	if _, err := f.raw.ServiceRemove(ctx, serviceID, client.ServiceRemoveOptions{}); err != nil && !isDockerNotFound(err) {
		return fmt.Errorf("remove exact ID %s: %w", shortID(serviceID), err)
	}
	return pollUntil(ctx, func() (bool, error) {
		_, inspectErr := f.raw.ServiceInspect(ctx, serviceID, client.ServiceInspectOptions{})
		if isDockerNotFound(inspectErr) {
			return true, nil
		}
		return false, inspectErr
	})
}

func (f *integrationFixture) removeOwnedNetwork(ctx context.Context, resource ownedResource) error {
	if resource.id == "" {
		return fmt.Errorf("refusing deletion of network %q without an immutable Docker ID", resource.name)
	}
	return pollUntil(ctx, func() (bool, error) {
		result, inspectErr := f.raw.NetworkInspect(ctx, resource.id, client.NetworkInspectOptions{})
		if isDockerNotFound(inspectErr) {
			return true, nil
		}
		if inspectErr != nil {
			return true, fmt.Errorf("inspect owned network %q: %w", resource.name, inspectErr)
		}
		if result.Network.ID != resource.id {
			return true, fmt.Errorf("refusing deletion because immutable network identity changed")
		}
		if result.Network.Name != resource.name || !labelsContain(result.Network.Labels, resource.labels) {
			return true, fmt.Errorf("refusing deletion because immutable identity/ownership changed")
		}
		networkID := result.Network.ID
		_, removeErr := f.raw.NetworkRemove(ctx, networkID, client.NetworkRemoveOptions{})
		if removeErr == nil || isDockerNotFound(removeErr) {
			return false, nil
		}
		confirmed, confirmErr := f.raw.NetworkInspect(ctx, networkID, client.NetworkInspectOptions{})
		if isDockerNotFound(confirmErr) {
			return true, nil
		}
		if confirmErr != nil {
			return true, fmt.Errorf("confirm exact ID %s after removal error: %w", shortID(networkID), confirmErr)
		}
		if confirmed.Network.ID != networkID || confirmed.Network.Name != resource.name || !labelsContain(confirmed.Network.Labels, resource.labels) {
			return true, fmt.Errorf("refusing deletion because immutable identity/ownership changed")
		}
		if isTransientNetworkRemovalError(removeErr) {
			return false, removeErr
		}
		return true, fmt.Errorf("remove exact ID %s: %w", shortID(networkID), removeErr)
	})
}

func isTransientNetworkRemovalError(err error) bool {
	return errdefs.IsConflict(err) || strings.Contains(strings.ToLower(err.Error()), "active endpoints")
}

func isDockerVersionConflict(err error) bool {
	return errdefs.IsConflict(err) || strings.Contains(strings.ToLower(err.Error()), "update out of sequence")
}

func isDockerNotFound(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return errdefs.IsNotFound(err) || strings.Contains(message, " not found") || strings.Contains(message, "no such ")
}

func labelsContain(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func cloneLabels(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func pollUntil(ctx context.Context, condition func() (bool, error)) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		done, err := condition()
		if done {
			return err
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("%w (last observation: %v)", ctx.Err(), lastErr)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (f *integrationFixture) reconcileApplicationUntilConverged(name string) *store.Application {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, convergenceBudget)
	defer cancel()
	var result *store.Application
	err := pollUntil(ctx, func() (bool, error) {
		if err := f.rec.ReconcileApplication(ctx, name); err != nil {
			return false, err
		}
		application, err := f.store.GetApplication(ctx, name)
		if err != nil {
			return false, err
		}
		if err := f.registerApplicationServiceIfPresent(ctx, application); err != nil {
			return false, err
		}
		result = application
		converged := application.DesiredGeneration == application.ObservedGeneration &&
			(application.ObservedState == store.ObservedStateRunning || application.ObservedState == store.ObservedStateStopped)
		return converged, nil
	})
	if err != nil {
		f.t.Fatalf("application %q did not converge: %v", name, err)
	}
	return result
}

func (f *integrationFixture) registerApplicationServiceIfPresent(ctx context.Context, application *store.Application) error {
	info, err := f.docker.GetService(ctx, moduleosswarm.ServiceName(application.Name))
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect application service for cleanup registration: %w", err)
	}
	if info.Labels[moduleosswarm.LabelManagedBy] != "true" || info.Labels[moduleosswarm.LabelAppID] != application.ID {
		return fmt.Errorf("refusing to register service %q with unexpected ownership", info.Name)
	}
	f.registerManagedService(info, application.ID)
	return nil
}

func (f *integrationFixture) waitApplicationConverged(name string, budget time.Duration) *store.Application {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, budget)
	defer cancel()
	var result *store.Application
	err := pollUntil(ctx, func() (bool, error) {
		application, err := f.store.GetApplication(ctx, name)
		if err != nil {
			return false, err
		}
		result = application
		return application.DesiredGeneration == application.ObservedGeneration &&
			(application.ObservedState == store.ObservedStateRunning || application.ObservedState == store.ObservedStateStopped), nil
	})
	if err != nil {
		f.t.Fatalf("application %q did not converge asynchronously: %v", name, err)
	}
	return result
}

func (f *integrationFixture) createProject(name, slug string) *store.Project {
	f.t.Helper()
	project, err := f.service.CreateProject(f.ctx, app.CreateProjectRequest{Name: name, Slug: slug})
	if err != nil {
		f.t.Fatalf("create project %q: %v", slug, err)
	}
	return project
}

func (f *integrationFixture) reconcileProject(project *store.Project) *moduleosswarm.NetworkInfo {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, convergenceBudget)
	defer cancel()
	var network *moduleosswarm.NetworkInfo
	err := pollUntil(ctx, func() (bool, error) {
		f.rec.Reconcile(ctx)
		observed, err := f.docker.GetNetwork(ctx, project.Network)
		if err != nil {
			return false, err
		}
		network = observed
		persisted, err := f.store.GetProject(ctx, project.Slug)
		if err != nil {
			return false, err
		}
		return persisted.ObservedState == store.ObservedStateReady, nil
	})
	if err != nil {
		f.t.Fatalf("project %q did not converge: %v", project.Slug, err)
	}
	f.registerManagedNetwork(network, project.ID)
	return network
}

func (f *integrationFixture) createApplication(project *store.Project, name, image string, replicas int) *store.Application {
	f.t.Helper()
	application, err := f.service.CreateApp(f.ctx, app.CreateAppRequest{
		Name:        name,
		ProjectSlug: project.Slug,
		Image:       image,
		Replicas:    replicas,
		EnvVars:     map[string]string{"MODULEOS_INTEGRATION": "true"},
	})
	if err != nil {
		f.t.Fatalf("create application %q: %v", name, err)
	}
	return application
}

func (f *integrationFixture) registerApplicationService(application *store.Application) *moduleosswarm.ServiceInfo {
	f.t.Helper()
	info, err := f.docker.GetService(f.ctx, moduleosswarm.ServiceName(application.Name))
	if err != nil {
		f.t.Fatalf("inspect application service %q: %v", application.Name, err)
	}
	f.registerManagedService(info, application.ID)
	return info
}

func (f *integrationFixture) startWorker(run func(context.Context)) context.CancelFunc {
	workerCtx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	f.workers = append(f.workers, workerHandle{cancel: cancel, done: done})
	go func() {
		defer close(done)
		run(workerCtx)
	}()
	return cancel
}

func joinWorkers(ctx context.Context, workers []workerHandle) error {
	for index := len(workers) - 1; index >= 0; index-- {
		select {
		case <-workers[index].done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (f *integrationFixture) stopWorkers() bool {
	joinCtx, cancelJoin := context.WithTimeout(context.Background(), workerJoinBudget)
	err := joinWorkers(joinCtx, f.workers)
	cancelJoin()
	if err == nil {
		return true
	}
	f.t.Errorf("workers did not stop after cancellation: %v", err)
	if f.docker == nil {
		return false
	}
	if closeErr := f.docker.Close(); closeErr != nil {
		f.t.Errorf("force-close Docker client after worker join timeout: %v", closeErr)
	}
	f.docker = nil
	f.raw = nil

	forcedCtx, cancelForced := context.WithTimeout(context.Background(), forcedJoinBudget)
	err = joinWorkers(forcedCtx, f.workers)
	cancelForced()
	if err != nil {
		f.t.Errorf("workers remained active after force-closing Docker client: %v", err)
		return false
	}

	replacement, replacementErr := moduleosswarm.NewDockerClientWithEndpoint(integrationSuite.endpoint)
	if replacementErr != nil {
		f.t.Errorf("construct independent Docker client for cleanup: %v", replacementErr)
		return false
	}
	f.docker = replacement
	f.raw = replacement.Client()
	return true
}

func TestCleanupDiscoveryToleratesUnavailableStore(t *testing.T) {
	fixture := &integrationFixture{}
	if err := fixture.discoverManagedRuntimeResources(t.Context()); err != nil {
		t.Fatalf("discoverManagedRuntimeResources() error = %v, want nil", err)
	}
}

func TestCleanupRefusesResourcesWithoutImmutableIDs(t *testing.T) {
	fixture := &integrationFixture{}
	service := ownedResource{kind: serviceResource, name: "moduleos_unresolved"}
	if err := fixture.removeOwnedService(t.Context(), service); err == nil {
		t.Fatal("removeOwnedService() succeeded without an immutable Docker ID")
	}
	network := ownedResource{kind: networkResource, name: "moduleos-unresolved-net"}
	if err := fixture.removeOwnedNetwork(t.Context(), network); err == nil {
		t.Fatal("removeOwnedNetwork() succeeded without an immutable Docker ID")
	}
}

func TestTransientNetworkRemovalClassification(t *testing.T) {
	if !isTransientNetworkRemovalError(fmt.Errorf("%w: network version conflict", errdefs.ErrConflict)) {
		t.Fatal("typed conflict was not classified as transient")
	}
	if !isTransientNetworkRemovalError(errors.New("network has active endpoints")) {
		t.Fatal("active-endpoint transition was not classified as transient")
	}
	if isTransientNetworkRemovalError(errors.New("permission denied")) {
		t.Fatal("terminal removal error was classified as transient")
	}
}

func TestDockerVersionConflictClassification(t *testing.T) {
	if !isDockerVersionConflict(fmt.Errorf("%w: service version conflict", errdefs.ErrConflict)) {
		t.Fatal("typed service conflict was not recognized")
	}
	if !isDockerVersionConflict(errors.New("rpc error: update out of sequence")) {
		t.Fatal("Docker sequence conflict was not recognized")
	}
	if isDockerVersionConflict(errors.New("permission denied")) {
		t.Fatal("terminal update error was classified as a version conflict")
	}
}

func TestDockerNotFoundClassification(t *testing.T) {
	if !isDockerNotFound(fmt.Errorf("%w: missing network", errdefs.ErrNotFound)) {
		t.Fatal("typed not-found error was not recognized")
	}
	if !isDockerNotFound(errors.New("Error response from daemon: network abc not found")) {
		t.Fatal("Docker network not-found response was not recognized")
	}
	if isDockerNotFound(errors.New("permission denied")) {
		t.Fatal("terminal error was classified as not found")
	}
}

func (f *integrationFixture) reopenStore(client moduleosswarm.Client) {
	f.t.Helper()
	if f.store != nil {
		if err := f.store.Close(); err != nil {
			f.t.Fatalf("close store before restart: %v", err)
		}
		f.store = nil
	}
	reopened, err := store.NewSQLiteStore(f.dbPath)
	if err != nil {
		f.t.Fatalf("reopen store after simulated restart: %v", err)
	}
	f.store = reopened
	f.service = app.NewService(reopened, client, "moduleos.local", f.log).
		WithRuntimeLimits(10, nil, 90*time.Second).
		WithResourceLimits(100, 20).
		WithIngressNetwork("moduleos-integration-ingress").
		WithDockerEndpoint(integrationSuite.endpoint)
	f.rec = reconciler.New(client, f.service, f.log).
		WithWorkers(1).
		WithMaxBackoff(time.Second).
		WithStabilizationWindow(0)
	f.service.WithReconcileQueue(f.rec)
}

func createDirectNetwork(ctx context.Context, f *integrationFixture, name string, labels map[string]string) string {
	f.t.Helper()
	labels = cloneLabels(labels)
	labels[integrationRunLabel] = integrationSuite.runID
	result, err := f.raw.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "overlay", Attachable: true, Labels: labels})
	if err != nil {
		f.t.Fatalf("create direct network %q: %v", name, err)
	}
	f.registerDirectNetwork(result.ID, name)
	return result.ID
}

func createDirectService(ctx context.Context, f *integrationFixture, name, image string, replicas uint64, networks []swarm.NetworkAttachmentConfig, labels map[string]string, command []string) string {
	f.t.Helper()
	labels = cloneLabels(labels)
	labels[integrationRunLabel] = integrationSuite.runID
	result, err := f.raw.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: name, Labels: labels},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{Image: image, Command: command},
			Networks:      networks,
		},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}},
	}})
	if err != nil {
		f.t.Fatalf("create direct service %q: %v", name, err)
	}
	f.registerDirectService(result.ID, name)
	return result.ID
}

func waitRunningTask(ctx context.Context, dockerClient *client.Client, serviceID string) (swarm.Task, error) {
	filters := make(client.Filters)
	filters.Add("service", serviceID)
	var result swarm.Task
	err := pollUntil(ctx, func() (bool, error) {
		tasks, err := dockerClient.TaskList(ctx, client.TaskListOptions{Filters: filters})
		if err != nil {
			return false, err
		}
		for _, task := range tasks.Items {
			if task.DesiredState == swarm.TaskStateRunning && task.Status.State == swarm.TaskStateRunning {
				result = task
				return true, nil
			}
		}
		return false, nil
	})
	return result, err
}

func sortedNetworkTargets(networks []swarm.NetworkAttachmentConfig) []string {
	result := make([]string, 0, len(networks))
	for _, network := range networks {
		result = append(result, network.Target)
	}
	sort.Strings(result)
	return result
}

func requireErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want errors.Is(_, %v)", err, target)
	}
}
