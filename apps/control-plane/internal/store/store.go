package store

import "context"

type Store interface {
	Ping(ctx context.Context) error
	// Project
	CreateProject(ctx context.Context, p *Project) error
	GetProject(ctx context.Context, slug string) (*Project, error)
	GetProjectByID(ctx context.Context, id string) (*Project, error)
	ListProjects(ctx context.Context) ([]*Project, error)
	DeleteProject(ctx context.Context, slug string) error
	CreateProjectDeletionIntent(ctx context.Context, slug string) (*Project, error)
	FinalizeProjectDeletion(ctx context.Context, projectID string) error
	MarkProjectObserved(ctx context.Context, projectID string, state ObservedState, code, message string) error

	// ProjectLink
	CreateProjectLink(ctx context.Context, pl *ProjectLink) error
	GetProjectLink(ctx context.Context, id string) (*ProjectLink, error)
	DeleteProjectLink(ctx context.Context, id string) error
	ListProjectLinksByProject(ctx context.Context, projectID string) ([]*ProjectLink, error)
	ListProjectLinksByApp(ctx context.Context, appID string) ([]*ProjectLink, error)
	MarkProjectLinkObserved(ctx context.Context, linkID string, generation int64) error

	// Application
	CreateApplication(ctx context.Context, app *Application) error
	GetApplication(ctx context.Context, name string) (*Application, error)
	GetApplicationByID(ctx context.Context, id string) (*Application, error)
	ListApplications(ctx context.Context) ([]*Application, error)
	ListApplicationsByProject(ctx context.Context, projectID string) ([]*Application, error)
	UpdateApplication(ctx context.Context, app *Application) error
	DeleteApplication(ctx context.Context, name string) error
	UpdateApplicationIntent(ctx context.Context, name string, expectedGeneration int64, mutation ApplicationMutation) (*Application, error)
	CreateDeploymentIntent(ctx context.Context, name string, d *Deployment) (*Application, error)
	MarkApplicationObserved(ctx context.Context, appID string, update ObservedApplicationUpdate) error
	CreateDeletionIntent(ctx context.Context, name string, expectedGeneration int64) (*Application, error)
	FinalizeApplicationDeletion(ctx context.Context, appID string, generation int64) error
	PersistReconcileDiagnostics(ctx context.Context, appID string, generation int64, code, message string, retryable bool, attempt int) error

	// Deployment
	CreateDeployment(ctx context.Context, d *Deployment) error
	GetDeployment(ctx context.Context, id string) (*Deployment, error)
	ListDeployments(ctx context.Context, appID string) ([]*Deployment, error)
	UpdateDeployment(ctx context.Context, d *Deployment) error
	MarkDeploymentState(ctx context.Context, id string, generation int64, status DeploymentStatus, code, message string) error
}
