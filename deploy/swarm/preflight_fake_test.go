package swarm_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPreflightAgainstFailClosedFakeDocker(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux host/socket preflight fixture; no Docker daemon is contacted")
	}
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Fatal("Linux Quality runner needs a local Docker socket for socket-path validation")
	}
	realDocker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("Docker CLI needed for client-only stack rendering")
	}
	realStat, err := exec.LookPath("stat")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, scenario, want string
		success              bool
	}{
		{"valid", "valid", "preflight passed", true},
		{"missing secret", "missing_secret", "missing_secret", false},
		{"wrong secret identity", "wrong_secret", "missing_secret", false},
		{"missing volume", "missing_volume", "invalid_volume", false},
		{"plugin volume", "plugin_volume", "invalid_volume", false},
		{"remote volume", "remote_volume", "invalid_volume", false},
		{"missing ingress", "missing_ingress", "preflight passed", true},
		{"nonattachable ingress", "bad_ingress", "invalid_ingress_network", false},
		{"wrong ingress driver", "wrong_ingress_driver", "invalid_ingress_network", false},
		{"explicit missing ingress", "missing_explicit_ingress", "invalid_ingress_network", false},
		{"rootless", "rootless", "unsupported_host", false},
		{"socket group zero", "socket_gid_zero", "unsupported_host", false},
		{"inactive swarm", "inactive", "unsupported_host", false},
		{"worker swarm", "worker", "unsupported_host", false},
		{"two nodes", "two_nodes", "unsupported_host", false},
		{"drained manager", "drained", "unsupported_host", false},
		{"foreign service", "foreign_service", "unsupported_host", false},
		{"old API", "old_api", "unsupported_host", false},
		{"malformed API", "bad_api", "unsupported_host", false},
		{"wrong image OS", "wrong_image_os", "MODULEOS_IMAGE platform", false},
		{"wrong image contract", "wrong_image_contract", "MODULEOS_IMAGE contract", false},
		{"app root aliases volume", "root_volume_overlap", "protected path overlap", false},
		{"app root aliases Docker", "root_docker_overlap", "protected path overlap", false},
		{"relative app root", "root_relative", "invalid_app_root", false},
		{"missing app root", "root_missing", "invalid_app_root", false},
		{"symlink app root", "root_symlink", "invalid_app_root", false},
		{"protected app root", "root_protected", "protected path overlap", false},
		{"untraversable app root", "root_untraversable", "cannot traverse", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "moduleos-preflight-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			if err := os.Chmod(root, 0755); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "bin")
			appRoot := filepath.Join(root, "apps")
			volumeRoot := filepath.Join(root, "volume")
			dockerRoot := filepath.Join(root, "docker-root")
			for _, dir := range []string{bin, appRoot, volumeRoot, dockerRoot} {
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			for name, value := range map[string]string{"scenario": tc.scenario, "app-root": appRoot, "volume-root": volumeRoot, "docker-root": dockerRoot, "real-docker": realDocker} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			fakeDocker := `#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -z "${DOCKER_HOST:-}${DOCKER_CONTEXT:-}${DOCKER_TLS_VERIFY:-}${DOCKER_API_VERSION:-}${API_KEY:-}" ] || exit 98
[ "${MODULEOS_PORT:-}" = 18888 ] || exit 98
printf '%s\n' "$*" >> "$root/ledger"
if [ "$1" = --host ]; then [ "$2" = unix:///var/run/docker.sock ] || exit 98; shift 2; fi
scenario=$(cat "$root/scenario")
case "$1 $2" in
  'info --format')
    state=active; control=true; security='[]'
    [ "$scenario" != rootless ] || security='["name=rootless"]'
    [ "$scenario" != inactive ] || state=inactive
    [ "$scenario" != worker ] || control=false
    printf '{"OSType":"linux","SecurityOptions":%s,"Swarm":{"LocalNodeState":"%s","ControlAvailable":%s},"DockerRootDir":"%s"}\n' "$security" "$state" "$control" "$(cat "$root/docker-root")"
    exit 0 ;;
  'version --format')
    [ "$scenario" != old_api ] || { echo 1.41; exit 0; }
    [ "$scenario" != bad_api ] || { echo invalid; exit 0; }
    echo 1.55; exit 0 ;;
  'node ls')
    availability=Active; [ "$scenario" != drained ] || availability=Drain
    printf '{"Status":"Ready","Availability":"%s","Self":true,"ManagerStatus":"Leader"}\n' "$availability"
    [ "$scenario" != two_nodes ] || printf '%s\n' '{"Status":"Ready","Availability":"Active","Self":false,"ManagerStatus":"Reachable"}'
    exit 0 ;;
  'service ls')
    [ "$scenario" != foreign_service ] || echo moduleos-system_moduleosd
    exit 0 ;;
  'service inspect')
    printf '%s\n' '{"Spec":{"Labels":{"com.docker.stack.namespace":"foreign"}}}'
    exit 0 ;;
  'secret inspect')
    [ "$scenario" != missing_secret ] || exit 1
    name=moduleos_api_key_v1
    [ "$scenario" != wrong_secret ] || name=other_v1
    printf '{"ID":"secret-fixture-id","Spec":{"Name":"%s"}}\n' "$name"; exit 0 ;;
  'volume inspect')
    [ "$scenario" != missing_volume ] || exit 1
    driver=local; opts='{}'; mount=$(cat "$root/volume-root")
    [ "$scenario" != plugin_volume ] || driver=plugin
    [ "$scenario" != remote_volume ] || opts='{"type":"nfs"}'
    [ "$scenario" != root_volume_overlap ] || mount=$(cat "$root/app-root")
    printf '{"Driver":"%s","Options":%s,"Mountpoint":"%s"}\n' "$driver" "$opts" "$mount"; exit 0 ;;
  'network inspect')
    [ "$scenario" != missing_ingress ] && [ "$scenario" != missing_explicit_ingress ] || exit 1
    attach=true; driver=overlay
    [ "$scenario" != bad_ingress ] || attach=false
    [ "$scenario" != wrong_ingress_driver ] || driver=bridge
    printf '{"Id":"network-fixture-id","Driver":"%s","Scope":"swarm","Attachable":%s}\n' "$driver" "$attach"; exit 0 ;;
  'network ls') exit 0 ;;
  'image inspect')
    os=linux; entry='["/usr/local/bin/moduleosd-container","start"]'
    [ "$scenario" != wrong_image_os ] || os=windows
    [ "$scenario" != wrong_image_contract ] || entry='["/bin/false"]'
    printf '{"Os":"%s","Architecture":"amd64","Id":"` + fixtureImageID + `","Config":{"Entrypoint":%s,"Healthcheck":{"Test":["CMD","/usr/local/bin/moduleosd-container","healthcheck"]},"Env":["ENV=production","PORT=3000"]}}\n' "$os" "$entry"; exit 0 ;;
  'stack config')
    shift 2
    exec "$(cat "$root/real-docker")" --host unix:///tmp/moduleos-no-daemon.sock stack config "$@" ;;
esac
exit 99
`
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDocker), 0700); err != nil {
				t.Fatal(err)
			}
			statScript := fmt.Sprintf(`#!/bin/sh
if [ "$1" = -c ] && [ "$2" = %%g ] && [ "$3" = /var/run/docker.sock ]; then
  if [ "$(cat "$(dirname "$0")/../scenario")" = socket_gid_zero ]; then echo 0; else echo 999; fi
  exit 0
fi
exec %q "$@"
`, realStat)
			if err := os.WriteFile(filepath.Join(bin, "stat"), []byte(statScript), 0700); err != nil {
				t.Fatal(err)
			}
			input := strings.Replace(validEnv, "/srv/moduleos-apps", appRoot, 1)
			if tc.scenario == "root_docker_overlap" {
				input = strings.Replace(input, appRoot, dockerRoot, 1)
			}
			if tc.scenario == "root_relative" {
				input = strings.Replace(input, appRoot, "relative/apps", 1)
			}
			if tc.scenario == "root_missing" {
				input = strings.Replace(input, appRoot, filepath.Join(root, "missing"), 1)
			}
			if tc.scenario == "root_protected" {
				input = strings.Replace(input, appRoot, "/usr/local/bin", 1)
			}
			if tc.scenario == "root_symlink" {
				link := filepath.Join(root, "apps-link")
				if err := os.Symlink(appRoot, link); err != nil {
					t.Fatal(err)
				}
				input = strings.Replace(input, appRoot, link, 1)
			}
			if tc.scenario == "root_untraversable" {
				if err := os.Chmod(appRoot, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.scenario == "missing_explicit_ingress" {
				input += "MODULEOS_INGRESS_NETWORK=private-net\n"
			}
			file := filepath.Join(root, "operator.env")
			if err := os.WriteFile(file, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(contractShell(t), "preflight.sh", "--env-file", file)
			cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + root, "LC_ALL=C", "DOCKER_HOST=tcp://invalid:2375", "DOCKER_CONTEXT=foreign", "DOCKER_TLS_VERIFY=1", "DOCKER_API_VERSION=1.0", "API_KEY=ambient-secret", "MODULEOS_PORT=9"}
			out, err := cmd.CombinedOutput()
			if tc.success == (err != nil) || !bytes.Contains(out, []byte(tc.want)) {
				t.Fatalf("preflight result %v %q; want %q success=%v", err, out, tc.want, tc.success)
			}
			ledger, readErr := os.ReadFile(filepath.Join(root, "ledger"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, line := range strings.Split(string(ledger), "\n") {
				if strings.Contains(line, " deploy") || strings.Contains(line, " create") || strings.Contains(line, " rm") || strings.Contains(line, " update") || strings.Contains(line, " rollback") {
					t.Fatalf("preflight mutation: %q", line)
				}
			}
			if bytes.Contains(out, []byte("ambient-secret")) {
				t.Fatal("ambient credential leaked")
			}
		})
	}
}

func TestCriticalIdentityRecheck(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		scenario, want string
		valid          bool
	}{
		{"valid", "recheck OK", true},
		{"secret_swapped", "identity_changed: secret", false},
		{"volume_swapped", "identity_changed: volume", false},
		{"network_swapped", "identity_changed: ingress network", false},
		{"service_appeared", "identity_changed: service", false},
		{"service_swapped", "identity_changed: service", false},
		{"image_swapped", "identity_changed: image", false},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			script := `work_dir=$MOCK_WORK_DIR
settings[MODULEOS_API_KEY_SECRET]=moduleos_api_key_v1
settings[MODULEOS_DATA_VOLUME]=moduleos-data
settings[MODULEOS_INGRESS_NETWORK]=moduleos-ingress
settings[MODULEOS_IMAGE]=example/moduleos:v1
secret_id=secret-1
network_id=network-1
volume_root=$(realpath -e -- "$MOCK_ROOT")
volume_identity=1:2
image_id=` + fixtureImageID + `
resolve_image=never
if [[ $MOCK_CASE == service_swapped ]]; then existing_service_id=service-1; fi
stat() { printf '1:2\n'; }
docker_call() {
  case "$1 $2" in
    'secret inspect')
      local id=secret-1
      [[ $MOCK_CASE != secret_swapped ]] || id=secret-2
      printf '{"ID":"%s","Spec":{"Name":"moduleos_api_key_v1"}}\n' "$id" ;;
    'volume inspect')
      local mount=$MOCK_ROOT
      [[ $MOCK_CASE != volume_swapped ]] || mount=/missing-volume-root
      printf '{"Driver":"local","Options":{},"Mountpoint":"%s"}\n' "$mount" ;;
    'network inspect')
      local id=network-1
      [[ $MOCK_CASE != network_swapped ]] || id=network-2
      printf '{"Id":"%s","Driver":"overlay","Scope":"swarm","Attachable":true}\n' "$id" ;;
    'service ls')
      if [[ $MOCK_CASE == service_appeared || $MOCK_CASE == service_swapped ]]; then printf 'moduleos-system_moduleosd\n'; fi ;;
    'service inspect')
      printf '%s\n' '{"ID":"service-2","Spec":{"Labels":{"com.docker.stack.namespace":"moduleos-system"}}}' ;;
    'image inspect')
      local id=` + fixtureImageID + `
      [[ $MOCK_CASE != image_swapped ]] || id=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
      printf '{"Id":"%s"}\n' "$id" ;;
    *) return 99 ;;
  esac
}
verify_critical_identities
printf 'recheck OK'`
			out, err := contractRun(t, "lib.sh", "unused", script, "MOCK_CASE="+tc.scenario, "MOCK_ROOT="+root, "MOCK_WORK_DIR="+t.TempDir())
			if tc.valid {
				if err != nil || string(out) != tc.want {
					t.Fatalf("recheck %v %q", err, out)
				}
			} else if err == nil || !bytes.Contains(out, []byte(tc.want)) {
				t.Fatalf("recheck accepted/wrong reason: %v %q", err, out)
			}
		})
	}
}
