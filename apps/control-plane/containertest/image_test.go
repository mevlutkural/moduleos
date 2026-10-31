//go:build container

package containertest_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestImageMetadata(t *testing.T) {
	var labels map[string]string
	if err := json.Unmarshal([]byte(docker(t, "image", "inspect", "--format", "{{json .Config.Labels}}", suite.image)), &labels); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"org.opencontainers.image.version":  suite.version,
		"org.opencontainers.image.revision": suite.revision,
		"org.opencontainers.image.created":  suite.buildDate,
		"org.opencontainers.image.licenses": "MIT",
		"org.opencontainers.image.source":   "https://github.com/mevlutkural/moduleos",
	}
	for key, value := range want {
		if labels[key] != value {
			t.Fatalf("label %s = %q, want %q", key, labels[key], value)
		}
	}
	if !strings.HasPrefix(labels["org.opencontainers.image.base.digest"], "sha256:") {
		t.Fatalf("base digest label = %q", labels["org.opencontainers.image.base.digest"])
	}

	instance := startDaemon(t, daemonOptions{data: newVolume(t)})
	status, body := request(t, http.MethodGet, instance.baseURL+"/api/v1/version", "", false)
	if status != http.StatusOK {
		t.Fatalf("version status = %d", status)
	}
	var version struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildDate string `json:"build_date"`
	}
	if err := json.Unmarshal(body, &version); err != nil {
		t.Fatal(err)
	}
	if version.Version != suite.version || version.Commit != suite.revision || version.BuildDate != suite.buildDate {
		t.Fatalf("/version = %+v, want %s %s %s", version, suite.version, suite.revision, suite.buildDate)
	}

	license, err := os.ReadFile("../../../LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	files := exportFiles(t)
	if got := files["usr/share/licenses/moduleos/LICENSE"]; !bytes.Equal(got.content, license) {
		t.Fatal("image license text differs from the repository LICENSE")
	}
}

type exportedFile struct {
	mode    int64
	uid     int
	gid     int
	kind    byte
	content []byte
}

func exportFiles(t *testing.T) map[string]exportedFile {
	t.Helper()
	id := docker(t, "create", "--label", runLabelArgument(), suite.image)
	trackContainer(t, id)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "export", id).Output()
	if err != nil {
		t.Fatalf("docker export: %v", err)
	}
	files := map[string]exportedFile{}
	reader := tar.NewReader(bytes.NewReader(output))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		file := exportedFile{mode: header.Mode & 0o7777, uid: header.Uid, gid: header.Gid, kind: header.Typeflag}
		if header.Typeflag == tar.TypeReg && header.Size <= 1<<20 {
			file.content, _ = io.ReadAll(reader)
		}
		files[strings.TrimSuffix(header.Name, "/")] = file
	}
	return files
}

func TestImageFilesystemContainsOnlyProductionContent(t *testing.T) {
	files := exportFiles(t)
	required := map[string]exportedFile{
		"usr/local/bin/moduleosd":             {mode: 0o555, kind: tar.TypeReg},
		"usr/local/bin/moduleosd-container":   {mode: 0o555, kind: tar.TypeReg},
		"usr/share/licenses/moduleos/LICENSE": {mode: 0o444, kind: tar.TypeReg},
		"var/lib/moduleos":                    {mode: 0o700, uid: 65532, gid: 65532, kind: tar.TypeDir},
	}
	for name, want := range required {
		got, exists := files[name]
		if !exists {
			t.Fatalf("image is missing %s", name)
		}
		if got.mode != want.mode || got.uid != want.uid || got.gid != want.gid || got.kind != want.kind {
			t.Fatalf("%s = mode %04o owner %d:%d type %q, want mode %04o owner %d:%d type %q",
				name, got.mode, got.uid, got.gid, got.kind, want.mode, want.uid, want.gid, want.kind)
		}
	}
	for name := range files {
		base := path.Base(name)
		switch {
		case strings.HasSuffix(name, "_test.go"), strings.Contains(name, "testdata"), strings.HasSuffix(name, ".go"):
			t.Fatalf("image contains source or test content: %s", name)
		case strings.Contains(name, ".git/"), base == ".git", strings.HasPrefix(name, "temp/"), strings.Contains(name, "/temp/"):
			t.Fatalf("image contains repository or scratch content: %s", name)
		case strings.HasSuffix(base, ".db"), strings.HasSuffix(base, ".db-wal"), strings.HasSuffix(base, ".db-shm"), strings.HasSuffix(base, ".sqlite"):
			t.Fatalf("image contains a database file: %s", name)
		case slices.Contains([]string{"sh", "bash", "ash", "busybox", "apt", "apt-get", "dpkg", "apk"}, base) && strings.Contains(name, "bin/"):
			t.Fatalf("image contains a shell or package manager: %s", name)
		}
	}
	for name := range files {
		if strings.HasPrefix(name, "var/lib/moduleos/") {
			t.Fatalf("image data root contains %s", name)
		}
	}
}

func TestDaemonRunsAsFixedNonRootIdentityAndStaysLiveWithoutDocker(t *testing.T) {
	instance := startDaemon(t, daemonOptions{data: newVolume(t)})

	report := inspectProcess(t, instance.id)
	if !slices.Equal(report.UID, []int{65532, 65532, 65532, 65532}) || !slices.Equal(report.GID, []int{65532, 65532, 65532, 65532}) {
		t.Fatalf("daemon identity uid=%v gid=%v, want 65532 everywhere", report.UID, report.GID)
	}
	if !slices.Equal(report.Groups, []int{65532}) {
		t.Fatalf("daemon groups = %v, want only 65532 without a Docker socket", report.Groups)
	}
	if !slices.Equal(report.Command, []string{"moduleosd"}) || report.Processes != 1 {
		t.Fatalf("PID 1 = %v with %d processes, want moduleosd alone", report.Command, report.Processes)
	}
	if report.NoNewPrivs != "1" || strings.Trim(report.CapEff, "0") != "" || strings.Trim(report.CapPrm, "0") != "" {
		t.Fatalf("NoNewPrivs=%s CapEff=%s CapPrm=%s, want 1 and no capabilities", report.NoNewPrivs, report.CapEff, report.CapPrm)
	}

	if status, _ := request(t, http.MethodGet, instance.baseURL+"/api/v1/live", "", false); status != http.StatusOK {
		t.Fatalf("/live status = %d, want 200", status)
	}
	if status, _ := request(t, http.MethodGet, instance.baseURL+"/api/v1/ready", "", false); status != http.StatusServiceUnavailable {
		t.Fatalf("/ready status = %d, want 503 without Docker", status)
	}
	if codes := logCodes(logsOf(t, instance.id)); !slices.Contains(codes, "docker_socket_absent") {
		t.Fatalf("missing socket warning; codes = %v", codes)
	}
}

func TestDaemonReceivesDockerSocketSupplementaryGroup(t *testing.T) {
	socket := startSocketServer(t, 23456)
	instance := startDaemon(t, daemonOptions{data: newVolume(t), socket: socket})

	report := inspectProcess(t, instance.id)
	groups := slices.Clone(report.Groups)
	slices.Sort(groups)
	if !slices.Equal(groups, []int{23456, 65532}) {
		t.Fatalf("daemon groups = %v, want [23456 65532]", report.Groups)
	}
	if !slices.Equal(report.UID, []int{65532, 65532, 65532, 65532}) {
		t.Fatalf("daemon uid = %v", report.UID)
	}
}

func TestSignalsTriggerBoundedGracefulShutdown(t *testing.T) {
	for _, signal := range []string{"TERM", "INT"} {
		t.Run(signal, func(t *testing.T) {
			socket := startSocketServer(t, 23457)
			instance := startDaemon(t, daemonOptions{data: newVolume(t), socket: socket})

			// With an unresponsive Docker socket, readiness stays in flight for
			// READINESS_TIMEOUT; shutdown must still drain it and exit cleanly.
			inFlight := make(chan int, 1)
			written := make(chan struct{})
			go func() {
				trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { close(written) }}
				request, err := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), http.MethodGet, instance.baseURL+"/api/v1/ready", nil)
				if err != nil {
					inFlight <- -1
					return
				}
				client := &http.Client{Timeout: 30 * time.Second}
				response, err := client.Do(request)
				if err != nil {
					inFlight <- -1
					return
				}
				_ = response.Body.Close()
				inFlight <- response.StatusCode
			}()
			select {
			case <-written:
			case <-time.After(10 * time.Second):
				t.Fatal("readiness request was not sent")
			}
			// Leave the request time to cross the published-port proxy and
			// reach the daemon's readiness check before signalling.
			time.Sleep(time.Second)

			started := time.Now()
			docker(t, "kill", "--signal", signal, instance.id)
			code := waitExited(t, instance.id, shutdownBudget+15*time.Second)
			elapsed := time.Since(started)

			if code != 0 {
				t.Fatalf("exit code after SIG%s = %d, want 0\n%s", signal, code, logsOf(t, instance.id))
			}
			if elapsed > shutdownBudget+5*time.Second {
				t.Fatalf("shutdown took %s, want within the %s budget", elapsed, shutdownBudget)
			}
			if status := <-inFlight; status != http.StatusServiceUnavailable {
				t.Fatalf("in-flight readiness status = %d, want a completed 503 response", status)
			}
			if !strings.Contains(logsOf(t, instance.id), `"msg":"daemon stopped"`) {
				t.Fatal("daemon did not log a controlled stop")
			}
		})
	}
}

func TestDataPersistsAcrossContainerReplacement(t *testing.T) {
	data := newVolume(t)
	keys := apiKeyVolume(t)
	first := startDaemon(t, daemonOptions{data: data, keys: keys})
	createProject(t, first, "persisted")
	stopDaemon(t, first.id)
	docker(t, "rm", first.id)

	second := startDaemon(t, daemonOptions{data: data, keys: keys})
	requireProject(t, second, "persisted")

	stopDaemon(t, second.id)
	files := inspectPaths(t, []string{"--volume", data + ":" + dataRoot}, dataRoot, dataRoot+"/moduleos.db")
	for _, name := range []string{dataRoot, dataRoot + "/moduleos.db"} {
		if item := files[name]; !item.Exists || item.UID != 65532 || item.GID != 65532 {
			t.Fatalf("%s = %+v, want owned by 65532:65532", name, item)
		}
	}
	if files[dataRoot].Mode != "0700" {
		t.Fatalf("data root mode = %s, want 0700", files[dataRoot].Mode)
	}
}

func TestRestoredRootOwnedDataIsAdoptedWithoutRecursiveChanges(t *testing.T) {
	data := newVolume(t)
	keys := apiKeyVolume(t)
	first := startDaemon(t, daemonOptions{data: data, keys: keys})
	createProject(t, first, "restored")
	stopDaemon(t, first.id)

	mounts := []string{"--volume", data + ":" + dataRoot}
	runFixture(t, mounts, "make-legacy", dataRoot)
	before := inspectPaths(t, mounts, dataRoot, dataRoot+"/moduleos.db", dataRoot+"/moduleos.db-wal", dataRoot+"/moduleos.db-shm")
	if before[dataRoot].UID != 0 || before[dataRoot+"/moduleos.db"].UID != 0 {
		t.Fatalf("fixture did not produce root-owned data: %+v", before)
	}

	second := startDaemon(t, daemonOptions{data: data, keys: keys})
	requireProject(t, second, "restored")
	// The daemon cannot open root-owned 0600 sidecars, so a working database
	// with both sidecars now owned by the runtime identity proves adoption.
	running := inspectPaths(t, mounts, dataRoot+"/moduleos.db-wal", dataRoot+"/moduleos.db-shm")
	for name, item := range running {
		if !item.Exists || item.UID != 65532 || item.GID != 65532 {
			t.Fatalf("%s = %+v while running, want adopted by 65532:65532", name, item)
		}
	}
	stopDaemon(t, second.id)

	after := inspectPaths(t, mounts,
		dataRoot, dataRoot+"/moduleos.db", dataRoot+"/moduleos.db-wal", dataRoot+"/moduleos.db-shm",
		dataRoot+"/unrelated.txt", dataRoot+"/nested", dataRoot+"/nested/inner.txt")
	for _, name := range []string{dataRoot, dataRoot + "/moduleos.db"} {
		if item := after[name]; item.UID != 65532 || item.GID != 65532 {
			t.Fatalf("%s = %+v, want adopted by 65532:65532", name, item)
		}
	}
	for _, name := range []string{dataRoot + "/moduleos.db-wal", dataRoot + "/moduleos.db-shm"} {
		if item := before[name]; !item.Exists || item.UID != 0 {
			t.Fatalf("fixture did not produce a root-owned %s: %+v", name, item)
		}
	}
	for _, name := range []string{dataRoot + "/unrelated.txt", dataRoot + "/nested", dataRoot + "/nested/inner.txt"} {
		if item := after[name]; !item.Exists || item.UID != 0 || item.GID != 0 {
			t.Fatalf("%s = %+v, want untouched and root-owned", name, item)
		}
	}
}

func TestUnsafePreparationFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T) daemonOptions
		code    string
		verify  func(t *testing.T, options daemonOptions)
	}{
		{
			name: "symlinked sidecar",
			prepare: func(t *testing.T) daemonOptions {
				data := newVolume(t)
				runFixture(t, []string{"--volume", data + ":" + dataRoot}, "symlink-sidecar", dataRoot)
				return daemonOptions{data: data}
			},
			code: "sqlite_file_invalid",
			verify: func(t *testing.T, options daemonOptions) {
				target := inspectPaths(t, []string{"--volume", options.data + ":" + dataRoot}, dataRoot+"/target.txt")[dataRoot+"/target.txt"]
				if target.UID != 0 || target.GID != 0 {
					t.Fatalf("symlink target ownership changed: %+v", target)
				}
			},
		},
		{
			name: "hard-linked database",
			prepare: func(t *testing.T) daemonOptions {
				data := newVolume(t)
				runFixture(t, []string{"--volume", data + ":" + dataRoot}, "hardlink-db", dataRoot)
				return daemonOptions{data: data}
			},
			code: "sqlite_file_invalid",
		},
		{
			name: "symlinked data root",
			prepare: func(t *testing.T) daemonOptions {
				parent := newVolume(t)
				runFixture(t, []string{"--volume", parent + ":/var/lib"}, "data-root-symlink", "/var/lib")
				return daemonOptions{data: parent, dataMount: "/var/lib"}
			},
			code: "data_root_invalid",
		},
		{
			name: "database outside the data root",
			prepare: func(t *testing.T) daemonOptions {
				return daemonOptions{data: newVolume(t), environment: []string{"DATABASE_PATH=/tmp/moduleos.db"}}
			},
			code: "database_path_invalid",
		},
		{
			name: "socket path is a regular file",
			prepare: func(t *testing.T) daemonOptions {
				socket := newVolume(t)
				runFixture(t, []string{"--volume", socket + ":" + socketMount}, "regular-file", socketPath)
				return daemonOptions{data: newVolume(t), socket: socket}
			},
			code: "docker_socket_invalid",
		},
		{
			name: "socket owned by the root group",
			prepare: func(t *testing.T) daemonOptions {
				return daemonOptions{data: newVolume(t), socket: startSocketServer(t, 0)}
			},
			code: "docker_socket_root_group",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := test.prepare(t)
			id := createDaemon(t, options)
			docker(t, "start", id)

			if code := waitExited(t, id, 60*time.Second); code == 0 {
				t.Fatal("container exited 0 after unsafe preparation")
			}
			logs := logsOf(t, id)
			if codes := logCodesAtLevel(logs, "ERROR"); !slices.Equal(codes, []string{test.code}) {
				t.Fatalf("error codes = %v, want exactly [%s]\n%s", codes, test.code, logs)
			}
			if strings.Contains(logs, "daemon started") {
				t.Fatal("daemon started after unsafe preparation")
			}
			if test.verify != nil {
				test.verify(t, options)
			}
		})
	}
}

func TestProductionImageRequiresAPIKeyFile(t *testing.T) {
	const sentinel = "sentinel-api-key-value-that-must-not-leak"
	for name, options := range map[string]daemonOptions{
		"no key source":          {data: newVolume(t), noKeyFile: true},
		"environment key source": {data: newVolume(t), noKeyFile: true, environment: []string{"API_KEY=" + sentinel}},
	} {
		t.Run(name, func(t *testing.T) {
			id := createDaemon(t, options)
			docker(t, "start", id)
			if code := waitExited(t, id, 60*time.Second); code == 0 {
				t.Fatal("production image started without API_KEY_FILE")
			}
			logs := logsOf(t, id)
			if strings.Contains(logs, sentinel) {
				t.Fatal("logs exposed the API key value")
			}
			if !strings.Contains(logs, `"phase":"config"`) || strings.Contains(logs, "daemon started") {
				t.Fatalf("want a config-phase bootstrap failure before startup:\n%s", logs)
			}
		})
	}
}

func TestReadOnlyRootFilesystem(t *testing.T) {
	instance := startDaemon(t, daemonOptions{data: newVolume(t), extra: []string{"--read-only"}})
	createProject(t, instance, "readonly")
	requireProject(t, instance, "readonly")
}

func TestNonRootStartSkipsPreparation(t *testing.T) {
	instance := startDaemon(t, daemonOptions{data: newVolume(t), extra: []string{"--user", "65532:65532"}})

	report := inspectProcess(t, instance.id)
	if !slices.Equal(report.UID, []int{65532, 65532, 65532, 65532}) {
		t.Fatalf("daemon uid = %v", report.UID)
	}
	if codes := logCodes(logsOf(t, instance.id)); len(codes) != 0 {
		t.Fatalf("non-root start ran preparation; codes = %v", codes)
	}
	createProject(t, instance, "nonroot")
}

// TestMeasureMinimumCapabilities records which reduced capability sets let
// start succeed, as input for Phase 8B3 stack hardening. It is a measurement,
// not a gate, so it runs only on request.
func TestMeasureMinimumCapabilities(t *testing.T) {
	if os.Getenv(measureCapabilitiesEnv) != "1" {
		t.Skip("set " + measureCapabilitiesEnv + "=1 to measure the minimum capability set")
	}
	candidates := [][]string{
		{"CHOWN", "SETUID", "SETGID"},
		{"CHOWN", "SETUID", "SETGID", "DAC_READ_SEARCH"},
		{"CHOWN", "SETUID", "SETGID", "DAC_OVERRIDE"},
	}
	for _, volumeState := range []string{"fresh", "restored"} {
		for _, capabilities := range candidates {
			data := newVolume(t)
			if volumeState == "restored" {
				keys := apiKeyVolume(t)
				first := startDaemon(t, daemonOptions{data: data, keys: keys})
				stopDaemon(t, first.id)
				runFixture(t, []string{"--volume", data + ":" + dataRoot}, "make-legacy", dataRoot)
			}
			extra := []string{"--cap-drop", "ALL"}
			for _, capability := range capabilities {
				extra = append(extra, "--cap-add", capability)
			}
			id := createDaemon(t, daemonOptions{data: data, extra: extra})
			docker(t, "start", id)
			result := "starts"
			deadline := time.Now().Add(60 * time.Second)
			for {
				state := docker(t, "inspect", "--format", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}", id)
				if state == "running healthy" {
					break
				}
				if strings.HasPrefix(state, "exited") {
					result = "fails: " + strings.Join(logCodes(logsOf(t, id)), ",")
					break
				}
				if time.Now().After(deadline) {
					result = "inconclusive: " + state
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			t.Logf("capability measurement: volume=%s cap-add=%s -> %s", volumeState, capabilities, result)
			_, _ = dockerOutput("rm", "--force", id)
		}
	}
}
