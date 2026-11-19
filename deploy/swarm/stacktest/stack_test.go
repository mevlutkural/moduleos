//go:build stack

package stacktest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const service = "moduleos-system_moduleosd"

type boundedOutput struct {
	data      []byte
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	const limit = 1 << 16
	if len(b.data)+len(p) > limit {
		b.truncated = true
	}
	remaining := limit - len(b.data)
	if remaining > len(p) {
		remaining = len(p)
	}
	b.data = append(b.data, p[:remaining]...)
	return len(p), nil
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...)...)
	command.Env = append(os.Environ(), "DOCKER_CONTEXT=", "DOCKER_HOST=")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v", args[0], err)
	}
	return strings.TrimSpace(string(out))
}

func dockerJSON(t *testing.T, kind, id string) map[string]any {
	t.Helper()
	args := []string{"inspect", "--format", "{{json .}}", id}
	if kind != "" {
		args = append([]string{kind}, args...)
	} else {
		args = append([]string{"inspect", "--type", "task"}, args[1:]...)
	}
	out := docker(t, args...)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed
}

func dockerObjectIDs(t *testing.T, kind string) string {
	t.Helper()
	ids := strings.Fields(docker(t, kind, "ls", "--quiet"))
	sort.Strings(ids)
	return strings.Join(ids, "\n")
}

func waitDockerID(t *testing.T, kind, name string, budget time.Duration) string {
	t.Helper()
	for until := time.Now().Add(budget); time.Now().Before(until); {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		out, err := exec.CommandContext(ctx, "docker", "--host", "unix:///var/run/docker.sock", kind, "inspect", "--format", "{{.ID}}", name).CombinedOutput()
		cancel()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return strings.TrimSpace(string(out))
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("owned %s did not appear within %s", kind, budget)
	return ""
}

func inspectServicePresent(ctx context.Context, dockerBinary, name string, callBudget time.Duration) (bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, callBudget)
	defer cancel()
	command := exec.CommandContext(callCtx, dockerBinary, "--host", "unix:///var/run/docker.sock", "service", "inspect", name)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	if callCtx.Err() != nil {
		return false, fmt.Errorf("service inspection timed out: %w", callCtx.Err())
	}
	if err == nil {
		if len(bytes.TrimSpace(output)) == 0 {
			return false, fmt.Errorf("service inspection returned empty output")
		}
		return true, nil
	}
	if strings.Contains(strings.ToLower(string(output)), "no such service") {
		return false, nil
	}
	return false, fmt.Errorf("service inspection failed: %w", err)
}

func waitServiceGone(ctx context.Context, dockerBinary, name string, budget time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for {
		present, err := inspectServicePresent(waitCtx, dockerBinary, name, 5*time.Second)
		if err != nil {
			return err
		}
		if !present {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("service removal deadline exceeded: %w", waitCtx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func TestWaitServiceGoneWithFakeDocker(t *testing.T) {
	for _, tc := range []struct {
		name, scenario string
		budget         time.Duration
		wantError      bool
	}{
		{"missing", "missing", 2 * time.Second, false},
		{"transition", "transition", 3 * time.Second, false},
		{"daemon error", "daemon_error", 2 * time.Second, true},
		{"hung inspect", "hung", 1200 * time.Millisecond, true},
		{"never removed", "present", 1200 * time.Millisecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, data := range map[string]string{
				"scenario": tc.scenario,
				"docker": `#!/bin/sh
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
case "$(cat "$root/scenario")" in
  missing) echo 'Error: no such service: fixture' >&2; exit 1 ;;
  daemon_error) echo 'Cannot connect to the Docker daemon' >&2; exit 1 ;;
  hung) echo $$ > "$root/pid"; exec sleep 30 ;;
  transition)
    count=$(cat "$root/count" 2>/dev/null || echo 0)
    count=$((count+1)); echo "$count" > "$root/count"
    if [ "$count" -gt 1 ]; then echo 'Error: no such service: fixture' >&2; exit 1; fi ;;
esac
echo '{"ID":"fixture"}'
`,
			} {
				mode := os.FileMode(0600)
				if name == "docker" {
					mode = 0700
				}
				if err := os.WriteFile(filepath.Join(root, name), []byte(data), mode); err != nil {
					t.Fatal(err)
				}
			}
			started := time.Now()
			err := waitServiceGone(context.Background(), filepath.Join(root, "docker"), "fixture", tc.budget)
			if (err != nil) != tc.wantError {
				t.Fatalf("removal result %v; wantError=%v", err, tc.wantError)
			}
			if elapsed := time.Since(started); elapsed > tc.budget+2*time.Second {
				t.Fatalf("removal check exceeded bounded budget: %s", elapsed)
			}
			if tc.scenario == "hung" {
				data, readErr := os.ReadFile(filepath.Join(root, "pid"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				if killErr := syscall.Kill(pid, 0); killErr != syscall.ESRCH {
					t.Fatalf("hung inspection child survived: %v", killErr)
				}
			}
		})
	}
}

func nested(t *testing.T, value any, keys ...string) any {
	t.Helper()
	for _, key := range keys {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("expected object at %s", key)
		}
		value = object[key]
	}
	return value
}

func request(t *testing.T, method, path, body, key string) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+os.Getenv("MODULEOS_STACK_PORT")+"/api/v1"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

func inspectMountedSecret(t *testing.T, containerID string) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "fixture")
	build := exec.CommandContext(t.Context(), "go", "build", "-tags=container", "-o", binary, "./apps/control-plane/containertest/fixture")
	build.Dir = filepath.Join("..", "..", "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build inspection fixture: %v: %s", err, output)
	}
	helper := docker(t, "create", "--label", "io.moduleos.stack.run="+os.Getenv("MODULEOS_STACK_RUN_ID"), "--user", "0:0", "--cap-add", "SYS_PTRACE", "--pid", "container:"+containerID,
		"--entrypoint", "/fixture", os.Getenv("MODULEOS_STACK_IMAGE"), "inspect", "/proc/1/root/run/secrets/moduleos_api_key")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		remove := exec.CommandContext(cleanupCtx, "docker", "--host", "unix:///var/run/docker.sock", "rm", "-f", helper)
		remove.WaitDelay = 2 * time.Second
		if err := remove.Run(); err != nil {
			t.Errorf("inspection helper cleanup failed: %v", err)
		}
	})
	docker(t, "cp", binary, helper+":/fixture")
	result := docker(t, "start", "--attach", helper)
	var info struct {
		Kind string `json:"kind"`
		UID  int    `json:"uid"`
		GID  int    `json:"gid"`
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal([]byte(result), &info); err != nil {
		t.Fatalf("secret metadata: %v", err)
	}
	if info.Kind != "regular" || info.UID != 65532 || info.GID != 65532 || info.Mode != "0400" {
		t.Fatalf("secret mount metadata: %+v", info)
	}
}

func buildDerivedImage(t *testing.T, tag, additional string) string {
	t.Helper()
	base := os.Getenv("MODULEOS_STACK_IMAGE")
	image := base + tag
	fixtureDir := t.TempDir()
	fixture := "FROM " + os.Getenv("MODULEOS_STACK_IMAGE") + "\nLABEL io.moduleos.stack.run=" + os.Getenv("MODULEOS_STACK_RUN_ID") + "\n" + additional + "\n"
	if err := os.WriteFile(filepath.Join(fixtureDir, "Dockerfile"), []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	buildCtx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(buildCtx, "docker", "--host", "unix:///var/run/docker.sock", "build", "--pull=false", "-t", image, fixtureDir)
	if _, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build derived test image: %v", err)
	}
	return image
}

func TestProductionStack(t *testing.T) {
	if os.Getenv("MODULEOS_STACK_DISPOSABLE") != "1" || os.Getenv("MODULEOS_STACK_RUN_ID") == "" || os.Getenv("MODULEOS_STACK_ENV_FILE") == "" {
		t.Fatal("stack suite requires the guarded disposable runner")
	}
	key, err := os.ReadFile(os.Getenv("MODULEOS_STACK_KEY_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	envFile := os.Getenv("MODULEOS_STACK_ENV_FILE")
	before := map[string]string{}
	for _, kind := range []string{"service", "network", "secret", "volume"} {
		before[kind] = dockerObjectIDs(t, kind)
	}
	original, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ key, previous, replacement, reason string }{
		{"MODULEOS_API_KEY_SECRET", os.Getenv("MODULEOS_STACK_SECRET"), "moduleos_missing_v1", "missing_secret"},
		{"MODULEOS_DATA_VOLUME", os.Getenv("MODULEOS_STACK_VOLUME"), "moduleos-missing-volume", "invalid_volume"},
		{"MODULEOS_APP_DATA_ROOT", os.Getenv("MODULEOS_STACK_APP_ROOT"), "/moduleos-missing-root", "invalid_app_root"},
		{"MODULEOS_IMAGE", os.Getenv("MODULEOS_STACK_IMAGE"), "moduleosd:latest", "invalid_input"},
		{"MODULEOS_PORT", os.Getenv("MODULEOS_STACK_PORT"), "0", "invalid_input"},
	} {
		bad := bytes.Replace(original, []byte(test.key+"="+test.previous), []byte(test.key+"="+test.replacement), 1)
		if bytes.Equal(bad, original) {
			t.Fatalf("negative fixture did not change %s", test.key)
		}
		badFile := filepath.Join(t.TempDir(), "invalid.env")
		if err := os.WriteFile(badFile, bad, 0600); err != nil {
			t.Fatal(err)
		}
		negative := exec.CommandContext(t.Context(), "bash", "../preflight.sh", "--env-file", badFile)
		if output, err := negative.CombinedOutput(); err == nil || !bytes.Contains(output, []byte(test.reason)) {
			t.Fatalf("%s preflight returned %v with wrong category", test.key, err)
		}
		for kind, expected := range before {
			if after := dockerObjectIDs(t, kind); after != expected {
				t.Fatalf("%s preflight changed %s objects", test.key, kind)
			}
		}
	}
	command := exec.CommandContext(t.Context(), "bash", "../deploy.sh", "--env-file", envFile)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	if bytes.Contains(output, key) {
		t.Fatal("credential in deploy output")
	}
	serviceID := docker(t, "service", "inspect", "--format", "{{.ID}}", service)
	if err := os.WriteFile(os.Getenv("MODULEOS_STACK_SERVICE_ID_FILE"), []byte(serviceID), 0600); err != nil {
		t.Fatal(err)
	}
	defaultNetworkID := waitDockerID(t, "network", "moduleos-system_default", 30*time.Second)
	if err := os.WriteFile(os.Getenv("MODULEOS_STACK_DEFAULT_NETWORK_ID_FILE"), []byte(defaultNetworkID), 0600); err != nil {
		t.Fatal(err)
	}
	spec := dockerJSON(t, "service", service)
	if replicas := nested(t, spec, "Spec", "Mode", "Replicated", "Replicas"); replicas != float64(1) {
		t.Fatalf("replicas = %v", replicas)
	}
	if readOnly := nested(t, spec, "Spec", "TaskTemplate", "ContainerSpec", "ReadOnly"); readOnly != true {
		t.Fatalf("read-only = %v", readOnly)
	}
	constraints := nested(t, spec, "Spec", "TaskTemplate", "Placement", "Constraints").([]any)
	if len(constraints) != 1 || constraints[0] != "node.role == manager" {
		t.Fatalf("manager placement = %v", constraints)
	}
	if grace := nested(t, spec, "Spec", "TaskTemplate", "ContainerSpec", "StopGracePeriod"); grace == nil {
		t.Fatal("missing stop grace")
	}
	if grace := nested(t, spec, "Spec", "TaskTemplate", "ContainerSpec", "StopGracePeriod").(float64); grace <= float64(15*time.Second) {
		t.Fatalf("stop grace = %v, want greater than 15s", grace)
	}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(specBytes, key) {
		t.Fatal("credential in service spec")
	}
	updateConfig := nested(t, spec, "Spec", "UpdateConfig")
	if nested(t, updateConfig, "FailureAction") != "pause" || nested(t, updateConfig, "Order") != "stop-first" || nested(t, spec, "Spec", "RollbackConfig", "FailureAction") != "pause" {
		t.Fatal("update/rollback pause contract missing")
	}
	if nested(t, spec, "Spec", "TaskTemplate", "RestartPolicy", "Condition") != "any" {
		t.Fatal("restart contract missing")
	}
	if status, _ := request(t, http.MethodGet, "/live", "", ""); status != 200 {
		t.Fatalf("live = %d", status)
	}
	if status, _ := request(t, http.MethodGet, "/ready", "", ""); status != 200 {
		t.Fatalf("ready = %d", status)
	}
	if status, _ := request(t, http.MethodGet, "/projects", "", ""); status < 400 {
		t.Fatalf("unauthenticated projects = %d", status)
	}
	slug := "smoke-" + os.Getenv("MODULEOS_STACK_RUN_ID")
	if status, _ := request(t, http.MethodPost, "/projects", fmt.Sprintf(`{"name":%q,"slug":%q}`, slug, slug), string(key)); status != 201 {
		t.Fatalf("create project = %d", status)
	}
	if status, _ := request(t, http.MethodGet, "/projects/"+slug, "", string(key)); status != 200 {
		t.Fatalf("read project = %d", status)
	}
	rootNetworkID := waitDockerID(t, "network", "moduleos-root-net", 90*time.Second)
	if err := os.WriteFile(os.Getenv("MODULEOS_STACK_ROOT_NETWORK_ID_FILE"), []byte(rootNetworkID), 0600); err != nil {
		t.Fatal(err)
	}
	projectNetworkID := waitDockerID(t, "network", "moduleos-"+slug+"-net", 90*time.Second)
	if err := os.WriteFile(os.Getenv("MODULEOS_STACK_APP_NETWORK_ID_FILE"), []byte(projectNetworkID), 0600); err != nil {
		t.Fatal(err)
	}
	// Inspect the host PID, not image USER metadata, for the actual daemon identity.
	task := docker(t, "service", "ps", "--no-trunc", "--filter", "desired-state=running", "--format", "{{.ID}}", service)
	task = strings.Split(task, "\n")[0]
	taskJSON := dockerJSON(t, "", task)
	containerID := nested(t, taskJSON, "Status", "ContainerStatus", "ContainerID")
	container := dockerJSON(t, "container", fmt.Sprint(containerID))
	containerBytes, err := json.Marshal(container)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(containerBytes, key) {
		t.Fatal("credential in container metadata")
	}
	if nested(t, container, "HostConfig", "ReadonlyRootfs") != true {
		t.Fatal("root filesystem is writable")
	}
	mounts, ok := container["Mounts"].([]any)
	if !ok {
		t.Fatal("container mounts unavailable")
	}
	wantMounts := map[string]bool{"/var/lib/moduleos": true, "/var/run/docker.sock": false, os.Getenv("MODULEOS_STACK_APP_ROOT"): false}
	for _, item := range mounts {
		mount := item.(map[string]any)
		target, _ := mount["Destination"].(string)
		if writable, wanted := wantMounts[target]; wanted {
			if mount["RW"] != writable {
				t.Fatalf("mount %s writable = %v", target, mount["RW"])
			}
			delete(wantMounts, target)
		}
	}
	if len(wantMounts) != 0 {
		t.Fatalf("missing required mounts: %v", wantMounts)
	}
	logConfig := nested(t, container, "HostConfig", "LogConfig")
	if nested(t, logConfig, "Type") != "json-file" || nested(t, logConfig, "Config", "max-size") != "10m" || nested(t, logConfig, "Config", "max-file") != "5" {
		t.Fatal("log rotation contract missing")
	}
	healthTest := nested(t, container, "Config", "Healthcheck", "Test")
	if fmt.Sprint(healthTest) != "[CMD /usr/local/bin/moduleosd-container healthcheck]" {
		t.Fatalf("image healthcheck replaced: %v", healthTest)
	}
	imageID := docker(t, "image", "inspect", "--format", "{{.Id}}", os.Getenv("MODULEOS_STACK_IMAGE"))
	if nested(t, container, "Image") != imageID {
		t.Fatal("running image differs from trusted local image")
	}
	inspectMountedSecret(t, fmt.Sprint(containerID))
	pidValue := nested(t, container, "State", "Pid")
	pid := int(pidValue.(float64))
	statusBytes, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	statusText := string(statusBytes)
	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Split(string(cmdline), "\x00")[0] != "moduleosd" {
		t.Fatal("daemon argv[0] differs from the entrypoint contract")
	}
	executable, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil || executable != "/usr/local/bin/moduleosd" {
		t.Fatalf("daemon executable is not PID 1: %q, %v", executable, err)
	}
	for _, line := range []string{"Uid:\t65532\t65532\t65532\t65532", "CapEff:\t0000000000000000", "CapPrm:\t0000000000000000", "NoNewPrivs:\t1"} {
		if !strings.Contains(statusText, line) {
			t.Errorf("process status missing %q", line)
		}
	}
	socketInfo, err := os.Stat("/var/run/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	socketGID := socketInfo.Sys().(*syscall.Stat_t).Gid
	groupPresent := false
	for _, line := range strings.Split(statusText, "\n") {
		if strings.HasPrefix(line, "Groups:") {
			for _, field := range strings.Fields(strings.TrimPrefix(line, "Groups:")) {
				if field == strconv.FormatUint(uint64(socketGID), 10) {
					groupPresent = true
				}
			}
		}
	}
	if !groupPresent {
		t.Fatal("daemon process lacks supplementary Docker socket group")
	}
	updatedImage := buildDerivedImage(t, "-updated", "LABEL io.moduleos.stack.revision=updated")
	volumeBefore := dockerJSON(t, "volume", os.Getenv("MODULEOS_STACK_VOLUME"))
	updatedEnv := bytes.Replace(original, []byte("MODULEOS_IMAGE="+os.Getenv("MODULEOS_STACK_IMAGE")), []byte("MODULEOS_IMAGE="+updatedImage), 1)
	updatedEnvFile := filepath.Join(t.TempDir(), "updated.env")
	if err := os.WriteFile(updatedEnvFile, updatedEnv, 0600); err != nil {
		t.Fatal(err)
	}
	updateCommand := exec.CommandContext(t.Context(), "bash", "../deploy.sh", "--env-file", updatedEnvFile)
	if _, err := updateCommand.CombinedOutput(); err != nil {
		t.Fatalf("known-good service replacement failed: %v", err)
	}
	if status, _ := request(t, http.MethodGet, "/projects/"+slug, "", string(key)); status != 200 {
		t.Fatalf("project lost after service replacement: %d", status)
	}
	if updatedID := docker(t, "image", "inspect", "--format", "{{.Id}}", updatedImage); updatedID == imageID {
		t.Fatal("replacement image did not change identity")
	}
	newTask := docker(t, "service", "ps", "--no-trunc", "--filter", "desired-state=running", "--format", "{{.ID}}", service)
	newTask = strings.Split(newTask, "\n")[0]
	newTaskJSON := dockerJSON(t, "", newTask)
	containerID = nested(t, newTaskJSON, "Status", "ContainerStatus", "ContainerID")
	stopStart := strconv.FormatInt(time.Now().Add(-time.Second).Unix(), 10)
	eventCtx, eventCancel := context.WithTimeout(t.Context(), 40*time.Second)
	eventCommand := exec.CommandContext(eventCtx, "docker", "--host", "unix:///var/run/docker.sock", "events",
		"--since", stopStart, "--until", strconv.FormatInt(time.Now().Add(35*time.Second).Unix(), 10),
		"--filter", "container="+fmt.Sprint(containerID), "--format", "{{json .}}")
	var eventOutput boundedOutput
	eventCommand.Stdout = &eventOutput
	eventCommand.Stderr = io.Discard
	if err := eventCommand.Start(); err != nil {
		t.Fatal("could not start bounded stop-event reader")
	}
	eventWaited := false
	defer func() {
		eventCancel()
		if !eventWaited {
			_ = eventCommand.Wait()
		}
	}()
	logCtx, logCancel := context.WithTimeout(t.Context(), 40*time.Second)
	logCommand := exec.CommandContext(logCtx, "docker", "--host", "unix:///var/run/docker.sock", "service", "logs", "--follow", "--raw", "--tail", "0", service)
	var logOutput boundedOutput
	logCommand.Stdout = &logOutput
	logCommand.Stderr = io.Discard
	if err := logCommand.Start(); err != nil {
		t.Fatal("could not start bounded stop-log reader")
	}
	logWaited := false
	defer func() {
		logCancel()
		if !logWaited {
			_ = logCommand.Wait()
		}
	}()
	time.Sleep(250 * time.Millisecond)
	docker(t, "stack", "rm", "moduleos-system")
	err = eventCommand.Wait()
	eventWaited = true
	if err != nil {
		t.Fatal("bounded stop-event collection failed")
	}
	logCancel()
	_ = logCommand.Wait()
	logWaited = true
	if eventOutput.truncated || logOutput.truncated {
		t.Fatal("stop evidence exceeded bounded reader")
	}
	if !bytes.Contains(logOutput.data, []byte("daemon stopped")) {
		t.Fatal("daemon-stopped log evidence missing")
	}
	stopEvents := string(eventOutput.data)
	var termination, exit float64
	for _, line := range strings.Split(stopEvents, "\n") {
		var event struct {
			Action   string  `json:"Action"`
			TimeNano float64 `json:"timeNano"`
			Actor    struct {
				Attributes map[string]string `json:"Attributes"`
			} `json:"Actor"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event.Action == "kill" && (event.Actor.Attributes["signal"] == "15" || event.Actor.Attributes["signal"] == "SIGTERM") {
			termination = event.TimeNano
		}
		if event.Action == "die" && event.Actor.Attributes["exitCode"] == "0" {
			exit = event.TimeNano
		}
		if event.Action == "oom" || event.Action == "kill" && (event.Actor.Attributes["signal"] == "9" || event.Actor.Attributes["signal"] == "SIGKILL") {
			t.Fatal("stop required OOM or SIGKILL")
		}
	}
	if termination == 0 || exit < termination || exit-termination > float64(17*time.Second) {
		t.Fatal("graceful SIGTERM-to-exit evidence missing or outside shutdown budget")
	}
	if err := waitServiceGone(t.Context(), "docker", service, 30*time.Second); err != nil {
		t.Fatalf("stack service removal unverified: %v", err)
	}
	if err := os.Remove(os.Getenv("MODULEOS_STACK_SERVICE_ID_FILE")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(os.Getenv("MODULEOS_STACK_DEFAULT_NETWORK_ID_FILE")); err != nil {
		t.Fatal(err)
	}
	// Externally owned objects must survive stack removal.
	volumeAfter := dockerJSON(t, "volume", os.Getenv("MODULEOS_STACK_VOLUME"))
	for _, field := range []string{"Name", "Driver", "Mountpoint", "CreatedAt"} {
		if volumeAfter[field] != volumeBefore[field] {
			t.Fatalf("external volume %s changed", field)
		}
	}
	docker(t, "secret", "inspect", os.Getenv("MODULEOS_STACK_SECRET"))
	command = exec.CommandContext(t.Context(), "bash", "../deploy.sh", "--env-file", envFile)
	if _, err := command.CombinedOutput(); err != nil {
		t.Fatalf("redeploy failed: %v", err)
	}
	serviceID = docker(t, "service", "inspect", "--format", "{{.ID}}", service)
	if err := os.WriteFile(os.Getenv("MODULEOS_STACK_SERVICE_ID_FILE"), []byte(serviceID), 0600); err != nil {
		t.Fatal(err)
	}
	defaultNetworkID = waitDockerID(t, "network", "moduleos-system_default", 30*time.Second)
	if err := os.WriteFile(os.Getenv("MODULEOS_STACK_DEFAULT_NETWORK_ID_FILE"), []byte(defaultNetworkID), 0600); err != nil {
		t.Fatal(err)
	}
	if status, _ := request(t, http.MethodGet, "/projects/"+slug, "", string(key)); status != 200 {
		t.Fatalf("project lost after redeploy = %d", status)
	}
	// The public application boundary must accept a child of the canonical root
	// and reject a sibling outside it. These assertions concern durable API
	// validation; the later operational phase tests full app rollout.
	child := filepath.Join(os.Getenv("MODULEOS_STACK_APP_ROOT"), "accepted")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	appName := "app" + os.Getenv("MODULEOS_STACK_RUN_ID")
	valid := fmt.Sprintf(`{"name":%q,"project_slug":%q,"image":%q,"volumes":[{"source":%q,"target":"/data"}]}`, appName, slug, os.Getenv("MODULEOS_STACK_IMAGE"), child)
	if status, _ := request(t, http.MethodPost, "/apps", valid, string(key)); status != 201 {
		t.Fatalf("valid mount rejected: %d", status)
	}
	invalid := fmt.Sprintf(`{"name":%q,"image":%q,"volumes":[{"source":"/etc","target":"/data"}]}`, appName+"bad", os.Getenv("MODULEOS_STACK_IMAGE"))
	if status, _ := request(t, http.MethodPost, "/apps", invalid, string(key)); status < 400 {
		t.Fatalf("protected mount accepted: %d", status)
	}
	escape := filepath.Join(os.Getenv("MODULEOS_STACK_APP_ROOT"), "escape")
	if err := os.Symlink("/etc", escape); err != nil {
		t.Fatal(err)
	}
	symlinkBody := fmt.Sprintf(`{"name":%q,"image":%q,"volumes":[{"source":%q,"target":"/data"}]}`, appName+"escape", os.Getenv("MODULEOS_STACK_IMAGE"), escape)
	if status, _ := request(t, http.MethodPost, "/apps", symlinkBody, string(key)); status < 400 {
		t.Fatalf("symlink escape accepted: %d", status)
	}
	// A deliberately broken test-only healthcheck must pause an update; it
	// must never cause an automatic rollback to the older daemon image.
	badImage := buildDerivedImage(t, "-unhealthy", "HEALTHCHECK --interval=1s --timeout=1s --start-period=0s --retries=1 CMD [\"/nonexistent\"]")
	badID := docker(t, "image", "inspect", "--format", "{{.Id}}", badImage)
	docker(t, "service", "update", "--detach=true", "--no-resolve-image", "--image", badID, service)
	paused := false
	for until := time.Now().Add(90 * time.Second); time.Now().Before(until); {
		updated := dockerJSON(t, "service", service)
		updateStatus, _ := updated["UpdateStatus"].(map[string]any)
		if updateStatus["State"] == "paused" {
			paused = true
			break
		}
		time.Sleep(time.Second)
	}
	if !paused {
		t.Fatal("failed health update did not pause")
	}
	pausedSpec := dockerJSON(t, "service", service)
	if nested(t, pausedSpec, "Spec", "TaskTemplate", "ContainerSpec", "Image") != badID {
		t.Fatal("paused service rolled back to another image")
	}
	for _, taskLine := range strings.Split(docker(t, "service", "ps", "--no-trunc", "--filter", "desired-state=running", "--format", "{{json .}}", service), "\n") {
		if taskLine == "" {
			continue
		}
		var task struct{ Image, CurrentState string }
		if err := json.Unmarshal([]byte(taskLine), &task); err != nil {
			t.Fatal("invalid paused-task evidence")
		}
		if task.Image != badID && strings.HasPrefix(task.CurrentState, "Running") {
			t.Fatal("older image restarted after failed update")
		}
	}
}
