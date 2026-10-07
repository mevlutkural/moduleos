package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")
var ErrGenerationConflict = errors.New("generation conflict")
var ErrStaleObservation = errors.New("stale observation")
var ErrInvalidTransition = errors.New("invalid state transition")
var ErrInvalidData = errors.New("invalid persisted data")

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	// SQLite pragmas such as foreign_keys are connection-local. Keeping one
	// connection guarantees every query observes the same FK and busy-timeout
	// policy and also makes :memory: databases deterministic in tests.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// recommended pragma settings for SQLite
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA foreign_keys=ON;
		PRAGMA busy_timeout=5000;
	`); err != nil {
		return nil, fmt.Errorf("failed to apply pragma settings: %w", err)
	}

	store := &SQLiteStore{db: db}

	if err := store.runMigrations(); err != nil {
		return nil, fmt.Errorf("migration failed: %w", err)
	}

	return store, nil
}

func (s *SQLiteStore) runMigrations() error {
	sourceDriver, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}

	dbDriver, err := sqlite3.WithInstance(s.db, &sqlite3.Config{})
	if err != nil {
		return err
	}

	m, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", dbDriver)
	if err != nil {
		return err
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}

	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// ── Project ───────────────────────────────────────────────────────────────────

func (s *SQLiteStore) CreateProject(ctx context.Context, p *Project) error {
	if p.ObservedState == "" {
		p.ObservedState = ObservedStatePending
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (id, name, slug, network, created_at, updated_at,
			observed_state, reconcile_error_code, reconcile_error_message, deletion_timestamp, finalizer_state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Slug, p.Network, p.CreatedAt, p.UpdatedAt,
		p.ObservedState, p.ReconcileErrorCode, p.ReconcileErrorMessage, p.DeletionTimestamp, "none",
	)
	if err != nil && isUniqueConstraintError(err) {
		return ErrConflict
	}
	return err
}

func (s *SQLiteStore) GetProject(ctx context.Context, slug string) (*Project, error) {
	p := &Project{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, network, created_at, updated_at, observed_state,
		reconcile_error_code, reconcile_error_message, deletion_timestamp, finalizer_state FROM projects WHERE slug = ?`, slug,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.Network, &p.CreatedAt, &p.UpdatedAt,
		&p.ObservedState, &p.ReconcileErrorCode, &p.ReconcileErrorMessage, &p.DeletionTimestamp, &p.FinalizerState)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *SQLiteStore) GetProjectByID(ctx context.Context, id string) (*Project, error) {
	p := &Project{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, network, created_at, updated_at, observed_state,
		reconcile_error_code, reconcile_error_message, deletion_timestamp, finalizer_state FROM projects WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.Network, &p.CreatedAt, &p.UpdatedAt,
		&p.ObservedState, &p.ReconcileErrorCode, &p.ReconcileErrorMessage, &p.DeletionTimestamp, &p.FinalizerState)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *SQLiteStore) ListProjects(ctx context.Context) ([]*Project, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, slug, network, created_at, updated_at, observed_state,
		reconcile_error_code, reconcile_error_message, deletion_timestamp, finalizer_state FROM projects ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var projects []*Project
	for rows.Next() {
		p := &Project{}
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.Network, &p.CreatedAt, &p.UpdatedAt,
			&p.ObservedState, &p.ReconcileErrorCode, &p.ReconcileErrorMessage, &p.DeletionTimestamp, &p.FinalizerState); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s *SQLiteStore) DeleteProject(ctx context.Context, slug string) error {
	if slug == "root" {
		return fmt.Errorf("root project cannot be deleted")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE slug = ?`, slug)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) CreateProjectDeletionIntent(ctx context.Context, slug string) (*Project, error) {
	if slug == "root" {
		return nil, ErrConflict
	}
	project, err := s.GetProject(ctx, slug)
	if err != nil {
		return nil, err
	}
	if project.DeletionTimestamp != nil {
		return project, nil
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET deletion_timestamp = ?, finalizer_state = 'pending', observed_state = 'pending', updated_at = ? WHERE id = ? AND deletion_timestamp IS NULL`, now, now, project.ID)
	if err != nil {
		return nil, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		return nil, ErrConflict
	}
	project.DeletionTimestamp = &now
	project.FinalizerState = "pending"
	project.ObservedState = ObservedStatePending
	return project, nil
}

func (s *SQLiteStore) FinalizeProjectDeletion(ctx context.Context, projectID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ? AND deletion_timestamp IS NOT NULL AND id <> ?`, projectID, RootProjectID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrConflict
	}
	return nil
}

func (s *SQLiteStore) MarkProjectObserved(ctx context.Context, projectID string, state ObservedState, code, message string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET observed_state = ?,
		reconcile_error_code = ?, reconcile_error_message = ?, updated_at = ? WHERE id = ?`,
		state, code, truncateSafeMessage(message), time.Now().UTC(), projectID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ── ProjectLink ───────────────────────────────────────────────────────────────

func (s *SQLiteStore) CreateProjectLink(ctx context.Context, pl *ProjectLink) error {
	if pl.DesiredGeneration == 0 {
		pl.DesiredGeneration = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO project_links (id, source_project_id, target_app_id, alias, created_at,
			desired_generation, observed_generation, deletion_timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pl.ID, pl.SourceProjectID, pl.TargetAppID, pl.Alias, pl.CreatedAt,
		pl.DesiredGeneration, pl.ObservedGeneration, pl.DeletionTimestamp,
	)
	if err != nil && isUniqueConstraintError(err) {
		return ErrConflict
	}
	return err
}

func (s *SQLiteStore) GetProjectLink(ctx context.Context, id string) (*ProjectLink, error) {
	pl := &ProjectLink{}
	err := s.db.QueryRowContext(ctx,
		`SELECT id, source_project_id, target_app_id, alias, created_at,
		desired_generation, observed_generation, deletion_timestamp FROM project_links WHERE id = ?`, id,
	).Scan(&pl.ID, &pl.SourceProjectID, &pl.TargetAppID, &pl.Alias, &pl.CreatedAt,
		&pl.DesiredGeneration, &pl.ObservedGeneration, &pl.DeletionTimestamp)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return pl, err
}

func (s *SQLiteStore) DeleteProjectLink(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM project_links WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) MarkProjectLinkObserved(ctx context.Context, linkID string, generation int64) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE project_links SET observed_generation = ?
		WHERE id = ? AND desired_generation = ? AND deletion_timestamp IS NULL`,
		generation, linkID, generation,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrStaleObservation
	}
	return nil
}

func (s *SQLiteStore) ListProjectLinksByProject(ctx context.Context, projectID string) ([]*ProjectLink, error) {
	return s.listProjectLinks(ctx, `SELECT id, source_project_id, target_app_id, alias, created_at,
		desired_generation, observed_generation, deletion_timestamp FROM project_links
		WHERE source_project_id = ? ORDER BY created_at ASC`, projectID)
}

func (s *SQLiteStore) ListProjectLinksByApp(ctx context.Context, appID string) ([]*ProjectLink, error) {
	return s.listProjectLinks(ctx, `SELECT id, source_project_id, target_app_id, alias, created_at,
		desired_generation, observed_generation, deletion_timestamp FROM project_links
		WHERE target_app_id = ? ORDER BY created_at ASC`, appID)
}

func (s *SQLiteStore) listProjectLinks(ctx context.Context, query string, args ...any) ([]*ProjectLink, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var links []*ProjectLink
	for rows.Next() {
		pl := &ProjectLink{}
		if err := rows.Scan(&pl.ID, &pl.SourceProjectID, &pl.TargetAppID, &pl.Alias, &pl.CreatedAt,
			&pl.DesiredGeneration, &pl.ObservedGeneration, &pl.DeletionTimestamp); err != nil {
			return nil, err
		}
		links = append(links, pl)
	}
	return links, rows.Err()
}

// ── Application ───────────────────────────────────────────────────────────────

const applicationColumns = `id, project_id, name, source_type, image, observed_image,
	status, desired_run_state, replicas, resume_replicas, desired_generation,
	observed_generation, observed_state, reconcile_error_code,
	reconcile_error_message, reconcile_retryable, reconcile_attempt,
	last_transition_at, last_reconciled_at, env_vars, ports, volumes,
	ingress_container_port, expose, domain, deletion_timestamp, finalizer_state,
	created_at, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanApplication(scanner rowScanner) (*Application, error) {
	app := &Application{}
	var expose, retryable int
	err := scanner.Scan(
		&app.ID, &app.ProjectID, &app.Name, &app.SourceType, &app.Image, &app.ObservedImage,
		&app.Status, &app.DesiredRunState, &app.Replicas, &app.ResumeReplicas,
		&app.DesiredGeneration, &app.ObservedGeneration, &app.ObservedState,
		&app.ReconcileErrorCode, &app.ReconcileErrorMessage, &retryable,
		&app.ReconcileAttempt, &app.LastTransitionAt, &app.LastReconciledAt,
		&app.EnvVars, &app.Ports, &app.Volumes, &app.IngressContainerPort,
		&expose, &app.Domain, &app.DeletionTimestamp, &app.FinalizerState,
		&app.CreatedAt, &app.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	app.Expose = expose == 1
	app.ReconcileRetryable = retryable == 1
	return app, nil
}

func normalizeNewApplication(app *Application) {
	if app.ProjectID == "" {
		app.ProjectID = RootProjectID
	}
	if app.SourceType == "" {
		app.SourceType = SourceTypeImage
	}
	if app.DesiredRunState == "" {
		if app.Status == AppStatusStopped {
			app.DesiredRunState = DesiredRunStateStopped
		} else {
			app.DesiredRunState = DesiredRunStateRunning
		}
	}
	if app.ResumeReplicas == 0 && app.Replicas > 0 {
		app.ResumeReplicas = app.Replicas
	}
	if app.DesiredGeneration == 0 {
		app.DesiredGeneration = 1
	}
	if app.ObservedState == "" {
		app.ObservedState = ObservedStatePending
	}
	if app.FinalizerState == "" {
		app.FinalizerState = "none"
	}
}

func (s *SQLiteStore) CreateApplication(ctx context.Context, app *Application) error {
	normalizeNewApplication(app)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO applications
			(id, project_id, name, source_type, image, observed_image, status,
			desired_run_state, replicas, resume_replicas, desired_generation,
			observed_generation, observed_state, reconcile_error_code,
			reconcile_error_message, reconcile_retryable, reconcile_attempt,
			last_transition_at, last_reconciled_at, env_vars, ports, volumes,
			ingress_container_port, expose, domain, deletion_timestamp,
			finalizer_state, created_at, updated_at)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		app.ID, app.ProjectID, app.Name, app.SourceType, app.Image, app.ObservedImage,
		app.Status, app.DesiredRunState, app.Replicas, app.ResumeReplicas,
		app.DesiredGeneration, app.ObservedGeneration, app.ObservedState,
		app.ReconcileErrorCode, app.ReconcileErrorMessage, boolToInt(app.ReconcileRetryable),
		app.ReconcileAttempt, app.LastTransitionAt, app.LastReconciledAt,
		app.EnvVars, app.Ports, app.Volumes, app.IngressContainerPort,
		boolToInt(app.Expose), app.Domain, app.DeletionTimestamp, app.FinalizerState,
		app.CreatedAt, app.UpdatedAt,
	)
	if err != nil && isUniqueConstraintError(err) {
		return ErrConflict
	}
	return err
}

func (s *SQLiteStore) GetApplication(ctx context.Context, name string) (*Application, error) {
	app, err := scanApplication(s.db.QueryRowContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE name = ?`, name))

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return app, nil
}

func (s *SQLiteStore) GetApplicationByID(ctx context.Context, id string) (*Application, error) {
	app, err := scanApplication(s.db.QueryRowContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE id = ?`, id))

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return app, nil
}

func (s *SQLiteStore) ListApplications(ctx context.Context) ([]*Application, error) {
	return s.listApplications(ctx, `SELECT `+applicationColumns+` FROM applications ORDER BY created_at DESC`)
}

func (s *SQLiteStore) ListApplicationsByProject(ctx context.Context, projectID string) ([]*Application, error) {
	return s.listApplications(ctx,
		`SELECT `+applicationColumns+` FROM applications WHERE project_id = ? ORDER BY created_at DESC`,
		projectID,
	)
}

func (s *SQLiteStore) listApplications(ctx context.Context, query string, args ...any) ([]*Application, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var apps []*Application
	for rows.Next() {
		app, err := scanApplication(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}

	return apps, rows.Err()
}

func (s *SQLiteStore) UpdateApplication(ctx context.Context, app *Application) error {
	app.UpdatedAt = time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE applications SET
			project_id = ?, source_type = ?, image = ?, observed_image = ?, status = ?,
			desired_run_state = ?, replicas = ?, resume_replicas = ?,
			desired_generation = ?, observed_generation = ?, observed_state = ?,
			reconcile_error_code = ?, reconcile_error_message = ?, reconcile_retryable = ?,
			reconcile_attempt = ?, last_transition_at = ?, last_reconciled_at = ?,
			env_vars = ?, ports = ?, volumes = ?, ingress_container_port = ?, expose = ?,
			domain = ?, deletion_timestamp = ?, finalizer_state = ?, updated_at = ?
		WHERE name = ?`,
		app.ProjectID, app.SourceType, app.Image, app.ObservedImage, app.Status,
		app.DesiredRunState, app.Replicas, app.ResumeReplicas, app.DesiredGeneration,
		app.ObservedGeneration, app.ObservedState, app.ReconcileErrorCode,
		app.ReconcileErrorMessage, boolToInt(app.ReconcileRetryable), app.ReconcileAttempt,
		app.LastTransitionAt, app.LastReconciledAt, app.EnvVars, app.Ports, app.Volumes,
		app.IngressContainerPort, boolToInt(app.Expose), app.Domain,
		app.DeletionTimestamp, app.FinalizerState, app.UpdatedAt,
		app.Name,
	)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}

	return nil
}

func (s *SQLiteStore) DeleteApplication(ctx context.Context, name string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM applications WHERE name = ?`, name)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}

	return nil
}

func (s *SQLiteStore) UpdateApplicationIntent(ctx context.Context, name string, expectedGeneration int64, mutation ApplicationMutation) (*Application, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	app, err := scanApplication(tx.QueryRowContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if expectedGeneration >= 0 && app.DesiredGeneration != expectedGeneration {
		return nil, fmt.Errorf("%w: expected %d, current %d", ErrGenerationConflict, expectedGeneration, app.DesiredGeneration)
	}
	if app.DeletionTimestamp != nil {
		return nil, fmt.Errorf("%w: application is deleting", ErrConflict)
	}

	if mutation.Image != nil {
		app.Image = *mutation.Image
	}
	if mutation.DesiredRunState != nil {
		app.DesiredRunState = *mutation.DesiredRunState
		if app.DesiredRunState == DesiredRunStateStopped {
			if app.Replicas > 0 {
				app.ResumeReplicas = app.Replicas
			}
			app.Replicas = 0
		} else if app.Replicas == 0 {
			app.Replicas = max(app.ResumeReplicas, 1)
		}
	}
	if mutation.Replicas != nil {
		if *mutation.Replicas < 0 {
			return nil, fmt.Errorf("%w: replicas cannot be negative", ErrInvalidData)
		}
		app.Replicas = *mutation.Replicas
		if app.Replicas > 0 {
			app.ResumeReplicas = app.Replicas
			app.DesiredRunState = DesiredRunStateRunning
		} else {
			app.DesiredRunState = DesiredRunStateStopped
		}
	}
	if mutation.EnvVars != nil {
		app.EnvVars = *mutation.EnvVars
	}
	if mutation.Ports != nil {
		app.Ports = *mutation.Ports
	}
	if mutation.Volumes != nil {
		app.Volumes = *mutation.Volumes
	}
	if mutation.Expose != nil {
		app.Expose = *mutation.Expose
	}
	if mutation.IngressContainerPort != nil {
		app.IngressContainerPort = *mutation.IngressContainerPort
	}
	if err := validateApplicationConfig(app); err != nil {
		return nil, err
	}

	app.DesiredGeneration++
	app.ObservedState = ObservedStatePending
	app.Status = AppStatusUpdating
	app.ReconcileErrorCode = ""
	app.ReconcileErrorMessage = ""
	app.ReconcileRetryable = false
	app.ReconcileAttempt = 0
	now := time.Now().UTC()
	app.UpdatedAt = now
	app.LastTransitionAt = &now
	if err := updateApplicationTx(ctx, tx, app); err != nil {
		return nil, err
	}
	if err := terminalizeOvertakenDeploymentsTx(ctx, tx, app.ID, app.DesiredGeneration, app.ObservedGeneration, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return app, nil
}

func validateApplicationConfig(app *Application) error {
	if app.DesiredRunState != DesiredRunStateRunning && app.DesiredRunState != DesiredRunStateStopped {
		return fmt.Errorf("%w: invalid desired run state", ErrInvalidData)
	}
	if app.Replicas < 0 || app.ResumeReplicas < 0 {
		return fmt.Errorf("%w: replica values cannot be negative", ErrInvalidData)
	}
	for field, raw := range map[string]string{"env_vars": app.EnvVars, "ports": app.Ports, "volumes": app.Volumes} {
		if !json.Valid([]byte(raw)) {
			return fmt.Errorf("%w: %s must contain valid JSON", ErrInvalidData, field)
		}
	}
	return nil
}

func updateApplicationTx(ctx context.Context, tx *sql.Tx, app *Application) error {
	result, err := tx.ExecContext(ctx, `UPDATE applications SET
		image = ?, observed_image = ?, status = ?, desired_run_state = ?, replicas = ?,
		resume_replicas = ?, desired_generation = ?, observed_generation = ?,
		observed_state = ?, reconcile_error_code = ?, reconcile_error_message = ?,
		reconcile_retryable = ?, reconcile_attempt = ?, last_transition_at = ?,
		last_reconciled_at = ?, env_vars = ?, ports = ?, volumes = ?,
		ingress_container_port = ?, expose = ?, deletion_timestamp = ?,
		finalizer_state = ?, updated_at = ? WHERE id = ?`,
		app.Image, app.ObservedImage, app.Status, app.DesiredRunState, app.Replicas,
		app.ResumeReplicas, app.DesiredGeneration, app.ObservedGeneration,
		app.ObservedState, app.ReconcileErrorCode, app.ReconcileErrorMessage,
		boolToInt(app.ReconcileRetryable), app.ReconcileAttempt, app.LastTransitionAt,
		app.LastReconciledAt, app.EnvVars, app.Ports, app.Volumes,
		app.IngressContainerPort, boolToInt(app.Expose), app.DeletionTimestamp,
		app.FinalizerState, app.UpdatedAt, app.ID,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) CreateDeploymentIntent(ctx context.Context, name string, d *Deployment) (*Application, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	app, err := scanApplication(tx.QueryRowContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if app.DeletionTimestamp != nil {
		return nil, fmt.Errorf("%w: application is deleting", ErrConflict)
	}

	app.DesiredGeneration++
	d.TargetGeneration = app.DesiredGeneration
	d.AppID = app.ID
	d.PreviousObservedImage = app.ObservedImage
	if d.Status == "" {
		d.Status = DeploymentStatusPending
	}
	if d.SourceType == "" {
		d.SourceType = SourceTypeImage
	}
	if d.TriggeredBy == "" {
		d.TriggeredBy = TriggeredByAPI
	}
	app.Image = d.Image
	app.Status = AppStatusUpdating
	app.ObservedState = ObservedStatePending
	now := time.Now().UTC()
	app.UpdatedAt = now
	app.LastTransitionAt = &now
	if err := updateApplicationTx(ctx, tx, app); err != nil {
		return nil, err
	}
	if err := terminalizeOvertakenDeploymentsTx(ctx, tx, app.ID, app.DesiredGeneration, app.ObservedGeneration, now); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO deployments
		(id, app_id, source_type, image, status, triggered_by, error_message,
		created_at, finished_at, target_generation, previous_observed_image,
		error_code, started_at, convergence_deadline, rollback_source_deployment_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.AppID, d.SourceType, d.Image, d.Status, d.TriggeredBy,
		d.ErrorMessage, d.CreatedAt, d.FinishedAt, d.TargetGeneration,
		d.PreviousObservedImage, d.ErrorCode, d.StartedAt, d.ConvergenceDeadline,
		d.RollbackSourceDeploymentID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return app, nil
}

func (s *SQLiteStore) MarkApplicationObserved(ctx context.Context, appID string, update ObservedApplicationUpdate) error {
	if update.Generation < 0 {
		return fmt.Errorf("%w: negative generation", ErrInvalidData)
	}
	now := time.Now().UTC()
	transitionAt := update.TransitionAt
	if transitionAt.IsZero() {
		transitionAt = now
	}
	result, err := s.db.ExecContext(ctx, `UPDATE applications SET
		observed_generation = ?, observed_state = ?, observed_image = ?,
		reconcile_error_code = ?, reconcile_error_message = ?, reconcile_retryable = ?,
		reconcile_attempt = ?, last_transition_at = ?, last_reconciled_at = ?,
		status = CASE ? WHEN 'running' THEN 'running' WHEN 'stopped' THEN 'stopped'
			WHEN 'failed' THEN 'failed' ELSE status END, updated_at = ?
		WHERE id = ? AND desired_generation = ? AND observed_generation <= ?`,
		update.Generation, update.State, update.Image, update.ErrorCode,
		truncateSafeMessage(update.ErrorMessage), boolToInt(update.Retryable), update.Attempt,
		transitionAt, now, update.State, now, appID, update.Generation, update.Generation)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows > 0 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM applications WHERE id = ?`, appID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return ErrStaleObservation
}

func (s *SQLiteStore) CreateDeletionIntent(ctx context.Context, name string, expectedGeneration int64) (*Application, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	app, err := scanApplication(tx.QueryRowContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if expectedGeneration >= 0 && app.DesiredGeneration != expectedGeneration {
		return nil, ErrGenerationConflict
	}
	if app.DeletionTimestamp != nil {
		return app, nil
	}
	now := time.Now().UTC()
	app.DesiredGeneration++
	app.DeletionTimestamp = &now
	app.FinalizerState = "pending"
	app.ObservedState = ObservedStateDeleting
	app.UpdatedAt = now
	app.LastTransitionAt = &now
	if err := updateApplicationTx(ctx, tx, app); err != nil {
		return nil, err
	}
	if err := terminalizeOvertakenDeploymentsTx(ctx, tx, app.ID, app.DesiredGeneration, app.ObservedGeneration, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return app, nil
}

func (s *SQLiteStore) FinalizeApplicationDeletion(ctx context.Context, appID string, generation int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM applications
		WHERE id = ? AND desired_generation = ? AND deletion_timestamp IS NOT NULL`, appID, generation)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return s.classifyApplicationCASMiss(ctx, appID, ErrGenerationConflict)
	}
	return nil
}

func (s *SQLiteStore) PersistReconcileDiagnostics(ctx context.Context, appID string, generation int64, code, message string, retryable bool, attempt int) error {
	result, err := s.db.ExecContext(ctx, `UPDATE applications SET reconcile_error_code = ?,
		reconcile_error_message = ?, reconcile_retryable = ?, reconcile_attempt = ?,
		observed_state = CASE WHEN ? = '' THEN 'reconciling' WHEN ? = 1 THEN 'degraded' ELSE 'failed' END,
		status = CASE WHEN ? <> '' AND ? = 0 THEN 'failed' ELSE status END,
		last_reconciled_at = ?, updated_at = ? WHERE id = ? AND desired_generation = ?`,
		code, truncateSafeMessage(message), boolToInt(retryable), attempt,
		code, boolToInt(retryable), code, boolToInt(retryable),
		time.Now().UTC(), time.Now().UTC(), appID, generation)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return s.classifyApplicationCASMiss(ctx, appID, ErrStaleObservation)
	}
	return nil
}

func truncateSafeMessage(message string) string {
	const maxLength = 1024
	if len(message) <= maxLength {
		return message
	}
	return message[:maxLength]
}

// ── Deployment ────────────────────────────────────────────────────────────────

const deploymentColumns = `id, app_id, source_type, image, status, triggered_by,
	error_message, created_at, finished_at, target_generation,
	previous_observed_image, error_code, started_at, convergence_deadline,
	rollback_source_deployment_id`

func scanDeployment(scanner rowScanner) (*Deployment, error) {
	d := &Deployment{}
	err := scanner.Scan(&d.ID, &d.AppID, &d.SourceType, &d.Image, &d.Status,
		&d.TriggeredBy, &d.ErrorMessage, &d.CreatedAt, &d.FinishedAt,
		&d.TargetGeneration, &d.PreviousObservedImage, &d.ErrorCode, &d.StartedAt,
		&d.ConvergenceDeadline, &d.RollbackSourceDeploymentID)
	return d, err
}

func (s *SQLiteStore) CreateDeployment(ctx context.Context, d *Deployment) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO deployments
			(id, app_id, source_type, image, status, triggered_by, error_message,
			created_at, finished_at, target_generation, previous_observed_image,
			error_code, started_at, convergence_deadline, rollback_source_deployment_id)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.AppID, d.SourceType, d.Image, d.Status,
		d.TriggeredBy, d.ErrorMessage, d.CreatedAt, d.FinishedAt,
		d.TargetGeneration, d.PreviousObservedImage, d.ErrorCode, d.StartedAt,
		d.ConvergenceDeadline, d.RollbackSourceDeploymentID,
	)
	return err
}

func (s *SQLiteStore) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	d, err := scanDeployment(s.db.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM deployments WHERE id = ?`, id))

	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return d, nil
}

func (s *SQLiteStore) ListDeployments(ctx context.Context, appID string) ([]*Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+deploymentColumns+`
		FROM deployments WHERE app_id = ? ORDER BY created_at DESC`, appID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var deployments []*Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		deployments = append(deployments, d)
	}

	return deployments, rows.Err()
}

func (s *SQLiteStore) UpdateDeployment(ctx context.Context, d *Deployment) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE deployments SET
			image = ?, status = ?, error_code = ?, error_message = ?, started_at = ?,
			finished_at = ?, convergence_deadline = ?, rollback_source_deployment_id = ?
		WHERE id = ?`,
		d.Image, d.Status, d.ErrorCode, truncateSafeMessage(d.ErrorMessage), d.StartedAt,
		d.FinishedAt, d.ConvergenceDeadline, d.RollbackSourceDeploymentID, d.ID,
	)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}

	return nil
}

func (s *SQLiteStore) MarkDeploymentState(ctx context.Context, id string, generation int64, status DeploymentStatus, code, message string) error {
	allowedSources, valid := deploymentTransitionSources(status)
	if !valid {
		return fmt.Errorf("%w: unknown deployment status %q", ErrInvalidData, status)
	}
	now := time.Now().UTC()
	var startedAt, finishedAt any
	if status == DeploymentStatusApplying || status == DeploymentStatusInProgress {
		startedAt = now
	}
	if status == DeploymentStatusSucceeded || status == DeploymentStatusSuccess || status == DeploymentStatusFailed || status == DeploymentStatusSuperseded {
		finishedAt = now
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(allowedSources)), ",")
	query := `UPDATE deployments SET status = ?,
		error_code = CASE WHEN status IN (?, ?, ?, ?) THEN error_code ELSE ? END,
		error_message = CASE WHEN status IN (?, ?, ?, ?) THEN error_message ELSE ? END,
		started_at = COALESCE(started_at, ?), finished_at = COALESCE(finished_at, ?)
		WHERE id = ? AND target_generation = ? AND status IN (` + placeholders + `)`
	args := []any{
		status,
		DeploymentStatusSuccess, DeploymentStatusSucceeded, DeploymentStatusFailed, DeploymentStatusSuperseded, code,
		DeploymentStatusSuccess, DeploymentStatusSucceeded, DeploymentStatusFailed, DeploymentStatusSuperseded, truncateSafeMessage(message),
		startedAt, finishedAt, id, generation,
	}
	for _, source := range allowedSources {
		args = append(args, source)
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		var currentGeneration int64
		var currentStatus DeploymentStatus
		if err := s.db.QueryRowContext(ctx, `SELECT target_generation, status FROM deployments WHERE id = ?`, id).Scan(&currentGeneration, &currentStatus); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if currentGeneration != generation {
			return ErrStaleObservation
		}
		return fmt.Errorf("%w: deployment cannot move from %q to %q", ErrInvalidTransition, currentStatus, status)
	}
	return nil
}

func deploymentTransitionSources(target DeploymentStatus) ([]DeploymentStatus, bool) {
	switch target {
	case DeploymentStatusPending:
		return []DeploymentStatus{DeploymentStatusPending}, true
	case DeploymentStatusBuilding:
		return []DeploymentStatus{DeploymentStatusPending, DeploymentStatusBuilding}, true
	case DeploymentStatusApplying:
		return []DeploymentStatus{DeploymentStatusPending, DeploymentStatusBuilding, DeploymentStatusApplying}, true
	case DeploymentStatusInProgress:
		return []DeploymentStatus{DeploymentStatusPending, DeploymentStatusBuilding, DeploymentStatusApplying, DeploymentStatusInProgress}, true
	case DeploymentStatusSuccess, DeploymentStatusSucceeded, DeploymentStatusFailed, DeploymentStatusSuperseded:
		return []DeploymentStatus{
			DeploymentStatusPending,
			DeploymentStatusBuilding,
			DeploymentStatusApplying,
			DeploymentStatusInProgress,
			target,
		}, true
	default:
		return nil, false
	}
}

func terminalizeOvertakenDeploymentsTx(ctx context.Context, tx *sql.Tx, appID string, generation, observedGeneration int64, finishedAt time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE deployments SET
			status = CASE WHEN target_generation > 0 AND target_generation = ? THEN ? ELSE ? END,
		finished_at = COALESCE(finished_at, ?)
		WHERE app_id = ? AND target_generation < ? AND status IN (?, ?, ?, ?)`,
		observedGeneration, DeploymentStatusSucceeded, DeploymentStatusSuperseded,
		finishedAt, appID, generation,
		DeploymentStatusPending, DeploymentStatusBuilding, DeploymentStatusApplying, DeploymentStatusInProgress)
	return err
}

func (s *SQLiteStore) classifyApplicationCASMiss(ctx context.Context, appID string, conflict error) error {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM applications WHERE id = ?`, appID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return conflict
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
