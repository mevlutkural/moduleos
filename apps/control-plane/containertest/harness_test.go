//go:build container

package containertest_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	imageEnv     = "MODULEOS_CONTAINER_IMAGE"
	versionEnv   = "MODULEOS_CONTAINER_VERSION"
	revisionEnv  = "MODULEOS_CONTAINER_REVISION"
	buildDateEnv = "MODULEOS_CONTAINER_BUILD_DATE"
	runIDEnv     = "MODULEOS_CONTAINER_RUN_ID"

	measureCapabilitiesEnv = "MODULEOS_CONTAINER_MEASURE_CAPABILITIES"
	runLabel               = "io.moduleos.container-test.run"

	dataRoot       = "/var/lib/moduleos"
	secretsMount   = "/run/moduleos-secrets"
	apiKeyPath     = secretsMount + "/api-key"
	socketMount    = "/run/moduleos-docker"
	socketPath     = socketMount + "/docker.sock"
	shutdownBudget = 10 * time.Second
)

var suite struct {
	image     string
	version   string
	revision  string
	buildDate string
	runID     string
	fixture   string
	apiKey    string

	mu         sync.Mutex
	containers []string
	volumes    []string
}

// TestMain refuses to run without a real Docker Engine and the image under
// test. It never mounts the host Docker socket, never touches Swarm, services,
// or networks, and removes exactly the containers and volumes it created.
func TestMain(m *testing.M) {
	os.Exit(runSuite(m))
}

func runSuite(m *testing.M) int {
	for _, required := range []struct {
		key    string
		target *string
	}{
		{imageEnv, &suite.image}, {versionEnv, &suite.version},
		{revisionEnv, &suite.revision}, {buildDateEnv, &suite.buildDate},
		{runIDEnv, &suite.runID},
	} {
		value := os.Getenv(required.key)
		if value == "" {
			fmt.Fprintf(os.Stderr, "container suite: %s is required; run scripts/check-container-image.sh\n", required.key)
			return 2
		}
		*required.target = value
	}
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(os.Stderr, "container suite: the Docker CLI is required")
		return 2
	}
	serverOS, err := dockerOutput("version", "--format", "{{.Server.Os}}/{{.Server.Arch}}")
	if err != nil || !strings.HasPrefix(serverOS, "linux/") {
		fmt.Fprintf(os.Stderr, "container suite: a Linux Docker Engine is required (%q): %v\n", serverOS, err)
		return 2
	}
	architecture := strings.TrimPrefix(serverOS, "linux/")

	suite.apiKey = randomHex(32)
	workspace, err := os.MkdirTemp("", "moduleos-container-suite-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "container suite:", err)
		return 2
	}
	defer os.RemoveAll(workspace)
	suite.fixture = filepath.Join(workspace, "fixture")
	build := exec.Command("go", "build", "-tags", "container", "-trimpath", "-o", suite.fixture, "./fixture")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+architecture)
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "container suite: build fixture: %v\n%s", err, output)
		return 2
	}

	code := m.Run()
	if err := cleanupSuite(); err != nil {
		fmt.Fprintln(os.Stderr, "container suite cleanup:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func cleanupSuite() error {
	suite.mu.Lock()
	containers := append([]string(nil), suite.containers...)
	volumes := append([]string(nil), suite.volumes...)
	suite.mu.Unlock()
	var failures []error
	for _, id := range containers {
		if _, err := dockerOutput("rm", "--force", "--volumes", id); err != nil && !strings.Contains(err.Error(), "No such container") {
			failures = append(failures, err)
		}
	}
	for _, name := range volumes {
		if _, err := dockerOutput("volume", "rm", "--force", name); err != nil {
			failures = append(failures, err)
		}
	}
	filter := "label=" + runLabel + "=" + suite.runID
	if leaked, err := dockerOutput("ps", "--all", "--quiet", "--filter", filter); err != nil || leaked != "" {
		failures = append(failures, fmt.Errorf("leaked containers %q: %v", leaked, err))
	}
	if leaked, err := dockerOutput("volume", "ls", "--quiet", "--filter", filter); err != nil || leaked != "" {
		failures = append(failures, fmt.Errorf("leaked volumes %q: %v", leaked, err))
	}
	return errors.Join(failures...)
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buffer)
}

func dockerOutput(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	output, err := dockerOutput(args...)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func runLabelArgument() string {
	return runLabel + "=" + suite.runID
}

func trackContainer(t *testing.T, id string) {
	t.Helper()
	suite.mu.Lock()
	suite.containers = append(suite.containers, id)
	suite.mu.Unlock()
	t.Cleanup(func() {
		if t.Failed() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			logs, _ := exec.CommandContext(ctx, "docker", "logs", id).CombinedOutput()
			cancel()
			if len(logs) != 0 {
				t.Logf("container %s logs:\n%s", id[:12], logs)
			}
		}
		_, _ = dockerOutput("rm", "--force", id)
	})
}

func newVolume(t *testing.T) string {
	t.Helper()
	name := "moduleos-container-test-" + suite.runID + "-" + randomHex(4)
	docker(t, "volume", "create", "--label", runLabelArgument(), name)
	suite.mu.Lock()
	suite.volumes = append(suite.volumes, name)
	suite.mu.Unlock()
	t.Cleanup(func() { _, _ = dockerOutput("volume", "rm", "--force", name) })
	return name
}

// apiKeyVolume returns a volume holding the suite's API key as a 0444 file,
// the same shape as a Docker Secret mount.
func apiKeyVolume(t *testing.T) string {
	t.Helper()
	volume := newVolume(t)
	source := filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "api-key"), []byte(suite.apiKey+"\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	id := docker(t, "create", "--label", runLabelArgument(), "--volume", volume+":"+secretsMount, suite.image)
	trackContainer(t, id)
	docker(t, "cp", source+"/.", id+":"+secretsMount)
	docker(t, "rm", id)
	return volume
}

// runFixture runs one fixture command as root inside the image under test
// and returns its standard output.
func runFixture(t *testing.T, createArgs []string, fixtureArgs ...string) string {
	t.Helper()
	id := createFixture(t, createArgs, fixtureArgs...)
	output := docker(t, "start", "--attach", id)
	if code := exitCode(t, id); code != 0 {
		t.Fatalf("fixture %v exited %d: %s", fixtureArgs, code, output)
	}
	return output
}

func createFixture(t *testing.T, createArgs []string, fixtureArgs ...string) string {
	t.Helper()
	args := append([]string{"create", "--label", runLabelArgument(), "--user", "0:0", "--entrypoint", "/fixture"}, createArgs...)
	args = append(args, suite.image)
	args = append(args, fixtureArgs...)
	id := docker(t, args...)
	trackContainer(t, id)
	docker(t, "cp", suite.fixture, id+":/fixture")
	return id
}

// startSocketServer serves a placeholder Docker socket owned by gid in a new
// volume and returns the volume name.
func startSocketServer(t *testing.T, gid int) string {
	t.Helper()
	volume := newVolume(t)
	id := createFixture(t, []string{"--volume", volume + ":" + socketMount}, "serve-socket", socketPath, fmt.Sprint(gid))
	docker(t, "start", id)
	waitFor(t, 30*time.Second, "socket fixture", func() bool {
		logs, _ := dockerOutput("logs", id)
		return strings.Contains(logs, "ready")
	})
	return volume
}

type inspection struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Kind   string `json:"kind"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
	Mode   string `json:"mode"`
}

func inspectPaths(t *testing.T, mounts []string, paths ...string) map[string]inspection {
	t.Helper()
	output := runFixture(t, mounts, append([]string{"inspect"}, paths...)...)
	results := map[string]inspection{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var item inspection
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			t.Fatalf("decode inspection %q: %v", scanner.Text(), err)
		}
		results[item.Path] = item
	}
	return results
}

type processReport struct {
	UID        []int    `json:"uid"`
	GID        []int    `json:"gid"`
	Groups     []int    `json:"groups"`
	NoNewPrivs string   `json:"no_new_privs"`
	CapEff     string   `json:"cap_eff"`
	CapPrm     string   `json:"cap_prm"`
	Command    []string `json:"command"`
	Processes  int      `json:"processes"`
}

func inspectProcess(t *testing.T, daemon string) processReport {
	t.Helper()
	output := runFixture(t, []string{"--pid", "container:" + daemon}, "proc")
	var report processReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode process report %q: %v", output, err)
	}
	return report
}

type daemonOptions struct {
	data        string
	dataMount   string
	keys        string
	socket      string
	environment []string
	extra       []string
	noKeyFile   bool
}

type daemon struct {
	id      string
	baseURL string
}

func createDaemon(t *testing.T, options daemonOptions) string {
	t.Helper()
	args := []string{
		"create", "--label", runLabelArgument(),
		"--publish", "127.0.0.1::3000",
		"--health-interval", "1s", "--health-start-period", "60s",
		"--env", "BASE_DOMAIN=moduleos.test",
		"--env", "SHUTDOWN_TIMEOUT=" + shutdownBudget.String(),
		"--env", "READINESS_TIMEOUT=5s",
	}
	if !options.noKeyFile {
		keys := options.keys
		if keys == "" {
			keys = apiKeyVolume(t)
		}
		args = append(args, "--volume", keys+":"+secretsMount+":ro", "--env", "API_KEY_FILE="+apiKeyPath)
	}
	if options.data != "" {
		mount := options.dataMount
		if mount == "" {
			mount = dataRoot
		}
		args = append(args, "--volume", options.data+":"+mount)
	}
	if options.socket != "" {
		args = append(args, "--volume", options.socket+":"+socketMount, "--env", "DOCKER_ENDPOINT=unix://"+socketPath)
	}
	for _, value := range options.environment {
		args = append(args, "--env", value)
	}
	args = append(args, options.extra...)
	args = append(args, suite.image)
	id := docker(t, args...)
	trackContainer(t, id)
	return id
}

func startDaemon(t *testing.T, options daemonOptions) daemon {
	t.Helper()
	id := createDaemon(t, options)
	docker(t, "start", id)
	waitHealthy(t, id)
	address := docker(t, "port", id, "3000/tcp")
	if index := strings.Index(address, "\n"); index >= 0 {
		address = address[:index]
	}
	return daemon{id: id, baseURL: "http://" + address}
}

func waitHealthy(t *testing.T, id string) {
	t.Helper()
	waitFor(t, 90*time.Second, "container health", func() bool {
		state := docker(t, "inspect", "--format", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}", id)
		if strings.HasPrefix(state, "exited") || strings.HasPrefix(state, "dead") {
			t.Fatalf("container exited before becoming healthy: %s\n%s", state, logsOf(t, id))
		}
		return state == "running healthy"
	})
}

func waitExited(t *testing.T, id string, timeout time.Duration) int {
	t.Helper()
	waitFor(t, timeout, "container exit", func() bool {
		return docker(t, "inspect", "--format", "{{.State.Status}}", id) == "exited"
	})
	return exitCode(t, id)
}

func exitCode(t *testing.T, id string) int {
	t.Helper()
	var code int
	if _, err := fmt.Sscan(docker(t, "inspect", "--format", "{{.State.ExitCode}}", id), &code); err != nil {
		t.Fatal(err)
	}
	return code
}

func logsOf(t *testing.T, id string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "logs", id).CombinedOutput()
	if err != nil {
		t.Fatalf("docker logs: %v", err)
	}
	return string(output)
}

func waitFor(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, description)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// logCodes returns the code attribute of every JSON log line.
func logCodes(logs string) []string {
	return logCodesAtLevel(logs, "")
}

// logCodesAtLevel returns the code attribute of JSON log lines at level, or
// of every line when level is empty.
func logCodesAtLevel(logs, level string) []string {
	var codes []string
	for _, line := range strings.Split(logs, "\n") {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if level != "" && record["level"] != level {
			continue
		}
		if value, ok := record["code"].(string); ok {
			codes = append(codes, value)
		}
	}
	return codes
}

func request(t *testing.T, method, url, body string, authenticated bool) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	httpRequest, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		httpRequest.Header.Set("Authorization", "Bearer "+suite.apiKey)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(httpRequest)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, payload
}

func createProject(t *testing.T, instance daemon, slug string) {
	t.Helper()
	status, body := request(t, http.MethodPost, instance.baseURL+"/api/v1/projects", fmt.Sprintf(`{"name":%q,"slug":%q}`, slug, slug), true)
	if status != http.StatusCreated {
		t.Fatalf("create project status = %d: %s", status, body)
	}
}

func requireProject(t *testing.T, instance daemon, slug string) {
	t.Helper()
	status, body := request(t, http.MethodGet, instance.baseURL+"/api/v1/projects/"+slug, "", true)
	if status != http.StatusOK {
		t.Fatalf("get project %s status = %d: %s", slug, status, body)
	}
}

func stopDaemon(t *testing.T, id string) {
	t.Helper()
	docker(t, "stop", "--time", fmt.Sprint(int((shutdownBudget + 10*time.Second).Seconds())), id)
	if code := exitCode(t, id); code != 0 {
		t.Fatalf("daemon exit code = %d, want 0\n%s", code, logsOf(t, id))
	}
}
