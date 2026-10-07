package swarmfake

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/containerd/errdefs"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

// Client is an in-memory swarm.Client for control-plane tests.
type Client struct {
	mu               sync.Mutex
	Services         map[string]*swarm.ServiceInfo
	Networks         map[string][]swarm.NetworkAttachment
	NetworkResources map[string]*swarm.NetworkInfo
	AttachCalls      []NetworkMutationCall
	DetachCalls      []NetworkMutationCall
	CreateCalls      int
	UpdateCalls      int
	RemoveCalls      int
	EnsureCalls      int
	// Set these to simulate errors in test scenarios
	CreateError        error
	UpdateError        error
	RemoveError        error
	EnsureNetworkError error
	AttachError        error
	DetachError        error
	AttachHook         func()
	DetachHook         func()
	HealthError        error
}

func (m *Client) Health(_ context.Context) error { return m.HealthError }

type NetworkMutationCall struct {
	ServiceID  string
	Attachment swarm.NetworkAttachment
	Network    string
}

func New() *Client {
	return &Client{
		Services:         make(map[string]*swarm.ServiceInfo),
		Networks:         make(map[string][]swarm.NetworkAttachment),
		NetworkResources: make(map[string]*swarm.NetworkInfo),
	}
}

func (m *Client) EnsureNetwork(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.EnsureCalls++
	if m.EnsureNetworkError != nil {
		return m.EnsureNetworkError
	}
	if _, exists := m.NetworkResources[name]; !exists {
		m.NetworkResources[name] = &swarm.NetworkInfo{ID: "mock-network-" + name, Name: name, Driver: "overlay", Attachable: true}
	}
	return nil
}

func (m *Client) EnsureProjectNetwork(ctx context.Context, name, projectID, projectSlug string) error {
	if err := m.EnsureNetwork(ctx, name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NetworkResources[name].Labels = map[string]string{
		swarm.LabelManagedBy: "true", swarm.LabelResourceKind: "project-network",
		swarm.LabelProjectID: projectID, swarm.LabelProject: projectSlug, swarm.LabelSchema: swarm.SchemaVersion,
	}
	return nil
}

func (m *Client) CreateService(_ context.Context, spec swarm.ServiceSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CreateCalls++
	if m.CreateError != nil {
		return m.CreateError
	}
	name := swarm.ServiceName(spec.Name)
	m.Services[name] = &swarm.ServiceInfo{
		ID:       "mock-id-" + spec.Name,
		Name:     name,
		Image:    spec.Image,
		Replicas: spec.Replicas,
		Running:  spec.Replicas,
		Labels:   spec.Labels,
		Spec:     spec,
	}
	return nil
}

func (m *Client) EnsureNetworkWithOptions(_ context.Context, _ string) error {
	return nil
}

func (m *Client) UpdateService(_ context.Context, serviceID string, spec swarm.ServiceSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.UpdateCalls++
	if m.UpdateError != nil {
		return m.UpdateError
	}
	svc, ok := m.Services[serviceID]
	if !ok {
		for _, candidate := range m.Services {
			if candidate.ID == serviceID {
				svc, ok = candidate, true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%w: service %s", errdefs.ErrNotFound, serviceID)
		}
	}
	svc.Image = spec.Image
	svc.Replicas = spec.Replicas
	svc.Running = spec.Replicas
	svc.Labels = spec.Labels
	svc.Spec = spec
	return nil
}

func (m *Client) RemoveService(_ context.Context, serviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RemoveCalls++
	if m.RemoveError != nil {
		return m.RemoveError
	}
	key := serviceID
	if _, ok := m.Services[key]; !ok {
		key = ""
		for candidateKey, candidate := range m.Services {
			if candidate.ID == serviceID {
				key = candidateKey
				break
			}
		}
		if key == "" {
			return fmt.Errorf("%w: service %s", errdefs.ErrNotFound, serviceID)
		}
	}
	delete(m.Services, key)
	return nil
}

func (m *Client) GetService(_ context.Context, serviceID string) (*swarm.ServiceInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc, ok := m.Services[serviceID]
	if !ok {
		for _, candidate := range m.Services {
			if candidate.ID == serviceID {
				svc, ok = candidate, true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("%w: service %s", errdefs.ErrNotFound, serviceID)
		}
	}
	copy := *svc
	return &copy, nil
}

func (m *Client) ListServices(_ context.Context) ([]swarm.ServiceInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]swarm.ServiceInfo, 0, len(m.Services))
	for _, svc := range m.Services {
		result = append(result, *svc)
	}
	return result, nil
}

func (m *Client) AttachServiceNetwork(ctx context.Context, serviceID string, attachment swarm.NetworkAttachment) error {
	m.AttachCalls = append(m.AttachCalls, NetworkMutationCall{ServiceID: serviceID, Attachment: attachment, Network: attachment.Network})
	if m.AttachHook != nil {
		m.AttachHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.AttachError != nil {
		return m.AttachError
	}
	if _, ok := m.Services[serviceID]; !ok {
		return fmt.Errorf("%w: service %s", errdefs.ErrNotFound, serviceID)
	}
	attachments := m.Networks[serviceID]
	for i, existing := range attachments {
		if existing.Network == attachment.Network {
			for _, alias := range attachment.Aliases {
				if !containsAlias(attachments[i].Aliases, alias) {
					attachments[i].Aliases = append(attachments[i].Aliases, alias)
				}
			}
			m.Networks[serviceID] = attachments
			return nil
		}
	}
	m.Networks[serviceID] = append(attachments, attachment)
	return nil
}

func (m *Client) DetachServiceNetwork(ctx context.Context, serviceID string, networkName string) error {
	m.DetachCalls = append(m.DetachCalls, NetworkMutationCall{ServiceID: serviceID, Network: networkName})
	if m.DetachHook != nil {
		m.DetachHook()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.DetachError != nil {
		return m.DetachError
	}
	if _, ok := m.Services[serviceID]; !ok {
		return fmt.Errorf("%w: service %s", errdefs.ErrNotFound, serviceID)
	}
	attachments := m.Networks[serviceID]
	kept := attachments[:0]
	for _, attachment := range attachments {
		if attachment.Network != networkName {
			kept = append(kept, attachment)
		}
	}
	m.Networks[serviceID] = kept
	return nil
}

func containsAlias(aliases []string, target string) bool {
	for _, alias := range aliases {
		if alias == target {
			return true
		}
	}
	return false
}

func (m *Client) GetNetwork(_ context.Context, networkNameOrID string) (*swarm.NetworkInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, network := range m.NetworkResources {
		if network.Name == networkNameOrID || network.ID == networkNameOrID {
			copy := *network
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("%w: network %s", errdefs.ErrNotFound, networkNameOrID)
}

func (m *Client) ListNetworks(_ context.Context) ([]swarm.NetworkInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]swarm.NetworkInfo, 0, len(m.NetworkResources))
	for _, network := range m.NetworkResources {
		result = append(result, *network)
	}
	return result, nil
}

func (m *Client) RemoveNetwork(_ context.Context, networkNameOrID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, network := range m.NetworkResources {
		if network.Name == networkNameOrID || network.ID == networkNameOrID {
			delete(m.NetworkResources, name)
			return nil
		}
	}
	return fmt.Errorf("%w: network %s", errdefs.ErrNotFound, networkNameOrID)
}

func (m *Client) GetServiceLogs(_ context.Context, _ string, _ string, _ bool) (io.ReadCloser, error) {
	line := []byte("mock log line\n")
	header := make([]byte, 8)
	header[0] = 0x01 // stdout
	binary.BigEndian.PutUint32(header[4:], uint32(len(line)))
	data := append(header, line...)
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *Client) WatchEvents(ctx context.Context) (<-chan swarm.SwarmEvent, <-chan error) {
	out := make(chan swarm.SwarmEvent)
	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		close(out)
		close(errCh)
	}()
	return out, errCh
}
