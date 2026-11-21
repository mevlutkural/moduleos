package scripts_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func executable(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestToolExtractionFailsOnUnknownCleanup(t *testing.T) {
	dir := t.TempDir()
	executable(t, dir, "timeout", "case $1 in --kill-after=*) shift;; esac\nshift\nexec \"$@\"\n")
	executable(t, dir, "setsid", "exec \"$@\"\n")
	executable(t, dir, "docker", `case "$1" in
 pull|create) exit 0;;
 cp) : > "$3";;
 container) exit 124;;
 *) exit 9;; esac
`)
	cmd := exec.Command("bash", "extract-ci-tool.sh", "trivy", dir)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "cleanup cannot be verified") {
		t.Fatalf("%v %s", err, output)
	}
}

func TestExtractionCleanupSurvivesSignal(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "inspecting")
	removed := filepath.Join(dir, "removed")
	executable(t, dir, "timeout", "case $1 in --kill-after=*) shift;; esac\nshift\nexec \"$@\"\n")
	executable(t, dir, "setsid", "exec \"$@\"\n")
	executable(t, dir, "docker", fmt.Sprintf(`case "$1 $2" in
 'pull '*|'create '*) exit 0;;
 'cp '*) : > "$3";;
 'container inspect')
   printf ready > %q
   sleep 0.3
   for item do owner=$item; done
   echo "$owner";;
 'container rm') touch %q;;
 *) exit 9;; esac
`, marker, removed))
	cmd := exec.Command("bash", "extract-ci-tool.sh", "trivy", dir)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "TMPDIR="+dir)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(marker)
		if string(b) == "ready" {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("cleanup inspect did not start")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%v: %s", err, output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not complete")
	}
	if _, err := os.Stat(removed); err != nil {
		t.Fatal("container removal skipped")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Fatalf("cleanup leaked workspace %s", e.Name())
		}
	}
}

func TestScanCancellationKillsActiveGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires real Linux setsid; exercised in standalone Linux diagnostic and CI")
	}
	for _, phase := range []string{"inspect", "scanner"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "pid")
			sleeper := fmt.Sprintf("echo $$ > %q\nexec sleep 60\n", marker)
			executable(t, dir, "timeout", "case $1 in --kill-after=*) shift;; esac\nshift\nexec \"$@\"\n")
			inspect := "echo 'sha256:test linux/arm64'\n"
			scanner := sleeper
			if phase == "inspect" {
				inspect = sleeper
				scanner = "exit 9\n"
			}
			executable(t, dir, "docker", "case \"$1 $2\" in\n'image inspect') "+inspect+";;\n'image save') : > \"$4\";;\n*) exit 9;; esac\n")
			executable(t, dir, "trivy", "if [ \"$1\" = --version ]; then exit 0; fi\n"+scanner)
			cmd := exec.Command("bash", "scan-container-image.sh", "local", "arm64")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			var child int
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				data, err := os.ReadFile(marker)
				if err == nil {
					if parsed, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && parsed > 0 {
						child = parsed
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
			}
			if child == 0 {
				t.Fatal("child did not start")
			}
			t.Cleanup(func() { _ = syscall.Kill(-child, syscall.SIGKILL) })
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled scan succeeded")
				}
			case <-time.After(6 * time.Second):
				t.Fatal("cancellation did not terminate parent")
			}
			if err := syscall.Kill(child, 0); err != syscall.ESRCH {
				t.Fatalf("child survived cancellation: %v; %s", err, output.String())
			}
		})
	}
}

func TestScanImagePolicy(t *testing.T) {
	for _, code := range []string{"0", "1", "2"} {
		t.Run(code, func(t *testing.T) {
			dir := t.TempDir()
			executable(t, dir, "timeout", "case $1 in --kill-after=*) shift;; esac\nshift\nexec \"$@\"\n")
			executable(t, dir, "setsid", "exec \"$@\"\n")
			executable(t, dir, "docker", `case "$1 $2" in
 'image inspect') echo 'sha256:test linux/arm64';;
 'image save') : > "$4";;
 *) exit 8;; esac
`)
			args := filepath.Join(dir, "args")
			executable(t, dir, "trivy", fmt.Sprintf(`if [ -n "${TRIVY_DOWNLOAD_DB_ONLY:-}" ]; then exit 9; fi
if [ "$1" = --version ]; then echo 'fixture scanner'; exit 0; fi
printf '%%s\n' "$@" > %q
exit %s
`, args, code))
			cmd := exec.Command("bash", "scan-container-image.sh", "local-image", "arm64")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "TRIVY_DOWNLOAD_DB_ONLY=true")
			output, err := cmd.CombinedOutput()
			if (err == nil) != (code == "0") {
				t.Fatalf("code %s: %v %s", code, err, output)
			}
			content, err := os.ReadFile(args)
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"--input\n", "--scanners\nvuln\n", "--severity\nHIGH,CRITICAL\n", "--ignore-unfixed=false\n", "--exit-code\n1\n", "--config\n", "--ignorefile\n"} {
				if !strings.Contains(string(content), required) {
					t.Fatalf("missing strict scanner option %q", required)
				}
			}
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if line == "--input" {
					if _, err := os.Stat(lines[i+1]); !os.IsNotExist(err) {
						t.Fatal("scan archive not cleaned up")
					}
				}
			}
		})
	}
}

func TestScanRequiresLocalMatchingImage(t *testing.T) {
	for _, body := range []string{"exit 1\n", "echo 'sha256:test linux/amd64'\n"} {
		dir := t.TempDir()
		executable(t, dir, "timeout", "case $1 in --kill-after=*) shift;; esac\nshift\nexec \"$@\"\n")
		executable(t, dir, "setsid", "exec \"$@\"\n")
		executable(t, dir, "docker", body)
		marker := filepath.Join(dir, "scanner-ran")
		executable(t, dir, "trivy", fmt.Sprintf("touch %q\n", marker))
		cmd := exec.Command("bash", "scan-container-image.sh", "missing-or-wrong-image", "arm64")
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		if err := cmd.Run(); err == nil {
			t.Fatal("invalid image accepted")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("scanner ran without local matching image")
		}
	}
}

func TestNativeArchitectureMismatch(t *testing.T) {
	dir := t.TempDir()
	executable(t, dir, "docker", `case "$1" in
 buildx) exit 0;;
 version) case "$*" in *Server.Os*) echo linux;; *) echo amd64;; esac;;
 *) exit 9;; esac
`)
	cmd := exec.Command("sh", "check-container-image.sh")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MODULEOS_EXPECT_ARCH=arm64")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "expected native arm64") {
		t.Fatalf("%v %s", err, output)
	}
}

func TestDockerBaseLabelMatchesPin(t *testing.T) {
	data, err := os.ReadFile("../apps/control-plane/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	var runtime string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "FROM gcr.io/distroless/") {
			runtime = strings.TrimPrefix(line, "FROM ")
		}
	}
	parts := strings.Split(runtime, "@")
	if len(parts) != 2 {
		t.Fatal("runtime must have an immutable digest")
	}
	for _, want := range []string{`org.opencontainers.image.base.name="` + parts[0] + `"`, `org.opencontainers.image.base.digest="` + parts[1] + `"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("stale OCI base label: %s", want)
		}
	}
}
