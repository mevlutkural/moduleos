package containerinit

import (
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
)

// start validates everything before it changes anything: the database path,
// the Docker socket, and every file it may adjust are inspected first, and
// ownership is then changed only through the descriptors that were inspected.
// It returns only on failure; on success the daemon replaces the process.
func start(deps dependencies, log *slog.Logger) error {
	sys, err := deps.newSystem()
	if err != nil {
		return err
	}
	current, err := sys.identity()
	if err != nil {
		return fail(codePrivilegeVerifyFailed)
	}
	if current.effectiveUID != 0 {
		// The operator started the container as a non-root user and owns
		// preparation. Never continue with any residual root identity.
		if current.includesRoot() {
			return fail(codeRootIdentityRefused)
		}
		return execDaemon(sys, deps)
	}

	databasePath, err := resolveDatabasePath(deps.lookup)
	if err != nil {
		return err
	}
	groups, err := runtimeGroups(sys, deps.lookup, log)
	if err != nil {
		return err
	}
	if err := prepareDataRoot(sys, filepath.Base(databasePath)); err != nil {
		return err
	}
	if err := dropPrivileges(sys, groups); err != nil {
		return err
	}
	return execDaemon(sys, deps)
}

// resolveDatabasePath accepts only a database file directly inside DataRoot,
// so preparation can never reach another directory.
func resolveDatabasePath(lookup func(string) (string, bool)) (string, error) {
	value, set := lookup("DATABASE_PATH")
	if !set || value == "" || strings.ContainsRune(value, '?') || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", fail(codeDatabasePathInvalid)
	}
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || filepath.Dir(value) != DataRoot {
		return "", fail(codeDatabasePathInvalid)
	}
	return value, nil
}

func runtimeGroups(sys system, lookup func(string) (string, bool), log *slog.Logger) ([]int, error) {
	groups := []int{RuntimeGID}
	socketPath, err := config.DockerSocketPath(lookup)
	if err != nil {
		return nil, fail(codeDockerEndpointInvalid)
	}
	info, err := sys.lstat(socketPath)
	if errors.Is(err, fs.ErrNotExist) {
		// Docker unavailability is not fatal to moduleosd; readiness reports it.
		log.Warn("docker socket is not present; continuing without its group", "code", codeDockerSocketAbsent)
		return groups, nil
	}
	if err != nil || info.kind != kindSocket {
		return nil, fail(codeDockerSocketInvalid)
	}
	if info.gid == 0 {
		return nil, fail(codeDockerSocketRootGroup)
	}
	if info.gid != RuntimeGID {
		groups = append(groups, info.gid)
	}
	return groups, nil
}

type ownershipTarget struct {
	descriptor int
	info       fileInfo
	chownCode  code
}

func prepareDataRoot(sys system, databaseName string) error {
	var opened []int
	defer func() {
		for _, descriptor := range opened {
			_ = sys.close(descriptor)
		}
	}()

	root, openErr := sys.openDirectory(DataRoot)
	if errors.Is(openErr, fs.ErrPermission) {
		return fail(codeDataRootInaccessible)
	}
	if openErr != nil {
		return fail(codeDataRootInvalid)
	}
	opened = append(opened, root)
	rootInfo, statErr := sys.fstat(root)
	if statErr != nil || rootInfo.kind != kindDirectory {
		return fail(codeDataRootInvalid)
	}
	targets := []ownershipTarget{{descriptor: root, info: rootInfo, chownCode: codeDataRootChownFailed}}

	for _, name := range []string{databaseName, databaseName + "-wal", databaseName + "-shm"} {
		descriptor, openErr := sys.openAt(root, name)
		if errors.Is(openErr, fs.ErrNotExist) {
			continue
		}
		// Without DAC_READ_SEARCH, root cannot search a data root it does not
		// own; report that as missing access, not as an unsafe file.
		if errors.Is(openErr, fs.ErrPermission) {
			return fail(codeDataRootInaccessible)
		}
		if openErr != nil {
			return fail(codeSQLiteFileInvalid)
		}
		opened = append(opened, descriptor)
		info, statErr := sys.fstat(descriptor)
		if statErr != nil || info.kind != kindRegular || info.nlink != 1 {
			return fail(codeSQLiteFileInvalid)
		}
		targets = append(targets, ownershipTarget{descriptor: descriptor, info: info, chownCode: codeSQLiteFileChownFailed})
	}

	for _, target := range targets {
		if target.info.uid == RuntimeUID && target.info.gid == RuntimeGID {
			continue
		}
		if err := sys.fchown(target.descriptor, RuntimeUID, RuntimeGID); err != nil {
			return fail(target.chownCode)
		}
	}
	return nil
}

func dropPrivileges(sys system, groups []int) error {
	if err := sys.setgroups(groups); err != nil {
		return fail(codePrivilegeDropFailed)
	}
	if err := sys.setgid(RuntimeGID); err != nil {
		return fail(codePrivilegeDropFailed)
	}
	if err := sys.setuid(RuntimeUID); err != nil {
		return fail(codePrivilegeDropFailed)
	}

	current, err := sys.identity()
	if err != nil {
		return fail(codePrivilegeVerifyFailed)
	}
	for _, value := range []int{current.realUID, current.effectiveUID, current.savedUID} {
		if value != RuntimeUID {
			return fail(codePrivilegeVerifyFailed)
		}
	}
	for _, value := range []int{current.realGID, current.effectiveGID, current.savedGID} {
		if value != RuntimeGID {
			return fail(codePrivilegeVerifyFailed)
		}
	}
	actual := slices.Clone(current.groups)
	expected := slices.Clone(groups)
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		return fail(codePrivilegeVerifyFailed)
	}
	if err := sys.setuid(0); err == nil {
		return fail(codePrivilegeVerifyFailed)
	}
	return nil
}

func execDaemon(sys system, deps dependencies) error {
	if err := sys.setNoNewPrivileges(); err != nil {
		return fail(codeNoNewPrivilegesFailed)
	}
	if err := sys.exec(DaemonPath, []string{"moduleosd"}, deps.environ()); err != nil {
		return fail(codeExecFailed)
	}
	// A successful exec never returns.
	return fail(codeExecFailed)
}
