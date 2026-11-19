package swarm_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStackRendersDeterministicallyWithoutDaemon(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker CLI unavailable; the Linux Stack smoke job requires this test")
	}
	root := t.TempDir()
	stack := filepath.Join("stack.yml")
	var first []byte
	for i := 0; i < 2; i++ {
		cmd := exec.Command("docker", "--host", "unix:///tmp/moduleos-no-daemon.sock", "stack", "config", "-c", stack)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + root,
			"MODULEOS_IMAGE=example/moduleos:v1", "MODULEOS_PORT=18888",
			"MODULEOS_BASE_DOMAIN=moduleos.example", "MODULEOS_API_KEY_SECRET=moduleos_api_key_v1",
			"MODULEOS_DATA_VOLUME=moduleos-data", "MODULEOS_APP_DATA_ROOT=/srv/moduleos-apps",
			"MODULEOS_INGRESS_NETWORK=moduleos-ingress", "MODULEOS_LOG_LEVEL=info",
			"MODULEOS_CORS_ALLOWED_ORIGINS=",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("client render: %v: %s", err, out)
		}
		if i == 0 {
			first = out
		} else if !bytes.Equal(first, out) {
			t.Fatal("render changed with identical inputs")
		}
	}
	for _, want := range []string{"node.role == manager", "replicas: 1", "mode: host", "stop_grace_period: 30s", "SHUTDOWN_TIMEOUT: 15s", "failure_action: pause", "read_only: true", "mode: 256", "external: true"} {
		if !bytes.Contains(first, []byte(want)) {
			t.Errorf("render missing %q", want)
		}
	}
	if bytes.Contains(first, []byte("API_KEY=")) {
		t.Fatal("API key environment field found")
	}
	if bytes.Contains(first, []byte("Ignoring unsupported options")) {
		t.Fatal("Docker ignored a stack field")
	}
}

func TestMalformedInputFailsBeforeDocker(t *testing.T) {
	shell := contractShell(t)
	realTimeout, err := exec.LookPath("timeout")
	if err != nil {
		realTimeout, err = exec.LookPath("gtimeout")
	}
	if err != nil {
		t.Skip("GNU timeout unavailable locally")
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realTimeout, filepath.Join(bin, "timeout")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "findmnt"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "setsid"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(bin, "docker-called")
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	path := bin + ":" + os.Getenv("PATH")
	if _, err := os.Stat("/opt/homebrew/opt/coreutils/libexec/gnubin"); err == nil {
		path = bin + ":/opt/homebrew/opt/coreutils/libexec/gnubin:" + os.Getenv("PATH")
	}
	base := "MODULEOS_IMAGE=example/moduleos:v1\nMODULEOS_PORT=18888\nMODULEOS_BASE_DOMAIN=moduleos.example\nMODULEOS_API_KEY_SECRET=moduleos_api_key_v1\nMODULEOS_DATA_VOLUME=moduleos-data\nMODULEOS_APP_DATA_ROOT=/srv/moduleos-apps\n"
	for _, tc := range []struct{ name, content string }{
		{"duplicate", base + "MODULEOS_PORT=18888\n"},
		{"unknown", base + "EXTRA=value\n"},
		{"command substitution", base + "MODULEOS_LOG_LEVEL=$(touch /tmp/moduleos-should-not-exist)\n"},
		{"control byte", base + "MODULEOS_LOG_LEVEL=info\x00\n"},
		{"missing required", strings.Replace(base, "MODULEOS_PORT=18888\n", "", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "input")
			if err := os.WriteFile(file, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(shell, "preflight.sh", "--env-file", file)
			cmd.Env = []string{"PATH=" + path, "HOME=" + os.Getenv("HOME")}
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("unexpected success: %s", out)
			}
			if !bytes.Contains(out, []byte("invalid_input")) {
				t.Fatalf("wrong failure: %s", out)
			}
			if bytes.Contains(out, []byte("touch /tmp")) {
				t.Fatal("raw value leaked")
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("malformed input reached Docker")
			}
		})
	}
}

func TestLocalTagSubmissionUsesInspectedImageID(t *testing.T) {
	for _, utility := range []string{"jq", "sed"} {
		if _, err := exec.LookPath(utility); err != nil {
			t.Skipf("%s unavailable locally", utility)
		}
	}
	shell := contractShell(t)
	const imageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const script = `source "$1"
work_dir=$2
settings[MODULEOS_IMAGE]='example/moduleos:v1'
docker_env=(env MODULEOS_IMAGE=example/moduleos:v1)
	docker_call() { printf '%s\n' '{"Os":"linux","Architecture":"` + runtime.GOARCH + `","Id":"` + imageID + `","Config":{"Entrypoint":["/usr/local/bin/moduleosd-container","start"],"Healthcheck":{"Test":["CMD","/usr/local/bin/moduleosd-container","healthcheck"]},"Env":["ENV=production","PORT=3000"]}}'; }
validate_image
[[ "$resolve_image" == never && "$deployment_image" == '` + imageID + `' && "${docker_env[1]}" == 'MODULEOS_IMAGE=` + imageID + `' ]]
[[ $(normalized_image_ref 'docker.io/team/moduleos@sha256:abc') == 'team/moduleos@sha256:abc' ]]
`
	cmd := exec.Command(shell, "-c", script, "test", filepath.Join("lib.sh"), t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("local image identity fixture failed: %v: %s", err, out)
	}
}
