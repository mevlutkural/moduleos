package operations

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestSocketMountUpdateReplacesWithoutRemovingTarget(t *testing.T) {
	for _, source := range []string{"/tmp/run/relay.sock", "/var/run/docker.sock"} {
		args := socketMountUpdateArgs(source, "owned-service")
		want := []string{"service", "update", "--detach=true", "--mount-add", "type=bind,source=" + source + ",target=/var/run/docker.sock,readonly", "owned-service"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("unexpected mount update: %v", args)
		}
		for _, arg := range args {
			if strings.HasPrefix(arg, "--mount-rm") {
				t.Fatal("replacement removed by Docker CLI target-removal ordering")
			}
		}
	}
}

func TestReplacementStillRequiresNewIdentityAndActualReadonlySocket(t *testing.T) {
	before := processIdentity{Task: "old-task", Container: "old-container", Started: "old-start", PID: 1}
	valid := processIdentity{Task: "new-task", Container: "new-container", Started: "new-start", PID: 2}
	for _, test := range []struct {
		name string
		now  processIdentity
		want string
	}{
		{"valid", valid, "none"}, {"old task", processIdentity{before.Task, valid.Container, valid.Started, valid.PID}, "task_not_replaced"},
		{"old container", processIdentity{valid.Task, before.Container, valid.Started, valid.PID}, "container_not_replaced"},
		{"old start", processIdentity{valid.Task, valid.Container, before.Started, valid.PID}, "start_not_replaced"},
		{"no process", processIdentity{valid.Task, valid.Container, valid.Started, 0}, "process_not_running"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := replacementIdentityCondition(before, test.now); got != test.want {
				t.Fatalf("condition %s, want %s", got, test.want)
			}
		})
	}
	for _, mount := range []socketMount{
		{Type: "bind", Source: "/expected", Destination: "/var/run/docker.sock"},
		{Type: "bind", Source: "/wrong", Destination: "/var/run/docker.sock"},
		{Type: "bind", Source: "/expected", Destination: "/wrong"},
		{Type: "volume", Source: "/expected", Destination: "/var/run/docker.sock"},
		{Type: "bind", Source: "/expected", Destination: "/var/run/docker.sock", RW: true},
	} {
		want := mount.Type == "bind" && mount.Source == "/expected" && mount.Destination == "/var/run/docker.sock" && !mount.RW
		if hasSocketMount([]socketMount{mount}, "/expected") != want {
			t.Fatal("socket mount validation weakened")
		}
	}
	if hasSocketMount(nil, "/expected") {
		t.Fatal("missing socket mount accepted")
	}
}

func TestReplacementDiagnosticsAllowlistAndSummary(t *testing.T) {
	for _, check := range []replacementCheck{restartReplacement, ownershipReplacement, relayEnterReplacement, relayExitReplacement} {
		e := Event{Run: "abcdef012345", Arch: "amd64", Source: "unknown", Image: "unknown", Stage: "outage", Status: "failed", Reason: "timeout", Check: string(check), Observation: "socket_mount_mismatch"}
		path := t.TempDir() + "/events.jsonl"
		if err := AppendEvent(path, e); err != nil {
			t.Fatal(err)
		}
		var encoded bytes.Buffer
		if err := Summary(strings.NewReader(`{"run":"abcdef012345","arch":"amd64","source":"unknown","image":"unknown","stage":"outage","status":"failed","reason":"timeout","elapsed_ms":1,"check":"`+string(check)+`","observation":"socket_mount_mismatch"}`), &encoded); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(encoded.String(), string(check)) || !strings.Contains(encoded.String(), "socket_mount_mismatch") {
			t.Fatal("diagnostic missing")
		}
		e.Check = "/raw/private/path"
		if e.Validate() == nil {
			t.Fatal("raw check accepted")
		}
		e.Check = string(check)
		e.Observation = "Authorization: Bearer untrusted"
		if e.Validate() == nil {
			t.Fatal("raw observation accepted")
		}
	}
}
