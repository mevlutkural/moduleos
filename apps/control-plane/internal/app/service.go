package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

type Service struct {
	resourceMu        sync.Mutex
	store             store.Store
	swarm             swarm.Client
	baseDomain        string
	ingressNetwork    string
	log               *slog.Logger
	queue             ReconcileQueue
	allowedMountRoots []string
	maxReplicas       int
	maxApplications   int
	maxProjects       int
	deploymentTimeout time.Duration
}

const (
	maxEnvironmentEntries = 200
	maxVolumeEntries      = 20
	maxPortEntries        = 20
)

// ReconcileQueue is the only runtime side-effect requested by application
// mutations. The durable database intent remains correct even when enqueueing
// is skipped by a crash; the reconciler's full scan will recover it.
type ReconcileQueue interface {
	Enqueue(name string)
}

func NewService(st store.Store, sw swarm.Client, baseDomain string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:             st,
		swarm:             sw,
		baseDomain:        baseDomain,
		ingressNetwork:    "moduleos-ingress",
		log:               log,
		maxReplicas:       20,
		maxApplications:   100,
		maxProjects:       20,
		deploymentTimeout: 5 * time.Minute,
	}
}

func (s *Service) WithRuntimeLimits(maxReplicas int, allowedMountRoots []string, deploymentTimeout time.Duration) *Service {
	if maxReplicas > 0 {
		s.maxReplicas = maxReplicas
	}
	s.allowedMountRoots = append([]string(nil), allowedMountRoots...)
	if deploymentTimeout > 0 {
		s.deploymentTimeout = deploymentTimeout
	}
	return s
}

func (s *Service) WithResourceLimits(maxApplications, maxProjects int) *Service {
	if maxApplications > 0 {
		s.maxApplications = maxApplications
	}
	if maxProjects > 0 {
		s.maxProjects = maxProjects
	}
	return s
}

func (s *Service) WithIngressNetwork(network string) *Service {
	if network != "" {
		s.ingressNetwork = network
	}
	return s
}

func (s *Service) WithReconcileQueue(queue ReconcileQueue) *Service {
	s.queue = queue
	return s
}

func (s *Service) enqueue(name string) {
	if s.queue != nil {
		s.queue.Enqueue(name)
	}
}

func (s *Service) Store() store.Store {
	return s.store
}

func (s *Service) IngressNetwork() string {
	return s.ingressNetwork
}

func (s *Service) BuildDesiredServiceSpec(ctx context.Context, application *store.Application) (swarm.ServiceSpec, error) {
	return s.buildDesiredServiceSpec(ctx, application)
}

// CreateAppRequest holds the information needed to create a new application.
type CreateAppRequest struct {
	Name                 string
	ProjectSlug          string // defaults to "root" if empty
	Image                string
	Replicas             int
	EnvVars              map[string]string
	Ports                []swarm.PortConfig
	Volumes              []swarm.VolumeConfig
	Expose               bool
	IngressContainerPort uint32
	SourceType           store.SourceType
}

// CreateApp persists desired state. Docker convergence is exclusively owned by
// the reconciler, so a successful return never claims runtime readiness.
func (s *Service) CreateApp(ctx context.Context, req CreateAppRequest) (*store.Application, error) {
	if err := validateName(req.Name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Image) == "" {
		return nil, fmt.Errorf("%w: image is required", store.ErrInvalidData)
	}
	if req.SourceType != "" && req.SourceType != store.SourceTypeImage {
		return nil, fmt.Errorf("%w: only image deployments are supported", store.ErrInvalidData)
	}
	if req.Replicas < 0 {
		return nil, fmt.Errorf("%w: replicas cannot be negative", store.ErrInvalidData)
	}

	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()

	if _, err := s.store.GetApplication(ctx, req.Name); err == nil {
		return nil, fmt.Errorf("%w: application name already exists", store.ErrConflict)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("check application name availability: %w", err)
	}
	applications, err := s.store.ListApplications(ctx)
	if err != nil {
		return nil, fmt.Errorf("list applications for resource limit: %w", err)
	}
	if len(applications) >= s.maxApplications {
		return nil, fmt.Errorf("%w: application limit reached", store.ErrConflict)
	}

	// fall back to root project if none specified
	projectSlug := req.ProjectSlug
	if projectSlug == "" {
		projectSlug = "root"
	}

	project, err := s.store.GetProject(ctx, projectSlug)
	if err != nil {
		return nil, fmt.Errorf("project not found '%s': %w", projectSlug, err)
	}
	if project.DeletionTimestamp != nil {
		return nil, fmt.Errorf("%w: project is being deleted", store.ErrConflict)
	}

	replicas := req.Replicas
	if replicas == 0 {
		replicas = 1
	}
	if replicas > s.maxReplicas {
		return nil, fmt.Errorf("%w: replicas exceed configured maximum", store.ErrInvalidData)
	}
	if err := s.validateMountRoots(req.Volumes); err != nil {
		return nil, err
	}
	if err := validateCollectionLimits(len(req.EnvVars), len(req.Ports), len(req.Volumes)); err != nil {
		return nil, err
	}
	if _, err := swarm.BuildDesiredServiceSpec(swarm.DesiredServiceInput{
		ApplicationID: uuid.NewString(), ProjectID: project.ID, ProjectSlug: project.Slug,
		Name: req.Name, Image: req.Image, DesiredRunning: true, Replicas: replicas, Generation: 1,
		Environment: decodeEnvVars(encodeEnvVars(req.EnvVars)), Ports: req.Ports, Volumes: req.Volumes,
		ProjectNetwork: swarm.NetworkAttachment{Network: project.Network}, Expose: req.Expose,
		IngressPort: req.IngressContainerPort, IngressNetwork: s.ingressNetwork, BaseDomain: s.baseDomain,
	}); err != nil {
		return nil, fmt.Errorf("%w: %v", store.ErrInvalidData, err)
	}

	now := time.Now()
	app := &store.Application{
		ID:                   uuid.NewString(),
		ProjectID:            project.ID,
		Name:                 req.Name,
		SourceType:           store.SourceTypeImage,
		Image:                req.Image,
		Status:               store.AppStatusCreated,
		Replicas:             replicas,
		EnvVars:              encodeEnvVars(req.EnvVars),
		Ports:                encodePorts(req.Ports),
		Volumes:              encodeVolumes(req.Volumes),
		Expose:               req.Expose,
		IngressContainerPort: req.IngressContainerPort,
		DesiredRunState:      store.DesiredRunStateRunning,
		ResumeReplicas:       replicas,
		DesiredGeneration:    1,
		ObservedState:        store.ObservedStatePending,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	if err := s.store.CreateApplication(ctx, app); err != nil {
		return nil, fmt.Errorf("failed to save application: %w", err)
	}
	s.enqueue(app.Name)
	s.log.Info("application intent created", "name", app.Name, "generation", app.DesiredGeneration)

	return app, nil
}

// StopApp records the desired stopped state for an application.
func (s *Service) StopApp(ctx context.Context, name string) error {
	_, err := s.SetRunState(ctx, name, store.DesiredRunStateStopped, -1)
	return err
}

// StartApp records the desired running state for an application.
func (s *Service) StartApp(ctx context.Context, name string) error {
	_, err := s.SetRunState(ctx, name, store.DesiredRunStateRunning, -1)
	return err
}

// DeleteApp records a finalizer-backed application deletion intent.
func (s *Service) DeleteApp(ctx context.Context, name string) error {
	_, err := s.DeleteAppIntent(ctx, name, -1)
	return err
}

func (s *Service) DeleteAppIntent(ctx context.Context, name string, expectedGeneration int64) (*store.Application, error) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()

	current, err := s.store.GetApplication(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("application not found: %w", err)
	}
	links, err := s.store.ListProjectLinksByApp(ctx, current.ID)
	if err != nil {
		return nil, fmt.Errorf("list application links: %w", err)
	}
	if len(links) > 0 {
		return nil, fmt.Errorf("%w: application has active project links", store.ErrConflict)
	}
	application, err := s.store.CreateDeletionIntent(ctx, name, expectedGeneration)
	if err != nil {
		return nil, fmt.Errorf("create deletion intent: %w", err)
	}
	s.enqueue(name)
	return application, nil
}

func (s *Service) SetRunState(ctx context.Context, name string, state store.DesiredRunState, expectedGeneration int64) (*store.Application, error) {
	current, err := s.store.GetApplication(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("application not found: %w", err)
	}
	if current.DesiredRunState == state {
		return current, nil
	}
	updated, err := s.store.UpdateApplicationIntent(ctx, name, expectedGeneration, store.ApplicationMutation{DesiredRunState: &state})
	if err != nil {
		return nil, fmt.Errorf("update run state intent: %w", err)
	}
	s.enqueue(name)
	return updated, nil
}

// GetApp returns the details of an application.
func (s *Service) GetApp(ctx context.Context, name string) (*store.Application, error) {
	app, err := s.store.GetApplication(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("application not found: %w", err)
	}
	return app, nil
}

// ListApps lists all applications.
func (s *Service) ListApps(ctx context.Context) ([]*store.Application, error) {
	return s.store.ListApplications(ctx)
}

// ScaleApp records the desired replica count for an application.
func (s *Service) ScaleApp(ctx context.Context, name string, replicas int) error {
	_, err := s.ScaleAppIntent(ctx, name, replicas, -1)
	return err
}

func (s *Service) ScaleAppIntent(ctx context.Context, name string, replicas int, expectedGeneration int64) (*store.Application, error) {
	if replicas < 0 || replicas > s.maxReplicas {
		return nil, fmt.Errorf("%w: replica count must be between 0 and %d", store.ErrInvalidData, s.maxReplicas)
	}
	current, err := s.store.GetApplication(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("application not found: %w", err)
	}
	if current.Replicas == replicas {
		return current, nil
	}
	updated, err := s.store.UpdateApplicationIntent(ctx, name, expectedGeneration, store.ApplicationMutation{Replicas: &replicas})
	if err != nil {
		return nil, fmt.Errorf("update scale intent: %w", err)
	}
	s.enqueue(name)
	return updated, nil
}

// UpdateAppRequest holds the fields that can be updated on an application.
// Only non-nil pointer fields are applied (partial update).
type UpdateAppRequest struct {
	AppName              string
	ExpectedGeneration   int64
	EnvVars              map[string]string // nil means no update
	Ports                *[]swarm.PortConfig
	Volumes              *[]swarm.VolumeConfig
	Expose               *bool
	IngressContainerPort *uint32
	Image                *string
}

// UpdateApp atomically updates desired configuration and schedules convergence.
func (s *Service) UpdateApp(ctx context.Context, req UpdateAppRequest) (*store.Application, error) {
	if req.ExpectedGeneration == 0 {
		req.ExpectedGeneration = -1
	}
	if req.EnvVars == nil && req.Ports == nil && req.Volumes == nil && req.Expose == nil && req.IngressContainerPort == nil && req.Image == nil {
		return nil, fmt.Errorf("%w: patch contains no mutable fields", store.ErrInvalidData)
	}
	mutation := store.ApplicationMutation{}
	current, err := s.store.GetApplication(ctx, req.AppName)
	if err != nil {
		return nil, fmt.Errorf("application not found: %w", err)
	}
	if req.EnvVars != nil {
		value := encodeEnvVars(req.EnvVars)
		mutation.EnvVars = &value
	}
	if req.Ports != nil {
		value := encodePorts(*req.Ports)
		mutation.Ports = &value
	}
	if req.Volumes != nil {
		if err := s.validateMountRoots(*req.Volumes); err != nil {
			return nil, err
		}
		value := encodeVolumes(*req.Volumes)
		mutation.Volumes = &value
	}
	mutation.Expose = req.Expose
	mutation.IngressContainerPort = req.IngressContainerPort
	if req.Image != nil {
		if strings.TrimSpace(*req.Image) == "" {
			return nil, fmt.Errorf("%w: image cannot be empty", store.ErrInvalidData)
		}
		mutation.Image = req.Image
	}
	candidate := *current
	applyMutationPreview(&candidate, mutation)
	ports, err := decodePorts(candidate.Ports)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", store.ErrInvalidData, err)
	}
	volumes, err := decodeVolumes(candidate.Volumes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", store.ErrInvalidData, err)
	}
	if err := validateCollectionLimits(len(decodeEnvVars(candidate.EnvVars)), len(ports), len(volumes)); err != nil {
		return nil, err
	}
	if _, err := s.buildDesiredServiceSpec(ctx, &candidate); err != nil {
		return nil, fmt.Errorf("%w: %v", store.ErrInvalidData, err)
	}
	if applicationConfigEqual(current, &candidate) {
		return current, nil
	}
	updated, err := s.store.UpdateApplicationIntent(ctx, req.AppName, req.ExpectedGeneration, mutation)
	if err != nil {
		return nil, fmt.Errorf("update application intent: %w", err)
	}
	s.enqueue(req.AppName)
	s.log.Info("application intent updated", "name", req.AppName, "generation", updated.DesiredGeneration)
	return updated, nil
}

// RollbackApp triggers a deploy using the image from a previous deployment.
func (s *Service) RollbackApp(ctx context.Context, appName string, deployID string) (*store.Deployment, error) {
	targetApp, err := s.store.GetApplication(ctx, appName)
	if err != nil {
		return nil, fmt.Errorf("application not found: %w", err)
	}

	targetDeployment, err := s.store.GetDeployment(ctx, deployID)
	if err != nil {
		return nil, fmt.Errorf("deployment not found: %w", err)
	}

	if targetDeployment.AppID != targetApp.ID {
		// Do not reveal whether a deployment ID belongs to another application.
		return nil, fmt.Errorf("deployment not found for application: %w", store.ErrNotFound)
	}
	if targetDeployment.Status != store.DeploymentStatusSucceeded && targetDeployment.Status != store.DeploymentStatusSuccess {
		return nil, fmt.Errorf("%w: rollback target must be succeeded", store.ErrInvalidData)
	}
	if targetDeployment.Image == "" {
		return nil, fmt.Errorf("%w: rollback target has no image", store.ErrInvalidData)
	}

	rollbackDeployment, err := s.createDeploymentIntent(ctx, DeployAppRequest{
		AppName: appName,
		Image:   targetDeployment.Image,
	}, &deployID)
	if err != nil {
		return rollbackDeployment, err
	}

	s.log.Info("rollback completed", "app", appName, "target_deployment_id", deployID, "rollback_deployment_id", rollbackDeployment.ID, "image", targetDeployment.Image)
	return rollbackDeployment, nil
}

// DeployAppRequest holds the information needed for a deploy request.
type DeployAppRequest struct {
	AppName string
	Image   string // required when source_type=image
}

// DeployApp creates an immutable deployment intent. Runtime success is recorded
// only by the reconciler after task convergence.
func (s *Service) DeployApp(ctx context.Context, req DeployAppRequest) (*store.Deployment, error) {
	return s.createDeploymentIntent(ctx, req, nil)
}

func (s *Service) createDeploymentIntent(ctx context.Context, req DeployAppRequest, rollbackSource *string) (*store.Deployment, error) {
	app, err := s.store.GetApplication(ctx, req.AppName)
	if err != nil {
		return nil, fmt.Errorf("application not found: %w", err)
	}

	if req.Image == "" {
		req.Image = app.Image
	}
	if req.Image == "" {
		return nil, fmt.Errorf("no image set: specify an image or deploy the app with an image first")
	}
	candidate := *app
	candidate.Image = req.Image
	candidate.DesiredGeneration++
	if _, err := s.buildDesiredServiceSpec(ctx, &candidate); err != nil {
		return nil, fmt.Errorf("%w: %v", store.ErrInvalidData, err)
	}

	now := time.Now().UTC()
	deadline := now.Add(s.deploymentTimeout)
	deployment := &store.Deployment{
		ID:                         uuid.NewString(),
		AppID:                      app.ID,
		SourceType:                 store.SourceTypeImage,
		Image:                      req.Image,
		Status:                     store.DeploymentStatusPending,
		TriggeredBy:                store.TriggeredByAPI,
		CreatedAt:                  now,
		ConvergenceDeadline:        &deadline,
		RollbackSourceDeploymentID: rollbackSource,
	}
	updated, err := s.store.CreateDeploymentIntent(ctx, req.AppName, deployment)
	if err != nil {
		return nil, fmt.Errorf("create deployment intent: %w", err)
	}
	deployment.TargetGeneration = updated.DesiredGeneration
	deployment.PreviousObservedImage = updated.ObservedImage
	s.enqueue(req.AppName)
	s.log.Info("deployment intent created", "app", req.AppName, "deployment_id", deployment.ID, "generation", deployment.TargetGeneration)
	return deployment, nil
}

// ListDeployments returns the deploy history for an application.
func (s *Service) ListDeployments(ctx context.Context, appName string) ([]*store.Deployment, error) {
	app, err := s.store.GetApplication(ctx, appName)
	if err != nil {
		return nil, fmt.Errorf("application not found: %w", err)
	}
	return s.store.ListDeployments(ctx, app.ID)
}

// GetLogs returns a log stream for an app. The caller is responsible for closing rc.
func (s *Service) GetLogs(ctx context.Context, serviceID string, tail string, follow bool) (io.ReadCloser, error) {
	rc, err := s.swarm.GetServiceLogs(ctx, serviceID, tail, follow)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve logs: %w", err)
	}
	return rc, nil
}

func (s *Service) GetApplicationLogs(ctx context.Context, application *store.Application, tail string, follow bool) (io.ReadCloser, error) {
	info, err := s.swarm.GetService(ctx, swarm.ServiceName(application.Name))
	if err != nil {
		return nil, fmt.Errorf("inspect service for logs: %w", err)
	}
	if info.Labels[swarm.LabelManagedBy] != "true" || info.Labels[swarm.LabelAppID] != application.ID {
		return nil, fmt.Errorf("%w: service is not owned by application", swarm.ErrOwnershipConflict)
	}
	return s.GetLogs(ctx, info.ID, tail, follow)
}

// UpdateAppStatus updates only the status field. Used by the watcher and reconciler.
func (s *Service) UpdateAppStatus(ctx context.Context, name string, status store.AppStatus) error {
	app, err := s.store.GetApplication(ctx, name)
	if err != nil {
		return err
	}
	app.Status = status
	return s.store.UpdateApplication(ctx, app)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *Service) buildDesiredServiceSpec(ctx context.Context, app *store.Application) (swarm.ServiceSpec, error) {
	project, err := s.store.GetProjectByID(ctx, app.ProjectID)
	if err != nil {
		return swarm.ServiceSpec{}, fmt.Errorf("resolve project for desired spec: %w", err)
	}
	ports, err := decodePorts(app.Ports)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	volumes, err := decodeVolumes(app.Volumes)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	if err := s.validateMountRoots(volumes); err != nil {
		return swarm.ServiceSpec{}, err
	}
	linkedNetworks := make([]swarm.NetworkAttachment, 0)
	links, err := s.store.ListProjectLinksByApp(ctx, app.ID)
	if err != nil {
		return swarm.ServiceSpec{}, fmt.Errorf("resolve project links for desired spec: %w", err)
	}
	for _, link := range links {
		sourceProject, err := s.store.GetProjectByID(ctx, link.SourceProjectID)
		if err != nil {
			return swarm.ServiceSpec{}, fmt.Errorf("resolve linked project for desired spec: %w", err)
		}
		linkedNetworks = append(linkedNetworks, swarm.NetworkAttachment{
			Network: sourceProject.Network,
			Aliases: []string{link.Alias},
		})
	}
	return swarm.BuildDesiredServiceSpec(swarm.DesiredServiceInput{
		ApplicationID:  app.ID,
		ProjectID:      app.ProjectID,
		ProjectSlug:    project.Slug,
		Name:           app.Name,
		Image:          app.Image,
		DesiredRunning: app.DesiredRunState != store.DesiredRunStateStopped,
		Replicas:       app.Replicas,
		Generation:     app.DesiredGeneration,
		Environment:    decodeEnvVars(app.EnvVars),
		Ports:          ports,
		Volumes:        volumes,
		ProjectNetwork: swarm.NetworkAttachment{
			Network: project.Network,
			Aliases: projectNetworkAliases(project.Slug, app.Name),
		},
		LinkedNetworks: linkedNetworks,
		Expose:         app.Expose,
		IngressPort:    app.IngressContainerPort,
		IngressNetwork: s.ingressNetwork,
		BaseDomain:     s.baseDomain,
	})
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: application name cannot be empty", store.ErrInvalidData)
	}
	if len(name) > 63 {
		return fmt.Errorf("%w: application name cannot exceed 63 characters", store.ErrInvalidData)
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return fmt.Errorf("%w: application name cannot start or end with a hyphen", store.ErrInvalidData)
	}
	for _, c := range name {
		if !isValidNameChar(c) {
			return fmt.Errorf("%w: application name may only contain lowercase letters, digits, and hyphens", store.ErrInvalidData)
		}
	}
	return nil
}

func isValidNameChar(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
}

func (s *Service) validateMountRoots(volumes []swarm.VolumeConfig) error {
	for _, volume := range volumes {
		source := filepath.Clean(volume.Source)
		if source == "/" || source == "/var/run/docker.sock" || source == "/proc" || strings.HasPrefix(source, "/proc/") || source == "/sys" || strings.HasPrefix(source, "/sys/") || source == "/dev" || strings.HasPrefix(source, "/dev/") || source == "/etc" || strings.HasPrefix(source, "/etc/") {
			return fmt.Errorf("%w: mount source is denied", store.ErrInvalidData)
		}
		if len(s.allowedMountRoots) == 0 {
			continue
		}
		allowed := false
		for _, root := range s.allowedMountRoots {
			relative, err := filepath.Rel(filepath.Clean(root), source)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("%w: mount source is outside allowed roots", store.ErrInvalidData)
		}
	}
	return nil
}

func validateCollectionLimits(environment, ports, volumes int) error {
	if environment > maxEnvironmentEntries {
		return fmt.Errorf("%w: environment entry limit exceeded", store.ErrInvalidData)
	}
	if ports > maxPortEntries {
		return fmt.Errorf("%w: published port limit exceeded", store.ErrInvalidData)
	}
	if volumes > maxVolumeEntries {
		return fmt.Errorf("%w: mount limit exceeded", store.ErrInvalidData)
	}
	return nil
}

func applyMutationPreview(application *store.Application, mutation store.ApplicationMutation) {
	if mutation.Image != nil {
		application.Image = *mutation.Image
	}
	if mutation.EnvVars != nil {
		application.EnvVars = *mutation.EnvVars
	}
	if mutation.Ports != nil {
		application.Ports = *mutation.Ports
	}
	if mutation.Volumes != nil {
		application.Volumes = *mutation.Volumes
	}
	if mutation.Expose != nil {
		application.Expose = *mutation.Expose
	}
	if mutation.IngressContainerPort != nil {
		application.IngressContainerPort = *mutation.IngressContainerPort
	}
}

func applicationConfigEqual(left, right *store.Application) bool {
	return left.Image == right.Image &&
		left.EnvVars == right.EnvVars &&
		left.Ports == right.Ports &&
		left.Volumes == right.Volumes &&
		left.Expose == right.Expose &&
		left.IngressContainerPort == right.IngressContainerPort
}

func encodeEnvVars(envVars map[string]string) string {
	encoded, err := json.Marshal(envVars)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func decodeEnvVars(raw string) []string {
	if raw == "" || raw == "{}" {
		return nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err == nil {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result := make([]string, 0, len(keys))
		for _, key := range keys {
			result = append(result, key+"="+values[key])
		}
		return result
	}
	return strings.Split(raw, "\n")
}

func decodePorts(raw string) ([]swarm.PortConfig, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var ports []swarm.PortConfig
	if err := json.Unmarshal([]byte(raw), &ports); err != nil {
		return nil, fmt.Errorf("decode persisted ports: %w", store.ErrInvalidData)
	}
	return ports, nil
}

func decodeVolumes(raw string) ([]swarm.VolumeConfig, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var volumes []swarm.VolumeConfig
	if err := json.Unmarshal([]byte(raw), &volumes); err != nil {
		return nil, fmt.Errorf("decode persisted volumes: %w", store.ErrInvalidData)
	}
	return volumes, nil
}

func encodePorts(ports []swarm.PortConfig) string {
	ordered := append([]swarm.PortConfig(nil), ports...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ContainerPort == ordered[j].ContainerPort {
			return ordered[i].PublishedPort < ordered[j].PublishedPort
		}
		return ordered[i].ContainerPort < ordered[j].ContainerPort
	})
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func encodeVolumes(volumes []swarm.VolumeConfig) string {
	ordered := append([]swarm.VolumeConfig(nil), volumes...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Target == ordered[j].Target {
			return ordered[i].Source < ordered[j].Source
		}
		return ordered[i].Target < ordered[j].Target
	})
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}
