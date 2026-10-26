//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	moduleosswarm "github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	"github.com/moby/moby/api/pkg/stdcopy"
	dockerswarm "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

func TestProjectLinkAttachmentAndDNSAgainstRealSwarm(t *testing.T) {
	f := newIntegrationFixture(t)
	source := f.createProject("Source", f.unique("source"))
	target := f.createProject("Target", f.unique("target"))
	sourceNetwork := f.reconcileProject(source)
	targetNetwork := f.reconcileProject(target)
	targetApp := f.createApplication(target, f.unique("target-app"), qualificationImage, 1)
	f.reconcileApplicationUntilConverged(targetApp.Name)
	targetService := f.registerApplicationService(targetApp)

	before := inspectRawService(t, f.ctx, f.raw, targetService.ID)
	alias := "linked-" + integrationSuite.runID
	link, err := f.service.CreateProjectLink(f.ctx, source.Slug, targetApp.Name, alias)
	if err != nil {
		t.Fatalf("create project link: %v", err)
	}
	if link.DesiredGeneration != 1 || link.ObservedGeneration != 0 {
		t.Fatalf("new project link generations = desired %d observed %d", link.DesiredGeneration, link.ObservedGeneration)
	}
	f.reconcileApplicationUntilConverged(targetApp.Name)

	observedLink, err := f.store.GetProjectLink(f.ctx, link.ID)
	if err != nil {
		t.Fatalf("read observed project link: %v", err)
	}
	if observedLink.ObservedGeneration != observedLink.DesiredGeneration {
		t.Fatalf("project link did not converge: %#v", observedLink)
	}
	afterAttach := inspectRawService(t, f.ctx, f.raw, targetService.ID)
	assertOnlyRawNetworksChanged(t, before.Spec, afterAttach.Spec)
	assertNetworkAttachment(t, afterAttach.Spec.TaskTemplate.Networks, targetNetwork.ID, nil, 1)
	assertNetworkAttachment(t, afterAttach.Spec.TaskTemplate.Networks, sourceNetwork.ID, []string{alias}, 1)

	versionAfterAttach := afterAttach.Version.Index
	if err := f.rec.ReconcileApplication(f.ctx, targetApp.Name); err != nil {
		t.Fatalf("repeat linked application reconciliation: %v", err)
	}
	idempotent := inspectRawService(t, f.ctx, f.raw, targetService.ID)
	if idempotent.Version.Index != versionAfterAttach {
		t.Fatalf("idempotent reconciliation changed service version: before=%d after=%d", versionAfterAttach, idempotent.Version.Index)
	}
	assertNetworkAttachment(t, idempotent.Spec.TaskTemplate.Networks, sourceNetwork.ID, []string{alias}, 1)

	probeName := f.unique("dns-probe")
	probeID := createDirectService(f.ctx, f, probeName, qualificationImage, 1,
		[]dockerswarm.NetworkAttachmentConfig{{Target: sourceNetwork.ID}}, nil, []string{"sleep", "3600"})
	probeTask, err := waitRunningTask(f.ctx, f.raw, probeID)
	if err != nil {
		t.Fatalf("wait for DNS probe task: %v", err)
	}
	if probeTask.Status.ContainerStatus == nil || probeTask.Status.ContainerStatus.ContainerID == "" {
		t.Fatalf("DNS probe task has no container identity: %#v", probeTask.Status)
	}
	containerID := probeTask.Status.ContainerStatus.ContainerID
	dnsCtx, dnsCancel := context.WithTimeout(f.ctx, 30*time.Second)
	err = pollUntil(dnsCtx, func() (bool, error) {
		output, execErr := execContainer(dnsCtx, f.raw, containerID, []string{"getent", "hosts", alias})
		if execErr != nil {
			return false, execErr
		}
		return output != "", nil
	})
	dnsCancel()
	if err != nil {
		t.Fatalf("project link alias did not resolve through Swarm DNS: %v", err)
	}

	if err := f.service.DeleteProjectLink(f.ctx, source.Slug, link.ID); err != nil {
		t.Fatalf("delete project link: %v", err)
	}
	f.reconcileApplicationUntilConverged(targetApp.Name)
	afterDetach := inspectRawService(t, f.ctx, f.raw, targetService.ID)
	assertOnlyRawNetworksChanged(t, before.Spec, afterDetach.Spec)
	assertNetworkAttachment(t, afterDetach.Spec.TaskTemplate.Networks, sourceNetwork.ID, nil, 0)
	assertNetworkAttachment(t, afterDetach.Spec.TaskTemplate.Networks, targetNetwork.ID, nil, 1)

	removedCtx, removedCancel := context.WithTimeout(f.ctx, 30*time.Second)
	err = pollUntil(removedCtx, func() (bool, error) {
		_, execErr := execContainer(removedCtx, f.raw, containerID, []string{"getent", "hosts", alias})
		if execErr == nil {
			return false, nil
		}
		var exitErr *execExitError
		if errors.As(execErr, &exitErr) {
			return true, nil
		}
		return false, execErr
	})
	removedCancel()
	if err != nil {
		t.Fatalf("project link alias remained resolvable after detach: %v", err)
	}
}

func TestProjectLinkConcurrentAttachmentsAgainstRealSwarm(t *testing.T) {
	f := newIntegrationFixture(t)
	baseName := f.unique("concurrent-base")
	baseID := createDirectNetwork(f.ctx, f, baseName, nil)
	serviceName := f.unique("concurrent-service")
	serviceID := createDirectService(f.ctx, f, serviceName, qualificationImage, 0,
		[]dockerswarm.NetworkAttachmentConfig{{Target: baseID}}, nil, nil)

	const attachmentCount = 3
	type attachmentFixture struct {
		name  string
		id    string
		alias string
	}
	attachments := make([]attachmentFixture, 0, attachmentCount)
	for index := 0; index < attachmentCount; index++ {
		name := f.unique(fmt.Sprintf("concurrent-%d", index))
		attachments = append(attachments, attachmentFixture{
			name: name, id: createDirectNetwork(f.ctx, f, name, nil), alias: fmt.Sprintf("link-%d-%s", index, integrationSuite.runID),
		})
	}

	start := make(chan struct{})
	results := make(chan error, attachmentCount)
	var workers sync.WaitGroup
	for _, attachment := range attachments {
		attachment := attachment
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- f.docker.AttachServiceNetwork(f.ctx, serviceID, moduleosswarm.NetworkAttachment{
				Network: attachment.name, Aliases: []string{attachment.alias},
			})
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent network attachment failed: %v", err)
		}
	}

	service := inspectRawService(t, f.ctx, f.raw, serviceID)
	assertNetworkAttachment(t, service.Spec.TaskTemplate.Networks, baseID, nil, 1)
	for _, attachment := range attachments {
		assertNetworkAttachment(t, service.Spec.TaskTemplate.Networks, attachment.id, []string{attachment.alias}, 1)
	}
}

func inspectRawService(t *testing.T, ctx context.Context, dockerClient *client.Client, serviceID string) dockerswarm.Service {
	t.Helper()
	result, err := dockerClient.ServiceInspect(ctx, serviceID, client.ServiceInspectOptions{})
	if err != nil {
		t.Fatalf("inspect service %s: %v", shortID(serviceID), err)
	}
	return result.Service
}

func assertOnlyRawNetworksChanged(t *testing.T, before, after dockerswarm.ServiceSpec) {
	t.Helper()
	before.TaskTemplate.Networks = nil
	after.TaskTemplate.Networks = nil
	before.TaskTemplate.ContainerSpec = cloneContainerWithoutNetworkHash(before.TaskTemplate.ContainerSpec)
	after.TaskTemplate.ContainerSpec = cloneContainerWithoutNetworkHash(after.TaskTemplate.ContainerSpec)
	if !reflect.DeepEqual(before, after) {
		beforeJSON, _ := json.MarshalIndent(before, "", "  ")
		afterJSON, _ := json.MarshalIndent(after, "", "  ")
		t.Fatalf("service fields outside network attachments changed\nbefore=%s\nafter=%s", beforeJSON, afterJSON)
	}
}

func cloneContainerWithoutNetworkHash(spec *dockerswarm.ContainerSpec) *dockerswarm.ContainerSpec {
	if spec == nil {
		return nil
	}
	cloned := *spec
	cloned.Labels = cloneLabels(spec.Labels)
	delete(cloned.Labels, moduleosswarm.LabelTaskTemplate)
	return &cloned
}

func assertNetworkAttachment(t *testing.T, networks []dockerswarm.NetworkAttachmentConfig, networkID string, aliases []string, count int) {
	t.Helper()
	seen := 0
	for _, attachment := range networks {
		if attachment.Target != networkID {
			continue
		}
		seen++
		actualAliases := append([]string(nil), attachment.Aliases...)
		sort.Strings(actualAliases)
		expectedAliases := append([]string(nil), aliases...)
		sort.Strings(expectedAliases)
		if aliases != nil && !reflect.DeepEqual(actualAliases, expectedAliases) {
			t.Fatalf("network %s aliases = %v, want %v", shortID(networkID), actualAliases, expectedAliases)
		}
	}
	if seen != count {
		t.Fatalf("network %s attachment count = %d, want %d", shortID(networkID), seen, count)
	}
}

func execContainer(ctx context.Context, dockerClient *client.Client, containerID string, command []string) (string, error) {
	created, err := dockerClient.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		AttachStdout: true, AttachStderr: true, Cmd: command,
	})
	if err != nil {
		return "", fmt.Errorf("create exec: %w", err)
	}
	attached, err := dockerClient.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("attach exec: %w", err)
	}
	stopClose := context.AfterFunc(ctx, attached.Close)
	defer func() {
		stopClose()
		attached.Close()
	}()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil {
		return "", fmt.Errorf("consume exec stream: %w", err)
	}
	inspected, err := dockerClient.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect exec: %w", err)
	}
	output := stdout.String() + stderr.String()
	if len(output) > 4096 {
		output = output[:4096] + "..."
	}
	if inspected.ExitCode != 0 {
		return output, &execExitError{code: inspected.ExitCode}
	}
	return output, nil
}

type execExitError struct {
	code int
}

func (e *execExitError) Error() string {
	return fmt.Sprintf("exec exited with code %d", e.code)
}

func requireServiceAbsent(ctx context.Context, dockerClient *moduleosswarm.DockerClient, name string) error {
	_, err := dockerClient.GetService(ctx, moduleosswarm.ServiceName(name))
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("service %q exists", name)
	}
	return err
}
