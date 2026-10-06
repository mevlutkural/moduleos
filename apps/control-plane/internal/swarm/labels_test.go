package swarm

import (
	"testing"
)

func TestServiceName(t *testing.T) {
	if got := ServiceName("my-api"); got != "moduleos_my-api" {
		t.Errorf("got %s, want moduleos_my-api", got)
	}
}
