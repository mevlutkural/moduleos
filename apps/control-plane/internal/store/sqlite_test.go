package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

func newTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// testApp returns a ready-to-use Application struct with the root project ID.
func testApp(name string) *store.Application {
	return &store.Application{
		ID:         uuid.NewString(),
		ProjectID:  store.RootProjectID,
		Name:       name,
		SourceType: store.SourceTypeImage,
		Image:      "nginx:latest",
		Status:     store.AppStatusCreated,
		Replicas:   1,
		EnvVars:    "{}",
		Ports:      "[]",
		Volumes:    "[]",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
}

func TestProject_CreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// root is seeded automatically by migration
	root, err := s.GetProject(ctx, "root")
	if err != nil {
		t.Fatalf("root project not found: %v", err)
	}
	if root.Slug != "root" {
		t.Errorf("slug: got %s, want root", root.Slug)
	}

	// create a new project
	p := &store.Project{
		ID:        uuid.NewString(),
		Name:      "E-Commerce",
		Slug:      "e-commerce",
		Network:   "moduleos-e-commerce-net",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject error: %v", err)
	}

	got, err := s.GetProject(ctx, "e-commerce")
	if err != nil {
		t.Fatalf("GetProject error: %v", err)
	}
	if got.Name != "E-Commerce" {
		t.Errorf("name: got %s, want E-Commerce", got.Name)
	}
}

func TestProject_DeleteRootFails(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.DeleteProject(ctx, "root")
	if err == nil {
		t.Error("root project must not be deletable")
	}
}

func TestApplication_CreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	app := testApp("my-api")
	if err := s.CreateApplication(ctx, app); err != nil {
		t.Fatalf("CreateApplication error: %v", err)
	}

	got, err := s.GetApplication(ctx, "my-api")
	if err != nil {
		t.Fatalf("GetApplication error: %v", err)
	}

	if got.Name != app.Name {
		t.Errorf("name: got %s, want %s", got.Name, app.Name)
	}
	if got.ProjectID != store.RootProjectID {
		t.Errorf("project_id: got %s, want root", got.ProjectID)
	}
	if got.Expose != app.Expose {
		t.Errorf("expose: got %v, want %v", got.Expose, app.Expose)
	}
}

func TestApplication_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetApplication(ctx, "nonexistent-app")
	if err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestApplication_UpdateStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	app := testApp("my-api")
	if err := s.CreateApplication(ctx, app); err != nil {
		t.Fatalf("CreateApplication error: %v", err)
	}

	app.Status = store.AppStatusRunning
	if err := s.UpdateApplication(ctx, app); err != nil {
		t.Fatalf("UpdateApplication error: %v", err)
	}

	got, _ := s.GetApplication(ctx, "my-api")
	if got.Status != store.AppStatusRunning {
		t.Errorf("status: got %s, want %s", got.Status, store.AppStatusRunning)
	}
}

func TestApplication_Delete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	app := testApp("my-api")
	_ = s.CreateApplication(ctx, app)

	if err := s.DeleteApplication(ctx, "my-api"); err != nil {
		t.Fatalf("DeleteApplication error: %v", err)
	}

	_, err := s.GetApplication(ctx, "my-api")
	if err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got: %v", err)
	}
}

func TestApplication_ListByProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// project for a separate team
	p := &store.Project{
		ID:        uuid.NewString(),
		Name:      "Payments",
		Slug:      "payments",
		Network:   "moduleos-payments-net",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = s.CreateProject(ctx, p)

	// add 2 apps to root, 1 to payments
	_ = s.CreateApplication(ctx, testApp("root-app-1"))
	_ = s.CreateApplication(ctx, testApp("root-app-2"))

	paymentsApp := testApp("payments-app")
	paymentsApp.ProjectID = p.ID
	_ = s.CreateApplication(ctx, paymentsApp)

	rootApps, _ := s.ListApplicationsByProject(ctx, store.RootProjectID)
	if len(rootApps) != 2 {
		t.Errorf("root app count: got %d, want 2", len(rootApps))
	}

	paymentsApps, _ := s.ListApplicationsByProject(ctx, p.ID)
	if len(paymentsApps) != 1 {
		t.Errorf("payments app count: got %d, want 1", len(paymentsApps))
	}
}

func TestDeployment_CreateAndList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	app := testApp("my-api")
	_ = s.CreateApplication(ctx, app)

	d := &store.Deployment{
		ID:          uuid.NewString(),
		AppID:       app.ID,
		SourceType:  store.SourceTypeImage,
		Image:       "nginx:latest",
		Status:      store.DeploymentStatusPending,
		TriggeredBy: store.TriggeredByAPI,
		CreatedAt:   time.Now(),
	}

	if err := s.CreateDeployment(ctx, d); err != nil {
		t.Fatalf("CreateDeployment error: %v", err)
	}

	list, err := s.ListDeployments(ctx, app.ID)
	if err != nil {
		t.Fatalf("ListDeployments error: %v", err)
	}

	if len(list) != 1 {
		t.Errorf("deployment count: got %d, want 1", len(list))
	}
	if list[0].Image != d.Image {
		t.Errorf("image: got %s, want %s", list[0].Image, d.Image)
	}
}

func TestDeployment_GetAndUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	app := testApp("my-api")
	_ = s.CreateApplication(ctx, app)

	now := time.Now()
	d := &store.Deployment{
		ID:          uuid.NewString(),
		AppID:       app.ID,
		SourceType:  store.SourceTypeImage,
		Image:       "nginx:1.0",
		Status:      store.DeploymentStatusPending,
		TriggeredBy: store.TriggeredByAPI,
		CreatedAt:   now,
	}
	_ = s.CreateDeployment(ctx, d)

	got, err := s.GetDeployment(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetDeployment error: %v", err)
	}
	if got.ID != d.ID {
		t.Errorf("id mismatch: got %s, want %s", got.ID, d.ID)
	}

	// Update status to success and set FinishedAt.
	finished := time.Now()
	got.Status = store.DeploymentStatusSuccess
	got.ResolvedImage = "nginx:1.0@sha256:" + strings.Repeat("a", 64)
	got.FinishedAt = &finished
	if err := s.UpdateDeployment(ctx, got); err != nil {
		t.Fatalf("UpdateDeployment error: %v", err)
	}

	updated, _ := s.GetDeployment(ctx, d.ID)
	if updated.Status != store.DeploymentStatusSuccess {
		t.Errorf("status: got %s, want success", updated.Status)
	}
	if updated.FinishedAt == nil {
		t.Error("finished_at was not persisted")
	}
	if updated.ResolvedImage != got.ResolvedImage {
		t.Errorf("resolved image: got %s, want %s", updated.ResolvedImage, got.ResolvedImage)
	}
}

func TestDeployment_GetNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetDeployment(ctx, "nonexistent-id")
	if err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// ── Project (extended) ────────────────────────────────────────────────────────

func TestProject_ListProjects(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// root is seeded automatically
	p := &store.Project{
		ID:        uuid.NewString(),
		Name:      "Backend",
		Slug:      "backend",
		Network:   "moduleos-backend-net",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = s.CreateProject(ctx, p)

	list, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects error: %v", err)
	}
	if len(list) < 2 {
		t.Errorf("expected at least 2 projects (root + backend), got %d", len(list))
	}
}

func TestProject_GetByID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// root project is seeded
	got, err := s.GetProjectByID(ctx, store.RootProjectID)
	if err != nil {
		t.Fatalf("GetProjectByID error: %v", err)
	}
	if got.Slug != "root" {
		t.Errorf("slug: got %s, want root", got.Slug)
	}
}

func TestProject_GetByID_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetProjectByID(ctx, "00000000-0000-0000-0000-000000000999")
	if err == nil {
		t.Error("expected error for nonexistent project ID")
	}
}

// ── ProjectLink ───────────────────────────────────────────────────────────────

func TestProjectLink_CreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sourceProj := &store.Project{
		ID:        uuid.NewString(),
		Name:      "Source",
		Slug:      "source",
		Network:   "moduleos-source-net",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = s.CreateProject(ctx, sourceProj)

	targetApp := testApp("target-app")
	_ = s.CreateApplication(ctx, targetApp)

	pl := &store.ProjectLink{
		ID:              uuid.NewString(),
		SourceProjectID: sourceProj.ID,
		TargetAppID:     targetApp.ID,
		Alias:           "target.app",
		CreatedAt:       time.Now(),
	}

	if err := s.CreateProjectLink(ctx, pl); err != nil {
		t.Fatalf("CreateProjectLink error: %v", err)
	}

	got, err := s.GetProjectLink(ctx, pl.ID)
	if err != nil {
		t.Fatalf("GetProjectLink error: %v", err)
	}

	if got.Alias != pl.Alias {
		t.Errorf("alias: got %s, want %s", got.Alias, pl.Alias)
	}
}

func TestProjectLink_Conflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sourceProj := &store.Project{
		ID:        uuid.NewString(),
		Name:      "Source",
		Slug:      "source",
		Network:   "moduleos-source-net",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = s.CreateProject(ctx, sourceProj)

	targetApp := testApp("target-app")
	_ = s.CreateApplication(ctx, targetApp)

	pl1 := &store.ProjectLink{
		ID:              uuid.NewString(),
		SourceProjectID: sourceProj.ID,
		TargetAppID:     targetApp.ID,
		Alias:           "target.app",
		CreatedAt:       time.Now(),
	}
	if err := s.CreateProjectLink(ctx, pl1); err != nil {
		t.Fatalf("first create error: %v", err)
	}

	pl2 := &store.ProjectLink{
		ID:              uuid.NewString(),
		SourceProjectID: sourceProj.ID,
		TargetAppID:     targetApp.ID,
		Alias:           "target.app",
		CreatedAt:       time.Now(),
	}
	err := s.CreateProjectLink(ctx, pl2)
	if err != store.ErrConflict {
		t.Fatalf("expected ErrConflict, got: %v", err)
	}
}

func TestProjectLink_Delete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sourceProj := &store.Project{
		ID:        uuid.NewString(),
		Name:      "Source",
		Slug:      "source",
		Network:   "moduleos-source-net",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = s.CreateProject(ctx, sourceProj)
	targetApp := testApp("target-app")
	_ = s.CreateApplication(ctx, targetApp)

	pl := &store.ProjectLink{
		ID:              uuid.NewString(),
		SourceProjectID: sourceProj.ID,
		TargetAppID:     targetApp.ID,
		Alias:           "target.app",
		CreatedAt:       time.Now(),
	}
	_ = s.CreateProjectLink(ctx, pl)

	if err := s.DeleteProjectLink(ctx, pl.ID); err != nil {
		t.Fatalf("DeleteProjectLink error: %v", err)
	}

	_, err := s.GetProjectLink(ctx, pl.ID)
	if err != store.ErrNotFound {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestProjectLink_CascadeDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sourceProj := &store.Project{
		ID:        uuid.NewString(),
		Name:      "Source",
		Slug:      "source",
		Network:   "moduleos-source-net",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = s.CreateProject(ctx, sourceProj)
	targetApp := testApp("target-app")
	_ = s.CreateApplication(ctx, targetApp)

	pl := &store.ProjectLink{
		ID:              uuid.NewString(),
		SourceProjectID: sourceProj.ID,
		TargetAppID:     targetApp.ID,
		Alias:           "target.app",
		CreatedAt:       time.Now(),
	}
	_ = s.CreateProjectLink(ctx, pl)

	// delete app, should cascade to project link
	_ = s.DeleteApplication(ctx, targetApp.Name)

	_, err := s.GetProjectLink(ctx, pl.ID)
	if err != store.ErrNotFound {
		t.Errorf("expected link to be deleted on app cascade, got err: %v", err)
	}
}

func TestApplication_GetByID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	app := testApp("my-api-id")
	if err := s.CreateApplication(ctx, app); err != nil {
		t.Fatalf("CreateApplication error: %v", err)
	}

	got, err := s.GetApplicationByID(ctx, app.ID)
	if err != nil {
		t.Fatalf("GetApplicationByID error: %v", err)
	}
	if got.Name != app.Name {
		t.Errorf("name: got %s, want %s", got.Name, app.Name)
	}
}

func TestApplication_GetByID_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetApplicationByID(context.Background(), "missing")
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestProjectLink_AliasConflictAcrossTargets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	source := &store.Project{ID: uuid.NewString(), Name: "Source", Slug: "source-alias", Network: "source-alias-net", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.CreateProject(ctx, source); err != nil {
		t.Fatal(err)
	}
	appOne := testApp("target-one")
	appTwo := testApp("target-two")
	if err := s.CreateApplication(ctx, appOne); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateApplication(ctx, appTwo); err != nil {
		t.Fatal(err)
	}
	first := &store.ProjectLink{ID: uuid.NewString(), SourceProjectID: source.ID, TargetAppID: appOne.ID, Alias: "shared.alias", CreatedAt: time.Now()}
	second := &store.ProjectLink{ID: uuid.NewString(), SourceProjectID: source.ID, TargetAppID: appTwo.ID, Alias: "shared.alias", CreatedAt: time.Now()}
	if err := s.CreateProjectLink(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProjectLink(ctx, second); err != store.ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestProjectLink_ForeignKeysEnforced(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	link := &store.ProjectLink{ID: uuid.NewString(), SourceProjectID: "missing-project", TargetAppID: "missing-app", Alias: "missing.target", CreatedAt: time.Now()}
	if err := s.CreateProjectLink(ctx, link); err == nil {
		t.Fatal("expected foreign key error")
	}
}

func TestProjectLink_SourceProjectCascadeDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	source := &store.Project{ID: uuid.NewString(), Name: "Source", Slug: "source-cascade", Network: "source-cascade-net", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.CreateProject(ctx, source); err != nil {
		t.Fatal(err)
	}
	target := testApp("cascade-target")
	if err := s.CreateApplication(ctx, target); err != nil {
		t.Fatal(err)
	}
	link := &store.ProjectLink{ID: uuid.NewString(), SourceProjectID: source.ID, TargetAppID: target.ID, Alias: "root.cascade-target", CreatedAt: time.Now()}
	if err := s.CreateProjectLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, source.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProjectLink(ctx, link.ID); err != store.ErrNotFound {
		t.Fatalf("expected cascade delete, got %v", err)
	}
}

func TestProjectLink_ConcurrentDuplicateCreate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	source := &store.Project{ID: uuid.NewString(), Name: "Source", Slug: "source-concurrent", Network: "source-concurrent-net", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.CreateProject(ctx, source); err != nil {
		t.Fatal(err)
	}
	target := testApp("concurrent-target")
	if err := s.CreateApplication(ctx, target); err != nil {
		t.Fatal(err)
	}

	results := make(chan error, 2)
	for range 2 {
		go func() {
			results <- s.CreateProjectLink(ctx, &store.ProjectLink{ID: uuid.NewString(), SourceProjectID: source.ID, TargetAppID: target.ID, Alias: "root.concurrent-target", CreatedAt: time.Now()})
		}()
	}
	var success, conflict int
	for range 2 {
		switch err := <-results; err {
		case nil:
			success++
		case store.ErrConflict:
			conflict++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d, want 1/1", success, conflict)
	}
}
