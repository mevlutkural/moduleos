package swarm

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	dockercontainer "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	dockerswarm "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

const servicePrefix = "moduleos_"

type DockerClient struct {
	docker *client.Client
}

func NewDockerClient() (*DockerClient, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &DockerClient{docker: cli}, nil
}

func NewDockerClientWithEndpoint(endpoint string) (*DockerClient, error) {
	if endpoint != "" && !strings.Contains(endpoint, "://") {
		endpoint = "unix://" + endpoint
	}
	cli, err := client.New(client.WithHost(endpoint))
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}
	return &DockerClient{docker: cli}, nil
}

func (c *DockerClient) Health(ctx context.Context) error {
	if _, err := c.docker.Ping(ctx, client.PingOptions{}); err != nil {
		return fmt.Errorf("docker_unavailable: %w", err)
	}
	result, err := c.docker.Info(ctx, client.InfoOptions{})
	if err != nil {
		return fmt.Errorf("docker_info_unavailable: %w", err)
	}
	if result.Info.Swarm.LocalNodeState != dockerswarm.LocalNodeStateActive {
		return fmt.Errorf("swarm_inactive")
	}
	if !result.Info.Swarm.ControlAvailable {
		return fmt.Errorf("swarm_manager_required")
	}
	return nil
}

func (c *DockerClient) Close() error {
	return c.docker.Close()
}

// Client returns the underlying Docker client for integration diagnostics.
func (c *DockerClient) Client() *client.Client {
	return c.docker
}

// EnsureNetwork creates an overlay network if it does not already exist.
func (c *DockerClient) EnsureNetwork(ctx context.Context, networkName string) error {
	return c.ensureNetwork(ctx, networkName, nil)
}

func (c *DockerClient) EnsureProjectNetwork(ctx context.Context, networkName, projectID, projectSlug string) error {
	return c.ensureNetwork(ctx, networkName, map[string]string{
		LabelManagedBy:    "true",
		LabelResourceKind: "project-network",
		LabelProjectID:    projectID,
		LabelProject:      projectSlug,
		LabelSchema:       SchemaVersion,
	})
}

func (c *DockerClient) ensureNetwork(ctx context.Context, networkName string, labels map[string]string) error {
	filters := make(client.Filters)
	filters.Add("name", networkName)
	networks, err := c.docker.NetworkList(ctx, client.NetworkListOptions{
		Filters: filters,
	})
	if err != nil {
		return fmt.Errorf("failed to list networks: %w", err)
	}

	for _, n := range networks.Items {
		if n.Name == networkName {
			if n.Driver != "overlay" || !n.Attachable {
				return fmt.Errorf("%w: network %q has incompatible driver or attachable mode", ErrInvalidSpec, networkName)
			}
			for key, value := range labels {
				if n.Labels[key] != value {
					return fmt.Errorf("%w: network %q ownership labels do not match", ErrOwnershipConflict, networkName)
				}
			}
			return nil // already exists
		}
	}

	_, err = c.docker.NetworkCreate(ctx, networkName, client.NetworkCreateOptions{
		Driver:     "overlay",
		Attachable: true,
		Labels:     labels,
	})
	if err != nil {
		return fmt.Errorf("failed to create network: %w", err)
	}

	return nil
}

// CreateService creates a new Swarm service.
func (c *DockerClient) CreateService(ctx context.Context, spec ServiceSpec) error {
	resolvedImage, err := c.resolveImage(ctx, spec.Image)
	if err != nil {
		return err
	}
	spec.Image = resolvedImage
	swarmSpec := buildSwarmSpec(spec)

	_, err = c.docker.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: swarmSpec})
	if err != nil {
		return fmt.Errorf("failed to create service: %w", err)
	}

	return nil
}

// UpdateService updates an existing service.
func (c *DockerClient) UpdateService(ctx context.Context, serviceID string, spec ServiceSpec) error {
	inspection, err := c.docker.ServiceInspect(ctx, serviceID, client.ServiceInspectOptions{})
	if err != nil {
		return fmt.Errorf("service not found: %w", err)
	}
	current := inspection.Service

	currentImage := ""
	if containerSpec := current.Spec.TaskTemplate.ContainerSpec; containerSpec != nil {
		currentImage = containerSpec.Image
	}
	refreshImage := !IsImmutableImageReference(spec.Image) &&
		(!imageEquivalent(spec.Image, currentImage) || spec.RefreshImage)
	if refreshImage {
		resolvedImage, err := c.resolveImage(ctx, spec.Image)
		if err != nil {
			return err
		}
		spec.Image = resolvedImage
	} else if imageEquivalent(spec.Image, currentImage) {
		spec.Image = currentImage
	}
	swarmSpec := buildSwarmSpec(spec)
	swarmSpec.Labels = mergeServiceLabels(current.Spec.Labels, swarmSpec.Labels)

	_, err = c.docker.ServiceUpdate(ctx, serviceID, client.ServiceUpdateOptions{
		Version: current.Version,
		Spec:    swarmSpec,
	})
	if err != nil {
		return fmt.Errorf("failed to update service: %w", err)
	}

	return nil
}

func (c *DockerClient) resolveImage(ctx context.Context, image string) (string, error) {
	if IsImmutableImageReference(image) {
		return image, nil
	}
	inspection, err := c.docker.DistributionInspect(ctx, image, client.DistributionInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrImageResolution, err)
	}
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrImageResolution, err)
	}
	resolved, err := reference.WithDigest(named, inspection.Descriptor.Digest)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrImageResolution, err)
	}
	return reference.FamiliarString(resolved), nil
}

// RemoveService removes a service.
func (c *DockerClient) RemoveService(ctx context.Context, serviceID string) error {
	if _, err := c.docker.ServiceRemove(ctx, serviceID, client.ServiceRemoveOptions{}); err != nil {
		return fmt.Errorf("failed to remove service: %w", err)
	}
	return nil
}

func (c *DockerClient) InspectService(ctx context.Context, serviceID string) (*ServiceInfo, error) {
	result, err := c.docker.ServiceInspect(ctx, serviceID, client.ServiceInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("service not found: %w", err)
	}
	return toServiceInfo(result.Service), nil
}

// AttachServiceNetwork attaches a network to a service, preserving other configurations.
func (c *DockerClient) AttachServiceNetwork(ctx context.Context, serviceID string, attachment NetworkAttachment) error {
	networkID, err := c.resolveNetworkID(ctx, attachment.Network)
	if err != nil {
		return err
	}
	attachment.Network = networkID
	return c.mutateServiceNetworks(ctx, serviceID, func(networks []dockerswarm.NetworkAttachmentConfig) ([]dockerswarm.NetworkAttachmentConfig, bool) {
		return attachNetworkMutator(networks, attachment)
	})
}

// DetachServiceNetwork removes a network attachment from a service.
func (c *DockerClient) DetachServiceNetwork(ctx context.Context, serviceID string, networkName string) error {
	networkID, err := c.resolveNetworkID(ctx, networkName)
	if err != nil {
		return err
	}
	return c.mutateServiceNetworks(ctx, serviceID, func(networks []dockerswarm.NetworkAttachmentConfig) ([]dockerswarm.NetworkAttachmentConfig, bool) {
		return detachNetworkMutator(networks, networkID)
	})
}

func (c *DockerClient) resolveNetworkID(ctx context.Context, networkNameOrID string) (string, error) {
	result, err := c.docker.NetworkInspect(ctx, networkNameOrID, client.NetworkInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("network inspect failed for %q: %w", networkNameOrID, err)
	}
	if result.Network.ID == "" {
		return "", fmt.Errorf("network inspect returned an empty ID for %q", networkNameOrID)
	}
	return result.Network.ID, nil
}

func attachNetworkMutator(networks []dockerswarm.NetworkAttachmentConfig, attachment NetworkAttachment) ([]dockerswarm.NetworkAttachmentConfig, bool) {
	for i, net := range networks {
		if net.Target == attachment.Network {
			// network found, check if all aliases exist
			changed := false
			for _, newAlias := range attachment.Aliases {
				found := false
				for _, existingAlias := range net.Aliases {
					if existingAlias == newAlias {
						found = true
						break
					}
				}
				if !found {
					networks[i].Aliases = append(networks[i].Aliases, newAlias)
					changed = true
				}
			}
			return networks, changed
		}
	}

	// network not found, add it
	networks = append(networks, dockerswarm.NetworkAttachmentConfig{
		Target:  attachment.Network,
		Aliases: attachment.Aliases,
	})
	return networks, true
}

func detachNetworkMutator(networks []dockerswarm.NetworkAttachmentConfig, networkName string) ([]dockerswarm.NetworkAttachmentConfig, bool) {
	var newNets []dockerswarm.NetworkAttachmentConfig
	changed := false
	for _, net := range networks {
		if net.Target == networkName {
			changed = true
		} else {
			newNets = append(newNets, net)
		}
	}
	return newNets, changed
}

func (c *DockerClient) mutateServiceNetworks(ctx context.Context, serviceID string, mutator func([]dockerswarm.NetworkAttachmentConfig) ([]dockerswarm.NetworkAttachmentConfig, bool)) error {
	return mutateServiceNetworksWithRetry(ctx, serviceID, mutator,
		func(ctx context.Context, serviceID string) (dockerswarm.Service, error) {
			result, err := c.docker.ServiceInspect(ctx, serviceID, client.ServiceInspectOptions{})
			return result.Service, err
		},
		func(ctx context.Context, serviceID string, version dockerswarm.Version, spec dockerswarm.ServiceSpec) error {
			_, err := c.docker.ServiceUpdate(ctx, serviceID, client.ServiceUpdateOptions{
				Version: version,
				Spec:    spec,
			})
			return err
		},
	)
}

type inspectServiceForMutation func(context.Context, string) (dockerswarm.Service, error)
type updateServiceForMutation func(context.Context, string, dockerswarm.Version, dockerswarm.ServiceSpec) error

func mutateServiceNetworksWithRetry(
	ctx context.Context,
	serviceID string,
	mutator func([]dockerswarm.NetworkAttachmentConfig) ([]dockerswarm.NetworkAttachmentConfig, bool),
	inspect inspectServiceForMutation,
	update updateServiceForMutation,
) error {
	const maxRetries = 10
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		current, err := inspect(ctx, serviceID)
		if err != nil {
			return fmt.Errorf("service inspect failed: %w", err)
		}

		newNets, changed := mutator(current.Spec.TaskTemplate.Networks)
		if !changed {
			return nil // idempotent success
		}

		current.Spec.TaskTemplate.Networks = newNets

		err = update(ctx, serviceID, current.Version, current.Spec)
		if err == nil {
			return nil // success
		}

		lastErr = err
		if !isServiceUpdateConflict(err) {
			return fmt.Errorf("failed to update service networks: %w", err)
		}
	}

	return fmt.Errorf("failed to update service networks after %d retries: %w", maxRetries, lastErr)
}

func isServiceUpdateConflict(err error) bool {
	return errdefs.IsConflict(err) || strings.Contains(strings.ToLower(err.Error()), "update out of sequence")
}

type slotObservation struct {
	latestErrorTask      dockerswarm.Task
	hasError             bool
	latestResolutionTask dockerswarm.Task
	hasResolution        bool
	latestRemovalTask    dockerswarm.Task
	hasRemoval           bool
	active               bool
}

// GetService returns service info. serviceID may be a name or an ID.
func (c *DockerClient) GetService(ctx context.Context, serviceID string) (*ServiceInfo, error) {
	result, err := c.docker.ServiceInspect(ctx, serviceID, client.ServiceInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("service not found: %w", err)
	}

	info := toServiceInfo(result.Service)
	for i := range info.Spec.Networks {
		networkResult, inspectErr := c.docker.NetworkInspect(ctx, info.Spec.Networks[i].Network, client.NetworkInspectOptions{})
		if inspectErr != nil && !errdefs.IsNotFound(inspectErr) {
			return nil, fmt.Errorf("failed to inspect network %q for service %q: %w", info.Spec.Networks[i].Network, serviceID, inspectErr)
		}
		if inspectErr == nil && networkResult.Network.Name != "" {
			info.Spec.Networks[i].Network = networkResult.Network.Name
		}
	}
	filters := make(client.Filters)
	filters.Add("service", result.Service.ID)
	tasks, taskErr := c.docker.TaskList(ctx, client.TaskListOptions{Filters: filters})
	if taskErr != nil {
		return nil, fmt.Errorf("failed to list tasks for service %q: %w", serviceID, taskErr)
	}
	info.Running = 0
	info.Terminating = 0
	currentTaskIDs := make([]string, 0, len(tasks.Items))
	tasksBySlot := make(map[int]slotObservation)
	unassignedTaskErrors := make([]string, 0)
	for _, task := range tasks.Items {
		if task.Status.State == dockerswarm.TaskStateRunning && task.DesiredState != dockerswarm.TaskStateRunning {
			info.Terminating++
		}
		if task.Slot > 0 && taskDesiredStateIsActive(task.DesiredState) {
			observation := tasksBySlot[task.Slot]
			observation.active = true
			tasksBySlot[task.Slot] = observation
		}
		if task.Slot > 0 && task.DesiredState == dockerswarm.TaskStateRemove {
			observation := tasksBySlot[task.Slot]
			if !observation.hasRemoval || taskAttemptIsNewer(task, observation.latestRemovalTask) {
				observation.latestRemovalTask = task
				observation.hasRemoval = true
			}
			tasksBySlot[task.Slot] = observation
		}
		if !taskMatchesTemplate(task.Spec, result.Service.Spec.TaskTemplate) {
			continue
		}
		if task.DesiredState == dockerswarm.TaskStateRunning {
			currentTaskIDs = append(currentTaskIDs, task.ID)
		}
		if task.Status.State == dockerswarm.TaskStateRunning && task.DesiredState == dockerswarm.TaskStateRunning {
			info.Running++
		}
		if task.Slot > 0 && info.Replicas > 0 {
			observation := tasksBySlot[task.Slot]
			if task.Status.Err != "" && taskErrorBelongsToCurrentServiceState(task) &&
				(!observation.hasError || taskAttemptIsNewer(task, observation.latestErrorTask)) {
				observation.latestErrorTask = task
				observation.hasError = true
			}
			if taskResolvesPriorRejection(task) &&
				(!observation.hasResolution || taskAttemptIsNewer(task, observation.latestResolutionTask)) {
				observation.latestResolutionTask = task
				observation.hasResolution = true
			}
			tasksBySlot[task.Slot] = observation
		} else if task.Slot == 0 && task.DesiredState == dockerswarm.TaskStateRunning && task.Status.Err != "" {
			unassignedTaskErrors = append(unassignedTaskErrors, task.Status.Err)
		}
	}
	sort.Strings(unassignedTaskErrors)
	info.TaskErrors = append(info.TaskErrors, unassignedTaskErrors...)
	slots := make([]int, 0, len(tasksBySlot))
	activeSlots := 0
	for slot := range tasksBySlot {
		slots = append(slots, slot)
		if tasksBySlot[slot].active {
			activeSlots++
		}
	}
	sort.Ints(slots)
	for _, slot := range slots {
		observation := tasksBySlot[slot]
		if !observation.hasError {
			continue
		}
		if observation.hasRemoval &&
			!taskAttemptIsNewer(observation.latestErrorTask, observation.latestRemovalTask) {
			continue
		}
		if !observation.active && uint64(activeSlots) >= info.Replicas {
			continue
		}
		if !serviceUpdateIsPaused(result.Service) && observation.hasResolution &&
			taskAttemptIsNewer(observation.latestResolutionTask, observation.latestErrorTask) {
			continue
		}
		info.TaskErrors = append(info.TaskErrors, observation.latestErrorTask.Status.Err)
	}
	sort.Strings(currentTaskIDs)
	info.TaskSetFingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(currentTaskIDs, "\x00"))))
	return info, nil
}

func taskAttemptIsNewer(candidate, current dockerswarm.Task) bool {
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	if candidate.Version.Index != current.Version.Index {
		return candidate.Version.Index > current.Version.Index
	}
	if !candidate.UpdatedAt.Equal(current.UpdatedAt) {
		return candidate.UpdatedAt.After(current.UpdatedAt)
	}
	if !candidate.Status.Timestamp.Equal(current.Status.Timestamp) {
		return candidate.Status.Timestamp.After(current.Status.Timestamp)
	}
	return candidate.ID > current.ID
}

func taskDesiredStateIsActive(state dockerswarm.TaskState) bool {
	switch state {
	case dockerswarm.TaskStateNew, dockerswarm.TaskStateAllocated, dockerswarm.TaskStatePending,
		dockerswarm.TaskStateAssigned, dockerswarm.TaskStateAccepted, dockerswarm.TaskStatePreparing,
		dockerswarm.TaskStateReady, dockerswarm.TaskStateStarting, dockerswarm.TaskStateRunning:
		return true
	default:
		return false
	}
}

func taskResolvesPriorRejection(task dockerswarm.Task) bool {
	if task.Status.State == dockerswarm.TaskStateRejected {
		return false
	}
	if taskDesiredStateIsActive(task.DesiredState) {
		return true
	}
	switch task.Status.State {
	case dockerswarm.TaskStateComplete, dockerswarm.TaskStateShutdown, dockerswarm.TaskStateFailed,
		dockerswarm.TaskStateRemove, dockerswarm.TaskStateOrphaned:
		return true
	default:
		return false
	}
}

func taskErrorBelongsToCurrentServiceState(task dockerswarm.Task) bool {
	if task.DesiredState == dockerswarm.TaskStateRunning {
		return true
	}
	return task.DesiredState == dockerswarm.TaskStateShutdown && task.Status.State == dockerswarm.TaskStateRejected
}

func serviceUpdateIsPaused(service dockerswarm.Service) bool {
	return service.UpdateStatus != nil &&
		(service.UpdateStatus.State == dockerswarm.UpdateStatePaused ||
			service.UpdateStatus.State == dockerswarm.UpdateStateRollbackPaused)
}

func serviceUpdateIsInProgress(service dockerswarm.Service) bool {
	return service.UpdateStatus != nil &&
		(service.UpdateStatus.State == dockerswarm.UpdateStateUpdating ||
			service.UpdateStatus.State == dockerswarm.UpdateStateRollbackStarted)
}

func taskMatchesTemplate(task, current dockerswarm.TaskSpec) bool {
	return reflect.DeepEqual(normalizeTaskRuntime(task), normalizeTaskRuntime(current))
}

func normalizeTaskRuntime(spec dockerswarm.TaskSpec) dockerswarm.TaskSpec {
	if spec.ContainerSpec != nil && (spec.Runtime == "" || spec.Runtime == dockerswarm.RuntimeContainer) {
		spec.Runtime = ""
	}
	return spec
}

func IsTransientError(err error) bool {
	if err == nil {
		return false
	}
	if client.IsErrConnectionFailed(err) || errdefs.IsUnavailable(err) || errdefs.IsConflict(err) ||
		errdefs.IsResourceExhausted(err) || errdefs.IsInternal(err) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

// ListServices lists all services managed by moduleos.
func (c *DockerClient) ListServices(ctx context.Context) ([]ServiceInfo, error) {
	filters := make(client.Filters)
	filters.Add("label", LabelManagedBy+"=true")
	services, err := c.docker.ServiceList(ctx, client.ServiceListOptions{
		Filters: filters,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list services: %w", err)
	}

	result := make([]ServiceInfo, 0, len(services.Items))
	for _, svc := range services.Items {
		result = append(result, *toServiceInfo(svc))
	}

	return result, nil
}

// GetServiceLogs returns a log stream for a service.
func (c *DockerClient) GetServiceLogs(ctx context.Context, serviceID string, tail string, follow bool) (io.ReadCloser, error) {
	logs, err := c.docker.ServiceLogs(ctx, serviceID, client.ServiceLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
		Timestamps: false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve logs: %w", err)
	}

	return logs, nil
}

// WatchEvents listens to the Docker event stream. Returns two channels: events and errors.
func (c *DockerClient) WatchEvents(ctx context.Context) (<-chan SwarmEvent, <-chan error) {
	out := make(chan SwarmEvent)
	errCh := make(chan error, 1)

	filters := make(client.Filters)
	filters.Add("type", "service", "container")
	dockerEvents := c.docker.Events(ctx, client.EventsListOptions{Filters: filters})

	go func() {
		defer close(out)
		defer close(errCh)
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-dockerEvents.Err:
				if ok && err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
					errCh <- err
				}
				return
			case e, ok := <-dockerEvents.Messages:
				if !ok {
					return
				}
				target := e.Actor.Attributes["com.docker.swarm.service.name"]
				if target == "" {
					target = e.Actor.Attributes["service"]
				}
				if target == "" {
					target = e.Actor.Attributes["name"]
				}
				evt := SwarmEvent{
					Type:   string(e.Type),
					Action: string(e.Action),
					Target: target,
				}
				select {
				case out <- evt:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, errCh
}

func (c *DockerClient) GetNetwork(ctx context.Context, networkNameOrID string) (*NetworkInfo, error) {
	result, err := c.docker.NetworkInspect(ctx, networkNameOrID, client.NetworkInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("network not found: %w", err)
	}
	return &NetworkInfo{
		ID:         result.Network.ID,
		Name:       result.Network.Name,
		Driver:     result.Network.Driver,
		Attachable: result.Network.Attachable,
		Labels:     result.Network.Labels,
	}, nil
}

func (c *DockerClient) ListNetworks(ctx context.Context) ([]NetworkInfo, error) {
	result, err := c.docker.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}
	networks := make([]NetworkInfo, 0, len(result.Items))
	for _, item := range result.Items {
		networks = append(networks, NetworkInfo{
			ID:         item.ID,
			Name:       item.Name,
			Driver:     item.Driver,
			Attachable: item.Attachable,
			Labels:     item.Labels,
		})
	}
	return networks, nil
}

func (c *DockerClient) RemoveNetwork(ctx context.Context, networkNameOrID string) error {
	if _, err := c.docker.NetworkRemove(ctx, networkNameOrID, client.NetworkRemoveOptions{}); err != nil {
		return fmt.Errorf("failed to remove network: %w", err)
	}
	return nil
}

// ServiceName produces the Swarm service name from an app name.
func ServiceName(appName string) string {
	return servicePrefix + appName
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func buildSwarmSpec(spec ServiceSpec) dockerswarm.ServiceSpec {
	replicas := spec.Replicas

	// Env vars
	env := make([]string, len(spec.EnvVars))
	copy(env, spec.EnvVars)

	// Mounts
	mounts := make([]mount.Mount, 0, len(spec.Volumes))
	for _, v := range spec.Volumes {
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   v.Source,
			Target:   v.Target,
			ReadOnly: v.ReadOnly,
		})
	}

	ports := make([]dockerswarm.PortConfig, 0, len(spec.Ports))
	for _, p := range spec.Ports {
		if p.PublishedPort == 0 {
			continue
		}
		ports = append(ports, dockerswarm.PortConfig{
			Protocol:      network.IPProtocol(p.Protocol),
			TargetPort:    p.ContainerPort,
			PublishedPort: p.PublishedPort,
			PublishMode:   dockerswarm.PortConfigPublishMode(p.PublishMode),
		})
	}

	// Networks + aliases
	netAttachments := make([]dockerswarm.NetworkAttachmentConfig, 0, len(spec.Networks))
	for _, n := range spec.Networks {
		netAttachments = append(netAttachments, dockerswarm.NetworkAttachmentConfig{
			Target:  n.Network,
			Aliases: n.Aliases,
		})
	}

	return dockerswarm.ServiceSpec{
		Annotations: dockerswarm.Annotations{
			Name:   ServiceName(spec.Name),
			Labels: spec.Labels,
		},
		TaskTemplate: dockerswarm.TaskSpec{
			ContainerSpec: &dockerswarm.ContainerSpec{
				Image: spec.Image,
				Labels: map[string]string{
					LabelTaskTemplate: spec.TaskTemplateHash,
				},
				Env:    env,
				Mounts: mounts,
			},
			Networks: netAttachments,
			RestartPolicy: &dockerswarm.RestartPolicy{
				Condition: dockerswarm.RestartPolicyCondition(spec.Restart.Condition),
				Delay:     &spec.Restart.Delay,
			},
		},
		Mode: dockerswarm.ServiceMode{
			Replicated: &dockerswarm.ReplicatedService{
				Replicas: &replicas,
			},
		},
		EndpointSpec: &dockerswarm.EndpointSpec{
			Mode:  dockerswarm.ResolutionMode(spec.EndpointMode),
			Ports: ports,
		},
		UpdateConfig: &dockerswarm.UpdateConfig{
			Parallelism:     spec.Update.Parallelism,
			Delay:           spec.Update.Delay,
			Monitor:         spec.Update.Monitor,
			MaxFailureRatio: spec.Update.MaxFailureRatio,
			FailureAction:   dockerswarm.FailureAction(spec.Update.FailureAction),
			Order:           dockerswarm.UpdateOrder(spec.Update.Order),
		},
		RollbackConfig: &dockerswarm.UpdateConfig{
			Parallelism:     spec.Rollback.Parallelism,
			Delay:           spec.Rollback.Delay,
			Monitor:         spec.Rollback.Monitor,
			MaxFailureRatio: spec.Rollback.MaxFailureRatio,
			FailureAction:   dockerswarm.FailureAction(spec.Rollback.FailureAction),
			Order:           dockerswarm.UpdateOrder(spec.Rollback.Order),
		},
	}
}

func mergeServiceLabels(current, desired map[string]string) map[string]string {
	merged := make(map[string]string, len(current)+len(desired))
	for key, value := range current {
		if !strings.HasPrefix(key, OwnedLabelPrefix) && !strings.HasPrefix(key, "traefik.") {
			merged[key] = value
		}
	}
	for key, value := range desired {
		merged[key] = value
	}
	return merged
}

func toServiceInfo(svc dockerswarm.Service) *ServiceInfo {
	var replicas uint64
	if svc.Spec.Mode.Replicated != nil && svc.Spec.Mode.Replicated.Replicas != nil {
		replicas = *svc.Spec.Mode.Replicated.Replicas
	}

	var running uint64
	if svc.ServiceStatus != nil {
		running = svc.ServiceStatus.RunningTasks
	}

	spec := serviceSpecFromDocker(svc.Spec)
	info := &ServiceInfo{
		ID:       svc.ID,
		Name:     svc.Spec.Name,
		Image:    spec.Image,
		Replicas: replicas,
		Running:  running,
		Labels:   svc.Spec.Labels,
		Spec:     spec,
	}
	if serviceUpdateIsPaused(svc) {
		info.RolloutPaused = true
		info.RolloutMessage = "Swarm rollout is paused"
		if svc.UpdateStatus.Message != "" {
			info.RolloutMessage = svc.UpdateStatus.Message
		}
	}
	if serviceUpdateIsInProgress(svc) {
		info.RolloutInProgress = true
		info.RolloutMessage = "Swarm rollout is in progress"
		if svc.UpdateStatus.Message != "" {
			info.RolloutMessage = svc.UpdateStatus.Message
		}
	}
	return info
}

func serviceSpecFromDocker(spec dockerswarm.ServiceSpec) ServiceSpec {
	result := ServiceSpec{
		Name:                    strings.TrimPrefix(spec.Name, servicePrefix),
		Labels:                  spec.Labels,
		UnsupportedTaskTemplate: taskTemplateHasUnsupportedFields(spec.TaskTemplate),
	}
	if spec.Mode.Replicated != nil && spec.Mode.Replicated.Replicas != nil {
		result.ServiceMode = "replicated"
		result.Replicas = *spec.Mode.Replicated.Replicas
	} else if spec.Mode.Global != nil {
		result.ServiceMode = "global"
	} else if spec.Mode.ReplicatedJob != nil {
		result.ServiceMode = "replicated-job"
	} else if spec.Mode.GlobalJob != nil {
		result.ServiceMode = "global-job"
	}
	if containerSpec := spec.TaskTemplate.ContainerSpec; containerSpec != nil {
		result.Image = containerSpec.Image
		result.TaskTemplateHash = containerSpec.Labels[LabelTaskTemplate]
		result.EnvVars = append([]string(nil), containerSpec.Env...)
		for _, mounted := range containerSpec.Mounts {
			if mounted.Type == mount.TypeBind {
				result.Volumes = append(result.Volumes, VolumeConfig{
					Source: mounted.Source, Target: mounted.Target, ReadOnly: mounted.ReadOnly,
				})
			}
		}
	}
	for _, attached := range spec.TaskTemplate.Networks {
		result.Networks = append(result.Networks, NetworkAttachment{
			Network: attached.Target,
			Aliases: append([]string(nil), attached.Aliases...),
		})
	}
	if spec.EndpointSpec != nil {
		result.EndpointMode = string(spec.EndpointSpec.Mode)
		for _, port := range spec.EndpointSpec.Ports {
			result.Ports = append(result.Ports, PortConfig{
				ContainerPort: port.TargetPort,
				PublishedPort: port.PublishedPort,
				Protocol:      string(port.Protocol),
				PublishMode:   string(port.PublishMode),
			})
		}
	}
	if spec.UpdateConfig != nil {
		result.Update = UpdatePolicy{
			Parallelism:     spec.UpdateConfig.Parallelism,
			Delay:           spec.UpdateConfig.Delay,
			Monitor:         spec.UpdateConfig.Monitor,
			MaxFailureRatio: spec.UpdateConfig.MaxFailureRatio,
			FailureAction:   string(spec.UpdateConfig.FailureAction),
			Order:           string(spec.UpdateConfig.Order),
		}
	}
	if spec.RollbackConfig != nil {
		result.Rollback = UpdatePolicy{
			Parallelism:     spec.RollbackConfig.Parallelism,
			Delay:           spec.RollbackConfig.Delay,
			Monitor:         spec.RollbackConfig.Monitor,
			MaxFailureRatio: spec.RollbackConfig.MaxFailureRatio,
			FailureAction:   string(spec.RollbackConfig.FailureAction),
			Order:           string(spec.RollbackConfig.Order),
		}
	}
	if spec.TaskTemplate.RestartPolicy != nil && spec.TaskTemplate.RestartPolicy.Delay != nil {
		result.Restart.Condition = string(spec.TaskTemplate.RestartPolicy.Condition)
		result.Restart.Delay = *spec.TaskTemplate.RestartPolicy.Delay
	} else if spec.TaskTemplate.RestartPolicy != nil {
		result.Restart.Condition = string(spec.TaskTemplate.RestartPolicy.Condition)
	}
	if ports, err := normalizePorts(result.Ports); err == nil {
		result.Ports = ports
	}
	if volumes, err := normalizeVolumes(result.Volumes); err == nil {
		result.Volumes = volumes
	}
	if networks, err := normalizeNetworks(result.Networks); err == nil {
		result.Networks = networks
	}
	sort.Strings(result.EnvVars)
	return result
}

func taskTemplateHasUnsupportedFields(spec dockerswarm.TaskSpec) bool {
	remainder := spec
	remainder.ContainerSpec = nil
	remainder.RestartPolicy = nil
	remainder.Networks = nil
	remainder.ForceUpdate = 0
	if remainder.Resources != nil && reflect.DeepEqual(*remainder.Resources, dockerswarm.ResourceRequirements{}) {
		remainder.Resources = nil
	}
	if remainder.Placement != nil {
		placement := *remainder.Placement
		placement.Platforms = nil
		if reflect.DeepEqual(placement, dockerswarm.Placement{}) {
			remainder.Placement = nil
		} else {
			remainder.Placement = &placement
		}
	}
	if remainder.Runtime == dockerswarm.RuntimeContainer {
		remainder.Runtime = ""
	}
	if !reflect.DeepEqual(remainder, dockerswarm.TaskSpec{}) {
		return true
	}

	if spec.RestartPolicy != nil {
		restart := *spec.RestartPolicy
		restart.Condition = ""
		restart.Delay = nil
		if restart.MaxAttempts != nil && *restart.MaxAttempts == 0 {
			restart.MaxAttempts = nil
		}
		if restart.Window != nil && *restart.Window == 0 {
			restart.Window = nil
		}
		if !reflect.DeepEqual(restart, dockerswarm.RestartPolicy{}) {
			return true
		}
	}
	for _, network := range spec.Networks {
		if len(network.DriverOpts) > 0 {
			return true
		}
	}
	if spec.ContainerSpec == nil {
		return false
	}
	for label := range spec.ContainerSpec.Labels {
		if label != LabelTaskTemplate {
			return true
		}
	}
	for _, mounted := range spec.ContainerSpec.Mounts {
		supported := mount.Mount{
			Type: mounted.Type, Source: mounted.Source, Target: mounted.Target, ReadOnly: mounted.ReadOnly,
		}
		if mounted.Type != mount.TypeBind || !reflect.DeepEqual(mounted, supported) {
			return true
		}
	}
	container := *spec.ContainerSpec
	container.Image = ""
	container.Labels = nil
	container.Env = nil
	container.Mounts = nil
	if container.Isolation == dockercontainer.IsolationDefault {
		container.Isolation = dockercontainer.IsolationEmpty
	}
	if container.StopGracePeriod != nil && *container.StopGracePeriod == 10*time.Second {
		container.StopGracePeriod = nil
	}
	if container.DNSConfig != nil && len(container.DNSConfig.Nameservers) == 0 &&
		len(container.DNSConfig.Search) == 0 && len(container.DNSConfig.Options) == 0 {
		container.DNSConfig = nil
	}
	return !reflect.DeepEqual(container, dockerswarm.ContainerSpec{})
}
