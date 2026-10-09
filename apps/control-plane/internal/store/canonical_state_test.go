package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

func canonicalTestApp(name string) *Application {
	now := time.Now().UTC()
	return &Application{
		ID:         uuid.NewString(),
		ProjectID:  RootProjectID,
		Name:       name,
		SourceType: SourceTypeImage,
		Image:      "nginx:1.27",
		Status:     AppStatusCreated,
		Replicas:   1,
		EnvVars:    `{"UNICODE":"şifre=✓\nline","COLON":"a:b,c=d"}`,
		Ports:      `[{"container_port":8080,"published_port":0}]`,
		Volumes:    `[{"source":"/srv/moduleos/a:b","target":"/data"}]`,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func TestCanonicalStateRoundTripAndConstraints(t *testing.T) {
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	app := canonicalTestApp("canonical-app")
	if err := st.CreateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetApplication(ctx, app.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.EnvVars != app.EnvVars || got.Ports != app.Ports || got.Volumes != app.Volumes {
		t.Fatalf("lossy config round trip: %#v", got)
	}
	if got.DesiredGeneration != 1 || got.ObservedGeneration != 0 || got.DesiredRunState != DesiredRunStateRunning {
		t.Fatalf("unexpected canonical defaults: %#v", got)
	}

	invalid := canonicalTestApp("invalid-json")
	invalid.EnvVars = "KEY=value"
	if err := st.CreateApplication(ctx, invalid); err == nil {
		t.Fatal("invalid JSON must be rejected")
	}
	for index, mutate := range []func(*Application){
		func(value *Application) { value.EnvVars = "null" },
		func(value *Application) { value.Ports = "null" },
		func(value *Application) { value.Volumes = "null" },
	} {
		invalid = canonicalTestApp(fmt.Sprintf("null-collection-%d", index))
		mutate(invalid)
		if err := st.CreateApplication(ctx, invalid); err == nil {
			t.Fatalf("null collection %d must be rejected", index)
		}
	}

	invalid = canonicalTestApp("invalid-project")
	invalid.ProjectID = uuid.NewString()
	if err := st.CreateApplication(ctx, invalid); err == nil {
		t.Fatal("missing project foreign key must be rejected")
	}
}

func TestApplicationIntentCASAndStaleObservation(t *testing.T) {
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	app := canonicalTestApp("cas-app")
	if err := st.CreateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}

	replicas := 3
	updated, err := st.UpdateApplicationIntent(ctx, app.Name, 1, ApplicationMutation{Replicas: &replicas})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DesiredGeneration != 2 || updated.Replicas != 3 || updated.ResumeReplicas != 3 {
		t.Fatalf("unexpected mutation result: %#v", updated)
	}
	if _, err := st.UpdateApplicationIntent(ctx, app.Name, 1, ApplicationMutation{}); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale mutation error = %v", err)
	}
	if err := st.MarkApplicationObserved(ctx, app.ID, ObservedApplicationUpdate{
		Generation: 1,
		State:      ObservedStateRunning,
		Image:      app.Image,
	}); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("stale observation error = %v", err)
	}
	if err := st.MarkApplicationObserved(ctx, app.ID, ObservedApplicationUpdate{
		Generation: 2,
		State:      ObservedStateRunning,
		Image:      app.Image,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationAndDeploymentConvergenceIsAtomic(t *testing.T) {
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	app := canonicalTestApp("atomic-convergence")
	if err := st.CreateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	deployment := &Deployment{
		ID:               uuid.NewString(),
		AppID:            app.ID,
		SourceType:       SourceTypeImage,
		Image:            app.Image,
		Status:           DeploymentStatusPending,
		TriggeredBy:      TriggeredByAPI,
		CreatedAt:        time.Now().UTC(),
		TargetGeneration: app.DesiredGeneration,
	}
	if err := st.CreateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	resolvedImage := "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64)
	observation := ObservedApplicationUpdate{
		Generation: app.DesiredGeneration,
		State:      ObservedStateRunning,
		Image:      resolvedImage,
	}
	if err := st.MarkApplicationConverged(ctx, app.ID, observation, "missing-deployment", resolvedImage); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing deployment error = %v, want ErrNotFound", err)
	}
	afterRollback, err := st.GetApplicationByID(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRollback.ObservedGeneration != 0 || afterRollback.ObservedImage != "" {
		t.Fatalf("failed convergence partially committed application state: %#v", afterRollback)
	}
	if err := st.MarkApplicationConverged(ctx, app.ID, observation, deployment.ID, resolvedImage); err != nil {
		t.Fatal(err)
	}
	converged, err := st.GetApplicationByID(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	persistedDeployment, err := st.GetDeployment(ctx, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if converged.ObservedGeneration != app.DesiredGeneration || converged.ObservedImage != resolvedImage ||
		persistedDeployment.Status != DeploymentStatusSucceeded || persistedDeployment.ResolvedImage != resolvedImage {
		t.Fatalf("atomic convergence was incomplete: app=%#v deployment=%#v", converged, persistedDeployment)
	}
}

func TestDeploymentIntentIsAtomic(t *testing.T) {
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	app := canonicalTestApp("deploy-app")
	if err := st.CreateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	d := &Deployment{ID: uuid.NewString(), Image: "nginx:1.28", CreatedAt: time.Now().UTC()}
	updated, err := st.CreateDeploymentIntent(ctx, app.Name, d)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DesiredGeneration != 2 || updated.Image != d.Image || d.TargetGeneration != 2 {
		t.Fatalf("application/deployment transaction diverged: app=%#v deployment=%#v", updated, d)
	}
	persisted, err := st.GetDeployment(ctx, d.ID)
	if err != nil || persisted.TargetGeneration != 2 {
		t.Fatalf("persisted deployment = %#v, %v", persisted, err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	second := &Deployment{ID: uuid.NewString(), Image: "nginx:broken", CreatedAt: time.Now().UTC()}
	if _, err := st.CreateDeploymentIntent(cancelled, app.Name, second); err == nil {
		t.Fatal("cancelled transaction must fail")
	}
	after, err := st.GetApplication(ctx, app.Name)
	if err != nil {
		t.Fatal(err)
	}
	if after.DesiredGeneration != 2 || after.Image != d.Image {
		t.Fatalf("cancelled transaction was not rolled back: %#v", after)
	}
}

func TestUpgradeFromMigration006(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	driver, err := sqlite3.WithInstance(db, &sqlite3.Config{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "sqlite", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(6); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "migration-006.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(fixture)); err != nil {
		t.Fatal(err)
	}
	if sourceErr, databaseErr := m.Close(); sourceErr != nil || databaseErr != nil {
		t.Fatalf("close migration: %v, %v", sourceErr, databaseErr)
	}

	upgraded, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	ctx := context.Background()
	app, err := upgraded.GetApplication(ctx, "legacy-valid")
	if err != nil {
		t.Fatal(err)
	}
	if app.ID != "20000000-0000-0000-0000-000000000001" || app.ProjectID != "10000000-0000-0000-0000-000000000001" ||
		app.Image != "nginx:1.27-alpine" || app.Replicas != 2 || app.DesiredRunState != DesiredRunStateRunning ||
		app.DesiredGeneration != 1 || app.ObservedGeneration != 0 || app.ObservedState != ObservedStateRunning {
		t.Fatalf("legacy application identity/state changed: %#v", app)
	}
	if app.EnvVars != `{"MODE":"production","SECRET":"preserved"}` ||
		app.Ports != `[{"container_port":80,"published_port":18080,"protocol":"tcp","publish_mode":"ingress"}]` ||
		app.Volumes != `[{"source":"/srv/legacy","target":"/data","read_only":true}]` {
		t.Fatalf("valid legacy JSON was not preserved: %#v", app)
	}
	if app.Expose || app.IngressContainerPort != 0 || app.Domain != "legacy.example.test" {
		t.Fatalf("legacy exposure was not disabled safely: %#v", app)
	}
	invalid, err := upgraded.GetApplication(ctx, "legacy-invalid")
	if err != nil {
		t.Fatal(err)
	}
	if invalid.SourceType != SourceTypeImage || invalid.Replicas != 0 || invalid.ResumeReplicas != 1 ||
		invalid.EnvVars != "{}" || invalid.Ports != "[]" || invalid.Volumes != "[]" || invalid.ObservedState != ObservedStateFailed {
		t.Fatalf("unsafe legacy values were not normalized: %#v", invalid)
	}
	stopped, err := upgraded.GetApplication(ctx, "legacy-stopped")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.DesiredRunState != DesiredRunStateStopped || stopped.ObservedState != ObservedStateStopped || stopped.ResumeReplicas != 1 {
		t.Fatalf("stopped legacy state changed: %#v", stopped)
	}
	nullCollections, err := upgraded.GetApplication(ctx, "legacy-null-collections")
	if err != nil {
		t.Fatal(err)
	}
	if nullCollections.EnvVars != "{}" || nullCollections.Ports != "[]" || nullCollections.Volumes != "[]" {
		t.Fatalf("legacy null collections were not normalized: %#v", nullCollections)
	}
	project, err := upgraded.GetProject(ctx, "legacy-project")
	if err != nil {
		t.Fatal(err)
	}
	if project.ID != "10000000-0000-0000-0000-000000000001" || project.ObservedState != ObservedStatePending || project.FinalizerState != "none" {
		t.Fatalf("legacy project changed: %#v", project)
	}
	deployments, err := upgraded.ListDeployments(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 3 {
		t.Fatalf("legacy deployment history changed: %#v", deployments)
	}
	deploymentsByID := make(map[string]*Deployment, len(deployments))
	for _, deployment := range deployments {
		deploymentsByID[deployment.ID] = deployment
		if deployment.TargetGeneration != 0 {
			t.Fatalf("legacy deployment target generation changed: %#v", deployment)
		}
	}
	immutableImage := "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64)
	if deploymentsByID["30000000-0000-0000-0000-000000000003"].ResolvedImage != immutableImage {
		t.Fatalf("immutable legacy deployment was not backfilled: %#v", deploymentsByID["30000000-0000-0000-0000-000000000003"])
	}
	if deploymentsByID["30000000-0000-0000-0000-000000000001"].ResolvedImage != "" {
		t.Fatalf("mutable legacy deployment was backfilled: %#v", deploymentsByID["30000000-0000-0000-0000-000000000001"])
	}
	links, err := upgraded.ListProjectLinksByApp(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ID != "50000000-0000-0000-0000-000000000001" || links[0].DesiredGeneration != 1 || links[0].ObservedGeneration != 0 {
		t.Fatalf("legacy project link changed: %#v", links)
	}
	var domain, migrationVersion string
	var migrationDirty bool
	if err := upgraded.db.QueryRowContext(ctx, `SELECT domain FROM domains WHERE id = '40000000-0000-0000-0000-000000000001'`).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if domain != "custom.legacy.example.test" {
		t.Fatalf("legacy domain history changed: %q", domain)
	}
	if err := upgraded.db.QueryRowContext(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&migrationVersion, &migrationDirty); err != nil {
		t.Fatal(err)
	}
	if migrationVersion != "11" || migrationDirty {
		t.Fatalf("migration state version=%s dirty=%v", migrationVersion, migrationDirty)
	}
	var integrity string
	if err := upgraded.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity check=%q err=%v", integrity, err)
	}
	foreignKeyRows, err := upgraded.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreignKeyRows.Close() }()
	if foreignKeyRows.Next() {
		t.Fatal("foreign key violations remain after migration")
	}
}

func TestDirtyMigrationFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dirty.db")
	st, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schema_migrations SET dirty = 1`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSQLiteStore(path); err == nil {
		t.Fatal("dirty migration state must prevent startup")
	}
}

func TestOfflineBackupRestore(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "primary.db")
	backup := filepath.Join(dir, "backup.db")
	st, err := NewSQLiteStore(primary)
	if err != nil {
		t.Fatal(err)
	}
	app := canonicalTestApp("restored-app")
	if err := st.CreateApplication(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(primary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := NewSQLiteStore(backup)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if _, err := restored.GetApplication(context.Background(), app.Name); err != nil {
		t.Fatalf("restore smoke test: %v", err)
	}
}
