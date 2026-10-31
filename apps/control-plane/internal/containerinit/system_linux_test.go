//go:build linux && (amd64 || arm64)

package containerinit

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestMainRejectsUsage(t *testing.T) {
	if exitCode := Main(nil); exitCode != 2 {
		t.Fatalf("Main(nil) = %d, want 2", exitCode)
	}
}

func TestLinuxSystemDescriptorOperations(t *testing.T) {
	sys, err := newSystem()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	regular := filepath.Join(directory, "moduleos.db")
	if err := os.WriteFile(regular, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(regular, filepath.Join(directory, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(directory, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(directory, "docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	root, err := sys.openDirectory(directory)
	if err != nil {
		t.Fatalf("openDirectory() error = %v", err)
	}
	t.Cleanup(func() { _ = sys.close(root) })
	rootInfo, err := sys.fstat(root)
	if err != nil || rootInfo.kind != kindDirectory {
		t.Fatalf("fstat(root) = %+v, %v", rootInfo, err)
	}

	kinds := map[string]fileKind{
		"moduleos.db": kindRegular,
		"link":        kindSymlink,
		"fifo":        kindOther,
		"nested":      kindDirectory,
		"docker.sock": kindSocket,
	}
	for name, want := range kinds {
		descriptor, err := sys.openAt(root, name)
		if err != nil {
			t.Fatalf("openAt(%s) error = %v", name, err)
		}
		info, err := sys.fstat(descriptor)
		if err != nil {
			t.Fatalf("fstat(%s) error = %v", name, err)
		}
		if info.kind != want {
			t.Fatalf("%s kind = %v, want %v", name, info.kind, want)
		}
		if name == "moduleos.db" {
			if info.nlink != 1 || info.uid != os.Getuid() || info.gid != os.Getgid() {
				t.Fatalf("regular file info = %+v", info)
			}
			// Changing ownership to the current owner needs no privilege and
			// proves the descriptor-based call reaches the inode.
			if err := sys.fchown(descriptor, os.Getuid(), os.Getgid()); err != nil {
				t.Fatalf("fchown() error = %v", err)
			}
		}
		if err := sys.close(descriptor); err != nil {
			t.Fatalf("close(%s) error = %v", name, err)
		}
	}

	if _, err := sys.openAt(root, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("openAt(missing) error = %v, want not-exist", err)
	}
	if _, err := sys.openDirectory(filepath.Join(directory, "link")); err == nil {
		t.Fatal("openDirectory followed a symlink")
	}
	if _, err := sys.fstat(-1); err == nil {
		t.Fatal("fstat(-1) succeeded")
	}

	socketInfo, err := sys.lstat(filepath.Join(directory, "docker.sock"))
	if err != nil || socketInfo.kind != kindSocket || socketInfo.gid != os.Getgid() {
		t.Fatalf("lstat(socket) = %+v, %v", socketInfo, err)
	}
	if linkInfo, err := sys.lstat(filepath.Join(directory, "link")); err != nil || linkInfo.kind != kindSymlink {
		t.Fatalf("lstat(link) = %+v, %v", linkInfo, err)
	}
	if _, err := sys.lstat(filepath.Join(directory, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lstat(missing) error = %v", err)
	}
}

func TestLinuxSystemIdentity(t *testing.T) {
	sys, err := newSystem()
	if err != nil {
		t.Fatal(err)
	}
	current, err := sys.identity()
	if err != nil {
		t.Fatal(err)
	}
	if current.realUID != os.Getuid() || current.effectiveUID != os.Geteuid() || current.realGID != os.Getgid() || current.effectiveGID != os.Getegid() {
		t.Fatalf("identity = %+v", current)
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(groups)
	got := slices.Clone(current.groups)
	slices.Sort(got)
	if !slices.Equal(got, groups) {
		t.Fatalf("groups = %v, want %v", got, groups)
	}
}

func TestLinuxSystemPrivilegeCallsFailWithoutPrivilege(t *testing.T) {
	if os.Geteuid() == 0 {
		// Running these as root would irreversibly drop the test process's
		// own privileges; the container suite covers the privileged path.
		t.Skip("privilege-drop adapter calls are exercised unprivileged only")
	}
	sys, err := newSystem()
	if err != nil {
		t.Fatal(err)
	}
	if err := sys.setgroups([]int{RuntimeGID}); err == nil {
		t.Fatal("setgroups succeeded without privilege")
	}
	if os.Getgid() != RuntimeGID {
		if err := sys.setgid(RuntimeGID); err == nil {
			t.Fatal("setgid succeeded without privilege")
		}
	}
	if os.Getuid() != RuntimeUID {
		if err := sys.setuid(RuntimeUID); err == nil {
			t.Fatal("setuid succeeded without privilege")
		}
	}
	if err := sys.exec(filepath.Join(t.TempDir(), "missing"), []string{"missing"}, nil); err == nil {
		t.Fatal("exec of a missing file succeeded")
	}
}

func TestLinuxSystemSetsNoNewPrivilegesOnALockedThread(t *testing.T) {
	sys, err := newSystem()
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	// The flag and the thread lock stay with this goroutine's thread, which
	// exits with the goroutine and does not affect the rest of the test.
	go func() {
		result <- sys.setNoNewPrivileges()
	}()
	if err := <-result; err != nil {
		t.Fatalf("setNoNewPrivileges() error = %v", err)
	}
}
