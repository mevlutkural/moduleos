//go:build operations

package operations

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOperationalJourney(t *testing.T) {
	j := newJourney(t)
	var originalTag, projectID string
	var deploymentA deployment
	j.stage("setup", func() { j.setupFixtures() })
	j.stage("boot", func() {
		j.deployOfficial()
		var live struct{ Status string }
		j.call("GET", "/live", nil, "", "", 200, &live)
		j.require(live.Status == "live", "liveness_contract")
		var version struct {
			Version   string `json:"version"`
			Commit    string `json:"commit"`
			BuildDate string `json:"build_date"`
		}
		j.call("GET", "/version", nil, "", "", 200, &version)
		j.require(version.Commit == j.source && version.Version == "stack-smoke" && version.BuildDate != "", "version_identity")
		s, _, _, err := j.request(j.ctx, "POST", "/projects", map[string]string{"name": "unauthenticated", "slug": "unauth-" + j.run}, "", "", false)
		j.require(err == nil && s == 401, "authentication_boundary")
		j.call("GET", "/projects/unauth-"+j.run, nil, "", "", 404, nil)
		var project struct{ ID, Slug string }
		j.call("POST", "/projects", map[string]string{"name": "Operational " + j.run, "slug": j.project}, "", "", 201, &project)
		projectID = project.ID
		j.require(projectID != "" && project.Slug == j.project, "project_identity")
		root := os.Getenv("MODULEOS_STACK_APP_ROOT")
		child := filepath.Join(root, "data")
		j.require(os.Mkdir(child, 0755) == nil, "mount_fixture")
		var a application
		h := j.call("POST", "/apps", map[string]any{"name": j.name, "project_slug": j.project, "image": j.images["a"], "env_vars": map[string]string{"OPS_MARKER": j.run, "OPS_CRASH": "0"}, "ports": []map[string]any{{"container_port": 8080, "published_port": j.port, "protocol": "tcp", "publish_mode": "ingress"}}, "volumes": []map[string]any{{"source": child, "target": "/data", "read_only": true}}, "expose": true, "ingress_container_port": 8080}, "", "", 201, &a)
		originalTag = h.Get("ETag")
		j.require(a.ID != "" && a.ProjectID == projectID && a.Generation == 1 && originalTag == fmt.Sprintf(`"app:%s:1"`, a.ID), "application_identity")
		j.record(Resource{Kind: "service", Name: "moduleos_" + j.name, OwnerKey: "moduleos.app.id", Owner: a.ID})
		j.converged(1)
		j.serving("a")
		for _, pair := range []struct{ kind, name, file string }{{"network", "moduleos-root-net", "MODULEOS_STACK_ROOT_NETWORK_ID_FILE"}, {"network", "moduleos-" + j.project + "-net", "MODULEOS_STACK_APP_NETWORK_ID_FILE"}, {"service", "moduleos_" + j.name, "MODULEOS_STACK_APP_SERVICE_ID_FILE"}, {"network", "moduleos-system_default", "MODULEOS_STACK_DEFAULT_NETWORK_ID_FILE"}} {
			id := j.docker(pair.kind, "inspect", "--format", "{{.ID}}", pair.name)
			j.require(os.WriteFile(os.Getenv(pair.file), id, 0600) == nil, "runtime_identity_record")
		}
	})
	j.stage("lifecycle", func() {
		a, tag := j.app()
		j.call("POST", "/apps/"+j.name+"/scale", map[string]int{"replicas": 1}, tag, "", 202, nil)
		noOp, newTag := j.app()
		j.require(noOp.Generation == a.Generation && newTag == tag, "noop_generation")
		j.call("PATCH", "/apps/"+j.name, map[string]any{"env_vars": map[string]string{"OPS_MARKER": j.run, "OPS_CRASH": "0", "OPS_SETTING": "persisted"}}, tag, "", 202, nil)
		j.converged(1)
		before, current := j.app()
		j.call("POST", "/apps/"+j.name+"/scale", map[string]int{"replicas": 2}, tag, "", 412, nil)
		j.call("POST", "/apps/"+j.name+"/scale", map[string]int{"replicas": 2}, "", "", 428, nil)
		j.call("POST", "/apps/"+j.name+"/scale", map[string]int{"replicas": 2}, "malformed", "", 400, nil)
		after, afterTag := j.app()
		j.require(sameIntent(before, after) && current == afterTag, "rejected_intent_mutated")
		j.call("POST", "/apps/"+j.name+"/scale", map[string]int{"replicas": 2}, current, "", 202, nil)
		j.converged(2)
		_, current = j.app()
		j.call("POST", "/apps/"+j.name+"/stop", nil, current, "", 202, nil)
		j.converged(0)
		_, current = j.app()
		j.call("POST", "/apps/"+j.name+"/scale", map[string]int{"replicas": 1}, current, "", 202, nil)
		_, current = j.app()
		j.call("POST", "/apps/"+j.name+"/start", nil, current, "", 202, nil)
		j.converged(1)
		j.serving("a")
	})
	j.stage("deploy", func() {
		aa := j.deploy(j.images["a"], "deploy-a-"+j.run)
		deploymentA = j.terminal(aa, "succeeded")
		j.require(deploymentA.Resolved == j.images["a"], "immutable_deployment_a")
		j.converged(1)
		j.serving("a")
		_, beforeTag := j.app()
		key := "deploy-b-" + j.run
		var b accepted
		j.call("POST", "/apps/"+j.name+"/deploy", map[string]string{"image": j.images["b"]}, beforeTag, key, 202, &b)
		j.replayTag = beforeTag
		j.replayKey = key
		j.replayExpected = b
		var replay accepted
		j.call("POST", "/apps/"+j.name+"/deploy", map[string]string{"image": j.images["b"]}, beforeTag, key, 202, &replay)
		j.require(replay == b, "idempotent_identity")
		_, currentTag := j.app()
		j.call("POST", "/apps/"+j.name+"/deploy", map[string]string{"image": j.images["a"]}, currentTag, key, 409, nil)
		d := j.terminal(b, "succeeded")
		j.require(d.Resolved == j.images["b"], "immutable_deployment_b")
		j.converged(1)
		j.serving("b")
		j.assertRuntimeImage("b")
		_, currentTag = j.app()
		var rollback accepted
		j.call("POST", "/apps/"+j.name+"/deployments/"+deploymentA.ID+"/rollback", nil, currentTag, "rollback-"+j.run, 202, &rollback)
		d = j.terminal(rollback, "succeeded")
		j.require(d.Rollback != nil && *d.Rollback == deploymentA.ID && d.Resolved == j.images["a"], "rollback_source")
		j.converged(1)
		j.serving("a")
		j.assertRuntimeImage("a")
		_, currentTag = j.app()
		j.call("PATCH", "/apps/"+j.name, map[string]any{"env_vars": map[string]string{"OPS_MARKER": j.run, "OPS_CRASH": "1", "OPS_SETTING": "persisted"}}, currentTag, "", 202, nil)
		failed := j.deploy(j.images["b"], "failed-"+j.run)
		failure := j.terminal(failed, "failed")
		j.require(failure.Failure != "", "terminal_failure_code")
		j.call("GET", "/live", nil, "", "", 200, nil)
		_, currentTag = j.app()
		j.call("PATCH", "/apps/"+j.name, map[string]any{"env_vars": map[string]string{"OPS_MARKER": j.run, "OPS_CRASH": "0", "OPS_SETTING": "persisted"}}, currentTag, "", 202, nil)
		j.terminal(j.deploy(j.images["a"], "recover-"+j.run), "succeeded")
		j.converged(1)
		j.serving("a")
	})
	j.stage("logs", func() { j.qualifyLogs() })
	j.stage("restart", func() { j.qualifyRestart(projectID) })
	j.stage("ownership", func() { j.qualifyOwnership() })
	j.stage("outage", func() { j.qualifyDockerOutage() })
	j.stage("routing", func() { j.qualifyRouting() })
	j.stage("restore", func() { j.qualifyRestore(projectID) })
	j.stage("delete", func() {
		_, tag := j.app()
		j.call("DELETE", "/apps/"+j.name, nil, tag, "", 202, nil)
		j.waitDeleted(j.name)
		j.require(os.WriteFile(os.Getenv("MODULEOS_STACK_APP_SERVICE_ID_FILE"), nil, 0600) == nil, "service_identity_record")
		var recreated application
		j.call("POST", "/apps", map[string]any{"name": j.name, "project_slug": j.project, "image": j.images["a"], "env_vars": map[string]string{"OPS_MARKER": j.run}}, "", "", 201, &recreated)
		j.require(recreated.Generation == 1, "new_incarnation_generation")
		j.record(Resource{Kind: "service", Name: "moduleos_" + j.name, OwnerKey: "moduleos.app.id", Owner: recreated.ID})
		j.call("POST", "/apps/"+j.name+"/stop", nil, originalTag, "", 412, nil)
		current, tag := j.app()
		j.require(current.ID == recreated.ID && current.Generation == 1, "wrong_incarnation_mutation")
		j.call("DELETE", "/apps/"+j.name, nil, tag, "", 202, nil)
		j.waitDeleted(j.name)
		j.call("DELETE", "/projects/"+j.project, nil, "", "", 202, nil)
		j.wait(90*time.Second, func(ctx context.Context) (bool, error) {
			s, _, _, err := j.request(ctx, "GET", "/projects/"+j.project, nil, "", "", true)
			return err == nil && s == 404, nil
		})
		j.checkCanaries()
	})
}
func (j *journey) assertRuntimeImage(variant string) {
	var svc serviceRecord
	j.jsonDocker(&svc, "service", "inspect", "--format", "{{json .}}", "moduleos_"+j.name)
	j.require(svc.Spec.TaskTemplate.ContainerSpec.Image == j.images[variant], "service_digest")
	tasks := strings.Fields(string(j.docker("service", "ps", "--filter", "desired-state=running", "--format", "{{.ID}}", svc.ID)))
	j.require(len(tasks) == 1, "runtime_singleton")
	var task taskRecord
	j.jsonDocker(&task, "inspect", "--type", "task", "--format", "{{json .}}", tasks[0])
	var c containerRecord
	j.jsonDocker(&c, "container", "inspect", "--format", "{{json .}}", task.Status.ContainerStatus.ContainerID)
	j.require(c.State.Running && c.Image == j.imageIDs[variant], "running_image_identity")
}
func (j *journey) waitDeleted(name string) {
	j.wait(90*time.Second, func(ctx context.Context) (bool, error) {
		s, _, _, err := j.request(ctx, "GET", "/apps/"+name, nil, "", "", true)
		if err != nil || s != 404 {
			return false, nil
		}
		_, err = Docker(ctx, 5*time.Second, "service", "inspect", "moduleos_"+name)
		var missing *commandFailure
		return errorsAsMissing(err, &missing), nil
	})
}
func errorsAsMissing(err error, target **commandFailure) bool {
	return err != nil && errors.As(err, target) && (*target).missing
}
func (j *journey) qualifyLogs() {
	j.wait(30*time.Second, func(ctx context.Context) (bool, error) {
		s, _, b, err := j.request(ctx, "GET", "/apps/"+j.name+"/logs?tail=20", nil, "", "", true)
		return err == nil && s == 200 && bytes.Contains(b, []byte("ops-"+j.run+"-a")), nil
	})
	var invalidTail struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	j.call("GET", "/apps/"+j.name+"/logs?tail=-1", nil, "", "", 422, &invalidTail)
	j.require(invalidTail.Error.Code == "validation_failed", "invalid_log_tail_contract")
	var held []*http.Response
	var cancels []context.CancelFunc
	streamClient := &http.Client{Transport: j.api.Transport, CheckRedirect: j.api.CheckRedirect}
	defer func() {
		for i, response := range held {
			cancels[i]()
			response.Body.Close()
		}
	}()
	// Default per-application admission is three streams. Keep all three
	// active, prove rejection, then prove canceled slots can be reused.
	for attempt := 0; attempt < 9; attempt++ {
		ctx, cancel := context.WithTimeout(j.ctx, 45*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", j.base+"/apps/"+j.name+"/logs/stream?tail=5", nil)
		req.Header.Set("Authorization", "Bearer "+j.key)
		var response *http.Response
		var err error
		j.wait(5*time.Second, func(context.Context) (bool, error) {
			response, err = streamClient.Do(req)
			if err == nil && response.StatusCode == 429 {
				response.Body.Close()
				return false, nil
			}
			return true, nil
		})
		defer cancel()
		j.require(err == nil, "sse_open")
		j.require(response.StatusCode == 200 && strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream"), "sse_contract")
		reader := bufio.NewScanner(io.LimitReader(response.Body, 1<<16))
		reader.Buffer(make([]byte, 4096), 8192)
		eventSeen, markerSeen := false, false
		for reader.Scan() {
			line := reader.Text()
			if line == "event: log" {
				eventSeen = true
			}
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, "ops-"+j.run+"-a") {
				var payload map[string]any
				j.require(json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload) == nil, "sse_json")
				markerSeen = true
				break
			}
		}
		j.require(eventSeen && markerSeen, "sse_marker")
		if attempt < 3 {
			held = append(held, response)
			cancels = append(cancels, cancel)
			if attempt == 2 {
				j.call("GET", "/apps/"+j.name+"/logs/stream?tail=5", nil, "", "", 429, nil)
				for i, active := range held {
					cancels[i]()
					active.Body.Close()
				}
			}
		} else {
			cancel()
			response.Body.Close()
		}
	}
}
func (j *journey) qualifyRestart(projectID string) {
	before, tag := j.app()
	history := j.history()
	old := j.process()
	j.docker("service", "update", "--force", "--detach=true", controlService)
	j.waitReplacement(old, "", restartReplacement)
	j.ready()
	now := j.process()
	j.require(now.Container != old.Container && now.Started != old.Started, "process_replacement")
	after, newTag := j.app()
	j.require(sameIntent(before, after) && tag == newTag && reflect.DeepEqual(history, j.history()), "restart_durability")
	j.checkDurableReplay()
	var p struct{ ID string }
	j.call("GET", "/projects/"+j.project, nil, "", "", 200, &p)
	j.require(p.ID == projectID, "project_restart_identity")
	j.converged(1)
	j.serving("a")
	// Drift is injected only on this UUID-owned runtime; the API intent is unchanged.
	j.docker("service", "scale", "--detach=true", "moduleos_"+j.name+"=0")
	j.converged(1)
	after, newTag = j.app()
	j.require(sameIntent(before, after) && newTag == tag, "drift_changed_intent")
}
func (j *journey) history() map[string]deployment {
	var list struct{ Deployments []deployment }
	j.call("GET", "/apps/"+j.name+"/deployments?limit=100", nil, "", "", 200, &list)
	m := map[string]deployment{}
	for _, d := range list.Deployments {
		j.require(d.ID != "", "history_identity")
		m[d.ID] = d
	}
	return m
}
func (j *journey) checkDurableReplay() {
	before, tag := j.app()
	history := j.history()
	var replay accepted
	j.call("POST", "/apps/"+j.name+"/deploy", map[string]string{"image": j.images["b"]}, j.replayTag, j.replayKey, 202, &replay)
	j.require(replay == j.replayExpected, "durable_command_replay")
	after, newTag := j.app()
	j.require(tag == newTag && sameIntent(before, after) && reflect.DeepEqual(history, j.history()), "replay_mutated_state")
}
func (j *journey) qualifyOwnership() {
	name := "conflict" + j.run
	serviceName := "moduleos_" + name
	label := "io.moduleos.operations.run=" + j.run
	id := j.fixture("service", serviceName, "service", "create", "--name", serviceName, "--label", label, "--container-label", label, "--env", "OPS_MARKER=foreign"+j.run, j.images["a"])
	before := j.docker("service", "inspect", "--format", "{{json .Spec}}", id)
	var a application
	j.call("POST", "/apps", map[string]any{"name": name, "project_slug": j.project, "image": j.images["a"], "env_vars": map[string]string{"OPS_MARKER": j.run}}, "", "", 201, &a)
	j.record(Resource{Kind: "service", Name: serviceName, OwnerKey: "moduleos.app.id", Owner: a.ID})
	j.wait(60*time.Second, func(ctx context.Context) (bool, error) {
		s, _, b, err := j.request(ctx, "GET", "/apps/"+name, nil, "", "", true)
		var current application
		return err == nil && s == 200 && json.Unmarshal(b, &current) == nil && current.Code == "ownership_conflict", nil
	})
	old := j.process()
	j.docker("service", "update", "--force", "--detach=true", controlService)
	j.waitReplacement(old, "", ownershipReplacement)
	j.ready()
	after := j.docker("service", "inspect", "--format", "{{json .Spec}}", id)
	j.require(bytes.Equal(before, after), "foreign_service_modified")
	// Remove only the fixture by its immutable ID after proving non-adoption.
	j.docker("service", "rm", id)
	j.wait(90*time.Second, func(ctx context.Context) (bool, error) {
		s, _, b, err := j.request(ctx, "GET", "/apps/"+name, nil, "", "", true)
		var current application
		return err == nil && s == 200 && json.Unmarshal(b, &current) == nil && current.ID == a.ID && current.Generation == current.ObservedGeneration && current.State == "running", nil
	})
	var current application
	h := j.call("GET", "/apps/"+name, nil, "", "", 200, &current)
	j.call("DELETE", "/apps/"+name, nil, h.Get("ETag"), "", 202, nil)
	j.waitDeleted(name)
	j.checkCanaries()
}
func (j *journey) qualifyDockerOutage() {
	socket := filepath.Join(j.work, "relay.sock")
	relay, err := NewRelay(socket, "/var/run/docker.sock")
	j.require(err == nil, "relay_setup")
	defer relay.Close()
	info, err := os.Stat("/var/run/docker.sock")
	j.require(err == nil, "socket_metadata")
	gid := info.Sys().(*syscall.Stat_t).Gid
	j.require(gid != 0 && os.Chown(socket, 0, int(gid)) == nil, "relay_permissions")
	beforeMount := j.process()
	j.docker(socketMountUpdateArgs(socket, controlService)...)
	j.waitReplacement(beforeMount, socket, relayEnterReplacement)
	j.ready()
	baseline := j.process()
	relay.SetAvailable(false)
	for n := 0; n < 3; n++ {
		j.call("GET", "/live", nil, "", "", 200, nil)
		var ready struct{ Code string }
		j.call("GET", "/ready", nil, "", "", 503, &ready)
		j.require(ready.Code == "docker_unavailable", "outage_readiness")
		j.require(j.process() == baseline, "outage_process_replaced")
	}
	relay.SetAvailable(true)
	j.ready()
	j.require(j.process() == baseline, "outage_restart_masked_recovery")
	beforeMutation, tag := j.app()
	j.call("PATCH", "/apps/"+j.name, map[string]any{"env_vars": map[string]string{"OPS_MARKER": j.run, "OPS_CRASH": "0", "OPS_SETTING": "after-outage"}}, tag, "", 202, nil)
	updated, _ := j.app()
	j.require(updated.ID == beforeMutation.ID && updated.Generation == beforeMutation.Generation+1, "outage_mutation_generation")
	j.converged(1)
	j.assertRuntimeSetting("after-outage")
	j.serving("a")
	j.require(j.process() == baseline, "outage_postmutation_restart")
	j.docker(socketMountUpdateArgs("/var/run/docker.sock", controlService)...)
	j.waitReplacement(baseline, "/var/run/docker.sock", relayExitReplacement)
	j.ready()
}
func (j *journey) qualifyRouting() {
	host := j.name + ".moduleos.example"
	j.wait(90*time.Second, func(ctx context.Context) (bool, error) {
		s, b, err := j.backend(ctx, j.proxyPort, host)
		return err == nil && s == 200 && b == "ops-"+j.run+"-a\n", nil
	})
	_, tag := j.app()
	j.call("PATCH", "/apps/"+j.name, map[string]bool{"expose": false}, tag, "", 202, nil)
	j.converged(1)
	j.wait(60*time.Second, func(ctx context.Context) (bool, error) {
		s, _, err := j.backend(ctx, j.proxyPort, host)
		return err == nil && s == 404, nil
	})
	j.canaryRoute()
	j.serving("a")
	var disabled serviceRecord
	j.jsonDocker(&disabled, "service", "inspect", "--format", "{{json .}}", "moduleos_"+j.name)
	j.require(disabled.Spec.Labels["traefik.enable"] == "false", "routing_label_disabled")
	_, tag = j.app()
	j.call("PATCH", "/apps/"+j.name, map[string]bool{"expose": true}, tag, "", 202, nil)
	j.converged(1)
	j.wait(90*time.Second, func(ctx context.Context) (bool, error) {
		s, b, err := j.backend(ctx, j.proxyPort, host)
		return err == nil && s == 200 && b == "ops-"+j.run+"-a\n", nil
	})
	j.canaryRoute()
}
func (j *journey) qualifyRestore(projectID string) {
	before, tag := j.app()
	history := j.history()
	old := j.process()
	j.docker("service", "scale", "--detach=true", controlService+"=0")
	j.wait(60*time.Second, func(ctx context.Context) (bool, error) {
		out, err := Docker(ctx, 5*time.Second, "container", "ls", "--quiet", "--filter", "label=com.docker.swarm.service.name="+controlService)
		return err == nil && len(bytes.TrimSpace(out)) == 0, nil
	})
	var stopped containerRecord
	out, err := Docker(j.ctx, 5*time.Second, "container", "inspect", "--format", "{{json .}}", old.Container)
	if err == nil {
		j.require(json.Unmarshal(out, &stopped) == nil && !stopped.State.Running, "writer_not_stopped")
	} else {
		var missing *commandFailure
		j.require(errorsAsMissing(err, &missing), "writer_state_unknown")
	}
	volumeA := os.Getenv("MODULEOS_STACK_VOLUME")
	source := string(j.docker("volume", "inspect", "--format", "{{.Mountpoint}}", volumeA))
	originalHashes, err := DirectoryHashes(source)
	j.require(err == nil, "backup_source")
	backup := filepath.Join(j.work, "offline-backup")
	j.require(os.Mkdir(backup, 0700) == nil && CopyOfflineDirectory(source, backup) == nil, "offline_snapshot")
	volumeB := "ops-" + j.run + "-restore"
	j.fixture("volume", volumeB, "volume", "create", "--label", "io.moduleos.operations.run="+j.run, volumeB)
	destination := string(j.docker("volume", "inspect", "--format", "{{.Mountpoint}}", volumeB))
	j.require(source != destination && volumeA != volumeB, "restore_volume_distinct")
	j.require(CopyOfflineDirectory(backup, destination) == nil, "offline_restore")
	restoredHashes, err := DirectoryHashes(destination)
	j.require(err == nil && reflect.DeepEqual(originalHashes, restoredHashes), "restore_content")
	files, err := filepath.Glob(filepath.Join(j.root(), "apps/control-plane/internal/store/migrations/*.up.sql"))
	j.require(err == nil && len(files) > 0, "migration_inventory")
	var latest int64
	for _, path := range files {
		n, err := strconv.ParseInt(strings.Split(filepath.Base(path), "_")[0], 10, 64)
		j.require(err == nil, "migration_inventory")
		if n > latest {
			latest = n
		}
	}
	j.require(CheckOfflineDatabase(filepath.Join(destination, "moduleos.db"), latest) == nil, "restore_integrity")
	unchanged, err := DirectoryHashes(source)
	j.require(err == nil && reflect.DeepEqual(originalHashes, unchanged), "source_volume_modified")
	env, err := os.ReadFile(j.envFile)
	j.require(err == nil, "restore_environment")
	needle := []byte("MODULEOS_DATA_VOLUME=" + volumeA + "\n")
	j.require(bytes.Count(env, needle) == 1, "restore_volume_selection")
	env = bytes.Replace(env, needle, []byte("MODULEOS_DATA_VOLUME="+volumeB+"\n"), 1)
	j.require(os.WriteFile(j.envFile, env, 0600) == nil, "restore_environment")
	j.deployOfficial()
	after, newTag := j.app()
	j.require(sameIntent(before, after) && tag == newTag && reflect.DeepEqual(history, j.history()), "restore_identity")
	j.checkDurableReplay()
	var project struct{ ID string }
	j.call("GET", "/projects/"+j.project, nil, "", "", 200, &project)
	j.require(project.ID == projectID, "restore_project_identity")
	j.converged(1)
	j.serving("a")
	beforeMutation, tag := j.app()
	j.call("PATCH", "/apps/"+j.name, map[string]any{"env_vars": map[string]string{"OPS_MARKER": j.run, "OPS_CRASH": "0", "OPS_SETTING": "after-restore"}}, tag, "", 202, nil)
	updated, _ := j.app()
	j.require(updated.ID == beforeMutation.ID && updated.Generation == beforeMutation.Generation+1, "restore_mutation_generation")
	j.converged(1)
	j.assertRuntimeSetting("after-restore")
	j.serving("a")
	unchanged, err = DirectoryHashes(source)
	j.require(err == nil && reflect.DeepEqual(originalHashes, unchanged), "source_volume_reused")
}
func (j *journey) assertRuntimeSetting(value string) {
	j.assertRuntimeImage("a")
	tasks := strings.Fields(string(j.docker("service", "ps", "--filter", "desired-state=running", "--format", "{{.ID}}", "moduleos_"+j.name)))
	j.require(len(tasks) == 1, "runtime_setting_task")
	var task taskRecord
	j.jsonDocker(&task, "inspect", "--type", "task", "--format", "{{json .}}", tasks[0])
	var env []string
	j.jsonDocker(&env, "container", "inspect", "--format", "{{json .Config.Env}}", task.Status.ContainerStatus.ContainerID)
	found := 0
	for _, item := range env {
		if item == "OPS_SETTING="+value {
			found++
		}
	}
	j.require(found == 1, "runtime_setting_mismatch")
}
