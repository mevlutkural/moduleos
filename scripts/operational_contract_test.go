package scripts_test

import (
	"gopkg.in/yaml.v3"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQualityRequiresOperationalAndNativeGates(t *testing.T) {
	data, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Name   string
			Needs  []string
			If     string
			RunsOn any `yaml:"runs-on"`
			Steps  []struct {
				Name, Run, If string
				Env           map[string]string
			}
		}
	}
	if yaml.Unmarshal(data, &workflow) != nil {
		t.Fatal("invalid workflow")
	}
	q := workflow.Jobs["quality"]
	if q.Name != "Quality" || !strings.Contains(q.If, "always()") {
		t.Fatal("required gate can be skipped")
	}
	for _, required := range []string{"source-quality", "integration", "operations", "container", "deploy-assets", "stack-smoke"} {
		if !strings.Contains(strings.Join(q.Needs, ","), required) {
			t.Fatal("missing required dependency")
		}
	}
	if len(q.Steps) != 1 {
		t.Fatal("unexpected aggregate gate")
	}
	step := q.Steps[0]
	names := []string{"SOURCE_RESULT", "INTEGRATION_RESULT", "OPERATIONS_RESULT", "CONTAINER_RESULT", "DEPLOY_RESULT", "STACK_RESULT"}
	for _, bad := range []string{"success", "failure", "cancelled", "skipped", ""} {
		for _, selected := range names {
			cmd := exec.Command("bash", "-c", step.Run)
			for _, name := range names {
				value := "success"
				if name == selected {
					value = bad
				}
				cmd.Env = append(cmd.Env, name+"="+value)
			}
			err := cmd.Run()
			if (err == nil) != (bad == "success") {
				t.Fatalf("gate accepted %s=%s", selected, bad)
			}
		}
	}
	integration := workflow.Jobs["operations"]
	found, summary := false, false
	for _, s := range integration.Steps {
		if strings.Contains(s.Run, "scripts/check-operations.sh") {
			found = true
			if s.Env["MODULEOS_OPERATIONS_DISPOSABLE"] != "1" || !strings.Contains(s.Run, "sudo env") {
				t.Fatal("unsafe operations invocation")
			}
		}
		if strings.Contains(s.Run, "operations/cmd/summary") {
			summary = strings.Contains(s.If, "always()")
		}
	}
	if !found || !summary {
		t.Fatal("operational journey or failure evidence omitted")
	}
	container := string(data)
	if !strings.Contains(container, "ubuntu-24.04-arm") || !strings.Contains(container, "MODULEOS_SCAN_IMAGE: \"1\"") {
		t.Fatal("native arm64 scan removed")
	}
}

func TestOperationalSummaryLogsValidatedOutputAndPropagatesFailure(t *testing.T) {
	data, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Jobs map[string]struct{ Steps []struct{ Name, Run string } }
	}
	if err := yaml.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	var run string
	for _, step := range w.Jobs["operations"].Steps {
		if step.Name == "Publish sanitized operational summary" {
			run = step.Run
		}
	}
	if run == "" {
		t.Fatal("summary step missing")
	}
	for _, failed := range []bool{false, true} {
		dir := t.TempDir()
		executable(t, dir, "sudo", `if [ "$1" = test ]; then exit 0; fi
if [ "$SUMMARY_FAIL" = 1 ]; then printf 'unvalidated-output'; exit 17; fi
printf '%s\n' '{"stage":"outage","status":"failed","reason":"timeout"}'
`)
		file := filepath.Join(dir, "summary")
		cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", run)
		flag := "0"
		if failed {
			flag = "1"
		}
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "SUMMARY_FAIL="+flag, "MODULEOS_OPERATIONS_EVIDENCE_DIR="+dir, "GITHUB_STEP_SUMMARY="+file)
		output, err := cmd.CombinedOutput()
		if (err != nil) != failed {
			t.Fatalf("summary failure propagation: %v", err)
		}
		written, _ := os.ReadFile(file)
		if failed {
			if len(output) != 0 || len(written) != 0 {
				t.Fatal("failed sanitizer output published")
			}
			continue
		}
		if !strings.Contains(string(output), `"stage":"outage"`) || string(written) != string(output) {
			t.Fatal("safe summary absent from logs or job summary")
		}
	}
}
func TestOperationalRunnerRefusesBeforeDocker(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "docker-called")
	executable(t, dir, "docker", "touch \"$MARKER\"\nexit 99\n")
	cmd := exec.Command("bash", "check-operations.sh")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MARKER="+marker, "MODULEOS_OPERATIONS_DISPOSABLE=0", "MODULEOS_OPERATIONS_EVIDENCE_DIR="+dir)
	if err := cmd.Run(); err == nil {
		t.Fatal("missing ack accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Docker touched before refusal")
	}
	data, err := os.ReadFile(filepath.Join(dir, "results.jsonl"))
	if err != nil || !strings.Contains(string(data), `"status":"failed"`) {
		t.Fatal("missing setup failure evidence")
	}
	data, err = os.ReadFile("check-swarm-stack.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MODULEOS_STACK_OPERATIONS", "operations-cleanup", "operations-ledger.json", "-tags=operations", "TestOperationalJourney", "LocalNodeState", "inactive", "cleanup failed"} {
		if !strings.Contains(strings.ToLower(string(data)), strings.ToLower(want)) {
			t.Fatalf("runner lost guard/cleanup/journey %s", want)
		}
	}
}

func TestOperationalOutcomeWaitsForCleanupAndSeparatesFailures(t *testing.T) {
	for _, test := range []struct{ suite, cleanup, expected string }{{"0", "0", "cleanup passed none\nfinal passed none\n"}, {"1", "0", "cleanup passed none\nfinal failed runner_failed\n"}, {"0", "1", "cleanup failed cleanup_failed\nfinal failed cleanup_failed\n"}, {"143", "0", "cleanup passed none\nfinal failed cancelled\n"}} {
		cmd := exec.Command("bash", "-c", `source operations-evidence.sh; operations_event(){ printf '%s %s %s\n' "$1" "$2" "$3"; }; operations_outcome "$1" "$2"`, "test", test.suite, test.cleanup)
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != test.expected {
			t.Fatalf("incorrect outcome: %v %s", err, out)
		}
	}
	source, err := os.ReadFile("check-swarm-stack.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	report := strings.Index(text, `operations_outcome "$suite_result" "$result"`)
	for _, mustFinishFirst := range []string{`rm -- "$capture_file"`, `rm -rf -- "$work_dir" "$app_root"`} {
		if report < strings.LastIndex(text, mustFinishFirst) {
			t.Fatal("success can precede cleanup completion")
		}
	}
}
