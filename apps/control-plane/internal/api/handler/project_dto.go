package handler

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

var (
	errInvalidPublicState = errors.New("resource cannot be represented safely")
	publicErrorCode       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	publicApplicationName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

const (
	maximumPublicProjects     = 1000
	maximumPublicApplications = 10000
	maximumPublicProjectLinks = 100
)

type CreateProjectRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
}

type CreateProjectLinkRequest struct {
	TargetAppName string `json:"target_app_name"`
	Alias         string `json:"alias,omitempty"`
}

type ProjectResponse struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	Slug                string              `json:"slug"`
	ObservedState       store.ObservedState `json:"observed_state"`
	ReconcileErrorCode  string              `json:"reconcile_error_code,omitempty"`
	DeletionRequestedAt *time.Time          `json:"deletion_requested_at,omitempty"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

type ProjectCollectionResponse struct {
	Projects []ProjectResponse `json:"projects"`
	Total    int               `json:"total"`
}

type ProjectApplicationSummary struct {
	ID                  string                `json:"id"`
	Name                string                `json:"name"`
	Status              store.AppStatus       `json:"status"`
	DesiredRunState     store.DesiredRunState `json:"desired_run_state"`
	Replicas            int                   `json:"replicas"`
	DesiredGeneration   int64                 `json:"desired_generation"`
	ObservedGeneration  int64                 `json:"observed_generation"`
	ObservedState       store.ObservedState   `json:"observed_state"`
	DeletionRequestedAt *time.Time            `json:"deletion_requested_at,omitempty"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
}

type ProjectApplicationCollectionResponse struct {
	Applications []ProjectApplicationSummary `json:"applications"`
	Total        int                         `json:"total"`
}

type ProjectLinkStatus string

const (
	ProjectLinkPending ProjectLinkStatus = "pending"
	ProjectLinkReady   ProjectLinkStatus = "ready"
)

type ProjectLinkResponse struct {
	ID                 string            `json:"id"`
	SourceProjectSlug  string            `json:"source_project_slug"`
	TargetProjectSlug  string            `json:"target_project_slug"`
	TargetAppName      string            `json:"target_app_name"`
	Alias              string            `json:"alias"`
	DesiredGeneration  int64             `json:"desired_generation"`
	ObservedGeneration int64             `json:"observed_generation"`
	Status             ProjectLinkStatus `json:"status"`
	CreatedAt          time.Time         `json:"created_at"`
}

type ProjectLinkCollectionResponse struct {
	Links []ProjectLinkResponse `json:"links"`
	Total int                   `json:"total"`
}

type AcceptedOperationResponse struct {
	Operation    string `json:"operation"`
	ResourceType string `json:"resource_type"`
	Resource     string `json:"resource"`
	Status       string `json:"status"`
}

func mapProjectResponse(project *store.Project) (ProjectResponse, error) {
	if project == nil || !canonicalUUID(project.ID) || !utf8.ValidString(project.Name) || strings.TrimSpace(project.Name) != project.Name || project.Name == "" || utf8.RuneCountInString(project.Name) > maximumProjectNameLength || !projectSlugPattern.MatchString(project.Slug) || len(project.Slug) > 48 || project.CreatedAt.IsZero() || project.UpdatedAt.IsZero() || !knownObservedState(project.ObservedState) {
		return ProjectResponse{}, errInvalidPublicState
	}
	if project.ReconcileErrorCode != "" && !publicErrorCode.MatchString(project.ReconcileErrorCode) {
		return ProjectResponse{}, errInvalidPublicState
	}
	return ProjectResponse{
		ID:                  project.ID,
		Name:                project.Name,
		Slug:                project.Slug,
		ObservedState:       project.ObservedState,
		ReconcileErrorCode:  project.ReconcileErrorCode,
		DeletionRequestedAt: utcTimePointer(project.DeletionTimestamp),
		CreatedAt:           project.CreatedAt.UTC(),
		UpdatedAt:           project.UpdatedAt.UTC(),
	}, nil
}

func mapProjectCollection(projects []*store.Project) (ProjectCollectionResponse, error) {
	if len(projects) > maximumPublicProjects {
		return ProjectCollectionResponse{}, errInvalidPublicState
	}
	response := ProjectCollectionResponse{Projects: make([]ProjectResponse, 0, len(projects))}
	for _, project := range projects {
		mapped, err := mapProjectResponse(project)
		if err != nil {
			return ProjectCollectionResponse{}, err
		}
		response.Projects = append(response.Projects, mapped)
	}
	response.Total = len(response.Projects)
	return response, nil
}

func mapProjectApplications(applications []*store.Application) (ProjectApplicationCollectionResponse, error) {
	if len(applications) > maximumPublicApplications {
		return ProjectApplicationCollectionResponse{}, errInvalidPublicState
	}
	response := ProjectApplicationCollectionResponse{Applications: make([]ProjectApplicationSummary, 0, len(applications))}
	for _, application := range applications {
		if application == nil || !canonicalUUID(application.ID) || !publicApplicationName.MatchString(application.Name) || application.CreatedAt.IsZero() || application.UpdatedAt.IsZero() || application.Replicas < 0 || application.DesiredGeneration < 1 || application.ObservedGeneration < 0 || application.ObservedGeneration > application.DesiredGeneration || !knownApplicationStatus(application.Status) || !knownDesiredRunState(application.DesiredRunState) || !knownObservedState(application.ObservedState) {
			return ProjectApplicationCollectionResponse{}, errInvalidPublicState
		}
		response.Applications = append(response.Applications, ProjectApplicationSummary{
			ID:                  application.ID,
			Name:                application.Name,
			Status:              application.Status,
			DesiredRunState:     application.DesiredRunState,
			Replicas:            application.Replicas,
			DesiredGeneration:   application.DesiredGeneration,
			ObservedGeneration:  application.ObservedGeneration,
			ObservedState:       application.ObservedState,
			DeletionRequestedAt: utcTimePointer(application.DeletionTimestamp),
			CreatedAt:           application.CreatedAt.UTC(),
			UpdatedAt:           application.UpdatedAt.UTC(),
		})
	}
	response.Total = len(response.Applications)
	return response, nil
}

func mapProjectLinkResponse(details *app.ProjectLinkDetails) (ProjectLinkResponse, error) {
	if details == nil || details.ProjectLink == nil || !canonicalUUID(details.ID) {
		return ProjectLinkResponse{}, errInvalidPublicState
	}
	if _, ok := validProjectSlug(details.SourceProjectSlug); !ok {
		return ProjectLinkResponse{}, errInvalidPublicState
	}
	if _, ok := validProjectSlug(details.TargetProjectSlug); !ok || !publicApplicationName.MatchString(details.TargetAppName) || app.ValidateAlias(details.Alias) != nil || details.CreatedAt.IsZero() || details.DesiredGeneration < 1 || details.ObservedGeneration < 0 || details.ObservedGeneration > details.DesiredGeneration || details.DeletionTimestamp != nil {
		return ProjectLinkResponse{}, errInvalidPublicState
	}
	status := ProjectLinkPending
	if details.ObservedGeneration == details.DesiredGeneration {
		status = ProjectLinkReady
	}
	return ProjectLinkResponse{
		ID:                 details.ID,
		SourceProjectSlug:  details.SourceProjectSlug,
		TargetProjectSlug:  details.TargetProjectSlug,
		TargetAppName:      details.TargetAppName,
		Alias:              details.Alias,
		DesiredGeneration:  details.DesiredGeneration,
		ObservedGeneration: details.ObservedGeneration,
		Status:             status,
		CreatedAt:          details.CreatedAt.UTC(),
	}, nil
}

func mapProjectLinkCollection(links []*app.ProjectLinkDetails) (ProjectLinkCollectionResponse, error) {
	if len(links) > maximumPublicProjectLinks {
		return ProjectLinkCollectionResponse{}, errInvalidPublicState
	}
	response := ProjectLinkCollectionResponse{Links: make([]ProjectLinkResponse, 0, len(links))}
	for _, link := range links {
		mapped, err := mapProjectLinkResponse(link)
		if err != nil {
			return ProjectLinkCollectionResponse{}, err
		}
		response.Links = append(response.Links, mapped)
	}
	response.Total = len(response.Links)
	return response, nil
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.UTC()
	return &converted
}

func knownObservedState(state store.ObservedState) bool {
	switch state {
	case store.ObservedStatePending, store.ObservedStateReady, store.ObservedStateReconciling, store.ObservedStateRunning, store.ObservedStateStopped, store.ObservedStateDegraded, store.ObservedStateFailed, store.ObservedStateDeleting:
		return true
	default:
		return false
	}
}

func knownApplicationStatus(status store.AppStatus) bool {
	switch status {
	case store.AppStatusCreated, store.AppStatusBuilding, store.AppStatusRunning, store.AppStatusStopped, store.AppStatusFailed, store.AppStatusUpdating:
		return true
	default:
		return false
	}
}

func knownDesiredRunState(state store.DesiredRunState) bool {
	return state == store.DesiredRunStateRunning || state == store.DesiredRunStateStopped
}
