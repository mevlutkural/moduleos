//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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
	processStartupDeadline = 8 * time.Second
	processRequestTimeout  = 2 * time.Second
	processExitDeadline    = 5 * time.Second
	processCleanupDeadline = 2 * time.Second
	testAPIKey             = "phase7d-test-key-0000000000000000"
)

var processBinary string

func TestMain(m *testing.M) {
	root, err := findRepositoryRoot()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	buildDir, err := os.MkdirTemp("", "moduleosd-process-test-*")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	processBinary = filepath.Join(buildDir, "moduleosd")
	command := exec.Command("go", "build", "-trimpath", "-o", processBinary, "./apps/control-plane/cmd/moduleosd")
	command.Dir = root
	if output, buildErr := command.CombinedOutput(); buildErr != nil {
		_ = os.RemoveAll(buildDir)
		_, _ = fmt.Fprintf(os.Stderr, "build moduleosd: %v\n%s", buildErr, boundedText(output))
		os.Exit(1)
	}
	status := m.Run()
	if cleanupErr := os.RemoveAll(buildDir); cleanupErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "remove moduleosd test build directory: %v\n", cleanupErr)
		if status == 0 {
			status = 1
		}
	}
	os.Exit(status)
}

func findRepositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("repository root containing go.mod was not found")
		}
		directory = parent
	}
}

type processResult struct {
	err  error
	code int
}

type processHarness struct {
	t           *testing.T
	directory   string
	database    string
	dockerSock  string
	dataRoot    string
	apiKeyFile  string
	address     string
	port        string
	stdoutPath  string
	stderrPath  string
	environment map[string]string

	command      *exec.Cmd
	done         chan struct{}
	result       processResult
	mu           sync.Mutex
	client       *http.Client
	streamClient *http.Client
}

func newProcessHarness(t *testing.T) *processHarness {
	t.Helper()
	// Keep Unix socket paths below Darwin's sockaddr_un limit while retaining a
	// unique, test-owned directory on both local macOS and Ubuntu CI hosts.
	directory, err := os.MkdirTemp("/tmp", "mosd-*")
	if err != nil {
		t.Fatalf("create process directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	dataRoot := filepath.Join(directory, "data")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatalf("create data root: %v", err)
	}
	// The database directory must stay separate from every allowed mount root.
	stateDirectory := filepath.Join(directory, "state")
	if err := os.Mkdir(stateDirectory, 0o700); err != nil {
		t.Fatalf("create database directory: %v", err)
	}
	apiKeyFile := filepath.Join(directory, "api-key")
	if err := os.WriteFile(apiKeyFile, []byte(testAPIKey), 0o600); err != nil {
		t.Fatalf("create API key file: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve process port: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release process port reservation: %v", err)
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split process address: %v", err)
	}
	harness := &processHarness{
		t: t, directory: directory,
		database:   filepath.Join(stateDirectory, "moduleos.db"),
		dockerSock: filepath.Join(directory, "docker.sock"),
		dataRoot:   dataRoot, apiKeyFile: apiKeyFile, address: address, port: port,
		stdoutPath: filepath.Join(directory, "stdout.log"),
		stderrPath: filepath.Join(directory, "stderr.log"),
		environment: map[string]string{
			"ENV": "production", "LOG_LEVEL": "debug", "LISTEN_ADDRESS": "127.0.0.1", "PORT": port,
			"DOCKER_ENDPOINT": "unix://" + filepath.Join(directory, "docker.sock"),
			"BASE_DOMAIN":     "moduleos.test", "DATABASE_PATH": filepath.Join(stateDirectory, "moduleos.db"),
			"API_KEY_FILE": apiKeyFile, "ALLOWED_MOUNT_ROOTS": dataRoot,
			"RECONCILE_INTERVAL": "1h", "RECONCILE_MAX_BACKOFF": "1s", "RECONCILE_WORKERS": "1",
			"STABILIZATION_WINDOW": "0s", "SHUTDOWN_TIMEOUT": "2s", "READINESS_TIMEOUT": "500ms",
			"HTTP_READ_TIMEOUT": "3s", "HTTP_WRITE_TIMEOUT": "5s", "HTTP_IDLE_TIMEOUT": "5s",
			"SSE_HEARTBEAT_INTERVAL": "100ms", "SSE_IDLE_TIMEOUT": "2s", "SSE_MAX_DURATION": "5s",
		},
		client:       &http.Client{Timeout: processRequestTimeout, Transport: &http.Transport{DisableKeepAlives: false}},
		streamClient: &http.Client{Transport: &http.Transport{DisableKeepAlives: false}},
	}
	t.Cleanup(harness.cleanup)
	return harness
}

func (h *processHarness) set(key, value string) { h.environment[key] = value }

func (h *processHarness) unset(key string) { delete(h.environment, key) }

func (h *processHarness) start() {
	h.t.Helper()
	if h.command != nil {
		h.t.Fatal("process already started")
	}
	stdout, err := os.Create(h.stdoutPath)
	if err != nil {
		h.t.Fatalf("create stdout capture: %v", err)
	}
	stderr, err := os.Create(h.stderrPath)
	if err != nil {
		_ = stdout.Close()
		h.t.Fatalf("create stderr capture: %v", err)
	}
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.directory, "TMPDIR=" + h.directory}
	keys := make([]string, 0, len(h.environment))
	for key := range h.environment {
		keys = append(keys, key)
	}
	slicesSort(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+h.environment[key])
	}
	command := exec.Command(processBinary)
	command.Dir = h.directory
	command.Env = environment
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		h.t.Fatalf("start moduleosd: %v", err)
	}
	h.command = command
	h.done = make(chan struct{})
	go func() {
		err := command.Wait()
		_ = stdout.Close()
		_ = stderr.Close()
		result := processResult{err: err}
		if err != nil {
			var exitError *exec.ExitError
			if errors.As(err, &exitError) {
				result.code = exitError.ExitCode()
			} else {
				result.code = -1
			}
		}
		h.mu.Lock()
		h.result = result
		h.mu.Unlock()
		close(h.done)
	}()
}

func slicesSort(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}

func (h *processHarness) wait(timeout time.Duration) (processResult, bool) {
	h.t.Helper()
	if h.done == nil {
		h.t.Fatal("process not started")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-h.done:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.result, true
	case <-timer.C:
		return processResult{}, false
	}
}

func (h *processHarness) signal(signal os.Signal) {
	h.t.Helper()
	if h.command == nil || h.command.Process == nil {
		h.t.Fatal("process not started")
	}
	if err := h.command.Process.Signal(signal); err != nil {
		h.t.Fatalf("signal process: %v", err)
	}
}

func (h *processHarness) assertRunning() {
	h.t.Helper()
	select {
	case <-h.done:
		output := h.output()
		h.t.Fatalf("process exited unexpectedly: %s", output)
	default:
	}
}

func (h *processHarness) cleanup() {
	if transport, ok := h.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	if transport, ok := h.streamClient.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	if h.command == nil || h.done == nil {
		return
	}
	select {
	case <-h.done:
		return
	default:
	}
	_ = h.command.Process.Kill()
	timer := time.NewTimer(processCleanupDeadline)
	defer timer.Stop()
	select {
	case <-h.done:
	case <-timer.C:
		h.t.Errorf("moduleosd was not reaped after fallback kill")
	}
}

func (h *processHarness) output() string {
	stdout, _ := os.ReadFile(h.stdoutPath)
	stderr, _ := os.ReadFile(h.stderrPath)
	return boundedText(append(append([]byte("stdout:\n"), stdout...), append([]byte("\nstderr:\n"), stderr...)...))
}

func boundedText(value []byte) string {
	const maximum = 16 * 1024
	if len(value) > maximum {
		value = value[len(value)-maximum:]
	}
	return string(value)
}

func (h *processHarness) url(path string) string { return "http://" + h.address + path }

func (h *processHarness) request(ctx context.Context, path string, authenticated bool) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url(path), nil)
	if err != nil {
		return nil, err
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+testAPIKey)
	}
	return h.client.Do(request)
}

func (h *processHarness) waitForStatus(path string, status int, target any) *http.Response {
	h.t.Helper()
	deadline := time.Now().Add(processStartupDeadline)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), processRequestTimeout)
		response, err := h.request(ctx, path, false)
		if err == nil {
			if response.StatusCode == status {
				if target != nil {
					if decodeErr := json.NewDecoder(response.Body).Decode(target); decodeErr != nil {
						_ = response.Body.Close()
						cancel()
						h.t.Fatalf("decode %s response: %v", path, decodeErr)
					}
				}
				_ = response.Body.Close()
				cancel()
				return response
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		cancel()
		if time.Now().After(deadline) {
			h.t.Fatalf("wait for %s status %d: %v\n%s", path, status, err, h.output())
		}
		timer := time.NewTimer(20 * time.Millisecond)
		<-timer.C
	}
}

func (h *processHarness) assertListenerReleased() {
	h.t.Helper()
	listener, err := net.Listen("tcp", h.address)
	if err != nil {
		h.t.Fatalf("listener was not released: %v", err)
	}
	_ = listener.Close()
}

func (h *processHarness) pid() int {
	if h.command == nil || h.command.Process == nil {
		return 0
	}
	return h.command.Process.Pid
}

func exitCode(result processResult) int {
	if result.err == nil {
		return 0
	}
	return result.code
}

func holdAddress(t *testing.T, address string) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("hold address: %v", err)
	}
	return listener
}

func requireExitCode(t *testing.T, harness *processHarness, want int) {
	t.Helper()
	result, completed := harness.wait(processExitDeadline)
	if !completed {
		t.Fatalf("process did not exit within deadline\n%s", harness.output())
	}
	if got := exitCode(result); got != want {
		t.Fatalf("exit code = %d, want %d (error %v)\n%s", got, want, result.err, harness.output())
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
