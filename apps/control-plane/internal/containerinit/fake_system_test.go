package containerinit

import (
	"fmt"
	"slices"
	"syscall"
	"testing"
)

const fakeRootName = "<root>"

// fakeSystem models the start boundary in memory. Ownership changes and
// identity transitions are applied only through descriptors and set* calls,
// so tests observe exactly what start would do to a real container.
type fakeSystem struct {
	t *testing.T

	current          identity
	identityErr      []error
	identityCalls    int
	ignoreSetuid     bool
	ignoreSetgroups  bool
	allowRegainRoot  bool
	rootInfo         *fileInfo
	rootOpenErr      error
	rootStatErr      error
	files            map[string]fileInfo
	openErr          map[string]error
	statErr          map[string]error
	chownErr         map[string]error
	socketPath       string
	socket           *fileInfo
	socketErr        error
	setgroupsErr     error
	setgidErr        error
	setuidErr        error
	noNewPrivsErr    error
	execErr          error
	nextDescriptor   int
	descriptors      map[int]string
	closed           map[int]bool
	calls            []string
	chowned          []string
	execPath         string
	execArgv         []string
	execEnvironment  []string
	lstatPaths       []string
	setgroupsHistory [][]int
}

func newRootFakeSystem(t *testing.T) *fakeSystem {
	t.Helper()
	return &fakeSystem{
		t:              t,
		current:        identity{groups: []int{0}},
		rootInfo:       &fileInfo{kind: kindDirectory, uid: 0, gid: 0, nlink: 2},
		files:          map[string]fileInfo{},
		openErr:        map[string]error{},
		statErr:        map[string]error{},
		chownErr:       map[string]error{},
		socketPath:     "/var/run/docker.sock",
		socket:         &fileInfo{kind: kindSocket, uid: 0, gid: 998, nlink: 1},
		nextDescriptor: 3,
		descriptors:    map[int]string{},
		closed:         map[int]bool{},
	}
}

func (f *fakeSystem) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeSystem) identity() (identity, error) {
	index := f.identityCalls
	f.identityCalls++
	f.record("identity")
	if index < len(f.identityErr) && f.identityErr[index] != nil {
		return identity{}, f.identityErr[index]
	}
	current := f.current
	current.groups = slices.Clone(f.current.groups)
	return current, nil
}

func (f *fakeSystem) allocate(name string) int {
	descriptor := f.nextDescriptor
	f.nextDescriptor++
	f.descriptors[descriptor] = name
	return descriptor
}

func (f *fakeSystem) openDirectory(path string) (int, error) {
	f.record("openDirectory %s", path)
	if path != DataRoot {
		f.t.Fatalf("openDirectory(%q), want only %q", path, DataRoot)
	}
	if f.rootOpenErr != nil {
		return -1, f.rootOpenErr
	}
	return f.allocate(fakeRootName), nil
}

func (f *fakeSystem) openAt(directory int, name string) (int, error) {
	f.record("openAt %s", name)
	if f.descriptors[directory] != fakeRootName {
		f.t.Fatalf("openAt used descriptor %d, want the data root descriptor", directory)
	}
	if err := f.openErr[name]; err != nil {
		return -1, err
	}
	if _, exists := f.files[name]; !exists {
		return -1, syscall.ENOENT
	}
	return f.allocate(name), nil
}

func (f *fakeSystem) fstat(descriptor int) (fileInfo, error) {
	name := f.descriptorName(descriptor)
	f.record("fstat %s", name)
	if name == fakeRootName {
		if f.rootStatErr != nil {
			return fileInfo{}, f.rootStatErr
		}
		return *f.rootInfo, nil
	}
	if err := f.statErr[name]; err != nil {
		return fileInfo{}, err
	}
	return f.files[name], nil
}

func (f *fakeSystem) fchown(descriptor, uid, gid int) error {
	name := f.descriptorName(descriptor)
	f.record("fchown %s %d:%d", name, uid, gid)
	if err := f.chownErr[name]; err != nil {
		return err
	}
	f.chowned = append(f.chowned, name)
	if name == fakeRootName {
		f.rootInfo.uid, f.rootInfo.gid = uid, gid
		return nil
	}
	info := f.files[name]
	info.uid, info.gid = uid, gid
	f.files[name] = info
	return nil
}

func (f *fakeSystem) close(descriptor int) error {
	name := f.descriptorName(descriptor)
	if f.closed[descriptor] {
		f.t.Fatalf("descriptor for %s closed twice", name)
	}
	f.closed[descriptor] = true
	return nil
}

func (f *fakeSystem) lstat(path string) (fileInfo, error) {
	f.record("lstat")
	f.lstatPaths = append(f.lstatPaths, path)
	if f.socketErr != nil {
		return fileInfo{}, f.socketErr
	}
	if path != f.socketPath || f.socket == nil {
		return fileInfo{}, syscall.ENOENT
	}
	return *f.socket, nil
}

func (f *fakeSystem) setgroups(groups []int) error {
	f.record("setgroups %v", groups)
	f.setgroupsHistory = append(f.setgroupsHistory, slices.Clone(groups))
	if f.setgroupsErr != nil {
		return f.setgroupsErr
	}
	if !f.ignoreSetgroups {
		f.current.groups = slices.Clone(groups)
	}
	return nil
}

func (f *fakeSystem) setgid(gid int) error {
	f.record("setgid %d", gid)
	if f.setgidErr != nil {
		return f.setgidErr
	}
	f.current.realGID, f.current.effectiveGID, f.current.savedGID = gid, gid, gid
	return nil
}

func (f *fakeSystem) setuid(uid int) error {
	f.record("setuid %d", uid)
	if uid == 0 && f.current.effectiveUID != 0 && !f.allowRegainRoot {
		return syscall.EPERM
	}
	if f.setuidErr != nil {
		return f.setuidErr
	}
	if !f.ignoreSetuid {
		f.current.realUID, f.current.effectiveUID, f.current.savedUID = uid, uid, uid
	}
	return nil
}

func (f *fakeSystem) setNoNewPrivileges() error {
	f.record("setNoNewPrivileges")
	return f.noNewPrivsErr
}

func (f *fakeSystem) exec(path string, argv, environment []string) error {
	f.record("exec %s", path)
	f.execPath = path
	f.execArgv = slices.Clone(argv)
	f.execEnvironment = slices.Clone(environment)
	return f.execErr
}

func (f *fakeSystem) descriptorName(descriptor int) string {
	name, exists := f.descriptors[descriptor]
	if !exists {
		f.t.Fatalf("unknown descriptor %d", descriptor)
	}
	if f.closed[descriptor] {
		f.t.Fatalf("descriptor for %s used after close", name)
	}
	return name
}

func (f *fakeSystem) assertAllClosed() {
	f.t.Helper()
	for descriptor, name := range f.descriptors {
		if !f.closed[descriptor] {
			f.t.Fatalf("descriptor for %s was not closed", name)
		}
	}
}

func (f *fakeSystem) called(prefix string) bool {
	for _, call := range f.calls {
		if len(call) >= len(prefix) && call[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func (f *fakeSystem) assertNoMutation() {
	f.t.Helper()
	for _, prefix := range []string{"fchown", "setgroups", "setgid", "setuid", "setNoNewPrivileges", "exec"} {
		if f.called(prefix) {
			f.t.Fatalf("unexpected %s call after a validation failure; calls = %v", prefix, f.calls)
		}
	}
}
