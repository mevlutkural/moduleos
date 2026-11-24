package operations

// These test-only observations are deliberately closed enums: never log a
// command error, host path, response body or runtime environment as a condition.
type replacementCheck string

const (
	restartReplacement    replacementCheck = "restart_replacement"
	ownershipReplacement  replacementCheck = "ownership_replacement"
	relayEnterReplacement replacementCheck = "outage_enter_relay"
	relayExitReplacement  replacementCheck = "outage_exit_relay"
)

var replacementChecks = map[string]bool{
	"": true, string(restartReplacement): true, string(ownershipReplacement): true,
	string(relayEnterReplacement): true, string(relayExitReplacement): true,
}
var replacementObservations = map[string]bool{
	"": true, "none": true, "process_inspect_failed": true,
	"process_not_singleton": true, "task_not_running": true, "process_not_running": true,
	"task_not_replaced": true, "container_not_replaced": true, "start_not_replaced": true,
	"mount_inspect_failed": true, "socket_mount_mismatch": true, "readiness_unavailable": true,
}

type processIdentity struct {
	Task, Container, Started string
	PID                      int
}

type socketMount struct {
	Type, Source, Destination string
	RW                        bool
}

func socketMountUpdateArgs(source, service string) []string {
	// Docker CLI 28.0.4 adds/updates by target before applying --mount-rm.
	// Combining both flags for this target would remove the replacement too.
	return []string{"service", "update", "--detach=true", "--mount-add", "type=bind,source=" + source + ",target=/var/run/docker.sock,readonly", service}
}

func replacementIdentityCondition(before, now processIdentity) string {
	if now.Task == before.Task || now.Task == "" {
		return "task_not_replaced"
	}
	if now.Container == before.Container || now.Container == "" {
		return "container_not_replaced"
	}
	if now.Started == before.Started || now.Started == "" {
		return "start_not_replaced"
	}
	if now.PID <= 0 {
		return "process_not_running"
	}
	return "none"
}

func hasSocketMount(mounts []socketMount, source string) bool {
	for _, mount := range mounts {
		if mount.Destination == "/var/run/docker.sock" && mount.Source == source && mount.Type == "bind" && !mount.RW {
			return true
		}
	}
	return false
}
