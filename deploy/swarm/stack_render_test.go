package swarm_test

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSemanticClientRender(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker CLI unavailable; render not executed")
	}
	cmd := exec.Command("docker", "--host", "unix:///tmp/moduleos-no-daemon.sock", "stack", "config", "-c", "stack.yml")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
		"MODULEOS_IMAGE=example/moduleos:v1", "MODULEOS_PORT=18888", "MODULEOS_BASE_DOMAIN=moduleos.example",
		"MODULEOS_API_KEY_SECRET=moduleos_api_key_v1", "MODULEOS_DATA_VOLUME=moduleos-data", "MODULEOS_APP_DATA_ROOT=/srv/moduleos-apps",
		"MODULEOS_INGRESS_NETWORK=moduleos-ingress", "MODULEOS_LOG_LEVEL=info", "MODULEOS_CORS_ALLOWED_ORIGINS=",
	}
	render, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("client render failed: %v", err)
	}
	missing := exec.Command("docker", "--host", "unix:///tmp/moduleos-no-daemon.sock", "stack", "config", "-c", "stack.yml")
	for _, item := range cmd.Env {
		if item != "MODULEOS_IMAGE=example/moduleos:v1" {
			missing.Env = append(missing.Env, item)
		}
	}
	if _, err := missing.CombinedOutput(); err == nil {
		t.Fatal("missing required image interpolation succeeded")
	}
	var doc struct {
		Version  string `yaml:"version"`
		Services map[string]struct {
			Image  string            `yaml:"image"`
			Env    map[string]string `yaml:"environment"`
			Deploy struct {
				Replicas  int `yaml:"replicas"`
				Placement struct {
					Constraints []string `yaml:"constraints"`
				} `yaml:"placement"`
				Update struct {
					Parallelism   int    `yaml:"parallelism"`
					Order         string `yaml:"order"`
					FailureAction string `yaml:"failure_action"`
					Monitor       string `yaml:"monitor"`
				} `yaml:"update_config"`
				Rollback struct {
					Parallelism   int    `yaml:"parallelism"`
					Order         string `yaml:"order"`
					FailureAction string `yaml:"failure_action"`
				} `yaml:"rollback_config"`
				Restart struct {
					Condition string `yaml:"condition"`
					Delay     string `yaml:"delay"`
				} `yaml:"restart_policy"`
			} `yaml:"deploy"`
			Ports []struct {
				Mode, Protocol    string
				Target, Published int
			} `yaml:"ports"`
			Volumes []struct {
				Type, Source, Target string
				ReadOnly             bool `yaml:"read_only"`
			} `yaml:"volumes"`
			Secrets []struct {
				Source, Target, UID, GID string
				Mode                     int
			} `yaml:"secrets"`
			CapAdd    []string `yaml:"cap_add"`
			CapDrop   []string `yaml:"cap_drop"`
			ReadOnly  bool     `yaml:"read_only"`
			StopGrace string   `yaml:"stop_grace_period"`
			Logging   struct {
				Driver  string
				Options map[string]string
			} `yaml:"logging"`
		} `yaml:"services"`
		Volumes map[string]struct {
			Name     string
			External bool
		} `yaml:"volumes"`
		Secrets map[string]struct {
			Name     string
			External bool
		} `yaml:"secrets"`
	}
	if err := yaml.Unmarshal(render, &doc); err != nil {
		t.Fatalf("render YAML invalid: %v", err)
	}
	service, exists := doc.Services["moduleosd"]
	if doc.Version != "3.8" || len(doc.Services) != 1 || !exists {
		t.Fatal("stack names/version changed")
	}
	if service.Image != "example/moduleos:v1" || service.Deploy.Replicas != 1 || !reflect.DeepEqual(service.Deploy.Placement.Constraints, []string{"node.role == manager"}) {
		t.Fatal("image/placement contract changed")
	}
	wantEnv := map[string]string{
		"API_KEY_FILE": "/run/secrets/moduleos_api_key", "DOCKER_ENDPOINT": "unix:///var/run/docker.sock", "SHUTDOWN_TIMEOUT": "15s",
		"BASE_DOMAIN": "moduleos.example", "ALLOWED_MOUNT_ROOTS": "/srv/moduleos-apps", "INGRESS_NETWORK": "moduleos-ingress", "LOG_LEVEL": "info", "CORS_ALLOWED_ORIGINS": "",
	}
	if !reflect.DeepEqual(service.Env, wantEnv) {
		t.Fatal("service environment differs from allowlist")
	}
	if len(service.Ports) != 1 || service.Ports[0].Mode != "host" || service.Ports[0].Protocol != "tcp" || service.Ports[0].Target != 3000 || service.Ports[0].Published != 18888 {
		t.Fatal("published port contract changed")
	}
	if len(service.Secrets) != 1 || service.Secrets[0].Source != "moduleos-api-key" || service.Secrets[0].Target != "moduleos_api_key" || service.Secrets[0].UID != "65532" || service.Secrets[0].GID != "65532" || service.Secrets[0].Mode != 256 {
		t.Fatal("secret mount contract changed")
	}
	if len(service.Volumes) != 3 || service.Volumes[0].Type != "volume" || service.Volumes[0].Target != "/var/lib/moduleos" || service.Volumes[0].ReadOnly || service.Volumes[1].Source != "/var/run/docker.sock" || !service.Volumes[1].ReadOnly || service.Volumes[2].Source != "/srv/moduleos-apps" || service.Volumes[2].Target != "/srv/moduleos-apps" || !service.Volumes[2].ReadOnly {
		t.Fatal("mount contract changed")
	}
	if !service.ReadOnly || service.StopGrace != "30s" || !reflect.DeepEqual(service.CapDrop, []string{"ALL"}) || !reflect.DeepEqual(service.CapAdd, []string{"CHOWN", "SETUID", "SETGID", "DAC_READ_SEARCH"}) {
		t.Fatal("hardening or stop budget changed")
	}
	if service.Deploy.Update.Parallelism != 1 || service.Deploy.Rollback.Parallelism != 1 || service.Deploy.Update.Order != "stop-first" || service.Deploy.Rollback.Order != "stop-first" || service.Deploy.Update.FailureAction != "pause" || service.Deploy.Rollback.FailureAction != "pause" || service.Deploy.Update.Monitor != "1m0s" || service.Deploy.Restart.Condition != "any" || service.Deploy.Restart.Delay != "5s" {
		t.Fatal("restart/update/rollback contract changed")
	}
	if service.Logging.Driver != "json-file" || !reflect.DeepEqual(service.Logging.Options, map[string]string{"max-size": "10m", "max-file": "5"}) {
		t.Fatal("log rotation contract changed")
	}
	if len(doc.Volumes) != 1 || doc.Volumes["moduleos-data"].Name != "moduleos-data" || !doc.Volumes["moduleos-data"].External || len(doc.Secrets) != 1 || doc.Secrets["moduleos-api-key"].Name != "moduleos_api_key_v1" || !doc.Secrets["moduleos-api-key"].External {
		t.Fatal("external resource contract changed")
	}
	var generic map[string]any
	if err := yaml.Unmarshal(render, &generic); err != nil {
		t.Fatal(err)
	}
	svc := generic["services"].(map[string]any)["moduleosd"].(map[string]any)
	for _, forbidden := range []string{"user", "entrypoint", "healthcheck", "build", "networks", "security_opt", "container_name", "restart"} {
		if _, exists := svc[forbidden]; exists {
			t.Fatalf("unsupported/forbidden service field: %s", forbidden)
		}
	}
	if _, exists := generic["networks"]; exists {
		t.Fatal("stack unexpectedly attaches explicit network")
	}
	if bytes.Contains(render, []byte("API_KEY=")) {
		t.Fatal("credential environment field present")
	}
}
