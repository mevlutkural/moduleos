package bootstrap

import (
	"errors"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
)

// Stable reasons logged with a bootstrap failure, alongside the config
// reasons defined by the config package. They name a class of problem and
// never carry a configured value, path, or raw error text.
const (
	reasonConfigInvalid           = "config_invalid"
	reasonLoggerUnavailable       = "logger_unavailable"
	reasonBannerFailed            = "banner_failed"
	reasonDatabaseOpenFailed      = "database_open_failed"
	reasonDatabaseMigrationFailed = "database_migration_failed"
	reasonDockerClientInvalid     = "docker_client_invalid"
	reasonListenerBindFailed      = "listener_bind_failed"
	reasonAssemblyFailed          = "assembly_failed"
	reasonInternalError           = "internal_error"
)

// startupFailure attaches a stable reason to an assembly error without
// changing its message.
type startupFailure struct {
	reason string
	err    error
}

func (failure *startupFailure) Error() string         { return failure.err.Error() }
func (failure *startupFailure) Unwrap() error         { return failure.err }
func (failure *startupFailure) StartupReason() string { return failure.reason }

func withReason(reason string, err error) error {
	return &startupFailure{reason: reason, err: err}
}

type reasoned interface {
	StartupReason() string
}

func startupReason(err error, fallback string) string {
	var classified reasoned
	if errors.As(err, &classified) {
		return classified.StartupReason()
	}
	return fallback
}

// configFailureAttributes returns the safe log attributes for a failed
// configuration load.
func configFailureAttributes(err error) []any {
	attributes := []any{"phase", "config"}
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) {
		return append(attributes, "reason", reasonConfigInvalid)
	}
	attributes = append(attributes, "reason", loadErr.Reason())
	if variable := loadErr.Variable(); variable != "" {
		attributes = append(attributes, "variable", variable)
	}
	return attributes
}
