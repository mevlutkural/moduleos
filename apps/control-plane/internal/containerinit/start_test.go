package containerinit

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"syscall"
	"testing"
)

const testDatabasePath = DataRoot + "/moduleos.db"

func environmentLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, set := values[key]
		return value, set
	}
}

func testDependencies(sys *fakeSystem, values map[string]string) dependencies {
	environment := []string{"ENV=production", "DATABASE_PATH=" + values["DATABASE_PATH"], "UNRELATED=kept"}
	return dependencies{
		lookup:  environmentLookup(values),
		environ: func() []string { return slices.Clone(environment) },
		newSystem: func() (system, error) {
			return sys, nil
		},
		stderr: io.Discard,
	}
}

func defaultValues() map[string]string {
	return map[string]string{"DATABASE_PATH": testDatabasePath}
}

func runStart(t *testing.T, sys *fakeSystem, values map[string]string) (string, error) {
	t.Helper()
	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, nil))
	err := start(testDependencies(sys, values), log)
	return output.String(), err
}

func requireCode(t *testing.T, err error, want code) {
	t.Helper()
	if got := codeOf(err, "unclassified"); got != want {
		t.Fatalf("code = %q, want %q (err = %v)", got, want, err)
	}
}

func rootOwnedSQLiteFiles(sys *fakeSystem) {
	for _, name := range []string{"moduleos.db", "moduleos.db-wal", "moduleos.db-shm"} {
		sys.files[name] = fileInfo{kind: kindRegular, uid: 0, gid: 0, nlink: 1}
	}
}

func TestStartPreparesExactFilesDropsPrivilegesAndExecsDaemon(t *testing.T) {
	sys := newRootFakeSystem(t)
	rootOwnedSQLiteFiles(sys)

	_, err := runStart(t, sys, defaultValues())

	requireCode(t, err, codeExecFailed)
	want := []string{
		"identity",
		"lstat",
		"openDirectory " + DataRoot,
		"fstat " + fakeRootName,
		"openAt moduleos.db", "fstat moduleos.db",
		"openAt moduleos.db-wal", "fstat moduleos.db-wal",
		"openAt moduleos.db-shm", "fstat moduleos.db-shm",
		"fchown " + fakeRootName + " 65532:65532",
		"fchown moduleos.db 65532:65532",
		"fchown moduleos.db-wal 65532:65532",
		"fchown moduleos.db-shm 65532:65532",
		"setgroups [65532 998]",
		"setgid 65532",
		"setuid 65532",
		"identity",
		"setuid 0",
		"setNoNewPrivileges",
		"exec " + DaemonPath,
	}
	if !slices.Equal(sys.calls, want) {
		t.Fatalf("calls =\n%s\nwant\n%s", strings.Join(sys.calls, "\n"), strings.Join(want, "\n"))
	}
	if !slices.Equal(sys.execArgv, []string{"moduleosd"}) {
		t.Fatalf("exec argv = %v", sys.execArgv)
	}
	wantEnvironment := []string{"ENV=production", "DATABASE_PATH=" + testDatabasePath, "UNRELATED=kept"}
	if !slices.Equal(sys.execEnvironment, wantEnvironment) {
		t.Fatalf("exec environment = %v, want unchanged %v", sys.execEnvironment, wantEnvironment)
	}
	if !slices.Equal(sys.lstatPaths, []string{"/var/run/docker.sock"}) {
		t.Fatalf("lstat paths = %v", sys.lstatPaths)
	}
	sys.assertAllClosed()
}

func TestStartSkipsOwnershipChangesWhenAlreadyOwned(t *testing.T) {
	sys := newRootFakeSystem(t)
	sys.rootInfo = &fileInfo{kind: kindDirectory, uid: RuntimeUID, gid: RuntimeGID, nlink: 2}
	sys.files["moduleos.db"] = fileInfo{kind: kindRegular, uid: RuntimeUID, gid: RuntimeGID, nlink: 1}

	_, err := runStart(t, sys, defaultValues())

	requireCode(t, err, codeExecFailed)
	if len(sys.chowned) != 0 {
		t.Fatalf("chowned = %v, want none", sys.chowned)
	}
	sys.assertAllClosed()
}

func TestStartChangesOnlyMismatchedOwnership(t *testing.T) {
	sys := newRootFakeSystem(t)
	sys.rootInfo = &fileInfo{kind: kindDirectory, uid: RuntimeUID, gid: 0, nlink: 2}
	sys.files["moduleos.db"] = fileInfo{kind: kindRegular, uid: RuntimeUID, gid: RuntimeGID, nlink: 1}
	sys.files["moduleos.db-wal"] = fileInfo{kind: kindRegular, uid: 0, gid: RuntimeGID, nlink: 1}

	_, err := runStart(t, sys, defaultValues())

	requireCode(t, err, codeExecFailed)
	if want := []string{fakeRootName, "moduleos.db-wal"}; !slices.Equal(sys.chowned, want) {
		t.Fatalf("chowned = %v, want %v", sys.chowned, want)
	}
}

func TestStartRejectsDatabasePathsOutsideTheDataRoot(t *testing.T) {
	tests := map[string]*string{
		"unset":            nil,
		"empty":            ptr(""),
		"relative":         ptr("moduleos.db"),
		"unclean":          ptr("/var/lib/moduleos/../moduleos/moduleos.db"),
		"trailing slash":   ptr("/var/lib/moduleos/moduleos.db/"),
		"query":            ptr("/var/lib/moduleos/moduleos.db?mode=memory"),
		"control":          ptr("/var/lib/moduleos/module\nos.db"),
		"different parent": ptr("/var/lib/other/moduleos.db"),
		"nested":           ptr("/var/lib/moduleos/nested/moduleos.db"),
		"data root itself": ptr(DataRoot),
		"uri":              ptr("file:/var/lib/moduleos/moduleos.db"),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			sys := newRootFakeSystem(t)
			values := map[string]string{}
			if value != nil {
				values["DATABASE_PATH"] = *value
			}
			_, err := runStart(t, sys, values)
			requireCode(t, err, codeDatabasePathInvalid)
			sys.assertNoMutation()
			if sys.called("openDirectory") || sys.called("lstat") {
				t.Fatalf("filesystem inspected before the database path was validated: %v", sys.calls)
			}
		})
	}
}

func TestStartAcceptsAnyDatabaseNameDirectlyInsideTheDataRoot(t *testing.T) {
	sys := newRootFakeSystem(t)
	sys.files["state.sqlite"] = fileInfo{kind: kindRegular, uid: 0, gid: 0, nlink: 1}
	sys.files["moduleos.db"] = fileInfo{kind: kindRegular, uid: 0, gid: 0, nlink: 1}

	_, err := runStart(t, sys, map[string]string{"DATABASE_PATH": DataRoot + "/state.sqlite"})

	requireCode(t, err, codeExecFailed)
	if want := []string{fakeRootName, "state.sqlite"}; !slices.Equal(sys.chowned, want) {
		t.Fatalf("chowned = %v, want %v; unrelated files must never change", sys.chowned, want)
	}
}

func TestStartDockerSocketPolicy(t *testing.T) {
	tests := []struct {
		name       string
		values     map[string]string
		socketPath string
		socket     *fileInfo
		socketErr  error
		wantCode   code
		wantGroups []int
		wantWarn   bool
	}{
		{name: "socket group added", socket: &fileInfo{kind: kindSocket, gid: 998}, wantCode: codeExecFailed, wantGroups: []int{RuntimeGID, 998}},
		{name: "socket group equals runtime group", socket: &fileInfo{kind: kindSocket, gid: RuntimeGID}, wantCode: codeExecFailed, wantGroups: []int{RuntimeGID}},
		{name: "configured endpoint path", values: map[string]string{"DOCKER_ENDPOINT": "unix:///run/custom/docker.sock"}, socketPath: "/run/custom/docker.sock", socket: &fileInfo{kind: kindSocket, gid: 4242}, wantCode: codeExecFailed, wantGroups: []int{RuntimeGID, 4242}},
		{name: "legacy endpoint path", values: map[string]string{"DOCKER_SOCKET": "/run/legacy.sock"}, socketPath: "/run/legacy.sock", socket: &fileInfo{kind: kindSocket, gid: 4243}, wantCode: codeExecFailed, wantGroups: []int{RuntimeGID, 4243}},
		{name: "absent socket continues", socket: nil, wantCode: codeExecFailed, wantGroups: []int{RuntimeGID}, wantWarn: true},
		{name: "root group refused", socket: &fileInfo{kind: kindSocket, gid: 0}, wantCode: codeDockerSocketRootGroup},
		{name: "regular file refused", socket: &fileInfo{kind: kindRegular, gid: 998}, wantCode: codeDockerSocketInvalid},
		{name: "symlink refused", socket: &fileInfo{kind: kindSymlink, gid: 998}, wantCode: codeDockerSocketInvalid},
		{name: "directory refused", socket: &fileInfo{kind: kindDirectory, gid: 998}, wantCode: codeDockerSocketInvalid},
		{name: "inspection error refused", socketErr: syscall.EACCES, wantCode: codeDockerSocketInvalid},
		{name: "remote endpoint refused", values: map[string]string{"DOCKER_ENDPOINT": "tcp://docker.internal:2375"}, wantCode: codeDockerEndpointInvalid},
		{name: "conflicting endpoints refused", values: map[string]string{"DOCKER_ENDPOINT": "unix:///run/a.sock", "DOCKER_SOCKET": "/run/b.sock"}, wantCode: codeDockerEndpointInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sys := newRootFakeSystem(t)
			if test.socketPath != "" {
				sys.socketPath = test.socketPath
			}
			sys.socket = test.socket
			sys.socketErr = test.socketErr
			values := defaultValues()
			for key, value := range test.values {
				values[key] = value
			}

			output, err := runStart(t, sys, values)

			requireCode(t, err, test.wantCode)
			if test.wantGroups == nil {
				sys.assertNoMutation()
				if sys.called("openDirectory") {
					t.Fatal("data root inspected after a socket validation failure")
				}
				return
			}
			if len(sys.setgroupsHistory) != 1 || !slices.Equal(sys.setgroupsHistory[0], test.wantGroups) {
				t.Fatalf("setgroups = %v, want %v", sys.setgroupsHistory, test.wantGroups)
			}
			warned := strings.Contains(output, string(codeDockerSocketAbsent))
			if warned != test.wantWarn {
				t.Fatalf("warning emitted = %v, want %v; output = %q", warned, test.wantWarn, output)
			}
		})
	}
}

func TestStartFailsClosedBeforeAnyOwnershipChange(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*fakeSystem)
		want    code
	}{
		{name: "data root cannot be opened", prepare: func(f *fakeSystem) { f.rootOpenErr = syscall.ENOTDIR }, want: codeDataRootInvalid},
		{name: "data root missing", prepare: func(f *fakeSystem) { f.rootOpenErr = syscall.ENOENT }, want: codeDataRootInvalid},
		{name: "data root stat fails", prepare: func(f *fakeSystem) { f.rootStatErr = syscall.EIO }, want: codeDataRootInvalid},
		{name: "data root is not a directory", prepare: func(f *fakeSystem) { f.rootInfo.kind = kindSymlink }, want: codeDataRootInvalid},
		{name: "data root not searchable", prepare: func(f *fakeSystem) { f.rootOpenErr = syscall.EACCES }, want: codeDataRootInaccessible},
		{name: "sidecar not reachable without search permission", prepare: func(f *fakeSystem) { f.openErr["moduleos.db"] = syscall.EACCES }, want: codeDataRootInaccessible},
		{name: "sidecar open fails", prepare: func(f *fakeSystem) { f.openErr["moduleos.db-shm"] = syscall.EIO }, want: codeSQLiteFileInvalid},
		{name: "sidecar is a symlink", prepare: func(f *fakeSystem) { f.files["moduleos.db-shm"] = fileInfo{kind: kindSymlink, nlink: 1} }, want: codeSQLiteFileInvalid},
		{name: "database is a directory", prepare: func(f *fakeSystem) { f.files["moduleos.db"] = fileInfo{kind: kindDirectory, nlink: 2} }, want: codeSQLiteFileInvalid},
		{name: "wal is a socket", prepare: func(f *fakeSystem) { f.files["moduleos.db-wal"] = fileInfo{kind: kindSocket, nlink: 1} }, want: codeSQLiteFileInvalid},
		{name: "shm is a device or fifo", prepare: func(f *fakeSystem) { f.files["moduleos.db-shm"] = fileInfo{kind: kindOther, nlink: 1} }, want: codeSQLiteFileInvalid},
		{name: "database is hard linked", prepare: func(f *fakeSystem) { f.files["moduleos.db"] = fileInfo{kind: kindRegular, nlink: 2} }, want: codeSQLiteFileInvalid},
		{name: "sidecar stat fails", prepare: func(f *fakeSystem) { f.statErr["moduleos.db-wal"] = syscall.EIO }, want: codeSQLiteFileInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sys := newRootFakeSystem(t)
			rootOwnedSQLiteFiles(sys)
			test.prepare(sys)

			_, err := runStart(t, sys, defaultValues())

			requireCode(t, err, test.want)
			sys.assertNoMutation()
			sys.assertAllClosed()
		})
	}
}

func TestStartReportsOwnershipChangeFailures(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   code
	}{
		{name: "data root", target: fakeRootName, want: codeDataRootChownFailed},
		{name: "database", target: "moduleos.db", want: codeSQLiteFileChownFailed},
		{name: "shm", target: "moduleos.db-shm", want: codeSQLiteFileChownFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sys := newRootFakeSystem(t)
			rootOwnedSQLiteFiles(sys)
			sys.chownErr[test.target] = syscall.EPERM

			_, err := runStart(t, sys, defaultValues())

			requireCode(t, err, test.want)
			if sys.called("setgroups") || sys.called("exec") {
				t.Fatalf("continued after an ownership failure: %v", sys.calls)
			}
			sys.assertAllClosed()
		})
	}
}

func TestStartPrivilegeDropFailures(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*fakeSystem)
		want    code
	}{
		{name: "setgroups fails", prepare: func(f *fakeSystem) { f.setgroupsErr = syscall.EPERM }, want: codePrivilegeDropFailed},
		{name: "setgid fails", prepare: func(f *fakeSystem) { f.setgidErr = syscall.EPERM }, want: codePrivilegeDropFailed},
		{name: "setuid fails", prepare: func(f *fakeSystem) { f.setuidErr = syscall.EPERM }, want: codePrivilegeDropFailed},
		{name: "identity unreadable after drop", prepare: func(f *fakeSystem) { f.identityErr = []error{nil, syscall.EIO} }, want: codePrivilegeVerifyFailed},
		{name: "uid not changed", prepare: func(f *fakeSystem) { f.ignoreSetuid = true }, want: codePrivilegeVerifyFailed},
		{name: "groups not changed", prepare: func(f *fakeSystem) { f.ignoreSetgroups = true }, want: codePrivilegeVerifyFailed},
		{name: "root can be regained", prepare: func(f *fakeSystem) { f.allowRegainRoot = true }, want: codePrivilegeVerifyFailed},
		{name: "no new privileges fails", prepare: func(f *fakeSystem) { f.noNewPrivsErr = syscall.EINVAL }, want: codeNoNewPrivilegesFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sys := newRootFakeSystem(t)
			test.prepare(sys)

			_, err := runStart(t, sys, defaultValues())

			requireCode(t, err, test.want)
			if sys.called("exec") {
				t.Fatalf("exec reached after %s: %v", test.name, sys.calls)
			}
		})
	}
}

func TestStartVerifiesEveryDroppedIdentityField(t *testing.T) {
	fields := []func(*identity){
		func(i *identity) { i.realUID = 0 },
		func(i *identity) { i.effectiveUID = 0 },
		func(i *identity) { i.savedUID = 0 },
		func(i *identity) { i.realGID = 0 },
		func(i *identity) { i.effectiveGID = 0 },
		func(i *identity) { i.savedGID = 0 },
		func(i *identity) { i.groups = append(i.groups, 0) },
	}
	for index, corrupt := range fields {
		sys := newRootFakeSystem(t)
		verifying := &identitySystem{fakeSystem: sys, corruptAfterDrop: corrupt}
		deps := testDependencies(sys, defaultValues())
		deps.newSystem = func() (system, error) { return verifying, nil }

		err := start(deps, slog.New(slog.NewJSONHandler(io.Discard, nil)))

		if codeOf(err, "unclassified") != codePrivilegeVerifyFailed {
			t.Fatalf("field %d: code = %q, want %q", index, codeOf(err, "unclassified"), codePrivilegeVerifyFailed)
		}
	}
}

// identitySystem corrupts the identity reported after the privilege drop.
type identitySystem struct {
	*fakeSystem
	corruptAfterDrop func(*identity)
}

func (s *identitySystem) identity() (identity, error) {
	current, err := s.fakeSystem.identity()
	if err == nil && s.identityCalls > 1 {
		s.corruptAfterDrop(&current)
	}
	return current, err
}

func TestStartReportsExecFailure(t *testing.T) {
	sys := newRootFakeSystem(t)
	sys.execErr = syscall.ENOENT

	_, err := runStart(t, sys, defaultValues())

	requireCode(t, err, codeExecFailed)
}

func TestStartReportsUnreadableInitialIdentity(t *testing.T) {
	sys := newRootFakeSystem(t)
	sys.identityErr = []error{syscall.EIO}

	_, err := runStart(t, sys, defaultValues())

	requireCode(t, err, codePrivilegeVerifyFailed)
	sys.assertNoMutation()
}

func TestStartReportsUnsupportedPlatform(t *testing.T) {
	deps := testDependencies(newRootFakeSystem(t), defaultValues())
	deps.newSystem = func() (system, error) { return nil, fail(codePlatformUnsupported) }

	err := start(deps, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	requireCode(t, err, codePlatformUnsupported)
}

func TestStartAsNonRootSkipsPreparation(t *testing.T) {
	sys := newRootFakeSystem(t)
	sys.current = identity{
		realUID: RuntimeUID, effectiveUID: RuntimeUID, savedUID: RuntimeUID,
		realGID: RuntimeGID, effectiveGID: RuntimeGID, savedGID: RuntimeGID,
		groups: []int{RuntimeGID},
	}

	_, err := runStart(t, sys, map[string]string{"DATABASE_PATH": "/anywhere/else.db"})

	requireCode(t, err, codeExecFailed)
	if want := []string{"identity", "setNoNewPrivileges", "exec " + DaemonPath}; !slices.Equal(sys.calls, want) {
		t.Fatalf("calls = %v, want %v", sys.calls, want)
	}
}

func TestStartAsNonRootRefusesResidualRootIdentity(t *testing.T) {
	residual := []func(*identity){
		func(i *identity) { i.realUID = 0 },
		func(i *identity) { i.savedUID = 0 },
		func(i *identity) { i.realGID = 0 },
		func(i *identity) { i.effectiveGID = 0 },
		func(i *identity) { i.savedGID = 0 },
		func(i *identity) { i.groups = []int{RuntimeGID, 0} },
	}
	for index, apply := range residual {
		sys := newRootFakeSystem(t)
		sys.current = identity{
			realUID: RuntimeUID, effectiveUID: RuntimeUID, savedUID: RuntimeUID,
			realGID: RuntimeGID, effectiveGID: RuntimeGID, savedGID: RuntimeGID,
			groups: []int{RuntimeGID},
		}
		apply(&sys.current)

		_, err := runStart(t, sys, defaultValues())

		if codeOf(err, "unclassified") != codeRootIdentityRefused {
			t.Fatalf("case %d: code = %q, want %q", index, codeOf(err, "unclassified"), codeRootIdentityRefused)
		}
		if sys.called("exec") || sys.called("setNoNewPrivileges") {
			t.Fatalf("case %d: continued with a residual root identity", index)
		}
	}
}

func TestCodeOfUsesFallbackForUnclassifiedErrors(t *testing.T) {
	if got := codeOf(errors.New("raw"), codeStartFailed); got != codeStartFailed {
		t.Fatalf("codeOf = %q", got)
	}
	if got := fail(codeExecFailed).Error(); got != string(codeExecFailed) {
		t.Fatalf("Error() = %q", got)
	}
}

func ptr(value string) *string {
	return &value
}
