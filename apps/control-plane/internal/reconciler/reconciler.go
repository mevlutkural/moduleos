package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/containerd/errdefs"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

var credentialURL = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)
var secretAssignment = regexp.MustCompile(`(?i)(authorization|password|passwd|token|secret)=([^\s;]+)`)
var bearerCredential = regexp.MustCompile(`(?i)\b(bearer)\s+[^\s,;]+`)

const (
	defaultInterval   = 30 * time.Second
	defaultWorkers    = 4
	defaultMaxBackoff = 30 * time.Second
	lockStripeCount   = 256
)

type retryState struct {
	cancel context.CancelFunc
	token  uint64
}

type stabilityObservation struct {
	generation int64
	since      time.Time
}

type Diagnostics struct {
	OrphanServices []string `json:"orphan_services"`
	OrphanNetworks []string `json:"orphan_networks"`
}

type Reconciler struct {
	swarm         swarm.Client
	store         store.Store
	appSvc        *app.Service
	interval      time.Duration
	workers       int
	maxBackoff    time.Duration
	stabilization time.Duration
	log           *slog.Logger

	queue    chan string
	pending  map[string]struct{}
	queueMu  sync.Mutex
	keyLocks [lockStripeCount]sync.Mutex

	attemptMu     sync.Mutex
	attempts      map[string]int
	retrying      map[string]retryState
	retrySequence uint64

	diagnosticsMu sync.RWMutex
	diagnostics   Diagnostics
	stableMu      sync.Mutex
	stableSince   map[string]stabilityObservation
	running       atomic.Bool
	stopping      atomic.Bool
}

func New(sw swarm.Client, appSvc *app.Service, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.Default()
	}
	return &Reconciler{
		swarm:       sw,
		store:       appSvc.Store(),
		appSvc:      appSvc,
		interval:    defaultInterval,
		workers:     defaultWorkers,
		maxBackoff:  defaultMaxBackoff,
		log:         log,
		queue:       make(chan string, 1024),
		pending:     make(map[string]struct{}),
		attempts:    make(map[string]int),
		retrying:    make(map[string]retryState),
		stableSince: make(map[string]stabilityObservation),
	}
}

func (r *Reconciler) WithInterval(d time.Duration) *Reconciler {
	if d > 0 {
		r.interval = d
	}
	return r
}

func (r *Reconciler) WithWorkers(workers int) *Reconciler {
	if workers > 0 {
		r.workers = workers
	}
	return r
}

func (r *Reconciler) WithMaxBackoff(duration time.Duration) *Reconciler {
	if duration > 0 {
		r.maxBackoff = duration
	}
	return r
}

func (r *Reconciler) WithStabilizationWindow(duration time.Duration) *Reconciler {
	if duration >= 0 {
		r.stabilization = duration
	}
	return r
}

func (r *Reconciler) Enqueue(name string) {
	if name == "" || r.stopping.Load() {
		return
	}
	r.queueMu.Lock()
	if _, exists := r.pending[name]; exists {
		r.queueMu.Unlock()
		return
	}
	r.pending[name] = struct{}{}
	r.queueMu.Unlock()
	select {
	case r.queue <- name:
	default:
		r.queueMu.Lock()
		delete(r.pending, name)
		r.queueMu.Unlock()
		r.log.Error("reconcile queue full", "app", name)
	}
}

func (r *Reconciler) Run(ctx context.Context) {
	if !r.running.CompareAndSwap(false, true) {
		r.log.Warn("reconciler is already running")
		return
	}
	defer r.running.Store(false)
	r.stopping.Store(false)
	r.log.Info("reconciler started", "interval", r.interval, "workers", r.workers)
	var workers sync.WaitGroup
	for range r.workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r.worker(ctx)
		}()
	}
	r.enqueueFullScan(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.stopping.Store(true)
			workers.Wait()
			r.clearQueue()
			r.cancelRetries()
			r.log.Info("reconciler stopped")
			return
		case <-ticker.C:
			r.enqueueFullScan(ctx)
		}
	}
}

func (r *Reconciler) clearQueue() {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	for {
		select {
		case <-r.queue:
		default:
			clear(r.pending)
			return
		}
	}
}

func (r *Reconciler) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case name := <-r.queue:
			r.queueMu.Lock()
			delete(r.pending, name)
			r.queueMu.Unlock()
			lock := r.applicationLock(name)
			lock.Lock()
			err := r.reconcileSafely(ctx, name)
			if err != nil && isRetryable(err) {
				r.scheduleRetry(ctx, name)
			} else {
				r.resetBackoff(name)
			}
			lock.Unlock()
		}
	}
}

func (r *Reconciler) reconcileSafely(ctx context.Context, name string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.log.Error("reconcile panic", "app", name, "panic", recovered)
			err = retryableError("reconcile_panic", fmt.Errorf("panic: %v", recovered))
		}
	}()
	return r.reconcileApplication(ctx, name)
}

func (r *Reconciler) scheduleRetry(ctx context.Context, name string) {
	r.attemptMu.Lock()
	r.attempts[name]++
	attempt := r.attempts[name]
	if _, exists := r.retrying[name]; exists {
		r.attemptMu.Unlock()
		return
	}
	r.retrySequence++
	token := r.retrySequence
	retryCtx, cancel := context.WithCancel(ctx)
	r.retrying[name] = retryState{cancel: cancel, token: token}
	r.attemptMu.Unlock()
	delay := min(time.Second*time.Duration(1<<min(attempt-1, 5)), r.maxBackoff)
	jitter := time.Duration(rand.Int64N(int64(max(delay/4, time.Millisecond))))
	go func() {
		timer := time.NewTimer(delay + jitter)
		defer timer.Stop()
		fired := false
		select {
		case <-retryCtx.Done():
		case <-timer.C:
			fired = true
		}
		r.attemptMu.Lock()
		if state, exists := r.retrying[name]; exists && state.token == token {
			delete(r.retrying, name)
		}
		r.attemptMu.Unlock()
		if fired {
			r.Enqueue(name)
		}
	}()
}

func (r *Reconciler) resetBackoff(name string) {
	r.attemptMu.Lock()
	delete(r.attempts, name)
	state, retrying := r.retrying[name]
	delete(r.retrying, name)
	r.attemptMu.Unlock()
	if retrying {
		state.cancel()
	}
}

func (r *Reconciler) cancelRetries() {
	r.attemptMu.Lock()
	retries := make([]retryState, 0, len(r.retrying))
	for _, state := range r.retrying {
		retries = append(retries, state)
	}
	clear(r.retrying)
	clear(r.attempts)
	r.attemptMu.Unlock()
	for _, state := range retries {
		state.cancel()
	}
}

func (r *Reconciler) enqueueFullScan(ctx context.Context) {
	projects, err := r.store.ListProjects(ctx)
	if err != nil {
		r.log.Error("failed to list projects", "error", err)
	} else {
		r.reconcileProjects(ctx, projects)
	}
	applications, appErr := r.store.ListApplications(ctx)
	if appErr != nil {
		r.log.Error("failed to list applications", "error", appErr)
	} else {
		for _, application := range applications {
			r.Enqueue(application.Name)
		}
	}
	if err == nil && appErr == nil {
		r.detectOrphans(ctx, applications, projects)
	}
}

// Reconcile is the synchronous full-scan entry point used by diagnostics and tests.
func (r *Reconciler) Reconcile(ctx context.Context) {
	projects, err := r.store.ListProjects(ctx)
	if err != nil {
		r.log.Error("failed to list projects", "error", err)
	} else {
		r.reconcileProjects(ctx, projects)
	}
	applications, appErr := r.store.ListApplications(ctx)
	if appErr != nil {
		r.log.Error("failed to list applications", "error", appErr)
	} else {
		for _, application := range applications {
			if err := r.ReconcileApplication(ctx, application.Name); err != nil {
				r.log.Warn("application reconcile failed", "app", application.Name, "error", err)
			}
		}
	}
	if err == nil && appErr == nil {
		r.detectOrphans(ctx, applications, projects)
	}
}

func (r *Reconciler) ReconcileApplication(ctx context.Context, name string) error {
	lock := r.applicationLock(name)
	lock.Lock()
	defer lock.Unlock()
	return r.reconcileApplication(ctx, name)
}

func (r *Reconciler) reconcileApplication(ctx context.Context, name string) error {
	application, err := r.store.GetApplication(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return retryableError("store_read", err)
	}
	generation := application.DesiredGeneration
	if err := r.store.PersistReconcileDiagnostics(ctx, application.ID, generation, "", "", false, 0); err != nil && !errors.Is(err, store.ErrStaleObservation) {
		return retryableError("diagnostic_clear", err)
	}
	serviceName := swarm.ServiceName(application.Name)
	observed, inspectErr := r.swarm.GetService(ctx, serviceName)
	missing := errdefs.IsNotFound(inspectErr)
	if inspectErr != nil && !missing {
		return r.persistFailure(ctx, application, "docker_unavailable", inspectErr, true)
	}

	if application.DeletionTimestamp != nil {
		r.clearStabilization(application.ID)
		if missing {
			if err := r.store.FinalizeApplicationDeletion(ctx, application.ID, generation); err != nil {
				return retryableError("application_finalize", err)
			}
			return nil
		}
		if err := verifyOwnership(observed, application); err != nil {
			return r.persistFailure(ctx, application, "ownership_conflict", err, false)
		}
		if err := r.swarm.RemoveService(ctx, observed.ID); err != nil && !errdefs.IsNotFound(err) {
			return r.persistFailure(ctx, application, "service_remove_failed", err, true)
		}
		if err := r.store.FinalizeApplicationDeletion(ctx, application.ID, generation); err != nil {
			return retryableError("application_finalize", err)
		}
		return nil
	}
	deployment, err := r.deploymentForGeneration(ctx, application.ID, generation)
	if err != nil {
		return r.persistFailure(ctx, application, "deployment_read", err, true)
	}
	if deployment != nil && deployment.Status == store.DeploymentStatusPending {
		if err := r.store.MarkDeploymentState(ctx, deployment.ID, generation, store.DeploymentStatusApplying, "", ""); err != nil {
			if errors.Is(err, store.ErrStaleObservation) {
				r.Enqueue(name)
				return nil
			}
			if errors.Is(err, store.ErrInvalidTransition) {
				deployment, err = r.deploymentForGeneration(ctx, application.ID, generation)
				if err != nil {
					return r.persistFailure(ctx, application, "deployment_read", err, true)
				}
			} else {
				return retryableError("deployment_state_write", err)
			}
		}
	}

	desired, err := r.appSvc.BuildDesiredServiceSpec(ctx, application)
	if err != nil {
		return r.persistFailureWithDeployment(ctx, application, deployment, "invalid_desired_spec", err, false)
	}
	if application.Expose {
		if _, err := r.swarm.GetNetwork(ctx, r.appSvc.IngressNetwork()); err != nil {
			return r.persistFailureWithDeployment(ctx, application, deployment, "ingress_network_missing", err, true)
		}
	}
	if code, dependencyErr, retryable := r.ensureApplicationProjectNetworks(ctx, application, desired); dependencyErr != nil {
		return r.persistFailureWithDeployment(ctx, application, deployment, code, dependencyErr, retryable)
	}

	if missing {
		if err := r.swarm.CreateService(ctx, desired); err != nil {
			return r.persistFailureWithDeployment(ctx, application, deployment, classifyDockerError(err), err, isRetryableMutation(err))
		}
		observed, err = r.swarm.GetService(ctx, serviceName)
		if err != nil {
			return r.persistFailureWithDeployment(ctx, application, deployment, "post_create_inspect", err, true)
		}
	} else {
		if err := verifyOwnership(observed, application); err != nil {
			return r.persistFailureWithDeployment(ctx, application, deployment, "ownership_conflict", err, false)
		}
		fields := swarm.DiffServiceSpec(desired, observed.Spec)
		if len(fields) > 0 {
			r.log.Info("service drift detected", "app", name, "fields", fields)
			if err := r.swarm.UpdateService(ctx, observed.ID, desired); err != nil {
				return r.persistFailureWithDeployment(ctx, application, deployment, classifyDockerError(err), err, isRetryableMutation(err))
			}
			observed, err = r.swarm.GetService(ctx, serviceName)
			if err != nil {
				return r.persistFailureWithDeployment(ctx, application, deployment, "post_update_inspect", err, true)
			}
		}
	}

	if fields := swarm.DiffServiceSpec(desired, observed.Spec); len(fields) > 0 {
		return r.persistFailureWithDeployment(ctx, application, deployment, "service_drift", errors.New(swarm.FormatDiff(fields)), true)
	}
	if len(observed.TaskErrors) > 0 {
		message := fmt.Sprintf("running tasks %d/%d: %s", observed.Running, desired.Replicas, strings.Join(observed.TaskErrors, "; "))
		return r.persistFailureWithDeployment(ctx, application, deployment, "task_rejected", errors.New(message), false)
	}
	if observed.Running != desired.Replicas {
		message := fmt.Sprintf("running tasks %d/%d", observed.Running, desired.Replicas)
		if deployment != nil && deployment.ConvergenceDeadline != nil && time.Now().UTC().After(*deployment.ConvergenceDeadline) {
			return r.persistFailureWithDeployment(ctx, application, deployment, "convergence_timeout", errors.New(message), false)
		}
		return r.persistFailureWithDeployment(ctx, application, deployment, "tasks_not_converged", errors.New(message), true)
	}
	if r.stabilization > 0 && desired.Replicas > 0 {
		r.stableMu.Lock()
		observation, exists := r.stableSince[application.ID]
		if !exists || observation.generation != generation {
			observation = stabilityObservation{generation: generation, since: time.Now().UTC()}
			r.stableSince[application.ID] = observation
		}
		stable := time.Since(observation.since) >= r.stabilization
		if stable {
			delete(r.stableSince, application.ID)
		}
		r.stableMu.Unlock()
		if !stable {
			return r.persistFailureWithDeployment(ctx, application, deployment, "stabilizing", errors.New("tasks are running within stabilization window"), true)
		}
	} else {
		r.clearStabilization(application.ID)
	}
	// Capture the exact link snapshot that may be acknowledged. A link created
	// after this read remains pending even if it races with the final CAS writes.
	links, err := r.store.ListProjectLinksByApp(ctx, application.ID)
	if err != nil {
		return r.persistFailureWithDeployment(ctx, application, deployment, "link_read", err, true)
	}
	current, err := r.store.GetApplication(ctx, name)
	if err != nil {
		return r.persistFailureWithDeployment(ctx, application, deployment, "store_read", err, true)
	}
	if current.DesiredGeneration != generation {
		r.Enqueue(name)
		return nil
	}
	currentDesired, err := r.appSvc.BuildDesiredServiceSpec(ctx, current)
	if err != nil {
		return r.persistFailureWithDeployment(ctx, application, deployment, "invalid_desired_spec", err, false)
	}
	if fields := swarm.DiffServiceSpec(currentDesired, observed.Spec); len(fields) > 0 {
		r.Enqueue(name)
		return nil
	}
	state := store.ObservedStateRunning
	if desired.Replicas == 0 {
		state = store.ObservedStateStopped
	}
	if err := r.store.MarkApplicationObserved(ctx, application.ID, store.ObservedApplicationUpdate{
		Generation:   generation,
		State:        state,
		Image:        observed.Image,
		TransitionAt: time.Now().UTC(),
	}); err != nil {
		if errors.Is(err, store.ErrStaleObservation) {
			return nil
		}
		return retryableError("application_observation_write", err)
	}
	if deployment != nil {
		if err := r.store.MarkDeploymentState(ctx, deployment.ID, generation, store.DeploymentStatusSucceeded, "", ""); err != nil &&
			!errors.Is(err, store.ErrStaleObservation) && !errors.Is(err, store.ErrInvalidTransition) {
			return retryableError("deployment_state_write", err)
		}
	}
	for _, link := range links {
		if err := r.store.MarkProjectLinkObserved(ctx, link.ID, link.DesiredGeneration); err != nil && !errors.Is(err, store.ErrStaleObservation) {
			return retryableError("link_observation_write", err)
		}
	}
	return nil
}

func (r *Reconciler) applicationLock(name string) *sync.Mutex {
	hash := uint32(2166136261)
	for index := range len(name) {
		hash ^= uint32(name[index])
		hash *= 16777619
	}
	return &r.keyLocks[hash%lockStripeCount]
}

func (r *Reconciler) clearStabilization(applicationID string) {
	r.stableMu.Lock()
	delete(r.stableSince, applicationID)
	r.stableMu.Unlock()
}

func (r *Reconciler) ensureApplicationProjectNetworks(ctx context.Context, application *store.Application, desired swarm.ServiceSpec) (string, error, bool) {
	primaryProject, err := r.store.GetProjectByID(ctx, application.ProjectID)
	if err != nil {
		return "network_dependency_read", err, true
	}
	projectsByNetwork := map[string]*store.Project{primaryProject.Network: primaryProject}
	links, err := r.store.ListProjectLinksByApp(ctx, application.ID)
	if err != nil {
		return "network_dependency_read", err, true
	}
	for _, link := range links {
		project, err := r.store.GetProjectByID(ctx, link.SourceProjectID)
		if err != nil {
			return "network_dependency_read", err, true
		}
		projectsByNetwork[project.Network] = project
	}
	for _, attachment := range desired.Networks {
		if application.Expose && attachment.Network == r.appSvc.IngressNetwork() {
			continue
		}
		project, exists := projectsByNetwork[attachment.Network]
		if !exists {
			return "network_dependency_missing", fmt.Errorf("project network %q has no durable owner", attachment.Network), false
		}
		if project.DeletionTimestamp != nil {
			return "network_dependency_deleting", fmt.Errorf("project network %q is being deleted", attachment.Network), false
		}
		if err := r.swarm.EnsureProjectNetwork(ctx, project.Network, project.ID, project.Slug); err != nil {
			retryable := !errors.Is(err, swarm.ErrOwnershipConflict) && !errors.Is(err, swarm.ErrInvalidSpec)
			return "network_dependency_reconcile_failed", err, retryable
		}
		r.markProjectObserved(ctx, project, store.ObservedStateReady, "", "")
	}
	return "", nil, false
}

func (r *Reconciler) reconcileProjects(ctx context.Context, projects []*store.Project) {
	for _, project := range projects {
		if project.DeletionTimestamp != nil {
			network, err := r.swarm.GetNetwork(ctx, project.Network)
			if errdefs.IsNotFound(err) {
				if err := r.store.FinalizeProjectDeletion(ctx, project.ID); err != nil {
					r.log.Error("project deletion finalization failed", "project", project.Slug, "error", err)
				}
				continue
			}
			if err != nil {
				r.markProjectObserved(ctx, project, store.ObservedStateDegraded, "network_inspect_failed", err.Error())
				continue
			}
			if network.Labels[swarm.LabelManagedBy] != "true" || network.Labels[swarm.LabelProjectID] != project.ID {
				r.markProjectObserved(ctx, project, store.ObservedStateFailed, "ownership_conflict", "project network is not owned by ModuleOS")
				continue
			}
			if err := r.swarm.RemoveNetwork(ctx, network.ID); err != nil && !errdefs.IsNotFound(err) {
				r.markProjectObserved(ctx, project, store.ObservedStateDegraded, "network_remove_failed", err.Error())
				continue
			}
			if err := r.store.FinalizeProjectDeletion(ctx, project.ID); err != nil {
				r.log.Error("project deletion finalization failed", "project", project.Slug, "error", err)
			}
			continue
		}
		if err := r.swarm.EnsureProjectNetwork(ctx, project.Network, project.ID, project.Slug); err != nil {
			r.log.Warn("project network reconcile failed", "project", project.Slug, "error", err)
			r.markProjectObserved(ctx, project, store.ObservedStateDegraded, "network_reconcile_failed", err.Error())
		} else {
			r.markProjectObserved(ctx, project, store.ObservedStateReady, "", "")
		}
	}
}

func (r *Reconciler) markProjectObserved(ctx context.Context, project *store.Project, state store.ObservedState, code, message string) {
	if err := r.store.MarkProjectObserved(ctx, project.ID, state, code, safeDiagnostic(message)); err != nil {
		r.log.Error("project observation write failed", "project", project.Slug, "state", state, "error", err)
	}
}

func (r *Reconciler) deploymentForGeneration(ctx context.Context, appID string, generation int64) (*store.Deployment, error) {
	deployments, err := r.store.ListDeployments(ctx, appID)
	if err != nil {
		return nil, err
	}
	for _, deployment := range deployments {
		if deployment.TargetGeneration == generation {
			return deployment, nil
		}
	}
	return nil, nil
}

func (r *Reconciler) persistFailureWithDeployment(ctx context.Context, application *store.Application, deployment *store.Deployment, code string, cause error, retryable bool) error {
	var deploymentErr error
	if deployment != nil && !retryable {
		if err := r.store.MarkDeploymentState(ctx, deployment.ID, application.DesiredGeneration, store.DeploymentStatusFailed, code, safeDiagnostic(cause.Error())); err != nil &&
			!errors.Is(err, store.ErrStaleObservation) && !errors.Is(err, store.ErrInvalidTransition) {
			deploymentErr = fmt.Errorf("mark deployment failed: %w", err)
		}
	}
	return errors.Join(r.persistFailure(ctx, application, code, cause, retryable), deploymentErr)
}

func verifyOwnership(info *swarm.ServiceInfo, application *store.Application) error {
	if info == nil || info.Labels[swarm.LabelManagedBy] != "true" || info.Labels[swarm.LabelAppID] != application.ID {
		return fmt.Errorf("%w: service name %q is not owned by application %s", swarm.ErrOwnershipConflict, swarm.ServiceName(application.Name), application.ID)
	}
	return nil
}

type classifiedError struct {
	code      string
	retryable bool
	err       error
}

func (e *classifiedError) Error() string { return e.code + ": " + e.err.Error() }
func (e *classifiedError) Unwrap() error { return e.err }

func retryableError(code string, err error) error {
	return &classifiedError{code: code, retryable: true, err: err}
}

func isRetryable(err error) bool {
	var classified *classifiedError
	if errors.As(err, &classified) {
		return classified.retryable
	}
	return errdefs.IsUnavailable(err) || errdefs.IsConflict(err) || errors.Is(err, context.DeadlineExceeded)
}

func isRetryableMutation(err error) bool {
	return isRetryable(err) || errdefs.IsNotFound(err)
}

func classifyDockerError(err error) string {
	lower := strings.ToLower(err.Error())
	switch {
	case errdefs.IsNotFound(err):
		return "docker_dependency_missing"
	case errdefs.IsConflict(err):
		return "docker_conflict"
	case errdefs.IsUnavailable(err), errors.Is(err, context.DeadlineExceeded):
		return "docker_unavailable"
	case strings.Contains(lower, "port is already allocated"), strings.Contains(lower, "port is already in use"):
		return "published_port_conflict"
	case strings.Contains(lower, "invalid reference format"):
		return "invalid_image"
	default:
		return "docker_mutation_failed"
	}
}

func (r *Reconciler) persistFailure(ctx context.Context, application *store.Application, code string, err error, retryable bool) error {
	if code != "stabilizing" {
		r.clearStabilization(application.ID)
	}
	r.attemptMu.Lock()
	attempt := r.attempts[application.Name] + 1
	r.attemptMu.Unlock()
	if persistErr := r.store.PersistReconcileDiagnostics(ctx, application.ID, application.DesiredGeneration, code, safeDiagnostic(err.Error()), retryable, attempt); persistErr != nil && !errors.Is(persistErr, store.ErrStaleObservation) {
		return errors.Join(err, persistErr)
	}
	return &classifiedError{code: code, retryable: retryable, err: err}
}

func safeDiagnostic(message string) string {
	message = credentialURL.ReplaceAllString(message, `${1}[redacted]@`)
	message = secretAssignment.ReplaceAllString(message, `${1}=[redacted]`)
	return bearerCredential.ReplaceAllString(message, `${1} [redacted]`)
}

func (r *Reconciler) detectOrphans(ctx context.Context, apps []*store.Application, projects []*store.Project) {
	knownServices := make(map[string]struct{}, len(apps))
	for _, application := range apps {
		knownServices[swarm.ServiceName(application.Name)] = struct{}{}
	}
	knownNetworks := make(map[string]struct{}, len(projects))
	for _, project := range projects {
		knownNetworks[project.Network] = struct{}{}
	}
	r.diagnosticsMu.RLock()
	diagnostics := Diagnostics{
		OrphanServices: append([]string(nil), r.diagnostics.OrphanServices...),
		OrphanNetworks: append([]string(nil), r.diagnostics.OrphanNetworks...),
	}
	r.diagnosticsMu.RUnlock()
	services, err := r.swarm.ListServices(ctx)
	if err == nil {
		diagnostics.OrphanServices = nil
		for _, service := range services {
			if service.Labels[swarm.LabelManagedBy] == "true" {
				if _, exists := knownServices[service.Name]; !exists {
					diagnostics.OrphanServices = append(diagnostics.OrphanServices, service.Name)
				}
			}
		}
	} else {
		r.log.Warn("failed to list services for orphan detection", "error", err)
	}
	networks, err := r.swarm.ListNetworks(ctx)
	if err == nil {
		diagnostics.OrphanNetworks = nil
		for _, network := range networks {
			if network.Labels[swarm.LabelManagedBy] == "true" && network.Labels[swarm.LabelResourceKind] == "project-network" {
				if _, exists := knownNetworks[network.Name]; !exists {
					diagnostics.OrphanNetworks = append(diagnostics.OrphanNetworks, network.Name)
				}
			}
		}
	} else {
		r.log.Warn("failed to list networks for orphan detection", "error", err)
	}
	sort.Strings(diagnostics.OrphanServices)
	sort.Strings(diagnostics.OrphanNetworks)
	r.diagnosticsMu.Lock()
	r.diagnostics = diagnostics
	r.diagnosticsMu.Unlock()
}

func (r *Reconciler) Diagnostics() Diagnostics {
	r.diagnosticsMu.RLock()
	defer r.diagnosticsMu.RUnlock()
	return Diagnostics{
		OrphanServices: append([]string(nil), r.diagnostics.OrphanServices...),
		OrphanNetworks: append([]string(nil), r.diagnostics.OrphanNetworks...),
	}
}
