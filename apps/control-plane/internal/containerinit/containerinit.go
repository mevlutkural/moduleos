// Package containerinit implements the production container helper. Its start
// command prepares only the data directory, the exact SQLite database files,
// and the Docker socket supplementary group, drops root irreversibly, and
// executes moduleosd. Its healthcheck command probes process liveness.
package containerinit

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"time"
)

const (
	// DataRoot is the only directory whose ownership start may adjust.
	DataRoot = "/var/lib/moduleos"
	// DaemonPath is the only executable start will run.
	DaemonPath = "/usr/local/bin/moduleosd"
	// RuntimeUID and RuntimeGID are the fixed non-root daemon identity.
	RuntimeUID = 65532
	RuntimeGID = 65532

	healthcheckTimeout = 2 * time.Second
)

// code is a stable, value-free failure identifier. Output never includes
// environment values, paths, or operating-system error text.
type code string

const (
	codeUsageInvalid              code = "usage_invalid"
	codePlatformUnsupported       code = "platform_unsupported"
	codeDatabasePathInvalid       code = "database_path_invalid"
	codeDataRootInvalid           code = "data_root_invalid"
	codeDataRootInaccessible      code = "data_root_inaccessible"
	codeDataRootChownFailed       code = "data_root_chown_failed"
	codeSQLiteFileInvalid         code = "sqlite_file_invalid"
	codeSQLiteFileChownFailed     code = "sqlite_file_chown_failed"
	codeDockerEndpointInvalid     code = "docker_endpoint_invalid"
	codeDockerSocketInvalid       code = "docker_socket_invalid"
	codeDockerSocketRootGroup     code = "docker_socket_root_group"
	codeDockerSocketAbsent        code = "docker_socket_absent"
	codePrivilegeDropFailed       code = "privilege_drop_failed"
	codePrivilegeVerifyFailed     code = "privilege_verify_failed"
	codeNoNewPrivilegesFailed     code = "no_new_privileges_failed"
	codeRootIdentityRefused       code = "root_identity_refused"
	codeExecFailed                code = "exec_failed"
	codeHealthcheckConfigInvalid  code = "healthcheck_config_invalid"
	codeHealthcheckUnreachable    code = "healthcheck_unreachable"
	codeHealthcheckUnhealthy      code = "healthcheck_unhealthy"
	codeStartFailed               code = "start_failed"
	codeHealthcheckFailedFallback code = "healthcheck_failed"
)

type failure struct {
	code code
}

func (f failure) Error() string {
	return string(f.code)
}

func fail(value code) error {
	return failure{code: value}
}

func codeOf(err error, fallback code) code {
	var known failure
	if errors.As(err, &known) {
		return known.code
	}
	return fallback
}

type dependencies struct {
	lookup             func(string) (string, bool)
	environ            func() []string
	newSystem          func() (system, error)
	stderr             io.Writer
	healthcheckTimeout time.Duration
}

// Main runs the helper command named by args and returns the process exit
// code. A successful start never returns because the daemon replaces the
// helper process.
func Main(args []string) int {
	return run(args, dependencies{
		lookup:             os.LookupEnv,
		environ:            os.Environ,
		newSystem:          newSystem,
		stderr:             os.Stderr,
		healthcheckTimeout: healthcheckTimeout,
	})
}

func run(args []string, deps dependencies) int {
	log := slog.New(slog.NewJSONHandler(deps.stderr, nil))
	if len(args) != 1 {
		log.Error("container helper failed", "code", codeUsageInvalid)
		return 2
	}
	switch args[0] {
	case "start":
		err := start(deps, log)
		log.Error("container start failed", "code", codeOf(err, codeStartFailed))
		return 1
	case "healthcheck":
		if err := runHealthcheck(deps); err != nil {
			log.Error("container healthcheck failed", "code", codeOf(err, codeHealthcheckFailedFallback))
			return 1
		}
		return 0
	default:
		log.Error("container helper failed", "code", codeUsageInvalid)
		return 2
	}
}
