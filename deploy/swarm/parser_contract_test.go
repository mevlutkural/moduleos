package swarm_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const validEnv = "MODULEOS_IMAGE=example/moduleos:v1\nMODULEOS_PORT=18888\nMODULEOS_BASE_DOMAIN=moduleos.example\nMODULEOS_API_KEY_SECRET=moduleos_api_key_v1\nMODULEOS_DATA_VOLUME=moduleos-data\nMODULEOS_APP_DATA_ROOT=/srv/moduleos-apps\n"

func contractShell(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"/opt/homebrew/bin/bash", "/usr/local/bin/bash", "bash"} {
		resolved, err := exec.LookPath(candidate)
		if err == nil && exec.Command(resolved, "-c", "((BASH_VERSINFO[0] >= 4))").Run() == nil {
			return resolved
		}
	}
	t.Skip("Bash 4+ unavailable; parser contract tests not executed")
	return ""
}

func contractRun(t *testing.T, source, envFile, script string, extraEnv ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(contractShell(t), "-c", `source "$1"; `+script, "test", source, envFile)
	path := os.Getenv("PATH")
	if _, err := os.Stat("/opt/homebrew/opt/coreutils/libexec/gnubin"); err == nil {
		path = "/opt/homebrew/opt/coreutils/libexec/gnubin:" + path
	}
	cmd.Env = append([]string{"PATH=" + path, "HOME=" + os.Getenv("HOME"), "LC_ALL=C"}, extraEnv...)
	return cmd.CombinedOutput()
}

func TestParserContract(t *testing.T) {
	for _, tc := range []struct {
		name, input, want, output string
	}{
		{"minimal", validEnv, "", "moduleos-ingress|info|"},
		{"full", "# comment\n\n" + validEnv + "MODULEOS_INGRESS_NETWORK=private-net\nMODULEOS_LOG_LEVEL=warn\nMODULEOS_CORS_ALLOWED_ORIGINS=https://example.com:443", "", "private-net|warn|https://example.com:443"},
		{"last line without LF", strings.TrimSuffix(validEnv, "\n"), "", "moduleos-ingress|info|"},
		{"duplicate equal", validEnv + "MODULEOS_PORT=18888\n", "duplicate", ""},
		{"duplicate different", validEnv + "MODULEOS_PORT=1234\n", "duplicate", ""},
		{"unknown", validEnv + "OTHER=value\n", "unknown key", ""},
		{"export", "export " + validEnv, "unknown key", ""},
		{"no separator", validEnv + "BADLINE\n", "malformed", ""},
		{"space before key", " MODULEOS_IMAGE=example/moduleos:v1\n" + strings.TrimPrefix(validEnv, "MODULEOS_IMAGE=example/moduleos:v1\n"), "unknown key", ""},
		{"space after value", strings.Replace(validEnv, "18888", "18888 ", 1), "whitespace", ""},
		{"single quote", validEnv + "MODULEOS_LOG_LEVEL='info'\n", "forbidden character", ""},
		{"double quote", validEnv + "MODULEOS_LOG_LEVEL=\"info\"\n", "forbidden character", ""},
		{"dollar", validEnv + "MODULEOS_LOG_LEVEL=$(touch /tmp/moduleos-parser-must-not-run)\n", "forbidden character", ""},
		{"backtick", validEnv + "MODULEOS_LOG_LEVEL=`id`\n", "forbidden character", ""},
		{"CRLF", strings.ReplaceAll(validEnv, "\n", "\r\n"), "control byte", ""},
		{"NUL", validEnv + "MODULEOS_LOG_LEVEL=info\x00\n", "control byte", ""},
		{"ESC", validEnv + "MODULEOS_LOG_LEVEL=info\x1b\n", "control byte", ""},
		{"oversize file", validEnv + strings.Repeat("#", 65537), "too large", ""},
		{"oversize line", validEnv + "#" + strings.Repeat("x", 4097), "too long", ""},
		{"empty required", strings.Replace(validEnv, "MODULEOS_PORT=18888", "MODULEOS_PORT=", 1), "missing MODULEOS_PORT", ""},
		{"missing required", strings.Replace(validEnv, "MODULEOS_PORT=18888\n", "", 1), "missing MODULEOS_PORT", ""},
		{"empty optional ingress", validEnv + "MODULEOS_INGRESS_NETWORK=\n", "empty MODULEOS_INGRESS_NETWORK", ""},
		{"empty optional log", validEnv + "MODULEOS_LOG_LEVEL=\n", "empty MODULEOS_LOG_LEVEL", ""},
		{"port zero", strings.Replace(validEnv, "18888", "0", 1), "MODULEOS_PORT", ""},
		{"port minimum", strings.Replace(validEnv, "18888", "1", 1), "", "moduleos-ingress|info|"},
		{"port max", strings.Replace(validEnv, "18888", "65535", 1), "", "moduleos-ingress|info|"},
		{"port overflow", strings.Replace(validEnv, "18888", "65536", 1), "MODULEOS_PORT", ""},
		{"port leading zero", strings.Replace(validEnv, "18888", "0123", 1), "MODULEOS_PORT", ""},
		{"port signed", strings.Replace(validEnv, "18888", "+80", 1), "MODULEOS_PORT", ""},
		{"port negative", strings.Replace(validEnv, "18888", "-80", 1), "MODULEOS_PORT", ""},
		{"port embedded space", strings.Replace(validEnv, "18888", "18 888", 1), "MODULEOS_PORT", ""},
		{"port huge", strings.Replace(validEnv, "18888", strings.Repeat("9", 50), 1), "MODULEOS_PORT", ""},
		{"raw image ID", strings.Replace(validEnv, "example/moduleos:v1", "sha256:"+strings.Repeat("a", 64), 1), "", "moduleos-ingress|info|"},
		{"secret unversioned", strings.Replace(validEnv, "moduleos_api_key_v1", "moduleos_api_key", 1), "MODULEOS_API_KEY_SECRET", ""},
		{"secret oversized", strings.Replace(validEnv, "moduleos_api_key_v1", strings.Repeat("s", 63)+"_v1", 1), "MODULEOS_API_KEY_SECRET", ""},
		{"bad CORS", validEnv + "MODULEOS_CORS_ALLOWED_ORIGINS=https://example.com/path\n", "MODULEOS_CORS_ALLOWED_ORIGINS", ""},
		{"IPv6 CORS", validEnv + "MODULEOS_CORS_ALLOWED_ORIGINS=https://[::1]:443\n", "", "moduleos-ingress|info|https://[::1]:443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "operator.env")
			if err := os.WriteFile(file, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := contractRun(t, "lib.sh", file, `parse_env "$2"; printf '%s|%s|%s' "${settings[MODULEOS_INGRESS_NETWORK]}" "${settings[MODULEOS_LOG_LEVEL]}" "${settings[MODULEOS_CORS_ALLOWED_ORIGINS]}"`, "MODULEOS_LOG_LEVEL=error", "API_KEY=ambient-sentinel")
			if tc.want == "" {
				if err != nil || string(out) != tc.output {
					t.Fatalf("parse result: %v %q; want %q", err, out, tc.output)
				}
			} else if err == nil || !bytes.Contains(out, []byte(tc.want)) {
				t.Fatalf("parse error: %v %q; want %q", err, out, tc.want)
			}
			if bytes.Contains(out, []byte("ambient-sentinel")) || bytes.Contains(out, []byte("touch /tmp")) {
				t.Fatal("input value leaked")
			}
		})
	}
	if _, err := os.Stat("/tmp/moduleos-parser-must-not-run"); err == nil {
		t.Fatal("command substitution executed")
	}
}

func TestEveryRequiredKeyAndAmbientPoisoning(t *testing.T) {
	for _, pair := range []string{
		"MODULEOS_IMAGE=example/moduleos:v1\n",
		"MODULEOS_PORT=18888\n",
		"MODULEOS_BASE_DOMAIN=moduleos.example\n",
		"MODULEOS_API_KEY_SECRET=moduleos_api_key_v1\n",
		"MODULEOS_DATA_VOLUME=moduleos-data\n",
		"MODULEOS_APP_DATA_ROOT=/srv/moduleos-apps\n",
	} {
		key := strings.SplitN(pair, "=", 2)[0]
		for _, mode := range []string{"missing", "empty"} {
			t.Run(key+"/"+mode, func(t *testing.T) {
				input := strings.Replace(validEnv, pair, "", 1)
				if mode == "empty" {
					input += key + "=\n"
				}
				file := filepath.Join(t.TempDir(), "operator.env")
				if err := os.WriteFile(file, []byte(input), 0600); err != nil {
					t.Fatal(err)
				}
				out, err := contractRun(t, "lib.sh", file, `parse_env "$2"`)
				if err == nil || !bytes.Contains(out, []byte("missing "+key)) {
					t.Fatalf("required key accepted: %v %q", err, out)
				}
			})
		}
	}
	file := filepath.Join(t.TempDir(), "operator.env")
	if err := os.WriteFile(file, []byte(validEnv), 0600); err != nil {
		t.Fatal(err)
	}
	ambient := []string{"API_KEY=ambient-sentinel"}
	for _, key := range []string{"MODULEOS_IMAGE", "MODULEOS_PORT", "MODULEOS_BASE_DOMAIN", "MODULEOS_API_KEY_SECRET", "MODULEOS_DATA_VOLUME", "MODULEOS_APP_DATA_ROOT", "MODULEOS_INGRESS_NETWORK", "MODULEOS_LOG_LEVEL", "MODULEOS_CORS_ALLOWED_ORIGINS"} {
		ambient = append(ambient, key+"=ambient-sentinel")
	}
	out, err := contractRun(t, "lib.sh", file, `parse_env "$2"; printf '%s|%s|%s' "${settings[MODULEOS_IMAGE]}" "${settings[MODULEOS_INGRESS_NETWORK]}" "${settings[MODULEOS_LOG_LEVEL]}"`, ambient...)
	if err != nil || string(out) != "example/moduleos:v1|moduleos-ingress|info" {
		t.Fatalf("ambient values influenced parsing: %v %q", err, out)
	}
}

func TestParserRejectsNonRegularFile(t *testing.T) {
	root := t.TempDir()
	for _, kind := range []string{"directory", "symlink", "fifo", "device"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(root, kind)
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(root, "regular")
				if err := os.WriteFile(target, []byte(validEnv), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Skipf("FIFO unavailable: %v", err)
				}
			case "device":
				path = "/dev/null"
			}
			out, err := contractRun(t, "lib.sh", path, `parse_env "$2"`)
			if err == nil || !bytes.Contains(out, []byte("regular file")) {
				t.Fatalf("%s accepted: %v %q", kind, err, out)
			}
		})
	}
}

func TestParserRejectsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode-000 fixture")
	}
	file := filepath.Join(t.TempDir(), "unreadable.env")
	if err := os.WriteFile(file, []byte(validEnv), 0000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0000); err != nil {
		t.Fatal(err)
	}
	out, err := contractRun(t, "lib.sh", file, `parse_env "$2"`)
	if err == nil || !bytes.Contains(out, []byte("unreadable environment file")) {
		t.Fatalf("unreadable file accepted: %v %q", err, out)
	}
}

func TestPathOverlapUsesDirectoryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		left, right string
		overlap     bool
	}{
		{"/srv/apps", "/srv/apps", true},
		{"/srv/apps", "/srv/apps/child", true},
		{"/srv/apps/child", "/srv/apps", true},
		{"/srv/apps", "/srv/apps-backup", false},
	} {
		script := `if is_overlap "$2" "$MOCK_OTHER"; then printf yes; else printf no; fi`
		out, err := contractRun(t, "lib.sh", tc.left, script, "MOCK_OTHER="+tc.right)
		if err != nil || (string(out) == "yes") != tc.overlap {
			t.Fatalf("overlap %q / %q: %v %q", tc.left, tc.right, err, out)
		}
	}
}

func TestImageReferenceGrammar(t *testing.T) {
	const id = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digest := "example/moduleos@" + id
	for _, tc := range []struct {
		name, ref, scenario, want string
		valid                     bool
	}{
		{"local tag", "localhost:5000/moduleos:v1", "valid", "never|" + id, true},
		{"digest", digest, "valid", "always|" + digest, true},
		{"raw ID", id, "valid", "MODULEOS_IMAGE tag", false},
		{"untagged", "example/moduleos", "valid", "MODULEOS_IMAGE tag", false},
		{"latest", "example/moduleos:latest", "valid", "MODULEOS_IMAGE tag", false},
		{"short digest", "example/moduleos@sha256:abc", "valid", "MODULEOS_IMAGE digest", false},
		{"missing local", "example/moduleos:v1", "missing", "local MODULEOS_IMAGE missing", false},
		{"wrong OS", "example/moduleos:v1", "wrong_os", "MODULEOS_IMAGE platform", false},
		{"wrong architecture", "example/moduleos:v1", "wrong_arch", "MODULEOS_IMAGE platform", false},
		{"wrong image contract", "example/moduleos:v1", "wrong_contract", "MODULEOS_IMAGE contract", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := `work_dir=$MOCK_WORK_DIR
settings[MODULEOS_IMAGE]=$2
docker_env=(env MODULEOS_IMAGE="$2")
uname() { if [[ ${1:-} == -m ]]; then echo x86_64; else echo Linux; fi; }
docker_call() {
  [[ $MOCK_CASE != missing ]] || return 1
  local os=linux arch=amd64 entry='["/usr/local/bin/moduleosd-container","start"]'
  [[ $MOCK_CASE != wrong_os ]] || os=windows
  [[ $MOCK_CASE != wrong_arch ]] || arch=arm64
  [[ $MOCK_CASE != wrong_contract ]] || entry='["/bin/false"]'
  printf '{"Os":"%s","Architecture":"%s","Id":"` + id + `","Config":{"Entrypoint":%s,"Healthcheck":{"Test":["CMD","/usr/local/bin/moduleosd-container","healthcheck"]},"Env":["ENV=production","PORT=3000"]}}\n' "$os" "$arch" "$entry"
}

validate_image
printf '%s|%s' "$resolve_image" "$deployment_image"`
			out, err := contractRun(t, "lib.sh", tc.ref, script, "MOCK_CASE="+tc.scenario, "MOCK_WORK_DIR="+t.TempDir())
			if tc.valid {
				if err != nil || string(out) != tc.want {
					t.Fatalf("image ref %v %q; want %q", err, out, tc.want)
				}
			} else if err == nil || !bytes.Contains(out, []byte(tc.want)) {
				t.Fatalf("image ref accepted/wrong reason: %v %q", err, out)
			}
		})
	}
}

func TestRenderedStopBudgetAndWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, render, warning, want string
		valid                       bool
	}{
		{"plain", "      SHUTDOWN_TIMEOUT: 15s\n    stop_grace_period: 30s\n", "", "render OK", true},
		{"double quoted", "      SHUTDOWN_TIMEOUT: \"15s\"\n    stop_grace_period: \"30s\"\n", "", "render OK", true},
		{"single quoted", "      SHUTDOWN_TIMEOUT: '15s'\n    stop_grace_period: '30s'\n", "", "render OK", true},
		{"equal", "      SHUTDOWN_TIMEOUT: 15s\n    stop_grace_period: 15s\n", "", "stop budget", false},
		{"reversed", "      SHUTDOWN_TIMEOUT: 30s\n    stop_grace_period: 15s\n", "", "stop budget", false},
		{"missing", "      SHUTDOWN_TIMEOUT: 15s\n", "", "missing stop budget", false},
		{"duplicate", "      SHUTDOWN_TIMEOUT: 15s\n      SHUTDOWN_TIMEOUT: 15s\n    stop_grace_period: 30s\n", "", "missing stop budget", false},
		{"invalid", "      SHUTDOWN_TIMEOUT: abc\n    stop_grace_period: 30s\n", "", "missing stop budget", false},
		{"overflow", "      SHUTDOWN_TIMEOUT: 99999999s\n    stop_grace_period: 999999999s\n", "", "stop budget", false},
		{"warning", "      SHUTDOWN_TIMEOUT: 15s\n    stop_grace_period: 30s\n", "Ignoring unsupported option", "Docker warning", false},
		{"secret field", "      SHUTDOWN_TIMEOUT: 15s\n    stop_grace_period: 30s\n      API_KEY=unsafe\n", "", "unexpected API_KEY", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "render-source")
			if err := os.WriteFile(input, []byte(tc.render), 0600); err != nil {
				t.Fatal(err)
			}
			script := `work_dir=$WORK_DIR
docker_call() { cat "$MOCK_RENDER"; if [[ -n $MOCK_WARNING ]]; then printf '%s\n' "$MOCK_WARNING" >&2; fi; }
validate_render
printf 'render OK'`
			out, err := contractRun(t, "lib.sh", "unused", script, "WORK_DIR="+root, "MOCK_RENDER="+input, "MOCK_WARNING="+tc.warning)
			if tc.valid {
				if err != nil || string(out) != tc.want {
					t.Fatalf("render %v %q", err, out)
				}
			} else if err == nil || !bytes.Contains(out, []byte(tc.want)) {
				t.Fatalf("render accepted/wrong failure: %v %q; want %q", err, out, tc.want)
			}
		})
	}
}

func TestPublicCommandArgumentBoundary(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "docker-called")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"preflight.sh", "deploy.sh"} {
		for _, tc := range []struct {
			name    string
			args    []string
			success bool
		}{
			{"help", []string{"--help"}, true},
			{"missing", nil, false},
			{"unknown", []string{"--bad"}, false},
			{"extra", []string{"--env-file", "file", "extra"}, false},
		} {
			t.Run(script+"/"+tc.name, func(t *testing.T) {
				cmd := exec.Command(contractShell(t), append([]string{script}, tc.args...)...)
				cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + root}
				out, err := cmd.CombinedOutput()
				if tc.success == (err != nil) || !bytes.Contains(out, []byte("Usage:")) {
					t.Fatalf("argument handling: %v %q", err, out)
				}
				if _, err := os.Stat(marker); err == nil {
					t.Fatal("Docker called before argument validation")
				}
			})
		}
	}
}
