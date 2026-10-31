package containerinit

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func runCommand(t *testing.T, args []string, deps dependencies) (int, []map[string]any) {
	t.Helper()
	var output bytes.Buffer
	deps.stderr = &output
	exitCode := run(args, deps)
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("stderr line is not JSON: %q", line)
		}
		records = append(records, record)
	}
	return exitCode, records
}

func TestRunRejectsInvalidUsage(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"start", "/bin/sh"}, {"shell"}, {"--help"}} {
		exitCode, records := runCommand(t, args, testDependencies(newRootFakeSystem(t), defaultValues()))
		if exitCode != 2 {
			t.Fatalf("args %q: exit code = %d, want 2", args, exitCode)
		}
		if len(records) != 1 || records[0]["code"] != string(codeUsageInvalid) {
			t.Fatalf("args %q: output = %v", args, records)
		}
	}
}

func TestRunStartFailureEmitsOneSafeLine(t *testing.T) {
	const sentinel = "sentinel-secret-value"
	sys := newRootFakeSystem(t)
	sys.socket = &fileInfo{kind: kindRegular, gid: 998}
	values := map[string]string{
		"DATABASE_PATH":   DataRoot + "/" + sentinel + ".db",
		"DOCKER_ENDPOINT": "unix:///run/" + sentinel + ".sock",
		"API_KEY":         sentinel,
	}
	sys.socketPath = "/run/" + sentinel + ".sock"

	var output bytes.Buffer
	deps := testDependencies(sys, values)
	deps.stderr = &output
	exitCode := run([]string{"start"}, deps)

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	text := output.String()
	if strings.Count(strings.TrimSpace(text), "\n") != 0 {
		t.Fatalf("want exactly one output line, got %q", text)
	}
	if strings.Contains(text, sentinel) || strings.Contains(text, DataRoot) || strings.Contains(text, "/run/") {
		t.Fatalf("output leaked a configured value or path: %q", text)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(text), &record); err != nil {
		t.Fatal(err)
	}
	if record["code"] != string(codeDockerSocketInvalid) || record["level"] != "ERROR" {
		t.Fatalf("record = %v", record)
	}
}

func TestRunStartWarnsThenFailsWithoutLeakingErrors(t *testing.T) {
	sys := newRootFakeSystem(t)
	sys.socket = nil
	sys.execErr = errorWithText("exec /usr/local/bin/moduleosd: permission denied")

	exitCode, records := runCommand(t, []string{"start"}, testDependencies(sys, defaultValues()))

	if exitCode != 1 || len(records) != 2 {
		t.Fatalf("exit code = %d, records = %v", exitCode, records)
	}
	if records[0]["level"] != "WARN" || records[0]["code"] != string(codeDockerSocketAbsent) {
		t.Fatalf("warning record = %v", records[0])
	}
	if records[1]["code"] != string(codeExecFailed) {
		t.Fatalf("failure record = %v", records[1])
	}
	for _, record := range records {
		for _, value := range record {
			if text, ok := value.(string); ok && strings.Contains(text, "permission denied") {
				t.Fatalf("raw operating-system error leaked: %v", record)
			}
		}
	}
}

func TestRunStartUsesFallbackCodeForUnclassifiedErrors(t *testing.T) {
	deps := testDependencies(newRootFakeSystem(t), defaultValues())
	deps.newSystem = func() (system, error) { return nil, errorWithText("raw platform detail") }

	exitCode, records := runCommand(t, []string{"start"}, deps)

	if exitCode != 1 || len(records) != 1 || records[0]["code"] != string(codeStartFailed) {
		t.Fatalf("exit code = %d, records = %v", exitCode, records)
	}
}

func TestRunHealthcheck(t *testing.T) {
	values := startLivenessServer(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	})
	deps := testDependencies(newRootFakeSystem(t), values)
	deps.healthcheckTimeout = time.Second

	exitCode, records := runCommand(t, []string{"healthcheck"}, deps)
	if exitCode != 0 || len(records) != 0 {
		t.Fatalf("healthy: exit code = %d, records = %v", exitCode, records)
	}

	deps.lookup = environmentLookup(map[string]string{"PORT": "invalid"})
	exitCode, records = runCommand(t, []string{"healthcheck"}, deps)
	if exitCode != 1 || len(records) != 1 || records[0]["code"] != string(codeHealthcheckConfigInvalid) {
		t.Fatalf("invalid: exit code = %d, records = %v", exitCode, records)
	}
}

func TestRunHealthcheckDropsRootBeforeProbing(t *testing.T) {
	values := startLivenessServer(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	})
	sys := newRootFakeSystem(t)
	deps := testDependencies(sys, values)
	deps.healthcheckTimeout = time.Second

	exitCode, records := runCommand(t, []string{"healthcheck"}, deps)

	if exitCode != 0 || len(records) != 0 {
		t.Fatalf("exit code = %d, records = %v", exitCode, records)
	}
	want := []string{"identity", "setgroups [65532]", "setgid 65532", "setuid 65532", "identity", "setuid 0"}
	if !slices.Equal(sys.calls, want) {
		t.Fatalf("calls = %v, want %v", sys.calls, want)
	}
}

func TestRunHealthcheckRefusesUnsafeIdentity(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*fakeSystem, *dependencies)
		want    code
	}{
		{name: "identity unreadable", prepare: func(f *fakeSystem, _ *dependencies) { f.identityErr = []error{syscall.EIO} }, want: codePrivilegeVerifyFailed},
		{name: "drop fails", prepare: func(f *fakeSystem, _ *dependencies) { f.setuidErr = syscall.EPERM }, want: codePrivilegeDropFailed},
		{name: "residual root group", prepare: func(f *fakeSystem, _ *dependencies) {
			f.current = identity{realUID: RuntimeUID, effectiveUID: RuntimeUID, savedUID: RuntimeUID, groups: []int{0}}
		}, want: codeRootIdentityRefused},
		{name: "unsupported platform", prepare: func(_ *fakeSystem, deps *dependencies) {
			deps.newSystem = func() (system, error) { return nil, fail(codePlatformUnsupported) }
		}, want: codePlatformUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var probed atomic.Bool
			values := startLivenessServer(t, func(response http.ResponseWriter, _ *http.Request) {
				probed.Store(true)
				response.WriteHeader(http.StatusOK)
			})
			sys := newRootFakeSystem(t)
			deps := testDependencies(sys, values)
			deps.healthcheckTimeout = time.Second
			test.prepare(sys, &deps)

			exitCode, records := runCommand(t, []string{"healthcheck"}, deps)

			if exitCode != 1 || len(records) != 1 || records[0]["code"] != string(test.want) {
				t.Fatalf("exit code = %d, records = %v, want code %s", exitCode, records, test.want)
			}
			if probed.Load() {
				t.Fatal("probed liveness with an unsafe identity")
			}
		})
	}
}

type errorWithText string

func (e errorWithText) Error() string {
	return string(e)
}
