package store

import (
	"context"
	"testing"
)

func TestProjectLinksMigrationAndConnectionPolicy(t *testing.T) {
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer func() { _ = st.Close() }()

	if got := st.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1 for connection-local SQLite pragmas", got)
	}

	var foreignKeys int
	if err := st.db.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	var tableName string
	if err := st.db.QueryRowContext(context.Background(), `
		SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'project_links'
	`).Scan(&tableName); err != nil {
		t.Fatalf("project_links migration was not applied: %v", err)
	}

	rows, err := st.db.QueryContext(context.Background(), `PRAGMA foreign_key_list(project_links)`)
	if err != nil {
		t.Fatalf("read project_links foreign keys: %v", err)
	}
	defer func() { _ = rows.Close() }()

	cascadeTargets := map[string]bool{"projects": false, "applications": false}
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign key: %v", err)
		}
		if onDelete == "CASCADE" {
			cascadeTargets[table] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign keys: %v", err)
	}
	for table, found := range cascadeTargets {
		if !found {
			t.Errorf("missing ON DELETE CASCADE foreign key to %s", table)
		}
	}
}
