package swarm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/containerd/errdefs"
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
	swarmSpec := buildSwarmSpec(spec)

	_, err := c.docker.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: swarmSpec})
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

// RemoveService removes a service.
func (c *DockerClient) RemoveService(ctx context.Context, serviceID string) error {
	if _, err := c.docker.ServiceRemove(ctx, serviceID, client.ServiceRemoveOptions{}); err != nil {
		return fmt.Errorf("failed to remove service: %w", err)
	}
	return nil
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
	filters.Add("desired-state", "running")
	tasks, taskErr := c.docker.TaskList(ctx, client.TaskListOptions{Filters: filters})
	if taskErr != nil {
		return nil, fmt.Errorf("failed to list tasks for service %q: %w", serviceID, taskErr)
	}
	info.Running = 0
	for _, task := range tasks.Items {
		if info.Spec.TaskTemplateHash != "" && taskTemplateMarker(task.Spec) != info.Spec.TaskTemplateHash {
			continue
		}
		if task.Status.State == dockerswarm.TaskStateRunning {
			info.Running++
		}
		if task.Status.Err != "" {
			info.TaskErrors = append(info.TaskErrors, task.Status.Err)
		}
	}
	return info, nil
}

func taskTemplateMarker(spec dockerswarm.TaskSpec) string {
	if spec.ContainerSpec == nil {
		return ""
	}
	return spec.ContainerSpec.Labels[LabelTaskTemplate]
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
	filters.Add("type", "service", "task")
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
				Condition: dockerswarm.RestartPolicyConditionAny,
				Delay:     &spec.Restart.Delay,
			},
		},
		Mode: dockerswarm.ServiceMode{
			Replicated: &dockerswarm.ReplicatedService{
				Replicas: &replicas,
			},
		},
		EndpointSpec: &dockerswarm.EndpointSpec{
			Mode:  dockerswarm.ResolutionModeVIP,
			Ports: ports,
		},
		UpdateConfig: &dockerswarm.UpdateConfig{
			Parallelism:   spec.Update.Parallelism,
			Delay:         spec.Update.Delay,
			Monitor:       spec.Update.Monitor,
			FailureAction: dockerswarm.UpdateFailureActionPause,
			Order:         dockerswarm.UpdateOrderStopFirst,
		},
		RollbackConfig: &dockerswarm.UpdateConfig{
			Parallelism:   1,
			Delay:         spec.Update.Delay,
			Monitor:       spec.Update.Monitor,
			FailureAction: dockerswarm.UpdateFailureActionPause,
			Order:         dockerswarm.UpdateOrderStopFirst,
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

	info := &ServiceInfo{
		ID:       svc.ID,
		Name:     svc.Spec.Name,
		Image:    svc.Spec.TaskTemplate.ContainerSpec.Image,
		Replicas: replicas,
		Running:  running,
		Labels:   svc.Spec.Labels,
	}
	info.Spec = serviceSpecFromDocker(svc.Spec)
	return info
}

func serviceSpecFromDocker(spec dockerswarm.ServiceSpec) ServiceSpec {
	result := ServiceSpec{
		Name:     strings.TrimPrefix(spec.Name, servicePrefix),
		Labels:   spec.Labels,
		Replicas: 0,
	}
	if spec.Mode.Replicated != nil && spec.Mode.Replicated.Replicas != nil {
		result.Replicas = *spec.Mode.Replicated.Replicas
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
			Parallelism: spec.UpdateConfig.Parallelism,
			Delay:       spec.UpdateConfig.Delay,
			Monitor:     spec.UpdateConfig.Monitor,
		}
	}
	if spec.TaskTemplate.RestartPolicy != nil && spec.TaskTemplate.RestartPolicy.Delay != nil {
		result.Restart.Delay = *spec.TaskTemplate.RestartPolicy.Delay
	}
	ports, _ := normalizePorts(result.Ports)
	result.Ports = ports
	volumes, _ := normalizeVolumes(result.Volumes)
	result.Volumes = volumes
	networks, _ := normalizeNetworks(result.Networks)
	result.Networks = networks
	sort.Strings(result.EnvVars)
	return result
}
