package store

import (
	"database/sql"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

func TestMigration012BackfillAndDownPreserveDeployments(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
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
	t.Cleanup(func() { _, _ = m.Close() })
	if err := m.Migrate(11); err != nil {
		t.Fatal(err)
	}
	const appID = "20000000-0000-0000-0000-000000000099"
	if _, err := db.Exec(`INSERT INTO applications
		(id, project_id, name, source_type, image, status, replicas, env_vars,
		ports, volumes, created_at, updated_at)
		VALUES (?, ?, 'upgrade-order', 'image', 'nginx:1.0', 'created', 1,
		'{}', '[]', '[]', '2025-01-01 00:00:00', '2025-01-01 00:00:00')`,
		appID, RootProjectID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"30000000-0000-0000-0000-000000000003",
		"30000000-0000-0000-0000-000000000001",
		"30000000-0000-0000-0000-000000000002",
	} {
		if _, err := db.Exec(`INSERT INTO deployments
			(id, app_id, source_type, image, status, triggered_by, created_at)
			VALUES (?, ?, 'image', 'nginx:1.0', 'success', 'api', '2025-01-02 00:00:00')`, id, appID); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Steps(1); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT deployment_id FROM deployment_order WHERE app_id = ? ORDER BY sequence ASC`, appID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "30000000-0000-0000-0000-000000000001" ||
		ids[1] != "30000000-0000-0000-0000-000000000002" ||
		ids[2] != "30000000-0000-0000-0000-000000000003" {
		t.Fatalf("nondeterministic backfill order: %v", ids)
	}
	if err := m.Steps(-1); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE app_id = ?`, appID).Scan(&count); err != nil || count != 3 {
		t.Fatalf("down migration damaged history: count=%d err=%v", count, err)
	}
}
