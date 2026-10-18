package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestNewSQLiteStoreInitializationFailureOwnership(t *testing.T) {
	openFailure := errors.New("open failure")
	pragmaFailure := errors.New("pragma failure")
	migrationFailure := errors.New("migration failure")

	t.Run("open failure stops initialization", func(t *testing.T) {
		pragmaCalls, migrationCalls := 0, 0
		store, err := newSQLiteStore("ignored", sqliteInitializationDependencies{
			open: func(string) (*sql.DB, error) {
				return nil, openFailure
			},
			applyPragmas: func(*sql.DB) error {
				pragmaCalls++
				return nil
			},
			runMigrations: func(*SQLiteStore) error {
				migrationCalls++
				return nil
			},
		})
		if store != nil || !errors.Is(err, openFailure) || !strings.Contains(err.Error(), "failed to open database") {
			t.Fatalf("newSQLiteStore() = %#v/%v", store, err)
		}
		if pragmaCalls != 0 || migrationCalls != 0 {
			t.Fatalf("later phases ran after open failure: pragmas=%d migrations=%d", pragmaCalls, migrationCalls)
		}
	})

	t.Run("pragma failure closes handle and skips migrations", func(t *testing.T) {
		db := healthySQLiteDatabase(t)
		openCalls, pragmaCalls, migrationCalls := 0, 0, 0
		store, err := newSQLiteStore("ignored", sqliteInitializationDependencies{
			open: func(string) (*sql.DB, error) {
				openCalls++
				return db, nil
			},
			applyPragmas: func(*sql.DB) error {
				pragmaCalls++
				return pragmaFailure
			},
			runMigrations: func(*SQLiteStore) error {
				migrationCalls++
				return nil
			},
		})
		if store != nil || !errors.Is(err, pragmaFailure) || !strings.Contains(err.Error(), "failed to apply pragma settings") {
			t.Fatalf("newSQLiteStore() = %#v/%v", store, err)
		}
		if openCalls != 1 || pragmaCalls != 1 || migrationCalls != 0 {
			t.Fatalf("phase calls = open:%d pragmas:%d migrations:%d", openCalls, pragmaCalls, migrationCalls)
		}
		assertSQLiteDatabaseClosed(t, db)
	})

	t.Run("migration failure closes handle", func(t *testing.T) {
		db := healthySQLiteDatabase(t)
		openCalls, pragmaCalls, migrationCalls := 0, 0, 0
		store, err := newSQLiteStore("ignored", sqliteInitializationDependencies{
			open: func(string) (*sql.DB, error) {
				openCalls++
				return db, nil
			},
			applyPragmas: func(database *sql.DB) error {
				pragmaCalls++
				return database.PingContext(t.Context())
			},
			runMigrations: func(*SQLiteStore) error {
				migrationCalls++
				return migrationFailure
			},
		})
		if store != nil || !errors.Is(err, migrationFailure) || !strings.Contains(err.Error(), "migration failed") {
			t.Fatalf("newSQLiteStore() = %#v/%v", store, err)
		}
		if openCalls != 1 || pragmaCalls != 1 || migrationCalls != 1 {
			t.Fatalf("phase calls = open:%d pragmas:%d migrations:%d", openCalls, pragmaCalls, migrationCalls)
		}
		assertSQLiteDatabaseClosed(t, db)
	})
}

func TestNewSQLiteStoreInitializationTransfersOwnershipOnSuccess(t *testing.T) {
	db := healthySQLiteDatabase(t)
	openCalls, pragmaCalls, migrationCalls := 0, 0, 0
	store, err := newSQLiteStore("ignored", sqliteInitializationDependencies{
		open: func(string) (*sql.DB, error) {
			openCalls++
			return db, nil
		},
		applyPragmas: func(database *sql.DB) error {
			pragmaCalls++
			return database.PingContext(t.Context())
		},
		runMigrations: func(candidate *SQLiteStore) error {
			migrationCalls++
			if candidate.db != db {
				t.Fatal("migration received the wrong database handle")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil || store.db != db {
		t.Fatalf("store = %#v", store)
	}
	if openCalls != 1 || pragmaCalls != 1 || migrationCalls != 1 {
		t.Fatalf("phase calls = open:%d pragmas:%d migrations:%d", openCalls, pragmaCalls, migrationCalls)
	}
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("success closed transferred database: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertSQLiteDatabaseClosed(t, db)
}

func TestNewSQLiteStoreRejectsInvalidInitializationDependencies(t *testing.T) {
	tests := []sqliteInitializationDependencies{
		{},
		{open: func(string) (*sql.DB, error) { return nil, nil }},
		{
			open:         func(string) (*sql.DB, error) { return nil, nil },
			applyPragmas: func(*sql.DB) error { return nil },
		},
	}
	for index, dependencies := range tests {
		if store, err := newSQLiteStore("ignored", dependencies); store != nil || err == nil {
			t.Fatalf("case %d = %#v/%v, want initialization error", index, store, err)
		}
	}

	store, err := newSQLiteStore("ignored", sqliteInitializationDependencies{
		open:          func(string) (*sql.DB, error) { return nil, nil },
		applyPragmas:  func(*sql.DB) error { return nil },
		runMigrations: func(*SQLiteStore) error { return nil },
	})
	if store != nil || err == nil || !strings.Contains(err.Error(), "database handle is nil") {
		t.Fatalf("nil database = %#v/%v", store, err)
	}
}

func healthySQLiteDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("preflight database ping: %v", err)
	}
	return db
}

func assertSQLiteDatabaseClosed(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.PingContext(context.Background()); err == nil {
		t.Fatal("database handle remained usable after ownership cleanup")
	}
}
