//go:build darwin || linux

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/handler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

func TestProcessRejectsInvalidConfigurationBeforeSideEffects(t *testing.T) {
	harness := newProcessHarness(t)
	fake := startFakeDocker(t, harness.dockerSock)
	sensitiveMarker := "not-a-credential"
	harness.set("API_KEY", sensitiveMarker)
	harness.start()
	requireExitCode(t, harness, 1)

	if _, err := os.Stat(harness.database); !os.IsNotExist(err) {
		t.Fatalf("database was created for invalid configuration: %v", err)
	}
	if fake.requestCount() != 0 {
		t.Fatalf("Docker received %d requests for invalid configuration", fake.requestCount())
	}
	output := harness.output()
	if strings.Contains(output, sensitiveMarker) || containsAny(output, "daemon started", "reconciler started", "event watcher started") {
		t.Fatalf("invalid startup output leaked a secret or claimed startup:\n%s", output)
	}
	harness.assertListenerReleased()
	fake.assertNoUnexpected()
}

func TestProcessListenerCollisionFailsBeforeWorkers(t *testing.T) {
	harness := newProcessHarness(t)
	collision := holdAddress(t, harness.address)
	t.Cleanup(func() { _ = collision.Close() })
	fake := startFakeDocker(t, harness.dockerSock)
	harness.start()
	requireExitCode(t, harness, 1)

	if fake.requestCount() != 0 {
		t.Fatalf("Docker received %d requests after listener collision", fake.requestCount())
	}
	output := harness.output()
	if containsAny(output, "daemon started", "reconciler started", "event watcher started", "Docker connected") {
		t.Fatalf("listener collision produced a false startup record:\n%s", output)
	}
	if _, err := os.Stat(harness.database); err != nil {
		t.Fatalf("SQLite should truthfully exist before listener binding: %v", err)
	}
	if err := collision.Close(); err != nil {
		t.Fatalf("close collision listener: %v", err)
	}
	harness.assertListenerReleased()
	fake.assertNoUnexpected()
}

func TestProcessDockerAbsentIsLiveButNotReady(t *testing.T) {
	harness := newProcessHarness(t)
	harness.start()
	assertLive(t, harness)
	readiness := readinessAt(t, harness, http.StatusServiceUnavailable)
	if readiness.Status != "not_ready" || readiness.Code != "docker_unavailable" || readiness.Components.Database != handler.ComponentReady || readiness.Components.Docker != handler.ComponentUnavailable {
		t.Fatalf("unexpected readiness response: %#v", readiness)
	}
	harness.assertRunning()
	harness.signal(syscall.SIGTERM)
	requireExitCode(t, harness, 0)
	harness.assertListenerReleased()
}

func TestProcessDockerRecoveryRestoresReadinessWithoutRestart(t *testing.T) {
	harness := newProcessHarness(t)
	harness.start()
	assertLive(t, harness)
	readiness := readinessAt(t, harness, http.StatusServiceUnavailable)
	if readiness.Code != "docker_unavailable" {
		t.Fatalf("initial readiness code = %q", readiness.Code)
	}
	pid := harness.pid()
	fake := startFakeDocker(t, harness.dockerSock)
	ready := readinessAt(t, harness, http.StatusOK)
	if ready.Status != "ready" || ready.Code != "" || ready.Components.Database != handler.ComponentReady || ready.Components.Docker != handler.ComponentReady || ready.Components.Swarm != handler.ComponentReady || ready.Components.Ingress != handler.ComponentNotRequired {
		t.Fatalf("recovered readiness response: %#v", ready)
	}
	if harness.pid() != pid {
		t.Fatalf("process PID changed from %d to %d", pid, harness.pid())
	}
	harness.assertRunning()
	waitForChannel(t, fake.eventOpened, processStartupDeadline, "recovered event stream opening")
	harness.signal(syscall.SIGTERM)
	requireExitCode(t, harness, 0)
	waitForChannel(t, fake.eventClosed, processExitDeadline, "event stream cancellation")
	fake.assertNoUnexpected()
}

func TestProcessSignalsProduceCleanBoundedShutdown(t *testing.T) {
	for _, test := range []struct {
		name   string
		signal os.Signal
	}{
		{name: "SIGTERM", signal: syscall.SIGTERM},
		{name: "SIGINT", signal: os.Interrupt},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newProcessHarness(t)
			fake := startFakeDocker(t, harness.dockerSock)
			harness.start()
			assertLive(t, harness)
			_ = readinessAt(t, harness, http.StatusOK)
			waitForChannel(t, fake.eventOpened, processStartupDeadline, "event stream opening")
			started := time.Now()
			harness.signal(test.signal)
			requireExitCode(t, harness, 0)
			if time.Since(started) > processExitDeadline {
				t.Fatal("graceful shutdown exceeded its harness deadline")
			}
			harness.assertListenerReleased()
			fake.assertNoUnexpected()
		})
	}
}

func TestProcessCompletesAcceptedReadinessDuringShutdown(t *testing.T) {
	harness := newProcessHarness(t)
	fake := startFakeDocker(t, harness.dockerSock)
	harness.start()
	assertLive(t, harness)
	_ = readinessAt(t, harness, http.StatusOK)
	entered, canceled, release := fake.armInfoBarrier()
	defer release()

	type readyResult struct {
		status int
		body   handler.ReadinessResponse
		err    error
	}
	result := make(chan readyResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), processExitDeadline)
		defer cancel()
		response, err := harness.request(ctx, "/api/v1/ready", false)
		completed := readyResult{err: err}
		if err == nil {
			completed.status = response.StatusCode
			completed.err = json.NewDecoder(response.Body).Decode(&completed.body)
			_ = response.Body.Close()
		}
		result <- completed
	}()
	waitForChannel(t, entered, processStartupDeadline, "blocked readiness request")
	harness.signal(syscall.SIGTERM)
	waitForChannel(t, canceled, processExitDeadline, "in-flight readiness cancellation")

	var completed readyResult
	select {
	case completed = <-result:
	case <-time.After(processExitDeadline):
		t.Fatal("timed out waiting for the accepted readiness request")
	}
	if completed.err != nil || completed.status != http.StatusServiceUnavailable || completed.body.Code != "shutting_down" {
		t.Fatalf("shutdown readiness result = %#v", completed)
	}
	requireExitCode(t, harness, 0)
	fake.assertNoUnexpected()
}

func TestProcessClosesActiveSSEAndUpstreamLogs(t *testing.T) {
	harness := newProcessHarness(t)
	application := seedLogApplication(t, harness.database)
	fake := startFakeDocker(t, harness.dockerSock)
	fake.setApplication(application)
	harness.start()
	assertLive(t, harness)
	_ = readinessAt(t, harness, http.StatusOK)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, harness.url("/api/v1/apps/"+application.Name+"/logs/stream"), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testAPIKey)
	response, err := harness.streamClient.Do(request)
	if err != nil {
		t.Fatalf("open SSE stream: %v\n%s", err, harness.output())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("SSE status = %d: %s", response.StatusCode, payload)
	}
	connected := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(response.Body)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				connected <- readErr
				return
			}
			if strings.TrimSpace(line) == ": connected" {
				connected <- nil
				return
			}
		}
	}()
	select {
	case connectedErr := <-connected:
		if connectedErr != nil {
			t.Fatalf("read SSE connected frame: %v", connectedErr)
		}
	case <-time.After(processStartupDeadline):
		t.Fatal("timed out waiting for SSE connected frame")
	}
	waitForChannel(t, fake.logOpened, processStartupDeadline, "upstream Docker log stream")
	waitForChannel(t, fake.eventOpened, processStartupDeadline, "Docker event stream")
	entered, canceled, release := fake.armInfoBarrier()
	defer release()
	type readyResult struct {
		body handler.ReadinessResponse
		err  error
	}
	ready := make(chan readyResult, 1)
	go func() {
		ctx, requestCancel := context.WithTimeout(context.Background(), processExitDeadline)
		defer requestCancel()
		readyResponse, requestErr := harness.request(ctx, "/api/v1/ready", false)
		completed := readyResult{err: requestErr}
		if requestErr == nil {
			completed.err = json.NewDecoder(readyResponse.Body).Decode(&completed.body)
			_ = readyResponse.Body.Close()
		}
		ready <- completed
	}()
	waitForChannel(t, entered, processStartupDeadline, "blocked readiness request")

	harness.signal(syscall.SIGTERM)
	waitForChannel(t, fake.logClosed, processExitDeadline, "upstream Docker log cancellation")
	waitForChannel(t, fake.eventClosed, processExitDeadline, "Docker event stream cancellation")
	waitForChannel(t, canceled, processExitDeadline, "in-flight readiness cancellation")
	var completed readyResult
	select {
	case completed = <-ready:
	case <-time.After(processExitDeadline):
		t.Fatal("timed out waiting for readiness shutdown response")
	}
	if completed.err != nil || completed.body.Code != "shutting_down" {
		t.Fatalf("readiness shutdown result = %#v", completed)
	}
	requireExitCode(t, harness, 0)
	if _, err := io.Copy(io.Discard, response.Body); err != nil && !isExpectedStreamClose(err) {
		t.Fatalf("SSE stream close: %v", err)
	}
	fake.assertNoUnexpected()
}

func assertLive(t *testing.T, harness *processHarness) {
	t.Helper()
	var body handler.LivenessResponse
	harness.waitForStatus("/api/v1/live", http.StatusOK, &body)
	if body.Status != "live" {
		t.Fatalf("liveness status = %q", body.Status)
	}
}

func readinessAt(t *testing.T, harness *processHarness, status int) handler.ReadinessResponse {
	t.Helper()
	var body handler.ReadinessResponse
	harness.waitForStatus("/api/v1/ready", status, &body)
	return body
}

func seedLogApplication(t *testing.T, database string) *store.Application {
	t.Helper()
	opened, err := store.NewSQLiteStore(database)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	now := time.Now().UTC()
	application := &store.Application{
		ID: "11111111-1111-4111-8111-111111111111", ProjectID: store.RootProjectID,
		Name: "stream-app", SourceType: store.SourceTypeImage, Image: "nginx:latest", ObservedImage: "nginx:latest",
		Status: store.AppStatusStopped, Replicas: 0, DesiredRunState: store.DesiredRunStateStopped,
		DesiredGeneration: 1, ObservedGeneration: 1, ObservedState: store.ObservedStateStopped,
		EnvVars: "{}", Ports: "[]", Volumes: "[]", CreatedAt: now, UpdatedAt: now,
	}
	if err := opened.CreateApplication(context.Background(), application); err != nil {
		_ = opened.Close()
		t.Fatalf("seed log application: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}
	return application
}

func isExpectedStreamClose(err error) bool {
	if err == nil || err == io.EOF {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "closed") || strings.Contains(message, "reset") || strings.Contains(message, "unexpected eof")
}
