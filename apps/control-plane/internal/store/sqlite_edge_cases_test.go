package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newInternalTestStore(t *testing.T) *SQLiteStore {
	t.Helper()

	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func createInternalTestApp(t *testing.T, st *SQLiteStore, name string) *Application {
	t.Helper()

	app := canonicalTestApp(name)
	if err := st.CreateApplication(context.Background(), app); err != nil {
		t.Fatalf("CreateApplication(%q): %v", name, err)
	}
	return app
}

func createInternalTestProject(t *testing.T, st *SQLiteStore, slug string) *Project {
	t.Helper()

	now := time.Now().UTC()
	project := &Project{
		ID:        uuid.NewString(),
		Name:      "Project " + slug,
		Slug:      slug,
		Network:   "moduleos-" + slug + "-net",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateProject(context.Background(), project); err != nil {
		t.Fatalf("CreateProject(%q): %v", slug, err)
	}
	return project
}

func TestStoreHealthAndListPaths(t *testing.T) {
	st := newInternalTestStore(t)
	ctx := context.Background()

	if err := st.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	apps, err := st.ListApplications(ctx)
	if err != nil {
		t.Fatalf("ListApplications empty: %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("ListApplications empty count = %d, want 0", len(apps))
	}

	project := createInternalTestProject(t, st, "list-paths")
	app := canonicalTestApp("list-path-app")
	app.ProjectID = project.ID
	if err := st.CreateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	link := &ProjectLink{
		ID:              uuid.NewString(),
		SourceProjectID: project.ID,
		TargetAppID:     app.ID,
		Alias:           "list-path.app",
		CreatedAt:       time.Now().UTC(),
	}
	if err := st.CreateProjectLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	if link.DesiredGeneration != 1 {
		t.Fatalf("default desired generation = %d, want 1", link.DesiredGeneration)
	}

	apps, err = st.ListApplications(ctx)
	if err != nil || len(apps) != 1 || apps[0].ID != app.ID {
		t.Fatalf("ListApplications = %#v, %v", apps, err)
	}
	links, err := st.ListProjectLinksByProject(ctx, project.ID)
	if err != nil || len(links) != 1 || links[0].ID != link.ID {
		t.Fatalf("ListProjectLinksByProject = %#v, %v", links, err)
	}
	if err := st.MarkProjectLinkObserved(ctx, link.ID, link.DesiredGeneration); err != nil {
		t.Fatalf("MarkProjectLinkObserved: %v", err)
	}
	observed, err := st.GetProjectLink(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.ObservedGeneration != observed.DesiredGeneration {
		t.Fatalf("observed generation = %d, want %d", observed.ObservedGeneration, observed.DesiredGeneration)
	}
	if err := st.MarkProjectLinkObserved(ctx, link.ID, link.DesiredGeneration+1); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("stale link observation error = %v", err)
	}
	if err := st.MarkProjectLinkObserved(ctx, "missing-link", 1); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("missing link observation error = %v", err)
	}
}

func TestApplicationJSONOmitsPersistenceOnlyDomain(t *testing.T) {
	app := canonicalTestApp("json-domain-app")
	app.Domain = "internal.example.test"

	payload, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"domain"`) || strings.Contains(string(payload), app.Domain) {
		t.Fatalf("persistence-only domain leaked into application JSON: %s", payload)
	}
}

func TestProjectDeletionLifecycleEdgeCases(t *testing.T) {
	st := newInternalTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateProjectDeletionIntent(ctx, "root"); !errors.Is(err, ErrConflict) {
		t.Fatalf("root deletion intent error = %v, want ErrConflict", err)
	}
	if _, err := st.CreateProjectDeletionIntent(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing deletion intent error = %v, want ErrNotFound", err)
	}
	if err := st.DeleteProject(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteProject missing error = %v, want ErrNotFound", err)
	}
	if err := st.FinalizeProjectDeletion(ctx, RootProjectID); !errors.Is(err, ErrConflict) {
		t.Fatalf("FinalizeProjectDeletion root error = %v, want ErrConflict", err)
	}

	project := createInternalTestProject(t, st, "delete-project")
	duplicate := &Project{
		ID:        uuid.NewString(),
		Name:      "Duplicate",
		Slug:      project.Slug,
		Network:   "moduleos-duplicate-net",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreateProject(ctx, duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate project error = %v, want ErrConflict", err)
	}
	if err := st.MarkProjectObserved(ctx, project.ID, ObservedStateReady, "", ""); err != nil {
		t.Fatalf("MarkProjectObserved: %v", err)
	}
	ready, err := st.GetProjectByID(ctx, project.ID)
	if err != nil || ready.ObservedState != ObservedStateReady {
		t.Fatalf("observed project = %#v, %v", ready, err)
	}
	if err := st.MarkProjectObserved(ctx, "missing-project", ObservedStateFailed, "missing", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkProjectObserved missing error = %v, want ErrNotFound", err)
	}
	if err := st.FinalizeProjectDeletion(ctx, project.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("finalize active project error = %v, want ErrConflict", err)
	}

	app := canonicalTestApp("project-dependent-app")
	app.ProjectID = project.ID
	if err := st.CreateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	intent, err := st.CreateProjectDeletionIntent(ctx, project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if intent.DeletionTimestamp == nil || intent.FinalizerState != "pending" || intent.ObservedState != ObservedStatePending {
		t.Fatalf("deletion intent = %#v", intent)
	}
	repeated, err := st.CreateProjectDeletionIntent(ctx, project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.DeletionTimestamp == nil || !repeated.DeletionTimestamp.Equal(*intent.DeletionTimestamp) {
		t.Fatalf("repeated deletion changed timestamp: first=%v repeated=%v", intent.DeletionTimestamp, repeated.DeletionTimestamp)
	}
	if err := st.FinalizeProjectDeletion(ctx, project.ID); err == nil {
		t.Fatal("project with a dependent application must not be finalized")
	}
	if err := st.DeleteApplication(ctx, app.Name); err != nil {
		t.Fatal(err)
	}
	if err := st.FinalizeProjectDeletion(ctx, project.ID); err != nil {
		t.Fatalf("FinalizeProjectDeletion: %v", err)
	}
	if _, err := st.GetProjectByID(ctx, project.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("project after finalization error = %v, want ErrNotFound", err)
	}
	if err := st.FinalizeProjectDeletion(ctx, project.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeated finalization error = %v, want ErrConflict", err)
	}
}

func TestApplicationDeletionLifecycleAndCAS(t *testing.T) {
	st := newInternalTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateDeletionIntent(ctx, "missing", -1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing deletion intent error = %v, want ErrNotFound", err)
	}
	app := createInternalTestApp(t, st, "delete-app")
	if _, err := st.CreateDeletionIntent(ctx, app.Name, 0); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale deletion intent error = %v, want ErrGenerationConflict", err)
	}

	intent, err := st.CreateDeletionIntent(ctx, app.Name, 1)
	if err != nil {
		t.Fatal(err)
	}
	if intent.DesiredGeneration != 2 || intent.DeletionTimestamp == nil || intent.FinalizerState != "pending" || intent.ObservedState != ObservedStateDeleting {
		t.Fatalf("deletion intent = %#v", intent)
	}
	repeated, err := st.CreateDeletionIntent(ctx, app.Name, 2)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.DesiredGeneration != 2 || repeated.DeletionTimestamp == nil || !repeated.DeletionTimestamp.Equal(*intent.DeletionTimestamp) {
		t.Fatalf("repeated deletion intent = %#v", repeated)
	}
	replicas := 2
	if _, err := st.UpdateApplicationIntent(ctx, app.Name, 2, ApplicationMutation{Replicas: &replicas}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mutation during deletion error = %v, want ErrConflict", err)
	}
	if err := st.FinalizeApplicationDeletion(ctx, app.ID, 1); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("wrong-generation finalization error = %v, want ErrGenerationConflict", err)
	}
	if err := st.FinalizeApplicationDeletion(ctx, "missing-app", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing finalization error = %v, want ErrNotFound", err)
	}
	if err := st.FinalizeApplicationDeletion(ctx, app.ID, 2); err != nil {
		t.Fatalf("FinalizeApplicationDeletion: %v", err)
	}
	if err := st.FinalizeApplicationDeletion(ctx, app.ID, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeated finalization error = %v, want ErrNotFound", err)
	}
}

func TestReconcileDiagnosticsStateAndCAS(t *testing.T) {
	st := newInternalTestStore(t)
	ctx := context.Background()
	app := createInternalTestApp(t, st, "diagnostics-app")
	longMessage := strings.Repeat("x", 1100)

	if err := st.PersistReconcileDiagnostics(ctx, app.ID, 1, "runtime_unavailable", longMessage, true, 7); err != nil {
		t.Fatal(err)
	}
	degraded, err := st.GetApplicationByID(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if degraded.ObservedState != ObservedStateDegraded || degraded.ReconcileErrorCode != "runtime_unavailable" ||
		len(degraded.ReconcileErrorMessage) != 1024 || !degraded.ReconcileRetryable || degraded.ReconcileAttempt != 7 || degraded.LastReconciledAt == nil {
		t.Fatalf("degraded diagnostics = %#v", degraded)
	}

	if err := st.PersistReconcileDiagnostics(ctx, app.ID, 1, "", "retry cleared", false, 0); err != nil {
		t.Fatal(err)
	}
	reconciling, err := st.GetApplicationByID(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciling.ObservedState != ObservedStateReconciling || reconciling.ReconcileErrorMessage != "retry cleared" || reconciling.ReconcileRetryable {
		t.Fatalf("cleared diagnostics = %#v", reconciling)
	}
	if err := st.PersistReconcileDiagnostics(ctx, app.ID, 0, "stale", "stale", false, 1); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("stale diagnostics error = %v, want ErrStaleObservation", err)
	}
	if err := st.PersistReconcileDiagnostics(ctx, "missing-app", 1, "missing", "missing", false, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing diagnostics error = %v, want ErrNotFound", err)
	}
}

func TestDeploymentStateTransitionsAndCAS(t *testing.T) {
	st := newInternalTestStore(t)
	ctx := context.Background()
	app := createInternalTestApp(t, st, "deployment-state-app")
	deployment := &Deployment{
		ID:               uuid.NewString(),
		AppID:            app.ID,
		SourceType:       SourceTypeImage,
		Image:            "nginx:1.27",
		Status:           DeploymentStatusPending,
		TriggeredBy:      TriggeredByAPI,
		CreatedAt:        time.Now().UTC(),
		TargetGeneration: 1,
	}
	if err := st.CreateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDeploymentState(ctx, "missing-deployment", 1, DeploymentStatusFailed, "missing", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing transition error = %v, want ErrNotFound", err)
	}
	if err := st.MarkDeploymentState(ctx, deployment.ID, 1, DeploymentStatusApplying, "", ""); err != nil {
		t.Fatal(err)
	}
	applying, err := st.GetDeployment(ctx, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if applying.Status != DeploymentStatusApplying || applying.StartedAt == nil || applying.FinishedAt != nil {
		t.Fatalf("applying deployment = %#v", applying)
	}
	startedAt := *applying.StartedAt
	if err := st.MarkDeploymentState(ctx, deployment.ID, 1, DeploymentStatusInProgress, "", ""); err != nil {
		t.Fatal(err)
	}
	inProgress, err := st.GetDeployment(ctx, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inProgress.StartedAt == nil || !inProgress.StartedAt.Equal(startedAt) {
		t.Fatalf("replayed start changed timestamp: first=%v next=%v", startedAt, inProgress.StartedAt)
	}
	if err := st.MarkDeploymentState(ctx, deployment.ID, 1, DeploymentStatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	succeeded, err := st.GetDeployment(ctx, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if succeeded.Status != DeploymentStatusSucceeded || succeeded.FinishedAt == nil {
		t.Fatalf("succeeded deployment = %#v", succeeded)
	}
	finishedAt := *succeeded.FinishedAt
	time.Sleep(2 * time.Millisecond)
	if err := st.MarkDeploymentState(ctx, deployment.ID, 1, DeploymentStatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	replayed, err := st.GetDeployment(ctx, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.FinishedAt == nil || !replayed.FinishedAt.Equal(finishedAt) {
		t.Fatalf("replayed terminal transition changed finished_at: first=%v replayed=%v", finishedAt, replayed.FinishedAt)
	}
	if err := st.MarkDeploymentState(ctx, deployment.ID, 1, DeploymentStatusFailed, "late_failure", "must not replace success"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal rewrite error = %v, want ErrInvalidTransition", err)
	}
	stillSucceeded, err := st.GetDeployment(ctx, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillSucceeded.Status != DeploymentStatusSucceeded || stillSucceeded.ErrorCode != "" ||
		stillSucceeded.FinishedAt == nil || !stillSucceeded.FinishedAt.Equal(finishedAt) {
		t.Fatalf("terminal rewrite mutated succeeded deployment: %#v", stillSucceeded)
	}
	if err := st.MarkDeploymentState(ctx, deployment.ID, 0, DeploymentStatusFailed, "stale", "stale"); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("stale transition error = %v, want ErrStaleObservation", err)
	}
	if err := st.MarkDeploymentState(ctx, deployment.ID, 1, DeploymentStatus("unknown"), "", ""); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("unknown status error = %v, want ErrInvalidData", err)
	}

	failed := &Deployment{
		ID:               uuid.NewString(),
		AppID:            app.ID,
		SourceType:       SourceTypeImage,
		Image:            "nginx:broken",
		Status:           DeploymentStatusPending,
		TriggeredBy:      TriggeredByAPI,
		CreatedAt:        time.Now().UTC(),
		TargetGeneration: 1,
	}
	if err := st.CreateDeployment(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDeploymentState(ctx, failed.ID, 1, DeploymentStatusFailed, "runtime_error", strings.Repeat("e", 1100)); err != nil {
		t.Fatal(err)
	}
	failedState, err := st.GetDeployment(ctx, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedState.FinishedAt == nil || failedState.ErrorCode != "runtime_error" || len(failedState.ErrorMessage) != 1024 {
		t.Fatalf("failed deployment = %#v", failedState)
	}
	failedAt := *failedState.FinishedAt
	if err := st.MarkDeploymentState(ctx, failed.ID, 1, DeploymentStatusSucceeded, "", ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("failed-to-succeeded error = %v, want ErrInvalidTransition", err)
	}
	if err := st.MarkDeploymentState(ctx, failed.ID, 1, DeploymentStatusFailed, "replacement", "replacement"); err != nil {
		t.Fatalf("idempotent failed replay: %v", err)
	}
	failedReplay, err := st.GetDeployment(ctx, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedReplay.Status != DeploymentStatusFailed || failedReplay.ErrorCode != "runtime_error" ||
		len(failedReplay.ErrorMessage) != 1024 || failedReplay.FinishedAt == nil || !failedReplay.FinishedAt.Equal(failedAt) {
		t.Fatalf("terminal replay mutated failed deployment: %#v", failedReplay)
	}
	missing := &Deployment{ID: "missing-deployment"}
	if err := st.UpdateDeployment(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateDeployment missing error = %v, want ErrNotFound", err)
	}
}

func TestUpdateApplicationIntentEdgeCases(t *testing.T) {
	t.Run("stop and resume preserves replicas", func(t *testing.T) {
		st := newInternalTestStore(t)
		ctx := context.Background()
		app := createInternalTestApp(t, st, "stop-resume-app")
		two := 2
		updated, err := st.UpdateApplicationIntent(ctx, app.Name, 1, ApplicationMutation{Replicas: &two})
		if err != nil {
			t.Fatal(err)
		}
		stopped := DesiredRunStateStopped
		updated, err = st.UpdateApplicationIntent(ctx, app.Name, updated.DesiredGeneration, ApplicationMutation{DesiredRunState: &stopped})
		if err != nil {
			t.Fatal(err)
		}
		if updated.Replicas != 0 || updated.ResumeReplicas != 2 || updated.DesiredRunState != DesiredRunStateStopped {
			t.Fatalf("stopped application = %#v", updated)
		}
		running := DesiredRunStateRunning
		updated, err = st.UpdateApplicationIntent(ctx, app.Name, updated.DesiredGeneration, ApplicationMutation{DesiredRunState: &running})
		if err != nil {
			t.Fatal(err)
		}
		if updated.Replicas != 2 || updated.ResumeReplicas != 2 || updated.DesiredRunState != DesiredRunStateRunning {
			t.Fatalf("resumed application = %#v", updated)
		}
	})

	t.Run("invalid mutations roll back", func(t *testing.T) {
		tests := []struct {
			name     string
			mutation func() ApplicationMutation
		}{
			{
				name: "negative replicas",
				mutation: func() ApplicationMutation {
					value := -1
					return ApplicationMutation{Replicas: &value}
				},
			},
			{
				name: "invalid desired state",
				mutation: func() ApplicationMutation {
					value := DesiredRunState("paused")
					return ApplicationMutation{DesiredRunState: &value}
				},
			},
			{
				name: "invalid environment JSON",
				mutation: func() ApplicationMutation {
					value := "KEY=value"
					return ApplicationMutation{EnvVars: &value}
				},
			},
			{
				name: "invalid ports JSON",
				mutation: func() ApplicationMutation {
					value := "80:8080"
					return ApplicationMutation{Ports: &value}
				},
			},
			{
				name: "invalid volumes JSON",
				mutation: func() ApplicationMutation {
					value := "/srv/data:/data"
					return ApplicationMutation{Volumes: &value}
				},
			},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				st := newInternalTestStore(t)
				ctx := context.Background()
				app := createInternalTestApp(t, st, "invalid-mutation-app")
				if _, err := st.UpdateApplicationIntent(ctx, app.Name, 1, test.mutation()); !errors.Is(err, ErrInvalidData) {
					t.Fatalf("error = %v, want ErrInvalidData", err)
				}
				persisted, err := st.GetApplication(ctx, app.Name)
				if err != nil {
					t.Fatal(err)
				}
				if persisted.DesiredGeneration != 1 || persisted.Image != app.Image || persisted.Replicas != app.Replicas {
					t.Fatalf("failed mutation changed state: %#v", persisted)
				}
			})
		}
	})

	t.Run("missing and cancellation fail closed", func(t *testing.T) {
		st := newInternalTestStore(t)
		ctx := context.Background()
		replicas := 2
		if _, err := st.UpdateApplicationIntent(ctx, "missing", -1, ApplicationMutation{Replicas: &replicas}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing mutation error = %v, want ErrNotFound", err)
		}
		app := createInternalTestApp(t, st, "cancelled-mutation-app")
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := st.UpdateApplicationIntent(cancelled, app.Name, 1, ApplicationMutation{Replicas: &replicas}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled mutation error = %v, want context.Canceled", err)
		}
		persisted, err := st.GetApplication(ctx, app.Name)
		if err != nil {
			t.Fatal(err)
		}
		if persisted.DesiredGeneration != 1 || persisted.Replicas != app.Replicas {
			t.Fatalf("cancelled mutation changed state: %#v", persisted)
		}
	})
}

func TestDeploymentIntentDefaultsSupersedeAndRollback(t *testing.T) {
	st := newInternalTestStore(t)
	ctx := context.Background()
	app := canonicalTestApp("deployment-intent-app")
	app.ObservedImage = "nginx:observed"
	if err := st.CreateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}

	first := &Deployment{ID: uuid.NewString(), Image: "nginx:first", CreatedAt: time.Now().UTC()}
	updated, err := st.CreateDeploymentIntent(ctx, app.Name, first)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DesiredGeneration != 2 || first.TargetGeneration != 2 || first.AppID != app.ID ||
		first.Status != DeploymentStatusPending || first.SourceType != SourceTypeImage || first.TriggeredBy != TriggeredByAPI ||
		first.PreviousObservedImage != app.ObservedImage {
		t.Fatalf("defaulted deployment intent: app=%#v deployment=%#v", updated, first)
	}

	second := &Deployment{ID: uuid.NewString(), Image: "nginx:second", CreatedAt: time.Now().UTC()}
	updated, err = st.CreateDeploymentIntent(ctx, app.Name, second)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DesiredGeneration != 3 || second.TargetGeneration != 3 {
		t.Fatalf("second deployment intent: app=%#v deployment=%#v", updated, second)
	}
	superseded, err := st.GetDeployment(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if superseded.Status != DeploymentStatusSuperseded {
		t.Fatalf("first deployment status = %s, want superseded", superseded.Status)
	}

	duplicate := &Deployment{ID: second.ID, Image: "nginx:must-rollback", CreatedAt: time.Now().UTC()}
	if _, err := st.CreateDeploymentIntent(ctx, app.Name, duplicate); err == nil {
		t.Fatal("duplicate deployment ID must fail")
	}
	afterRollback, err := st.GetApplication(ctx, app.Name)
	if err != nil {
		t.Fatal(err)
	}
	if afterRollback.DesiredGeneration != 3 || afterRollback.Image != second.Image {
		t.Fatalf("failed deployment intent was not rolled back: %#v", afterRollback)
	}
	secondAfterRollback, err := st.GetDeployment(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondAfterRollback.Status != DeploymentStatusPending {
		t.Fatalf("failed transaction superseded current deployment: %#v", secondAfterRollback)
	}

	if _, err := st.CreateDeploymentIntent(ctx, "missing", &Deployment{ID: uuid.NewString(), Image: "nginx:missing", CreatedAt: time.Now().UTC()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing application error = %v, want ErrNotFound", err)
	}
	deleting, err := st.CreateDeletionIntent(ctx, app.Name, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDeploymentIntent(ctx, deleting.Name, &Deployment{ID: uuid.NewString(), Image: "nginx:blocked", CreatedAt: time.Now().UTC()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("deployment during deletion error = %v, want ErrConflict", err)
	}
}

func TestApplicationObservationEdges(t *testing.T) {
	st := newInternalTestStore(t)
	ctx := context.Background()

	if err := st.MarkApplicationObserved(ctx, "missing", ObservedApplicationUpdate{Generation: -1}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("negative observation error = %v, want ErrInvalidData", err)
	}
	if err := st.MarkApplicationObserved(ctx, "missing", ObservedApplicationUpdate{Generation: 1, State: ObservedStateRunning}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing observation error = %v, want ErrNotFound", err)
	}
	app := createInternalTestApp(t, st, "observation-app")
	transition := time.Now().UTC().Add(-time.Second)
	if err := st.MarkApplicationObserved(ctx, app.ID, ObservedApplicationUpdate{
		Generation:   1,
		State:        ObservedStateRunning,
		Image:        "nginx:observed",
		TransitionAt: transition,
	}); err != nil {
		t.Fatal(err)
	}
	observed, err := st.GetApplicationByID(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observed.ObservedGeneration != 1 || observed.ObservedState != ObservedStateRunning || observed.ObservedImage != "nginx:observed" ||
		observed.LastTransitionAt == nil || !observed.LastTransitionAt.Equal(transition) {
		t.Fatalf("observed application = %#v", observed)
	}
	if err := st.MarkApplicationObserved(ctx, app.ID, ObservedApplicationUpdate{Generation: 2, State: ObservedStateRunning}); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("future observation error = %v, want ErrStaleObservation", err)
	}
}
