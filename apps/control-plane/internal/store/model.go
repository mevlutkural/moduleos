package store

import "time"

type SourceType string
type AppStatus string
type DeploymentStatus string
type TriggeredBy string
type DesiredRunState string
type ObservedState string

const (
	SourceTypeImage SourceType = "image"
	SourceTypeTar   SourceType = "tar"
)

const (
	AppStatusCreated  AppStatus = "created"
	AppStatusBuilding AppStatus = "building"
	AppStatusRunning  AppStatus = "running"
	AppStatusStopped  AppStatus = "stopped"
	AppStatusFailed   AppStatus = "failed"
	AppStatusUpdating AppStatus = "updating"
)

const (
	DeploymentStatusPending    DeploymentStatus = "pending"
	DeploymentStatusBuilding   DeploymentStatus = "building"
	DeploymentStatusInProgress DeploymentStatus = "in_progress"
	DeploymentStatusSuccess    DeploymentStatus = "success"
	DeploymentStatusFailed     DeploymentStatus = "failed"
	DeploymentStatusSucceeded  DeploymentStatus = "succeeded"
	DeploymentStatusApplying   DeploymentStatus = "applying"
	DeploymentStatusSuperseded DeploymentStatus = "superseded"
)

const (
	DesiredRunStateRunning DesiredRunState = "running"
	DesiredRunStateStopped DesiredRunState = "stopped"
)

const (
	ObservedStatePending     ObservedState = "pending"
	ObservedStateReady       ObservedState = "ready"
	ObservedStateReconciling ObservedState = "reconciling"
	ObservedStateRunning     ObservedState = "running"
	ObservedStateStopped     ObservedState = "stopped"
	ObservedStateDegraded    ObservedState = "degraded"
	ObservedStateFailed      ObservedState = "failed"
	ObservedStateDeleting    ObservedState = "deleting"
)

const (
	TriggeredByAPI        TriggeredBy = "api"
	TriggeredByReconciler TriggeredBy = "reconciler"
	TriggeredByEvent      TriggeredBy = "event"
)

const RootProjectID = "00000000-0000-0000-0000-000000000001"

type Project struct {
	ID                    string        `db:"id"         json:"id"`
	Name                  string        `db:"name"       json:"name"`
	Slug                  string        `db:"slug"       json:"slug"`
	Network               string        `db:"network"    json:"network"`
	CreatedAt             time.Time     `db:"created_at" json:"created_at"`
	UpdatedAt             time.Time     `db:"updated_at" json:"updated_at"`
	ObservedState         ObservedState `db:"observed_state" json:"observed_state"`
	ReconcileErrorCode    string        `db:"reconcile_error_code" json:"reconcile_error_code,omitempty"`
	ReconcileErrorMessage string        `db:"reconcile_error_message" json:"reconcile_error_message,omitempty"`
	DeletionTimestamp     *time.Time    `db:"deletion_timestamp" json:"deletion_timestamp,omitempty"`
	FinalizerState        string        `db:"finalizer_state" json:"finalizer_state"`
}

type ProjectLink struct {
	ID                 string     `db:"id"                json:"id"`
	SourceProjectID    string     `db:"source_project_id" json:"source_project_id"`
	TargetAppID        string     `db:"target_app_id"     json:"target_app_id"`
	Alias              string     `db:"alias"             json:"alias"`
	CreatedAt          time.Time  `db:"created_at"        json:"created_at"`
	DesiredGeneration  int64      `db:"desired_generation" json:"desired_generation"`
	ObservedGeneration int64      `db:"observed_generation" json:"observed_generation"`
	DeletionTimestamp  *time.Time `db:"deletion_timestamp" json:"deletion_timestamp,omitempty"`
}

type ApplicationMutation struct {
	Image                *string
	DesiredRunState      *DesiredRunState
	Replicas             *int
	EnvVars              *string
	Ports                *string
	Volumes              *string
	Expose               *bool
	IngressContainerPort *uint32
}

type ObservedApplicationUpdate struct {
	Generation   int64
	State        ObservedState
	Image        string
	ErrorCode    string
	ErrorMessage string
	Retryable    bool
	Attempt      int
	TransitionAt time.Time
}

type Application struct {
	ID                    string          `db:"id"          json:"id"`
	ProjectID             string          `db:"project_id"  json:"project_id"`
	Name                  string          `db:"name"        json:"name"`
	SourceType            SourceType      `db:"source_type" json:"source_type"`
	Image                 string          `db:"image"       json:"image"`
	ObservedImage         string          `db:"observed_image" json:"observed_image"`
	Status                AppStatus       `db:"status"      json:"status"`
	Replicas              int             `db:"replicas"    json:"replicas"`
	DesiredRunState       DesiredRunState `db:"desired_run_state" json:"desired_run_state"`
	ResumeReplicas        int             `db:"resume_replicas" json:"resume_replicas"`
	DesiredGeneration     int64           `db:"desired_generation" json:"desired_generation"`
	ObservedGeneration    int64           `db:"observed_generation" json:"observed_generation"`
	ObservedState         ObservedState   `db:"observed_state" json:"observed_state"`
	ReconcileErrorCode    string          `db:"reconcile_error_code" json:"reconcile_error_code,omitempty"`
	ReconcileErrorMessage string          `db:"reconcile_error_message" json:"reconcile_error_message,omitempty"`
	ReconcileRetryable    bool            `db:"reconcile_retryable" json:"reconcile_retryable"`
	ReconcileAttempt      int             `db:"reconcile_attempt" json:"reconcile_attempt"`
	LastTransitionAt      *time.Time      `db:"last_transition_at" json:"last_transition_at,omitempty"`
	LastReconciledAt      *time.Time      `db:"last_reconciled_at" json:"last_reconciled_at,omitempty"`
	EnvVars               string          `db:"env_vars"    json:"env_vars"`
	Ports                 string          `db:"ports"       json:"ports"`
	Volumes               string          `db:"volumes"     json:"volumes"`
	Expose                bool            `db:"expose"      json:"expose"`
	IngressContainerPort  uint32          `db:"ingress_container_port" json:"ingress_container_port"`
	Domain                string          `db:"domain"      json:"-"`
	DeletionTimestamp     *time.Time      `db:"deletion_timestamp" json:"deletion_timestamp,omitempty"`
	FinalizerState        string          `db:"finalizer_state" json:"finalizer_state"`
	CreatedAt             time.Time       `db:"created_at"  json:"created_at"`
	UpdatedAt             time.Time       `db:"updated_at"  json:"updated_at"`
}

type Deployment struct {
	ID                         string           `db:"id"            json:"id"`
	AppID                      string           `db:"app_id"        json:"app_id"`
	SourceType                 SourceType       `db:"source_type"   json:"source_type"`
	Image                      string           `db:"image"         json:"image"`
	Status                     DeploymentStatus `db:"status"        json:"status"`
	TriggeredBy                TriggeredBy      `db:"triggered_by"  json:"triggered_by"`
	ErrorMessage               string           `db:"error_message" json:"error_message"`
	CreatedAt                  time.Time        `db:"created_at"    json:"created_at"`
	FinishedAt                 *time.Time       `db:"finished_at"   json:"finished_at"`
	TargetGeneration           int64            `db:"target_generation" json:"target_generation"`
	PreviousObservedImage      string           `db:"previous_observed_image" json:"previous_observed_image"`
	ErrorCode                  string           `db:"error_code" json:"error_code,omitempty"`
	StartedAt                  *time.Time       `db:"started_at" json:"started_at,omitempty"`
	ConvergenceDeadline        *time.Time       `db:"convergence_deadline" json:"convergence_deadline,omitempty"`
	RollbackSourceDeploymentID *string          `db:"rollback_source_deployment_id" json:"rollback_source_deployment_id,omitempty"`
}
