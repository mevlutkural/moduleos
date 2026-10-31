package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWithDatabaseDirectoryAcceptsOnlyCanonicalAbsolutePaths(t *testing.T) {
	for _, value := range []string{"", "relative/state", "/var/lib/../lib/moduleos", "/var/lib/moduleos/"} {
		service, _, _, _ := newEdgeService(t)
		if service.WithDatabaseDirectory(value).databaseDirectory != "" {
			t.Fatalf("WithDatabaseDirectory(%q) enabled the check", value)
		}
	}
	service, _, _, _ := newEdgeService(t)
	if got := service.WithDatabaseDirectory("/var/lib/moduleos").databaseDirectory; got != "/var/lib/moduleos" {
		t.Fatalf("databaseDirectory = %q", got)
	}
}

func TestSensitiveMountSourceDeniesTheDatabaseDirectory(t *testing.T) {
	base := resolvedTempDir(t)
	database := mkdir(t, filepath.Join(base, "state"))
	link := filepath.Join(base, "state-link")
	if err := os.Symlink(database, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	unrelated := mkdir(t, filepath.Join(base, "apps"))

	for _, configured := range []string{database, link} {
		service, _, _, _ := newEdgeService(t)
		service.WithDatabaseDirectory(configured)
		for _, test := range []struct {
			name   string
			source string
			denied bool
		}{
			{name: "database directory", source: database, denied: true},
			{name: "inside the database directory", source: filepath.Join(database, "moduleos.db"), denied: true},
			{name: "parent of the database directory", source: base, denied: true},
			{name: "unrelated sibling", source: unrelated, denied: false},
		} {
			if got := service.sensitiveMountSource(test.source); got != test.denied {
				t.Fatalf("configured %q, %s: denied = %v, want %v", configured, test.name, got, test.denied)
			}
		}
	}

	disabled, _, _, _ := newEdgeService(t)
	if disabled.sensitiveMountSource(database) {
		t.Fatal("database check applied without a configured database directory")
	}
}

func TestAllowedMountRootOverlappingTheDatabaseDirectoryIsDenied(t *testing.T) {
	base := resolvedTempDir(t)
	database := mkdir(t, filepath.Join(base, "state"))
	mkdir(t, filepath.Join(database, "apps", "data"))
	rootLink := filepath.Join(base, "root-link")
	if err := os.Symlink(database, rootLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for _, test := range []struct {
		name   string
		root   string
		source string
	}{
		{name: "root is the database directory", root: database, source: filepath.Join(database, "apps")},
		{name: "root contains the database directory", root: base, source: filepath.Join(base, "state", "apps")},
		{name: "root is inside the database directory", root: filepath.Join(database, "apps"), source: filepath.Join(database, "apps", "data")},
		{name: "root resolves to the database directory", root: rootLink, source: filepath.Join(rootLink, "apps")},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, st, _, queue := newEdgeService(t)
			service.WithRuntimeLimits(20, []string{test.root}, time.Minute).WithDatabaseDirectory(database)

			_, err := service.CreateApp(t.Context(), CreateAppRequest{
				Name: "database-mount", Image: "nginx:1.27",
				Volumes: []swarm.VolumeConfig{{Source: test.source, Target: "/data"}},
			})

			if !errors.Is(err, store.ErrInvalidData) {
				t.Fatalf("create error = %v, want ErrInvalidData", err)
			}
			if _, err := st.GetApplication(t.Context(), "database-mount"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("rejected application was persisted: %v", err)
			}
			if len(queue.snapshot()) != 0 {
				t.Fatalf("rejected application was enqueued: %#v", queue.snapshot())
			}
		})
	}
}

func TestUnrelatedMountsRemainAllowedWithADatabaseDirectory(t *testing.T) {
	base := resolvedTempDir(t)
	database := mkdir(t, filepath.Join(base, "state"))
	root := mkdir(t, filepath.Join(base, "apps"))
	source := mkdir(t, filepath.Join(root, "data"))
	service, _, _, _ := newEdgeService(t)
	service.WithRuntimeLimits(20, []string{root}, time.Minute).WithDatabaseDirectory(database)

	created := mustCreateApp(t, service, CreateAppRequest{
		Name: "unrelated-mount", Image: "nginx:1.27",
		Volumes: []swarm.VolumeConfig{{Source: source, Target: "/data"}},
	})

	if _, err := service.BuildDesiredServiceSpec(t.Context(), created); err != nil {
		t.Fatalf("desired spec error = %v", err)
	}
}

func TestDesiredSpecRejectsAPersistedMountThatReachesTheDatabaseDirectory(t *testing.T) {
	base := resolvedTempDir(t)
	root := mkdir(t, filepath.Join(base, "apps"))
	source := mkdir(t, filepath.Join(root, "data"))
	service, st, _, _ := newEdgeService(t)
	service.WithRuntimeLimits(20, []string{root}, time.Minute)
	created := mustCreateApp(t, service, CreateAppRequest{
		Name: "moved-database", Image: "nginx:1.27",
		Volumes: []swarm.VolumeConfig{{Source: source, Target: "/data"}},
	})

	// The database later lives where an existing application mount points.
	service.WithDatabaseDirectory(source)

	if _, err := service.BuildDesiredServiceSpec(t.Context(), created); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("desired spec error = %v, want ErrInvalidData", err)
	}
	persisted, err := st.GetApplication(t.Context(), created.Name)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.DesiredGeneration != created.DesiredGeneration {
		t.Fatalf("rejected revalidation changed the application: %#v", persisted)
	}
}
