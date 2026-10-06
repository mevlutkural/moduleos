package swarm

import (
	"fmt"
)

const (
	LabelManagedBy = "moduleos.managed"
	LabelAppName   = "moduleos.app"
)

// AutoDomain constructs the auto-assigned subdomain for an app from baseDomain.
// e.g. appName="my-api", baseDomain="moduleos.local" → "my-api.moduleos.local"
func AutoDomain(appName, baseDomain string) string {
	return fmt.Sprintf("%s.%s", appName, baseDomain)
}
