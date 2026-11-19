package swarm_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fixtureImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeDeploy struct {
	root, shell, envFile, ledger, scenario string
}

func newFakeDeploy(t *testing.T) fakeDeploy {
	t.Helper()
	shell := contractShell(t)
	realTimeout, err := exec.LookPath("timeout")
	if err != nil {
		realTimeout, err = exec.LookPath("gtimeout")
	}
	if err != nil {
		t.Skip("GNU timeout unavailable; fake deployment tests not executed")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"deploy.sh"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	libPath, err := filepath.Abs("lib.sh")
	if err != nil {
		t.Fatal(err)
	}
	shim := fmt.Sprintf(`source %q
preflight() {
  printf '%%s' "$work_dir" > "$mock_root/workspace"
  if [[ $(cat "$mock_root/scenario") == preflight_failure ]]; then fail 'invalid_volume: fixture'; fi
  if [[ $(cat "$mock_root/scenario") == signal_captured_inspect ]]; then
    local captured
    capture_docker captured 65536 info --format '{{json .}}'
  fi
  render_file=$work_dir/stack.yml
  printf 'ORIGINAL_RENDER\n' > "$render_file"
  image_id=%s
  deployment_image=$image_id
  resolve_image=never
  if [[ $(cat "$mock_root/scenario") == changed_input ]]; then printf 'CHANGED_ENV\n' > "$mock_root/operator.env"; fi
}
verify_critical_identities() {
  if [[ $(cat "$mock_root/scenario") == changed_identity ]]; then fail 'identity_changed: secret'; fi
}
sleep() { case $(cat "$mock_root/scenario") in convergence_timeout|curl_failure|no_task) SECONDS=$((SECONDS+301));; esac; }
`, libPath, fixtureImageID)
	if err := os.WriteFile(filepath.Join(root, "lib.sh"), []byte(shim), 0600); err != nil {
		t.Fatal(err)
	}
	const dockerScript = `#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
printf '%s\n' "$*" >> "$root/ledger"
if [ "$1" = --host ]; then shift 2; fi
scenario=$(cat "$root/scenario")
case "$1 $2" in
  'info --format')
    if [ "$scenario" = signal_captured_inspect ]; then echo $$ > "$root/hang-pid"; sleep 30; fi
    printf '%s\n' '{"OSType":"linux"}'
    exit 0 ;;
  'stack deploy')
    case "$scenario" in
      deploy_failure|forged_logs|safe_logs|safe_stderr_logs|hung_logs) exit 1 ;;
      deploy_warning) echo 'unsafe raw warning SECRET_SENTINEL' >&2; exit 0 ;;
      hung_deploy|signal_hung_deploy) echo $$ > "$root/hang-pid"; sleep 30; exit 0 ;;
    esac
    for arg in "$@"; do
      if [ "$arg" = '-c' ]; then next=1; continue; fi
      if [ "${next:-0}" = 1 ]; then cat "$arg" > "$root/submitted"; break; fi
    done
    exit 0 ;;
  'service inspect')
    [ "$scenario" != inspect_failure ] || exit 1
    image='` + fixtureImageID + `'
    replicas=1
    readonly=true
    update=completed
    [ "$scenario" != wrong_image_reference ] || image='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
    [ "$scenario" != wrong_spec ] || replicas=2
    [ "$scenario" != paused ] || update=paused
    if [ "$scenario" = convergence_timeout ]; then update=updating; fi
    if [ "$scenario" = updating_then_done ]; then
      count=$(cat "$root/inspect-count" 2>/dev/null || echo 0)
      count=$((count+1)); printf '%s' "$count" > "$root/inspect-count"
      [ "$count" -gt 1 ] || update=updating
    fi
    printf '{"Spec":{"Mode":{"Replicated":{"Replicas":%s}},"TaskTemplate":{"ContainerSpec":{"ReadOnly":%s,"Image":"%s"}}},"UpdateStatus":{"State":"%s"}}\n' "$replicas" "$readonly" "$image" "$update"
    exit 0 ;;
  'service ps')
    [ "$scenario" != no_task ] || exit 0
    printf '%s\n' '{"ID":"task-1","CurrentState":"Running 1 second"}'
    exit 0 ;;
  'inspect --format')
    printf '%s\n' '{"Status":{"ContainerStatus":{"ContainerID":"container-1"}}}'
    exit 0 ;;
  'container inspect')
    image='` + fixtureImageID + `'
    [ "$scenario" != wrong_running_image ] || image='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
    printf '{"Image":"%s","State":{"Health":{"Status":"healthy"}}}\n' "$image"
    exit 0 ;;
  'service logs')
    case "$scenario" in
      forged_logs) printf '%s\n' '{"msg":"bootstrap failed","phase":"config","reason":"value_invalid","variable":"SECRET_SENTINEL"}' ;;
      safe_logs) printf '%s\n' '{"msg":"bootstrap failed","phase":"config","reason":"value_invalid","variable":"PORT"}' ;;
      safe_stderr_logs) printf '%s\n' '{"msg":"container start failed","code":"docker_socket_invalid"}' >&2 ;;
      hung_logs) sleep 30 ;;
    esac
    exit 0 ;;
esac
exit 99
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(dockerScript), 0700); err != nil {
		t.Fatal(err)
	}
	timeoutScript := fmt.Sprintf(`#!/bin/sh
set -eu
while :; do case "$1" in --signal=*|--kill-after=*) shift;; *) break;; esac; done
shift
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
limit=2s
case "$(cat "$root/scenario" 2>/dev/null)" in signal_hung_deploy|signal_captured_inspect) limit=30s;; esac
exec %q --signal=TERM --kill-after=1s "$limit" "$@"
`, realTimeout)
	if err := os.WriteFile(filepath.Join(bin, "timeout"), []byte(timeoutScript), 0700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" {
		realSetsid, err := exec.LookPath("setsid")
		if err != nil {
			t.Fatal("Linux test host needs setsid")
		}
		if err := os.Symlink(realSetsid, filepath.Join(bin, "setsid")); err != nil {
			t.Fatal(err)
		}
	} else {
		setsidScript := `#!/usr/bin/python3
import os
import sys
os.setsid()
os.execvp(sys.argv[1], sys.argv[1:])
`
		if err := os.WriteFile(filepath.Join(bin, "setsid"), []byte(setsidScript), 0700); err != nil {
			t.Fatal(err)
		}
	}
	curlScript := `#!/bin/sh
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
printf 'curl %s\n' "$*" >> "$root/ledger"
case "$(cat "$root/scenario")" in curl_failure) exit 7;; hung_curl) sleep 30;; esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(curlScript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "findmnt"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(root, "operator.env")
	if err := os.WriteFile(envFile, []byte(validEnv), 0600); err != nil {
		t.Fatal(err)
	}
	return fakeDeploy{root: root, shell: shell, envFile: envFile, ledger: filepath.Join(root, "ledger"), scenario: filepath.Join(root, "scenario")}
}

func (f fakeDeploy) run(t *testing.T, scenario string) ([]byte, error) {
	t.Helper()
	if err := os.WriteFile(f.scenario, []byte(scenario), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(f.shell, filepath.Join(f.root, "deploy.sh"), "--env-file", f.envFile)
	cmd.Env = []string{"PATH=" + filepath.Join(f.root, "bin") + ":" + os.Getenv("PATH"), "HOME=" + f.root, "TMPDIR=" + f.root, "mock_root=" + f.root, "LC_ALL=C"}
	return cmd.CombinedOutput()
}

func TestFakeDeploymentSignalStopsChildren(t *testing.T) {
	for _, scenario := range []string{"signal_hung_deploy", "signal_captured_inspect"} {
		t.Run(scenario, func(t *testing.T) { testFakeDeploymentSignalStopsChildren(t, scenario) })
	}
}

func testFakeDeploymentSignalStopsChildren(t *testing.T, scenario string) {
	f := newFakeDeploy(t)
	if err := os.WriteFile(f.scenario, []byte(scenario), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(f.shell, filepath.Join(f.root, "deploy.sh"), "--env-file", f.envFile)
	cmd.Env = []string{"PATH=" + filepath.Join(f.root, "bin") + ":" + os.Getenv("PATH"), "HOME=" + f.root, "TMPDIR=" + f.root, "mock_root=" + f.root, "LC_ALL=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	var childPID int
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if childPID != 0 {
			if group, err := syscall.Getpgid(childPID); err == nil && group != cmd.Process.Pid {
				_ = syscall.Kill(-group, syscall.SIGKILL)
			}
		}
		if !waited {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}
	}()
	pidFile := filepath.Join(f.root, "hang-pid")
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
		data, err := os.ReadFile(pidFile)
		if err == nil && strings.TrimSpace(string(data)) != "" {
			if _, err := fmt.Sscan(string(data), &childPID); err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("fake Docker child did not start")
	}
	startedCancel := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		waited = true
	case <-time.After(5 * time.Second):
		t.Fatal("deploy did not exit after SIGTERM")
	}
	if elapsed := time.Since(startedCancel); elapsed >= 4*time.Second {
		t.Fatalf("cancellation took %s despite a tracked child", elapsed)
	} else {
		t.Logf("SIGTERM stopped the parent in %s with a 30s normal timeout", elapsed)
	}
	stopped := false
	for until := time.Now().Add(time.Second); time.Now().Before(until); {
		if err := syscall.Kill(childPID, 0); err == syscall.ESRCH {
			stopped = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !stopped {
		t.Fatal("fake Docker child survived deploy cancellation")
	}
	workspace, err := os.ReadFile(filepath.Join(f.root, "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(workspace)); !os.IsNotExist(err) {
		t.Fatalf("private workspace remained after cancellation: %v", err)
	}
	if scenario == "signal_captured_inspect" && strings.Contains(f.commands(t), "stack deploy") {
		t.Fatal("canceled captured preflight reached deploy")
	}
}

func (f fakeDeploy) commands(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.ledger)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFakeDeploymentFailureMatrix(t *testing.T) {
	for _, tc := range []struct {
		scenario, want string
		success        bool
	}{
		{"preflight_failure", "invalid_volume", false},
		{"changed_identity", "identity_changed", false},
		{"success", "/live OK", true},
		{"no_change_redeploy", "/live OK", true},
		{"changed_input", "/live OK", true},
		{"deploy_failure", "deployment_failed", false},
		{"deploy_warning", "deployment_failed", false},
		{"hung_deploy", "deployment_failed", false},
		{"inspect_failure", "verification_failed", false},
		{"paused", "update paused", false},
		{"wrong_spec", "service contract", false},
		{"wrong_image_reference", "service image reference", false},
		{"wrong_running_image", "image_mismatch", false},
		{"curl_failure", "deployment_timeout", false},
		{"updating_then_done", "/live OK", true},
		{"convergence_timeout", "deployment_timeout", false},
		{"forged_logs", "deployment_failed", false},
		{"safe_logs", "deployment_failed", false},
		{"safe_stderr_logs", "deployment_failed", false},
		{"hung_logs", "deployment_failed", false},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			f := newFakeDeploy(t)
			out, err := f.run(t, tc.scenario)
			if tc.success == (err != nil) || !bytes.Contains(out, []byte(tc.want)) {
				t.Fatalf("result %v %q; want %q success=%v", err, out, tc.want, tc.success)
			}
			ledger := f.commands(t)
			if tc.scenario == "preflight_failure" || tc.scenario == "changed_identity" {
				if ledger != "" {
					t.Fatalf("failed preflight used Docker: %q", ledger)
				}
			} else if strings.Count(ledger, "stack deploy") != 1 {
				t.Fatalf("expected one deployment, got %q", ledger)
			}
			for _, forbidden := range []string{"stack rm", "service rm", "service rollback", "swarm init", "secret create", "volume create"} {
				if strings.Contains(ledger, forbidden) {
					t.Fatalf("unexpected mutation %q in %q", forbidden, ledger)
				}
			}
			if tc.scenario == "changed_input" {
				submitted, readErr := os.ReadFile(filepath.Join(f.root, "submitted"))
				if readErr != nil || string(submitted) != "ORIGINAL_RENDER\n" {
					t.Fatalf("changed input altered render: %v %q", readErr, submitted)
				}
			}
			if bytes.Contains(out, []byte("SECRET_SENTINEL")) {
				t.Fatalf("raw diagnostic leaked: %q", out)
			}
			if tc.scenario == "safe_logs" && !bytes.Contains(out, []byte("variable=PORT")) {
				t.Fatalf("allowlisted diagnostic missing: %q", out)
			}
			if tc.scenario == "safe_stderr_logs" && !bytes.Contains(out, []byte("container start failed code=docker_socket_invalid")) {
				t.Fatalf("stderr diagnostic missing: %q", out)
			}
		})
	}
}
