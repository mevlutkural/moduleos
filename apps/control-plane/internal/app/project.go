package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

var slugRegex = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var aliasLabelRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

const maxProjectLinks = 100

var (
	ErrValidation   = errors.New("validation failed")
	ErrSwarmApply   = errors.New("swarm apply failed")
	ErrInternal     = errors.New("internal error")
	ErrInconsistent = errors.New("project link state inconsistent")
)

// ProjectLinkDetails resolves a persisted link with the related project and application names.
type ProjectLinkDetails struct {
	*store.ProjectLink
	SourceProjectSlug string
	TargetAppName     string
	TargetProjectSlug string
}

// CreateProjectRequest holds the information needed to create a new project.
type CreateProjectRequest struct {
	Name string
	Slug string // generated from Name if empty
}

// CreateProject persists desired project state. Network provisioning belongs to
// the reconciler's periodic project scan.
func (s *Service) CreateProject(ctx context.Context, req CreateProjectRequest) (*store.Project, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, fmt.Errorf("%w: project name cannot be empty", ErrValidation)
	}
	slug := req.Slug
	if slug == "" {
		slug = slugify(req.Name)
	}

	if err := validateSlug(slug); err != nil {
		return nil, err
	}

	if slug == "root" {
		return nil, fmt.Errorf("%w: root is reserved", store.ErrConflict)
	}

	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()

	if _, err := s.store.GetProject(ctx, slug); err == nil {
		return nil, fmt.Errorf("%w: project slug already exists", store.ErrConflict)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("check project slug availability: %w", err)
	}
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects for resource limit: %w", err)
	}
	if len(projects) >= s.maxProjects {
		return nil, fmt.Errorf("%w: project limit reached", store.ErrConflict)
	}

	networkName := projectNetwork(slug)

	now := time.Now()
	project := &store.Project{
		ID:            uuid.NewString(),
		Name:          req.Name,
		Slug:          slug,
		Network:       networkName,
		CreatedAt:     now,
		UpdatedAt:     now,
		ObservedState: store.ObservedStatePending,
	}

	if err := s.store.CreateProject(ctx, project); err != nil {
		return nil, fmt.Errorf("failed to save project: %w", err)
	}

	s.log.Info("project created", "slug", slug, "network", networkName)
	return project, nil
}

// GetProject retrieves a project by slug.
func (s *Service) GetProject(ctx context.Context, slug string) (*store.Project, error) {
	p, err := s.store.GetProject(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}
	return p, nil
}

// GetProjectByID retrieves a project by ID.
func (s *Service) GetProjectByID(ctx context.Context, id string) (*store.Project, error) {
	p, err := s.store.GetProjectByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}
	return p, nil
}

// ListProjects lists all projects.
func (s *Service) ListProjects(ctx context.Context) ([]*store.Project, error) {
	return s.store.ListProjects(ctx)
}

// DeleteProject records a finalizer-backed project deletion intent.
func (s *Service) DeleteProject(ctx context.Context, slug string) error {
	if slug == "root" {
		return fmt.Errorf("%w: root project cannot be deleted", store.ErrConflict)
	}

	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()

	project, err := s.store.GetProject(ctx, slug)
	if err != nil {
		return fmt.Errorf("project not found: %w", err)
	}

	apps, err := s.store.ListApplicationsByProject(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("failed to list project applications: %w", err)
	}
	if len(apps) > 0 {
		return fmt.Errorf("%w: project has applications", store.ErrConflict)
	}

	links, err := s.store.ListProjectLinksByProject(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("failed to list project links: %w", err)
	}
	if len(links) > 0 {
		return fmt.Errorf("%w: project has active links", store.ErrConflict)
	}

	if _, err := s.store.CreateProjectDeletionIntent(ctx, slug); err != nil {
		return fmt.Errorf("failed to create project deletion intent: %w", err)
	}

	s.log.Info("project deletion intent created", "slug", slug)
	return nil
}

// ListProjectApps lists the applications belonging to a project.
func (s *Service) ListProjectApps(ctx context.Context, slug string) ([]*store.Application, error) {
	project, err := s.store.GetProject(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}
	return s.store.ListApplicationsByProject(ctx, project.ID)
}

// CreateProjectLink creates a new project link.
// IMPORTANT SECURITY NOTE: Project Link is a network attachment operation.
// Docker overlay networks do not provide strict one-way isolation. When a target app
// is attached to a source project's network, mutual reachability is possible.
// The source can reach the target, and the target can reach the source network.
// If strict one-way isolation is required, a different approach (like a proxy or sidecar) is needed.
func (s *Service) CreateProjectLink(ctx context.Context, projectSlug, targetAppName, alias string) (*ProjectLinkDetails, error) {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()

	p, err := s.store.GetProject(ctx, projectSlug)
	if err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}

	targetApp, err := s.store.GetApplication(ctx, targetAppName)
	if err != nil {
		return nil, fmt.Errorf("target application not found: %w", err)
	}

	targetProject, err := s.store.GetProjectByID(ctx, targetApp.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("target project not found: %w", err)
	}

	if p.DeletionTimestamp != nil || targetProject.DeletionTimestamp != nil || targetApp.DeletionTimestamp != nil {
		return nil, fmt.Errorf("%w: cannot link deleting resources", store.ErrConflict)
	}

	if p.ID == targetProject.ID {
		return nil, store.ErrConflict // same project conflict
	}

	if alias == "" {
		alias = fmt.Sprintf("%s.%s", targetProject.Slug, targetApp.Name)
	}

	if err := ValidateAlias(alias); err != nil {
		return nil, err // validation error
	}

	sourceApps, err := s.store.ListApplicationsByProject(ctx, p.ID)
	if err != nil {
		return nil, projectInternalError("list source applications", err)
	}
	for _, app := range sourceApps {
		if alias == app.Name || alias == fmt.Sprintf("%s.%s", p.Slug, app.Name) {
			return nil, store.ErrConflict // alias conflict with existing app
		}
	}
	links, err := s.store.ListProjectLinksByProject(ctx, p.ID)
	if err != nil {
		return nil, projectInternalError("list project links for resource limit", err)
	}
	if len(links) >= maxProjectLinks {
		return nil, fmt.Errorf("%w: project link limit reached", store.ErrConflict)
	}

	link := &store.ProjectLink{
		ID:              uuid.NewString(),
		SourceProjectID: p.ID,
		TargetAppID:     targetApp.ID,
		Alias:           alias,
		CreatedAt:       time.Now(),
	}

	if err := s.store.CreateProjectLink(ctx, link); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, err
		}
		return nil, projectInternalError("create project link", err)
	}

	s.enqueue(targetApp.Name)

	s.log.Info("project link created", "operation", "create_project_link", "link_id", link.ID, "source_project", projectSlug, "target_project", targetProject.Slug, "target_app", targetAppName, "network", p.Network, "alias", alias, "result", "success")
	return &ProjectLinkDetails{
		ProjectLink:       link,
		SourceProjectSlug: p.Slug,
		TargetAppName:     targetApp.Name,
		TargetProjectSlug: targetProject.Slug,
	}, nil
}

// ListProjectLinks lists links for a project.
func (s *Service) ListProjectLinks(ctx context.Context, projectSlug string) ([]*ProjectLinkDetails, error) {
	p, err := s.store.GetProject(ctx, projectSlug)
	if err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}
	links, err := s.store.ListProjectLinksByProject(ctx, p.ID)
	if err != nil {
		return nil, projectInternalError("list project links", err)
	}
	details := make([]*ProjectLinkDetails, 0, len(links))
	for _, link := range links {
		targetApp, err := s.store.GetApplicationByID(ctx, link.TargetAppID)
		if err != nil {
			return nil, projectInternalError("resolve target app for project link", err)
		}
		targetProject, err := s.store.GetProjectByID(ctx, targetApp.ProjectID)
		if err != nil {
			return nil, projectInternalError("resolve target project for project link", err)
		}
		details = append(details, &ProjectLinkDetails{
			ProjectLink:       link,
			SourceProjectSlug: p.Slug,
			TargetAppName:     targetApp.Name,
			TargetProjectSlug: targetProject.Slug,
		})
	}
	return details, nil
}

// DeleteProjectLink removes a project link.
func (s *Service) DeleteProjectLink(ctx context.Context, projectSlug, linkID string) error {
	s.resourceMu.Lock()
	defer s.resourceMu.Unlock()

	p, err := s.store.GetProject(ctx, projectSlug)
	if err != nil {
		return fmt.Errorf("project not found: %w", err)
	}

	link, err := s.store.GetProjectLink(ctx, linkID)
	if err != nil {
		return fmt.Errorf("project link not found: %w", err)
	}

	if link.SourceProjectID != p.ID {
		return store.ErrNotFound
	}

	targetApp, err := s.store.GetApplicationByID(ctx, link.TargetAppID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("failed to get target application: %w", err)
	}

	if err := s.store.DeleteProjectLink(ctx, linkID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return err
		}
		return projectInternalError("delete project link", err)
	}

	if targetApp != nil {
		s.enqueue(targetApp.Name)
	}

	s.log.Info("project link deleted", "operation", "delete_project_link", "link_id", linkID, "source_project", projectSlug, "network", p.Network, "alias", link.Alias, "result", "success")
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func projectInternalError(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s: %w", ErrInternal, operation, err)
	}
	return fmt.Errorf("%w: %s: %v", ErrInternal, operation, err)
}

// projectNetwork produces the Docker network name from a project slug.
func projectNetwork(slug string) string {
	return "moduleos-" + slug + "-net"
}

// projectNetworkAliases returns the network alias list for an app.
// Within the same project: "appname"
// Cross-project: "projectslug.appname"
func projectNetworkAliases(projectSlug, appName string) []string {
	return []string{
		appName,                     // same project
		projectSlug + "." + appName, // cross-project
	}
}

func slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	// collapse consecutive hyphens
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	return s
}

func validateSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("%w: project name cannot be empty", ErrValidation)
	}
	if len(slug) > 48 {
		return fmt.Errorf("%w: project name cannot exceed 48 characters", ErrValidation)
	}
	if !slugRegex.MatchString(slug) {
		return fmt.Errorf("%w: project name may only contain lowercase letters, digits, and hyphens", ErrValidation)
	}
	return nil
}

// ValidateAlias checks if a Project Link alias conforms to DNS label rules.
func ValidateAlias(alias string) error {
	if len(alias) > 253 {
		return fmt.Errorf("%w: alias cannot exceed 253 characters", ErrValidation)
	}
	if alias == "" {
		return fmt.Errorf("%w: alias cannot be empty", ErrValidation)
	}
	labels := strings.Split(alias, ".")
	for _, label := range labels {
		if len(label) == 0 {
			return fmt.Errorf("%w: alias contains empty label", ErrValidation)
		}
		if len(label) > 63 {
			return fmt.Errorf("%w: alias label cannot exceed 63 characters", ErrValidation)
		}
		if !aliasLabelRegex.MatchString(label) {
			return fmt.Errorf("%w: invalid alias label format", ErrValidation)
		}
	}
	return nil
}
