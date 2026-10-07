package app_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/testkit/swarmfake"
)

type applicationMutationBarrierStore struct {
	store.Store
	entered chan<- string
	release <-chan struct{}
}

func (s *applicationMutationBarrierStore) UpdateApplicationIntent(ctx context.Context, name string, expectedGeneration int64, mutation store.ApplicationMutation) (*store.Application, error) {
	if err := s.wait(ctx, "update"); err != nil {
		return nil, err
	}
	return s.Store.UpdateApplicationIntent(ctx, name, expectedGeneration, mutation)
}

func (s *applicationMutationBarrierStore) CreateDeploymentIntent(ctx context.Context, name string, deployment *store.Deployment) (*store.Application, error) {
	if err := s.wait(ctx, "deploy"); err != nil {
		return nil, err
	}
	return s.Store.CreateDeploymentIntent(ctx, name, deployment)
}

func (s *applicationMutationBarrierStore) CreateDeletionIntent(ctx context.Context, name string, expectedGeneration int64) (*store.Application, error) {
	if err := s.wait(ctx, "delete"); err != nil {
		return nil, err
	}
	return s.Store.CreateDeletionIntent(ctx, name, expectedGeneration)
}

func (s *applicationMutationBarrierStore) wait(ctx context.Context, operation string) error {
	select {
	case s.entered <- operation:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func serviceWithStore(st store.Store, mock *swarmfake.Client) *app.Service {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return app.NewService(st, mock, "moduleos.local", log)
}

func newTestService(t *testing.T) (*app.Service, *store.SQLiteStore, *swarmfake.Client) {
	t.Helper()

	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mock := swarmfake.New()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := app.NewService(st, mock, "moduleos.local", log)

	return svc, st, mock
}

func mustCreateTestApp(t *testing.T, svc *app.Service, ctx context.Context, request app.CreateAppRequest) *store.Application {
	t.Helper()
	created, err := svc.CreateApp(ctx, request)
	if err != nil {
		t.Fatalf("CreateApp(%q): %v", request.Name, err)
	}
	return created
}

func mustCreateTestProject(t *testing.T, svc *app.Service, ctx context.Context, request app.CreateProjectRequest) *store.Project {
	t.Helper()
	created, err := svc.CreateProject(ctx, request)
	if err != nil {
		t.Fatalf("CreateProject(%q): %v", request.Slug, err)
	}
	return created
}

func mustGetTestApp(t *testing.T, st store.Store, ctx context.Context, name string) *store.Application {
	t.Helper()
	application, err := st.GetApplication(ctx, name)
	if err != nil {
		t.Fatalf("GetApplication(%q): %v", name, err)
	}
	return application
}

func mustUpdateTestApp(t *testing.T, st store.Store, ctx context.Context, application *store.Application) {
	t.Helper()
	if err := st.UpdateApplication(ctx, application); err != nil {
		t.Fatalf("UpdateApplication(%q): %v", application.Name, err)
	}
}

func mustDeployTestApp(t *testing.T, svc *app.Service, ctx context.Context, request app.DeployAppRequest) *store.Deployment {
	t.Helper()
	deployment, err := svc.DeployApp(ctx, request)
	if err != nil {
		t.Fatalf("DeployApp(%q): %v", request.AppName, err)
	}
	return deployment
}

func mustCreateTestLink(t *testing.T, svc *app.Service, ctx context.Context, projectSlug, targetApp, alias string) *app.ProjectLinkDetails {
	t.Helper()
	link, err := svc.CreateProjectLink(ctx, projectSlug, targetApp, alias)
	if err != nil {
		t.Fatalf("CreateProjectLink(%q, %q): %v", projectSlug, targetApp, err)
	}
	return link
}

func TestCreateApp_Success(t *testing.T) {
	svc, st, mock := newTestService(t)
	ctx := context.Background()

	created, err := svc.CreateApp(ctx, app.CreateAppRequest{
		Name:  "my-api",
		Image: "nginx:latest",
	})
	if err != nil {
		t.Fatalf("CreateApp error: %v", err)
	}

	if created.Name != "my-api" {
		t.Errorf("name: got %s, want my-api", created.Name)
	}
	if created.Status != store.AppStatusCreated {
		t.Errorf("status: got %s, want created", created.Status)
	}

	fromDB, err := st.GetApplication(ctx, "my-api")
	if err != nil {
		t.Fatalf("failed to read from DB: %v", err)
	}
	if fromDB.ID != created.ID {
		t.Errorf("DB ID mismatch")
	}

	// Runtime mutation is asynchronous and owned by the reconciler.
	if _, ok := mock.Services["moduleos_my-api"]; ok {
		t.Error("CreateApp mutated Swarm directly")
	}
}

func TestCreateApp_DuplicateName(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	req := app.CreateAppRequest{Name: "my-api", Image: "nginx:latest"}
	mustCreateTestApp(t, svc, ctx, req)

	_, err := svc.CreateApp(ctx, req)
	if err == nil {
		t.Error("expected error for duplicate name")
	}
}

func TestCreateApp_InvalidName(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	cases := []string{"", "MY_API", "my app", "my.api", "a-very-long-name-that-exceeds-the-sixty-three-character-limit-xyz"}
	for _, name := range cases {
		_, err := svc.CreateApp(ctx, app.CreateAppRequest{Name: name, Image: "nginx:latest"})
		if err == nil {
			t.Errorf("expected error for invalid name '%s'", name)
		}
	}
}

func TestCreateApp_AcceptsIntentWhenSwarmFails(t *testing.T) {
	svc, st, mock := newTestService(t)
	ctx := context.Background()

	mock.CreateError = fmt.Errorf("swarm error")

	_, err := svc.CreateApp(ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:latest"})
	if err != nil {
		t.Fatalf("intent must not depend on Swarm: %v", err)
	}

	// Runtime availability does not change the newly persisted pending observation.
	fromDB := mustGetTestApp(t, st, ctx, "my-api")
	if fromDB.ObservedState != store.ObservedStatePending {
		t.Errorf("observed state should remain pending, got: %s", fromDB.ObservedState)
	}
}

func TestStopApp_Success(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:latest"})

	app2 := mustGetTestApp(t, st, ctx, "my-api")
	app2.Status = store.AppStatusRunning
	mustUpdateTestApp(t, st, ctx, app2)

	if err := svc.StopApp(ctx, "my-api"); err != nil {
		t.Fatalf("StopApp error: %v", err)
	}

	fromDB := mustGetTestApp(t, st, ctx, "my-api")
	if fromDB.DesiredRunState != store.DesiredRunStateStopped || fromDB.Replicas != 0 {
		t.Errorf("stop intent not persisted: %#v", fromDB)
	}
}

func TestStopApp_AlreadyStoppedIsIdempotent(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:latest"})
	if err := svc.StopApp(ctx, "my-api"); err != nil {
		t.Fatalf("first stop failed: %v", err)
	}
	stopped, err := st.GetApplication(ctx, "my-api")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.StopApp(ctx, "my-api"); err != nil {
		t.Fatalf("idempotent stop failed: %v", err)
	}
	replayed, err := st.GetApplication(ctx, "my-api")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.DesiredGeneration != stopped.DesiredGeneration {
		t.Fatalf("idempotent stop advanced generation: first=%d replay=%d", stopped.DesiredGeneration, replayed.DesiredGeneration)
	}
}

func TestStartApp_Success(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:latest"})
	app2 := mustGetTestApp(t, st, ctx, "my-api")
	app2.Status = store.AppStatusStopped
	mustUpdateTestApp(t, st, ctx, app2)

	if err := svc.StartApp(ctx, "my-api"); err != nil {
		t.Fatalf("StartApp error: %v", err)
	}

	fromDB := mustGetTestApp(t, st, ctx, "my-api")
	if fromDB.DesiredRunState != store.DesiredRunStateRunning {
		t.Errorf("desired state: got %s, want running", fromDB.DesiredRunState)
	}
}

func TestDeleteApp_Success(t *testing.T) {
	svc, st, mock := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:latest"})

	if err := svc.DeleteApp(ctx, "my-api"); err != nil {
		t.Fatalf("DeleteApp error: %v", err)
	}

	// Deletion is durable before Docker cleanup and finalization.
	deleting, err := st.GetApplication(ctx, "my-api")
	if err != nil || deleting.DeletionTimestamp == nil {
		t.Fatalf("deletion intent not persisted: %#v, %v", deleting, err)
	}

	// Deletion intent does not mutate runtime state directly.
	if _, ok := mock.Services["moduleos_my-api"]; ok {
		t.Error("not removed from Swarm")
	}
}

func TestListApps(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "app-one", Image: "nginx:latest"})
	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "app-two", Image: "redis:7"})

	apps, err := svc.ListApps(ctx)
	if err != nil {
		t.Fatalf("ListApps error: %v", err)
	}
	if len(apps) != 2 {
		t.Errorf("app count: got %d, want 2", len(apps))
	}
}

func TestDeployApp_Success(t *testing.T) {
	svc, st, mock := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:1.0"})

	deployment, err := svc.DeployApp(ctx, app.DeployAppRequest{
		AppName: "my-api",
		Image:   "nginx:2.0",
	})
	if err != nil {
		t.Fatalf("DeployApp error: %v", err)
	}

	if deployment.Status != store.DeploymentStatusPending {
		t.Errorf("deployment status: got %s, want pending", deployment.Status)
	}
	if deployment.FinishedAt != nil {
		t.Error("pending deployment must not be finished")
	}

	fromDB := mustGetTestApp(t, st, ctx, "my-api")
	if fromDB.Image != "nginx:2.0" {
		t.Errorf("image: got %s, want nginx:2.0", fromDB.Image)
	}
	if fromDB.Status != store.AppStatusUpdating {
		t.Errorf("status: got %s, want updating", fromDB.Status)
	}

	if _, exists := mock.Services["moduleos_my-api"]; exists {
		t.Error("deploy mutated Swarm directly")
	}
}

func TestDeployApp_AcceptsIntentWhenSwarmFails(t *testing.T) {
	svc, st, mock := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:1.0"})
	mock.UpdateError = fmt.Errorf("swarm error")

	deployment, err := svc.DeployApp(ctx, app.DeployAppRequest{
		AppName: "my-api",
		Image:   "nginx:2.0",
	})
	if err != nil {
		t.Fatalf("intent must not depend on Swarm: %v", err)
	}

	if deployment.Status != store.DeploymentStatusPending {
		t.Errorf("deployment status: got %s, want pending", deployment.Status)
	}

	fromDB := mustGetTestApp(t, st, ctx, "my-api")
	if fromDB.Status != store.AppStatusUpdating {
		t.Errorf("app status: got %s, want updating", fromDB.Status)
	}
}

func TestDeployApp_EmptyImage_UsesCurrentImage(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:1.0"})

	// Empty image should fall back to the app's current image (nginx:1.0), not error.
	deployment, err := svc.DeployApp(ctx, app.DeployAppRequest{AppName: "my-api", Image: ""})
	if err != nil {
		t.Fatalf("expected no error for empty image (should use current), got: %v", err)
	}
	if deployment.Status != store.DeploymentStatusPending {
		t.Errorf("status: got %s, want pending", deployment.Status)
	}

	fromDB := mustGetTestApp(t, st, ctx, "my-api")
	if fromDB.Image != "nginx:1.0" {
		t.Errorf("image should remain nginx:1.0, got %s", fromDB.Image)
	}
}

func TestDeployApp_EmptyImage_NoImageEverSet(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()

	created, err := svc.CreateApp(ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:1.0"})
	if err != nil {
		t.Fatal(err)
	}
	created.Image = ""
	if err := st.UpdateApplication(ctx, created); err != nil {
		t.Fatal(err)
	}

	_, err = svc.DeployApp(ctx, app.DeployAppRequest{AppName: "my-api", Image: ""})
	if err == nil {
		t.Error("expected error: no image has ever been set")
	}
}

func TestListDeployments(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:1.0"})
	mustDeployTestApp(t, svc, ctx, app.DeployAppRequest{AppName: "my-api", Image: "nginx:2.0"})
	mustDeployTestApp(t, svc, ctx, app.DeployAppRequest{AppName: "my-api", Image: "nginx:3.0"})

	deployments, err := svc.ListDeployments(ctx, "my-api")
	if err != nil {
		t.Fatalf("ListDeployments error: %v", err)
	}
	if len(deployments) != 2 {
		t.Errorf("deployment count: got %d, want 2", len(deployments))
	}
}

func TestScaleApp_Success(t *testing.T) {
	svc, st, mock := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:1.0"})

	if err := svc.ScaleApp(ctx, "my-api", 3); err != nil {
		t.Fatalf("ScaleApp error: %v", err)
	}

	fromDB := mustGetTestApp(t, st, ctx, "my-api")
	if fromDB.Replicas != 3 {
		t.Errorf("replicas: got %d, want 3", fromDB.Replicas)
	}
	if _, exists := mock.Services["moduleos_my-api"]; exists {
		t.Error("scale mutated Swarm directly")
	}
}

func TestScaleApp_NegativeReplicas(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:1.0"})

	if err := svc.ScaleApp(ctx, "my-api", -1); err == nil {
		t.Error("expected error for negative replicas")
	}
}

func TestScaleApp_NotFound(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	if err := svc.ScaleApp(ctx, "ghost", 2); err == nil {
		t.Error("expected error for unknown app")
	}
}

func TestGetApp_Success(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:1.0"})

	got, err := svc.GetApp(ctx, "my-api")
	if err != nil {
		t.Fatalf("GetApp error: %v", err)
	}
	if got.Name != "my-api" {
		t.Errorf("name: got %s, want my-api", got.Name)
	}
}

func TestGetApp_NotFound(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.GetApp(ctx, "ghost")
	if err == nil {
		t.Error("expected error for unknown app")
	}
}

func TestUpdateApp_EnvVars(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:1.0"})

	newVars := map[string]string{"FOO": "bar", "BAZ": "qux"}
	updated, err := svc.UpdateApp(ctx, app.UpdateAppRequest{
		AppName: "my-api",
		EnvVars: newVars,
	})
	if err != nil {
		t.Fatalf("UpdateApp error: %v", err)
	}
	if updated.EnvVars == "{}" || updated.EnvVars == "" {
		t.Error("env vars not updated")
	}

	fromDB := mustGetTestApp(t, st, ctx, "my-api")
	if fromDB.EnvVars == "{}" {
		t.Error("env vars not persisted")
	}
}

func TestUpdateApp_Expose(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:1.0"})

	expose := true
	ingressPort := uint32(8080)
	_, err := svc.UpdateApp(ctx, app.UpdateAppRequest{
		AppName:              "my-api",
		Expose:               &expose,
		IngressContainerPort: &ingressPort,
	})
	if err != nil {
		t.Fatalf("UpdateApp error: %v", err)
	}

	fromDB := mustGetTestApp(t, st, ctx, "my-api")
	if !fromDB.Expose {
		t.Error("expose flag not persisted")
	}
}

func TestUpdateApp_Image(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-api", Image: "nginx:1.0"})

	newImage := "nginx:2.0"
	_, err := svc.UpdateApp(ctx, app.UpdateAppRequest{
		AppName: "my-api",
		Image:   &newImage,
	})
	if err != nil {
		t.Fatalf("UpdateApp error: %v", err)
	}

	fromDB := mustGetTestApp(t, st, ctx, "my-api")
	if fromDB.Image != "nginx:2.0" {
		t.Errorf("image: got %s, want nginx:2.0", fromDB.Image)
	}
}

func TestUpdateApp_NotFound(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.UpdateApp(ctx, app.UpdateAppRequest{AppName: "ghost"})
	if err == nil {
		t.Error("expected error for unknown app")
	}
}

// ── Project Link ──────────────────────────────────────────────────────────────

func TestCreateProjectLink_Success(t *testing.T) {
	svc, _, mock := newTestService(t)
	ctx := context.Background()

	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Target Project", Slug: "target-proj"})
	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "target-app", ProjectSlug: "target-proj", Image: "nginx:latest"})

	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Source Project", Slug: "source-proj"})

	link, err := svc.CreateProjectLink(ctx, "source-proj", "target-app", "custom.alias")
	if err != nil {
		t.Fatalf("CreateProjectLink error: %v", err)
	}
	if link.Alias != "custom.alias" {
		t.Errorf("alias: got %s, want custom.alias", link.Alias)
	}

	if len(mock.AttachCalls) != 0 {
		t.Fatalf("link intent mutated Swarm directly: %v", mock.AttachCalls)
	}
}

func TestCreateProjectLink_StoppedTargetPersistsWithoutRuntimeMutation(t *testing.T) {
	svc, st, mock := newTestService(t)
	ctx := context.Background()

	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Target Project", Slug: "target-proj"})
	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "target-app", ProjectSlug: "target-proj", Image: "nginx:latest"})

	target := mustGetTestApp(t, st, ctx, "target-app")
	target.Status = store.AppStatusStopped
	mustUpdateTestApp(t, st, ctx, target)

	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Source Project", Slug: "source-proj"})

	_, err := svc.CreateProjectLink(ctx, "source-proj", "target-app", "custom.alias")
	if err != nil {
		t.Fatalf("CreateProjectLink error: %v", err)
	}

	if len(mock.AttachCalls) != 0 {
		t.Fatalf("link intent mutated Swarm directly: %v", mock.AttachCalls)
	}
}

func TestCreateProjectLink_DefaultAlias(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Target Project", Slug: "target-proj"})
	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "target-app", ProjectSlug: "target-proj", Image: "nginx:latest"})

	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Source Project", Slug: "source-proj"})

	link, err := svc.CreateProjectLink(ctx, "source-proj", "target-app", "")
	if err != nil {
		t.Fatalf("CreateProjectLink error: %v", err)
	}
	if link.Alias != "target-proj.target-app" {
		t.Errorf("alias: got %s, want target-proj.target-app", link.Alias)
	}
}

func TestCreateProjectLink_ConflictSameProject(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Target Project", Slug: "target-proj"})
	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "target-app", ProjectSlug: "target-proj", Image: "nginx:latest"})

	_, err := svc.CreateProjectLink(ctx, "target-proj", "target-app", "")
	if err != store.ErrConflict {
		t.Fatalf("expected ErrConflict for same project, got: %v", err)
	}
}

func TestCreateProjectLink_AliasConflict(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Target Project", Slug: "target-proj"})
	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "target-app", ProjectSlug: "target-proj", Image: "nginx:latest"})

	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Source Project", Slug: "source-proj"})
	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "my-alias", ProjectSlug: "source-proj", Image: "nginx:latest"})

	_, err := svc.CreateProjectLink(ctx, "source-proj", "target-app", "my-alias")
	if err != store.ErrConflict {
		t.Fatalf("expected ErrConflict for alias conflict, got: %v", err)
	}
}

func TestDeleteProjectLink_Success(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Target Project", Slug: "target-proj"})
	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "target-app", ProjectSlug: "target-proj", Image: "nginx:latest"})
	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Source Project", Slug: "source-proj"})

	link := mustCreateTestLink(t, svc, ctx, "source-proj", "target-app", "")

	err := svc.DeleteProjectLink(ctx, "source-proj", link.ID)
	if err != nil {
		t.Fatalf("DeleteProjectLink error: %v", err)
	}

	links, err := svc.ListProjectLinks(ctx, "source-proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("link was not deleted")
	}
}

func TestDeleteProjectLink_WrongProject(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Target Project", Slug: "target-proj"})
	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "target-app", ProjectSlug: "target-proj", Image: "nginx:latest"})
	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Source Project", Slug: "source-proj"})
	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Other Project", Slug: "other-proj"})

	link := mustCreateTestLink(t, svc, ctx, "source-proj", "target-app", "")

	err := svc.DeleteProjectLink(ctx, "other-proj", link.ID)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound for wrong project ownership, got: %v", err)
	}
}

func TestValidateAlias(t *testing.T) {
	valid := []string{"redis", "root.redis", "a-b.c9"}
	for _, alias := range valid {
		if err := app.ValidateAlias(alias); err != nil {
			t.Errorf("ValidateAlias(%q): %v", alias, err)
		}
	}

	invalid := []string{"", "Redis", "a_b", ".redis", "redis.", "a..b", "-redis", "redis-", string(make([]byte, 64))}
	for _, alias := range invalid {
		if err := app.ValidateAlias(alias); !errors.Is(err, app.ErrValidation) {
			t.Errorf("ValidateAlias(%q) = %v, want ErrValidation", alias, err)
		}
	}
}

func FuzzValidateAliasNeverPanics(f *testing.F) {
	for _, seed := range []string{"root.redis", "", "a..b", "-bad"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, alias string) {
		_ = app.ValidateAlias(alias)
	})
}

func TestCreateProjectLink_PersistsWhileSwarmUnavailable(t *testing.T) {
	svc, st, mock := newTestService(t)
	ctx := context.Background()
	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Target", Slug: "target"})
	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "target-app", ProjectSlug: "target", Image: "nginx:latest"})
	source := mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Source", Slug: "source"})
	mock.AttachError = errors.New("attach failed")

	_, err := svc.CreateProjectLink(ctx, "source", "target-app", "target.app")
	if err != nil {
		t.Fatalf("intent depends on Swarm: %v", err)
	}
	links, listErr := st.ListProjectLinksByProject(ctx, source.ID)
	if listErr != nil || len(links) != 1 {
		t.Fatalf("link intent missing: %v, err=%v", links, listErr)
	}
}

func TestDeleteProject_ActiveLinkRejected(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Target", Slug: "target"})
	mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "target-app", ProjectSlug: "target", Image: "nginx:latest"})
	mustCreateTestProject(t, svc, ctx, app.CreateProjectRequest{Name: "Source", Slug: "source"})
	mustCreateTestLink(t, svc, ctx, "source", "target-app", "target.app")

	if err := svc.DeleteProject(ctx, "source"); err == nil {
		t.Fatal("expected project deletion with active link to fail")
	}
}

func TestConcurrentDeploymentsProduceOneCurrentGeneration(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()
	created, err := svc.CreateApp(ctx, app.CreateAppRequest{Name: "concurrent", Image: "nginx:1.0"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, image := range []string{"nginx:2.0", "nginx:3.0"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.DeployApp(ctx, app.DeployAppRequest{AppName: created.Name, Image: image}); err != nil {
				t.Errorf("deploy: %v", err)
			}
		}()
	}
	wg.Wait()
	deployments, err := st.ListDeployments(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pending, superseded int
	for _, deployment := range deployments {
		switch deployment.Status {
		case store.DeploymentStatusPending:
			pending++
		case store.DeploymentStatusSuperseded:
			superseded++
		}
	}
	if pending != 1 || superseded != 1 {
		t.Fatalf("pending=%d superseded=%d deployments=%#v", pending, superseded, deployments)
	}
}

func TestConcurrentDeployAndScaleSerializeWithoutLostIntent(t *testing.T) {
	baseService, st, mock := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := baseService.CreateApp(ctx, app.CreateAppRequest{Name: "deploy-scale", Image: "nginx:1.0", Replicas: 1})
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan string, 2)
	release := make(chan struct{})
	svc := serviceWithStore(&applicationMutationBarrierStore{Store: st, entered: entered, release: release}, mock)
	type result struct {
		operation string
		err       error
	}
	results := make(chan result, 2)
	go func() {
		_, deployErr := svc.DeployApp(ctx, app.DeployAppRequest{AppName: created.Name, Image: "nginx:2.0"})
		results <- result{operation: "deploy", err: deployErr}
	}()
	go func() {
		_, scaleErr := svc.ScaleAppIntent(ctx, created.Name, 2, created.DesiredGeneration)
		results <- result{operation: "scale", err: scaleErr}
	}()
	requireBarrierEntries(t, ctx, entered, "deploy", "update")
	close(release)

	operationErrors := map[string]error{}
	for range 2 {
		outcome := <-results
		operationErrors[outcome.operation] = outcome.err
	}
	if operationErrors["deploy"] != nil {
		t.Fatalf("deploy must remain accepted: %v", operationErrors["deploy"])
	}
	if scaleErr := operationErrors["scale"]; scaleErr != nil && !errors.Is(scaleErr, store.ErrGenerationConflict) {
		t.Fatalf("scale returned an unexpected error: %v", scaleErr)
	}

	persisted, err := st.GetApplication(ctx, created.Name)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Image != "nginx:2.0" {
		t.Fatalf("deploy intent was lost: image=%q", persisted.Image)
	}
	if operationErrors["scale"] == nil {
		if persisted.Replicas != 2 || persisted.DesiredGeneration != 3 {
			t.Fatalf("serialized deploy+scale state is inconsistent: %#v", persisted)
		}
	} else if persisted.Replicas != 1 || persisted.DesiredGeneration != 2 {
		t.Fatalf("conflicted scale partially mutated state: %#v", persisted)
	}
	deployments, err := st.ListDeployments(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 1 {
		t.Fatalf("deployment history = %#v", deployments)
	}
	if deployments[0].TargetGeneration < persisted.DesiredGeneration {
		if deployments[0].Status != store.DeploymentStatusSuperseded || deployments[0].FinishedAt == nil ||
			deployments[0].TargetGeneration != persisted.DesiredGeneration-1 {
			t.Fatalf("overtaken deployment was not terminalized: %#v", deployments[0])
		}
	} else if deployments[0].Status != store.DeploymentStatusPending || deployments[0].TargetGeneration != persisted.DesiredGeneration {
		t.Fatalf("current deployment lost its generation: %#v", deployments[0])
	}
}

func TestConcurrentUpdateAndDeleteHaveSingleGenerationWinner(t *testing.T) {
	baseService, st, mock := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := baseService.CreateApp(ctx, app.CreateAppRequest{Name: "update-delete", Image: "nginx:1.0"})
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan string, 2)
	release := make(chan struct{})
	svc := serviceWithStore(&applicationMutationBarrierStore{Store: st, entered: entered, release: release}, mock)
	newImage := "nginx:2.0"
	type result struct {
		operation string
		err       error
	}
	results := make(chan result, 2)
	go func() {
		_, updateErr := svc.UpdateApp(ctx, app.UpdateAppRequest{AppName: created.Name, Image: &newImage, ExpectedGeneration: created.DesiredGeneration})
		results <- result{operation: "update", err: updateErr}
	}()
	go func() {
		_, deleteErr := svc.DeleteAppIntent(ctx, created.Name, created.DesiredGeneration)
		results <- result{operation: "delete", err: deleteErr}
	}()
	requireBarrierEntries(t, ctx, entered, "update", "delete")
	close(release)

	operationErrors := map[string]error{}
	for range 2 {
		outcome := <-results
		operationErrors[outcome.operation] = outcome.err
	}
	successes := 0
	for operation, operationErr := range operationErrors {
		if operationErr == nil {
			successes++
			continue
		}
		if !errors.Is(operationErr, store.ErrGenerationConflict) {
			t.Fatalf("%s returned an unexpected error: %v", operation, operationErr)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent update/delete successes=%d, want exactly one: %#v", successes, operationErrors)
	}

	persisted, err := st.GetApplication(ctx, created.Name)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.DesiredGeneration != 2 {
		t.Fatalf("concurrent mutation advanced generation more than once: %#v", persisted)
	}
	if operationErrors["delete"] == nil {
		if persisted.DeletionTimestamp == nil || persisted.Image != "nginx:1.0" {
			t.Fatalf("winning deletion contains a partial update: %#v", persisted)
		}
	} else if persisted.DeletionTimestamp != nil || persisted.Image != newImage {
		t.Fatalf("winning update contains a partial deletion: %#v", persisted)
	}
}

func requireBarrierEntries(t *testing.T, ctx context.Context, entered <-chan string, expected ...string) {
	t.Helper()
	seen := make(map[string]bool, len(expected))
	for range expected {
		select {
		case operation := <-entered:
			seen[operation] = true
		case <-ctx.Done():
			t.Fatalf("concurrent operations did not reach mutation barrier: %v", ctx.Err())
		}
	}
	for _, operation := range expected {
		if !seen[operation] {
			t.Fatalf("operation %q did not reach mutation barrier; got %#v", operation, seen)
		}
	}
}

func TestApplicationResourceLimitIsEnforced(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.WithResourceLimits(1, 20)
	if _, err := svc.CreateApp(t.Context(), app.CreateAppRequest{Name: "first", Image: "nginx:1.27"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateApp(t.Context(), app.CreateAppRequest{Name: "second", Image: "nginx:1.27"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second application error = %v, want conflict", err)
	}
}

func TestProjectResourceLimitIncludesRootProject(t *testing.T) {
	svc, _, _ := newTestService(t)
	svc.WithResourceLimits(100, 2)
	if _, err := svc.CreateProject(t.Context(), app.CreateProjectRequest{Name: "First", Slug: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateProject(t.Context(), app.CreateProjectRequest{Name: "Second", Slug: "second"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second user project error = %v, want conflict", err)
	}
}

func TestApplicationCollectionLimitsAreEnforced(t *testing.T) {
	svc, _, _ := newTestService(t)
	environment := make(map[string]string, 201)
	for index := range 201 {
		environment[fmt.Sprintf("KEY_%d", index)] = "value"
	}
	if _, err := svc.CreateApp(t.Context(), app.CreateAppRequest{Name: "too-many-env", Image: "nginx:1.27", EnvVars: environment}); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("collection limit error = %v, want invalid data", err)
	}
}
