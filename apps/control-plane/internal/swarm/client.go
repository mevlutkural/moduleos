package swarm

import (
	"context"
	"io"
	"time"
)

// ServiceSpec holds the information needed to create or update a Swarm service.
type ServiceSpec struct {
	Name     string
	Image    string
	Replicas uint64
	EnvVars  []string // ["KEY=VALUE", ...]
	Ports    []PortConfig
	Volumes  []VolumeConfig
	Labels   map[string]string
	Networks []NetworkAttachment
	Update   UpdatePolicy
	Restart  RestartPolicy
}

type UpdatePolicy struct {
	Parallelism uint64
	Delay       time.Duration
	Monitor     time.Duration
}

type RestartPolicy struct {
	Delay time.Duration
}

// NetworkAttachment defines the network and aliases a service is attached to.
type NetworkAttachment struct {
	Network string
	Aliases []string // short names within this network
}

type PortConfig struct {
	ContainerPort uint32 `json:"container_port"`
	PublishedPort uint32 `json:"published_port"` // 0 means not published
	Protocol      string `json:"protocol"`
	PublishMode   string `json:"publish_mode"`
}

type VolumeConfig struct {
	Source   string `json:"source"` // host path
	Target   string `json:"target"` // container path
	ReadOnly bool   `json:"read_only"`
}

// ServiceInfo holds the runtime state of a Swarm service.
type ServiceInfo struct {
	ID         string
	Name       string
	Image      string
	Replicas   uint64
	Running    uint64 // number of actively running tasks
	Labels     map[string]string
	Spec       ServiceSpec
	TaskErrors []string
}

type NetworkInfo struct {
	ID         string
	Name       string
	Driver     string
	Attachable bool
	Labels     map[string]string
}

// SwarmEvent represents an event received from Docker.
type SwarmEvent struct {
	Type   string // "service" | "task"
	Action string // "create" | "update" | "remove"
	Target string // service name or ID
}

type Client interface {
	Health(ctx context.Context) error
	EnsureNetwork(ctx context.Context, networkName string) error
	EnsureProjectNetwork(ctx context.Context, networkName, projectID, projectSlug string) error
	CreateService(ctx context.Context, spec ServiceSpec) error
	UpdateService(ctx context.Context, serviceID string, spec ServiceSpec) error
	RemoveService(ctx context.Context, serviceID string) error
	GetService(ctx context.Context, serviceID string) (*ServiceInfo, error)
	ListServices(ctx context.Context) ([]ServiceInfo, error)
	GetServiceLogs(ctx context.Context, serviceID string, tail string, follow bool) (io.ReadCloser, error)
	WatchEvents(ctx context.Context) (<-chan SwarmEvent, <-chan error)
	GetNetwork(ctx context.Context, networkNameOrID string) (*NetworkInfo, error)
	ListNetworks(ctx context.Context) ([]NetworkInfo, error)
	RemoveNetwork(ctx context.Context, networkNameOrID string) error

	AttachServiceNetwork(ctx context.Context, serviceID string, attachment NetworkAttachment) error
	DetachServiceNetwork(ctx context.Context, serviceID string, networkName string) error
}
