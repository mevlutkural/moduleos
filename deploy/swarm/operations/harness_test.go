//go:build operations

package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const controlService = "moduleos-system_moduleosd"
const registryImage = "registry:3.0.0@sha256:6c5666b861f3505b116bb9aa9b25175e71210414bd010d92035ff64018f9457e"
const traefikImage = "traefik:v3.7.19@sha256:a6c0194dda77bcb804824580f36fa7edc56e315b1acfe933366c8b52844a78a4"

type application struct {
	ID                 string                     `json:"id"`
	Name               string                     `json:"name"`
	ProjectID          string                     `json:"project_id"`
	Image              string                     `json:"image"`
	ObservedImage      string                     `json:"observed_image"`
	Replicas           int                        `json:"replicas"`
	Generation         int64                      `json:"desired_generation"`
	ObservedGeneration int64                      `json:"observed_generation"`
	State              string                     `json:"observed_state"`
	RunState           string                     `json:"desired_run_state"`
	Code               string                     `json:"reconcile_error_code"`
	Expose             bool                       `json:"expose"`
	Ingress            int                        `json:"ingress_container_port"`
	Environment        map[string]json.RawMessage `json:"env_vars"`
	Ports              []json.RawMessage          `json:"ports"`
	Volumes            []json.RawMessage          `json:"volumes"`
}
type deployment struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Image       string     `json:"image"`
	Resolved    string     `json:"resolved_image"`
	Generation  int64      `json:"target_generation"`
	Rollback    *string    `json:"rollback_source_deployment_id"`
	Failure     string     `json:"failure_code"`
	Created     time.Time  `json:"created_at"`
	Started     *time.Time `json:"started_at"`
	Finished    *time.Time `json:"finished_at"`
	SourceType  string     `json:"source_type"`
	TriggeredBy string     `json:"triggered_by"`
}
type accepted struct {
	ID         string `json:"deployment_id"`
	Generation int64  `json:"target_generation"`
}
type serviceRecord struct {
	ID   string `json:"ID"`
	Spec struct {
		Name         string
		Labels       map[string]string
		Mode         struct{ Replicated struct{ Replicas int } }
		TaskTemplate struct {
			ContainerSpec struct {
				Image  string
				Env    []string
				Labels map[string]string
			}
		}
	}
	Endpoint struct{ Ports []struct{ PublishedPort int } }
}
type taskRecord struct {
	ID     string `json:"ID"`
	Status struct {
		State           string
		ContainerStatus struct{ ContainerID string }
	}
	DesiredState string
}
type containerRecord struct {
	ID    string `json:"Id"`
	Image string
	State struct {
		Running   bool
		StartedAt string
		Pid       int
	}
}
type processIdentity struct {
	Task, Container, Started string
	PID                      int
}
type journey struct {
	t                                                                                          *testing.T
	ctx                                                                                        context.Context
	run, source, image, envFile, work, key, base, project, name, ingress, ledgerFile, evidence string
	api                                                                                        *http.Client
	ledger                                                                                     Ledger
	port, proxyPort                                                                            int
	images                                                                                     map[string]string
	imageIDs                                                                                   map[string]string
	canaries                                                                                   map[string][]byte
	replayTag, replayKey                                                                       string
	replayExpected                                                                             accepted
	usedPorts                                                                                  map[int]bool
	failureReason                                                                              string
	expectedHTTP, observedHTTP                                                                 int
}

func newJourney(t *testing.T) *journey {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || os.Geteuid() != 0 {
		t.Fatal("native disposable Linux amd64 root required")
	}
	if os.Getenv("MODULEOS_STACK_OPERATIONS") != "1" || os.Getenv("MODULEOS_STACK_DISPOSABLE") != "1" {
		t.Fatal("guarded operational runner required")
	}
	run := os.Getenv("MODULEOS_STACK_RUN_ID")
	if !identity.MatchString(run) || run == "pending" {
		t.Fatal("invalid operational run")
	}
	signalCtx, stop := signal.NotifyContext(t.Context(), syscall.SIGINT, syscall.SIGTERM)
	t.Cleanup(stop)
	ctx, cancel := context.WithTimeout(signalCtx, 22*time.Minute)
	t.Cleanup(cancel)
	j := &journey{t: t, ctx: ctx, run: run, source: os.Getenv("MODULEOS_OPERATIONS_SOURCE"), image: os.Getenv("MODULEOS_OPERATIONS_IMAGE_ID"), envFile: os.Getenv("MODULEOS_STACK_ENV_FILE"), work: os.Getenv("MODULEOS_OPERATIONS_WORK"), ledgerFile: os.Getenv("MODULEOS_OPERATIONS_LEDGER"), evidence: os.Getenv("MODULEOS_OPERATIONS_EVIDENCE"), name: "app" + run, project: "smoke-" + run, images: map[string]string{}, imageIDs: map[string]string{}, canaries: map[string][]byte{}}
	if !revision.MatchString(j.source) || !artifact.MatchString(j.image) || j.work == "" || j.ledgerFile == "" || j.evidence == "" {
		t.Fatal("operational identity missing")
	}
	key, err := os.ReadFile(os.Getenv("MODULEOS_STACK_KEY_FILE"))
	j.require(err == nil, "setup_failed")
	j.key = string(key)
	j.base = "http://127.0.0.1:" + os.Getenv("MODULEOS_STACK_PORT") + "/api/v1"
	j.port = j.freePort()
	j.proxyPort = j.freePort()
	j.ingress = "ops-" + run + "-ingress"
	j.api = &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 8, IdleConnTimeout: time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(j.api.CloseIdleConnections)
	j.ledger = Ledger{Run: run}
	j.require(SaveLedger(j.ledgerFile, j.ledger) == nil, "infrastructure")
	var engine struct {
		ID, OSType, Architecture, OperatingSystem string
		SecurityOptions                           []string
		Swarm                                     struct {
			LocalNodeState   string
			ControlAvailable bool
			Nodes            int
		}
	}
	j.jsonDocker(&engine, "info", "--format", "{{json .}}")
	guard, err := os.ReadFile(os.Getenv("MODULEOS_OPERATIONS_GUARD"))
	j.require(err == nil && string(guard) == engine.ID+" "+run, "guarded_engine_identity")
	j.require(engine.OSType == "linux" && engine.Architecture == "x86_64" && engine.Swarm.ControlAvailable && engine.Swarm.Nodes == 1 && !strings.Contains(engine.OperatingSystem, "OrbStack") && !strings.Contains(engine.OperatingSystem, "Docker Desktop"), "engine_setup")
	for _, option := range engine.SecurityOptions {
		j.require(!strings.Contains(option, "rootless") && !strings.Contains(option, "userns"), "engine_setup")
	}
	j.require(len(j.docker("service", "ls", "--quiet")) == 0 && len(j.docker("container", "ls", "--all", "--quiet")) == 0, "empty_engine_setup")
	return j
}
func (j *journey) require(ok bool, reason string) {
	j.t.Helper()
	if !ok {
		j.failureReason = "product_mismatch"
		if strings.Contains(reason, "timeout") {
			j.failureReason = "timeout"
		} else if strings.Contains(reason, "setup") || strings.Contains(reason, "infrastructure") || strings.Contains(reason, "fixture") || strings.Contains(reason, "ledger") || strings.Contains(reason, "evidence") {
			j.failureReason = "infrastructure"
		}
		if j.ctx.Err() == context.Canceled {
			j.failureReason = "cancelled"
		}
		j.t.Fatal(reason)
	}
}
func (j *journey) stage(name string, fn func()) {
	j.failureReason = "none"
	j.expectedHTTP = 0
	j.observedHTTP = 0
	start := time.Now()
	j.require(AppendEvent(j.evidence, Event{Run: j.run, Arch: "amd64", Source: j.source, Image: j.image, Stage: name, Status: "started", Reason: "none", ElapsedMS: time.Since(start).Milliseconds()}) == nil, "infrastructure")
	completed := false
	defer func() {
		status, reason := "passed", "none"
		if !completed {
			status, reason = "failed", "product_mismatch"
			if j.failureReason != "none" {
				reason = j.failureReason
			}
		}
		if AppendEvent(j.evidence, Event{Run: j.run, Arch: "amd64", Source: j.source, Image: j.image, Stage: name, Status: status, Reason: reason, ElapsedMS: time.Since(start).Milliseconds(), ExpectedHTTP: j.expectedHTTP, ObservedHTTP: j.observedHTTP}) != nil {
			j.t.Error("evidence_write")
		}
	}()
	fn()
	completed = true
}
func (j *journey) freePort() int {
	if j.usedPorts == nil {
		j.usedPorts = map[int]bool{}
		management, _ := strconv.Atoi(os.Getenv("MODULEOS_STACK_PORT"))
		j.usedPorts[management] = true
	}
	for i := 0; i < 50; i++ {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		j.require(err == nil, "infrastructure")
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		if !j.usedPorts[p] {
			j.usedPorts[p] = true
			return p
		}
	}
	j.require(false, "infrastructure_ports")
	return 0
}
func (j *journey) docker(args ...string) []byte {
	j.t.Helper()
	out, err := Docker(j.ctx, 12*time.Second, args...)
	j.require(err == nil, "docker_infrastructure")
	return out
}
func (j *journey) dockerLong(args ...string) []byte {
	j.t.Helper()
	out, err := Docker(j.ctx, 6*time.Minute, args...)
	j.require(err == nil, "fixture_infrastructure")
	return out
}
func (j *journey) jsonDocker(target any, args ...string) {
	j.t.Helper()
	j.require(json.Unmarshal(j.docker(args...), target) == nil, "docker_representation")
}
func (j *journey) wait(budget time.Duration, probe func(context.Context) (bool, error)) {
	j.t.Helper()
	err := Poll(j.ctx, budget, probe)
	if err != nil {
		if err.Error() == "poll_timeout" || errors.Is(err, context.DeadlineExceeded) {
			j.require(false, "condition_timeout")
		} else {
			j.require(false, "condition_mismatch")
		}
	}
}
func (j *journey) record(r Resource) {
	j.ledger.Resources = append(j.ledger.Resources, r)
	j.require(SaveLedger(j.ledgerFile, j.ledger) == nil, "ledger_write")
}
func (j *journey) fixture(kind, name string, args ...string) string {
	r := Resource{Kind: kind, Name: name, OwnerKey: "io.moduleos.operations.run", Owner: j.run}
	j.record(r)
	if kind == "service" && len(args) > 1 && args[0] == "service" && args[1] == "create" {
		args = append(append([]string{}, args[:2]...), append([]string{"--detach=true"}, args[2:]...)...)
	}
	output := j.dockerLong(args...)
	id := strings.TrimSpace(string(output))
	if kind == "image" {
		id = string(j.docker("image", "inspect", "--format", "{{.Id}}", name))
	}
	if kind == "service" {
		id = string(j.docker("service", "inspect", "--format", "{{.ID}}", name))
	}
	if kind == "volume" {
		var v struct{ Name, CreatedAt, Mountpoint string }
		j.jsonDocker(&v, "volume", "inspect", "--format", "{{json .}}", name)
		id = volumeIdentity(v.Name, v.CreatedAt, v.Mountpoint)
	}
	{
		j.require(id != "", "fixture_identity")
		j.ledger.Resources[len(j.ledger.Resources)-1].ID = id
		j.require(SaveLedger(j.ledgerFile, j.ledger) == nil, "ledger_write")
	}
	return id
}
func (j *journey) request(ctx context.Context, method, path string, body any, tag, key string, auth bool) (int, http.Header, []byte, error) {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, j.base+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+j.key)
	}
	if tag != "" {
		req.Header.Set("If-Match", tag)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := j.api.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("http_transport")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return 0, nil, nil, fmt.Errorf("http_body_limit")
	}
	return resp.StatusCode, resp.Header, b, nil
}
func (j *journey) call(method, path string, body any, tag, key string, status int, target any) http.Header {
	j.t.Helper()
	s, h, data, err := j.request(j.ctx, method, path, body, tag, key, true)
	j.expectedHTTP = status
	j.observedHTTP = s
	j.require(err == nil && s == status, "http_contract")
	if target != nil {
		j.require(json.Unmarshal(data, target) == nil, "http_representation")
	}
	return h
}
func (j *journey) app() (application, string) {
	j.t.Helper()
	var a application
	h := j.call("GET", "/apps/"+j.name, nil, "", "", 200, &a)
	tag := h.Get("ETag")
	j.require(tag == fmt.Sprintf(`"app:%s:%d"`, a.ID, a.Generation), "strong_etag")
	return a, tag
}
func (j *journey) converged(replicas int) application {
	j.t.Helper()
	var found application
	j.wait(150*time.Second, func(ctx context.Context) (bool, error) {
		s, _, b, err := j.request(ctx, "GET", "/apps/"+j.name, nil, "", "", true)
		if err != nil || s != 200 {
			return false, nil
		}
		if json.Unmarshal(b, &found) != nil {
			return false, fmt.Errorf("representation")
		}
		want := "running"
		if replicas == 0 {
			want = "stopped"
		}
		if found.Generation != found.ObservedGeneration || found.State != want {
			return false, nil
		}
		var svc serviceRecord
		out, err := Docker(ctx, 5*time.Second, "service", "inspect", "--format", "{{json .}}", "moduleos_"+j.name)
		if err != nil {
			return false, nil
		}
		if json.Unmarshal(out, &svc) != nil {
			return false, fmt.Errorf("representation")
		}
		if svc.Spec.Labels["moduleos.app.id"] != found.ID || svc.Spec.Labels["moduleos.generation"] != strconv.FormatInt(found.Generation, 10) || svc.Spec.Labels["moduleos.managed"] != "true" {
			return false, fmt.Errorf("ownership")
		}
		rows, err := Docker(ctx, 5*time.Second, "service", "ps", "--no-trunc", "--format", "{{json .}}", svc.ID)
		if svc.Spec.Mode.Replicated.Replicas != replicas {
			return false, nil
		}
		if err != nil {
			return false, nil
		}
		running := 0
		for _, line := range bytes.Split(rows, []byte("\n")) {
			var task struct{ CurrentState, DesiredState string }
			if json.Unmarshal(line, &task) != nil {
				continue
			}
			if strings.HasPrefix(task.CurrentState, "Running") {
				running++
			}
		}
		return running == replicas, nil
	})
	return found
}
func (j *journey) deploy(image, key string) accepted {
	j.t.Helper()
	_, tag := j.app()
	var a accepted
	j.call("POST", "/apps/"+j.name+"/deploy", map[string]any{"image": image}, tag, key, 202, &a)
	j.require(a.ID != "" && a.Generation > 0, "deployment_acceptance")
	return a
}
func (j *journey) terminal(a accepted, status string) deployment {
	j.t.Helper()
	var d deployment
	j.wait(180*time.Second, func(ctx context.Context) (bool, error) {
		s, _, b, err := j.request(ctx, "GET", "/apps/"+j.name+"/deployments/"+a.ID, nil, "", "", true)
		if err != nil || s != 200 {
			return false, nil
		}
		if json.Unmarshal(b, &d) != nil {
			return false, fmt.Errorf("deployment_representation")
		}
		if d.ID != a.ID || d.Generation != a.Generation {
			return false, fmt.Errorf("deployment_identity")
		}
		if d.Status == "failed" || d.Status == "succeeded" {
			if d.Status != status {
				return false, fmt.Errorf("wrong_terminal_state")
			}
			return true, nil
		}
		return false, nil
	})
	return d
}
func (j *journey) backend(ctx context.Context, port int, host string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	if err != nil {
		return 0, "", err
	}
	req.Host = host
	r, err := j.api.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(b) > 4096 {
		return 0, "", fmt.Errorf("route_body")
	}
	return r.StatusCode, string(b), nil
}
func (j *journey) serving(variant string) {
	j.t.Helper()
	j.wait(90*time.Second, func(ctx context.Context) (bool, error) {
		s, b, err := j.backend(ctx, j.port, "backend.example")
		return err == nil && s == 200 && b == "ops-"+j.run+"-"+variant+"\n", nil
	})
}
func (j *journey) process() processIdentity {
	var tasks []string
	out := j.docker("service", "ps", "--no-trunc", "--filter", "desired-state=running", "--format", "{{.ID}}", controlService)
	tasks = strings.Fields(string(out))
	j.require(len(tasks) == 1, "singleton_task")
	var task taskRecord
	j.jsonDocker(&task, "inspect", "--type", "task", "--format", "{{json .}}", tasks[0])
	j.require(task.Status.State == "running" && task.Status.ContainerStatus.ContainerID != "", "running_task")
	var c containerRecord
	j.jsonDocker(&c, "container", "inspect", "--format", "{{json .}}", task.Status.ContainerStatus.ContainerID)
	j.require(c.State.Running && c.State.Pid > 0, "running_process")
	return processIdentity{task.ID, c.ID, c.State.StartedAt, c.State.Pid}
}
func (j *journey) ready() {
	j.wait(90*time.Second, func(ctx context.Context) (bool, error) {
		s, _, b, err := j.request(ctx, "GET", "/ready", nil, "", "", false)
		var r struct{ Status string }
		if err != nil || json.Unmarshal(b, &r) != nil {
			return false, nil
		}
		return s == 200 && r.Status == "ready", nil
	})
}
func readProcess(ctx context.Context) (processIdentity, error) {
	out, err := Docker(ctx, 5*time.Second, "service", "ps", "--no-trunc", "--filter", "desired-state=running", "--format", "{{.ID}}", controlService)
	if err != nil {
		return processIdentity{}, err
	}
	ids := strings.Fields(string(out))
	if len(ids) != 1 {
		return processIdentity{}, fmt.Errorf("process_not_singleton")
	}
	out, err = Docker(ctx, 5*time.Second, "inspect", "--type", "task", "--format", "{{json .}}", ids[0])
	var task taskRecord
	if err != nil || json.Unmarshal(out, &task) != nil || task.Status.State != "running" || task.Status.ContainerStatus.ContainerID == "" {
		return processIdentity{}, fmt.Errorf("task_not_running")
	}
	out, err = Docker(ctx, 5*time.Second, "container", "inspect", "--format", "{{json .}}", task.Status.ContainerStatus.ContainerID)
	var c containerRecord
	if err != nil || json.Unmarshal(out, &c) != nil || !c.State.Running || c.State.Pid <= 0 {
		return processIdentity{}, fmt.Errorf("process_not_running")
	}
	return processIdentity{task.ID, c.ID, c.State.StartedAt, c.State.Pid}, nil
}
func (j *journey) waitReplacement(before processIdentity, socketSource string) {
	j.wait(120*time.Second, func(ctx context.Context) (bool, error) {
		now, err := readProcess(ctx)
		if err != nil || now.Task == before.Task || now.Container == before.Container || now.Started == before.Started {
			return false, nil
		}
		if socketSource != "" {
			out, err := Docker(ctx, 5*time.Second, "container", "inspect", "--format", "{{json .Mounts}}", now.Container)
			var mounts []struct{ Source, Destination string }
			if err != nil || json.Unmarshal(out, &mounts) != nil {
				return false, nil
			}
			found := false
			for _, m := range mounts {
				if m.Destination == "/var/run/docker.sock" && m.Source == socketSource {
					found = true
				}
			}
			if !found {
				return false, nil
			}
		}
		status, _, _, err := j.request(ctx, "GET", "/ready", nil, "", "", false)
		return err == nil && status == 200, nil
	})
}
func (j *journey) deployOfficial() {
	j.t.Helper()
	_, err := Command(j.ctx, 6*time.Minute, "bash", "../deploy.sh", "--env-file", j.envFile)
	j.require(err == nil, "official_deploy")
	id := j.docker("service", "inspect", "--format", "{{.ID}}", controlService)
	j.require(os.WriteFile(os.Getenv("MODULEOS_STACK_SERVICE_ID_FILE"), id, 0600) == nil, "service_identity_record")
	j.ready()
}
func sameIntent(a, b application) bool {
	a.ObservedGeneration = 0
	a.State = ""
	a.Code = ""
	a.ObservedImage = ""
	b.ObservedGeneration = 0
	b.State = ""
	b.Code = ""
	b.ObservedImage = ""
	return reflect.DeepEqual(a, b)
}
func (j *journey) rememberCanaries() {
	for _, kind := range []string{"network", "volume", "secret"} {
		id := j.docker(kind, "ls", "--quiet", "--filter", "label=io.moduleos.stack.canary="+j.run)
		j.require(len(bytes.TrimSpace(id)) > 0, "sentinel_missing")
		j.canaries[kind] = append([]byte(nil), id...)
	}
}
func (j *journey) checkCanaries() {
	for kind, id := range j.canaries {
		after := j.docker(kind, "ls", "--quiet", "--filter", "label=io.moduleos.stack.canary="+j.run)
		j.require(bytes.Equal(id, after), "sentinel_changed")
	}
}
func (j *journey) root() string { return filepath.Clean(filepath.Join("..", "..", "..")) }
