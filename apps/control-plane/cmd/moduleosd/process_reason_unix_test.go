//go:build darwin || linux

package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

const processReasonSentinel = "sentinel-private-startup-detail"

// bootstrapFailure returns the single bootstrap failure record the process
// emitted.
func bootstrapFailure(t *testing.T, output string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, line := range strings.Split(output, "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &record) != nil || record["msg"] != "bootstrap failed" {
			continue
		}
		if found != nil {
			t.Fatalf("more than one bootstrap failure record:\n%s", output)
		}
		found = record
	}
	if found == nil {
		t.Fatalf("no bootstrap failure record:\n%s", output)
	}
	return found
}

func seedMigrationState(t *testing.T, database string, dirty bool) {
	t.Helper()
	st, err := store.NewSQLiteStore(database)
	if err != nil {
		t.Fatalf("seed database: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatalf("open seeded database: %v", err)
	}
	defer db.Close()
	statement := `UPDATE schema_migrations SET version = 9999`
	if dirty {
		statement = `UPDATE schema_migrations SET dirty = 1`
	}
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("set migration state: %v", err)
	}
}

func TestProcessReportsStableStartupFailureReasons(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T, *processHarness)
		phase     string
		reason    string
		variable  string
		database  bool
	}{
		{name: "conflicting key sources", configure: func(_ *testing.T, h *processHarness) {
			h.set("API_KEY", processReasonSentinel)
		}, phase: "config", reason: "api_key_source_conflict"},
		{name: "missing production key file", configure: func(_ *testing.T, h *processHarness) {
			h.unset("API_KEY_FILE")
		}, phase: "config", reason: "api_key_source_invalid", variable: "API_KEY_FILE"},
		{name: "invalid key file", configure: func(t *testing.T, h *processHarness) {
			if err := os.WriteFile(h.apiKeyFile, []byte(processReasonSentinel), 0o600); err != nil {
				t.Fatal(err)
			}
		}, phase: "config", reason: "api_key_file_invalid", variable: "API_KEY_FILE"},
		{name: "explicitly empty value", configure: func(_ *testing.T, h *processHarness) {
			h.set("LOG_LEVEL", "")
		}, phase: "config", reason: "value_empty", variable: "LOG_LEVEL"},
		{name: "unparsable value", configure: func(_ *testing.T, h *processHarness) {
			h.set("RECONCILE_INTERVAL", processReasonSentinel)
		}, phase: "config", reason: "value_unparsable", variable: "RECONCILE_INTERVAL"},
		{name: "invalid value", configure: func(_ *testing.T, h *processHarness) {
			h.set("LISTEN_ADDRESS", processReasonSentinel)
		}, phase: "config", reason: "value_invalid", variable: "LISTEN_ADDRESS"},
		{name: "missing required value", configure: func(_ *testing.T, h *processHarness) {
			h.unset("BASE_DOMAIN")
		}, phase: "config", reason: "value_required", variable: "BASE_DOMAIN"},
		{name: "database cannot be opened", configure: func(t *testing.T, h *processHarness) {
			if err := os.Mkdir(h.database, 0o700); err != nil {
				t.Fatal(err)
			}
		}, phase: "prepare", reason: "database_open_failed", database: true},
		{name: "database at an unknown migration version", configure: func(t *testing.T, h *processHarness) {
			seedMigrationState(t, h.database, false)
		}, phase: "prepare", reason: "database_migration_failed", database: true},
		{name: "database with a dirty migration", configure: func(t *testing.T, h *processHarness) {
			seedMigrationState(t, h.database, true)
		}, phase: "prepare", reason: "database_migration_failed", database: true},
		{name: "listener already bound", configure: func(t *testing.T, h *processHarness) {
			collision := holdAddress(t, h.address)
			t.Cleanup(func() { _ = collision.Close() })
		}, phase: "prepare", reason: "listener_bind_failed", database: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newProcessHarness(t)
			fake := startFakeDocker(t, harness.dockerSock)
			test.configure(t, harness)
			harness.start()
			requireExitCode(t, harness, 1)

			output := harness.output()
			record := bootstrapFailure(t, output)
			if record["phase"] != test.phase || record["reason"] != test.reason {
				t.Fatalf("record = %v, want phase %q reason %q", record, test.phase, test.reason)
			}
			if variable, present := record["variable"]; (test.variable == "" && present) || (test.variable != "" && variable != test.variable) {
				t.Fatalf("record variable = %v, want %q", variable, test.variable)
			}
			if strings.Contains(output, processReasonSentinel) || strings.Contains(output, harness.directory) {
				t.Fatalf("startup failure output leaked a value or path:\n%s", output)
			}
			if containsAny(output, "daemon started", "reconciler started", "event watcher started") {
				t.Fatalf("failed startup claimed to start:\n%s", output)
			}
			if !test.database {
				if _, err := os.Stat(harness.database); !os.IsNotExist(err) {
					t.Fatalf("configuration failure created the database: %v", err)
				}
			}
			if fake.requestCount() != 0 {
				t.Fatalf("Docker received %d requests during failed startup", fake.requestCount())
			}
			fake.assertNoUnexpected()
		})
	}
}
