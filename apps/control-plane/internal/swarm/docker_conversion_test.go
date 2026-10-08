package swarm

import (
	"reflect"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockerswarm "github.com/moby/moby/api/types/swarm"
)

func TestDockerServiceSpecRoundTrip(t *testing.T) {
	want := canonicalServiceSpec()
	want.EnvVars = []string{"B=2", "A=1"}
	dockerSpec := buildSwarmSpec(want)
	got := serviceSpecFromDocker(dockerSpec)

	want.EnvVars = []string{"A=1", "B=2"}
	want.Networks, _ = normalizeNetworks(want.Networks)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestServiceSpecFromDockerToleratesOptionalSections(t *testing.T) {
	got := serviceSpecFromDocker(dockerswarm.ServiceSpec{Annotations: dockerswarm.Annotations{Name: "moduleos_empty"}})
	if got.Name != "empty" || got.Replicas != 0 || got.Image != "" || len(got.Ports) != 0 || len(got.Networks) != 0 {
		t.Fatalf("minimal Docker spec converted incorrectly: %#v", got)
	}
}

func TestServiceSpecFromDockerFiltersAndNormalizesRuntimeData(t *testing.T) {
	replicas := uint64(3)
	restartDelay := 4 * time.Second
	spec := dockerswarm.ServiceSpec{
		Annotations: dockerswarm.Annotations{Name: "moduleos_api", Labels: map[string]string{LabelAppID: "app-id"}},
		TaskTemplate: dockerswarm.TaskSpec{
			ContainerSpec: &dockerswarm.ContainerSpec{
				Image: "nginx:1.27",
				Env:   []string{"B=2", "A=1"},
				Mounts: []mount.Mount{
					{Type: mount.TypeVolume, Source: "named", Target: "/ignored"},
					{Type: mount.TypeBind, Source: "/srv/moduleos/api", Target: "/data", ReadOnly: true},
				},
			},
			Networks: []dockerswarm.NetworkAttachmentConfig{
				{Target: "network-b", Aliases: []string{"b"}},
				{Target: "network-a", Aliases: []string{"a"}},
			},
			RestartPolicy: &dockerswarm.RestartPolicy{Delay: &restartDelay},
		},
		Mode: dockerswarm.ServiceMode{Replicated: &dockerswarm.ReplicatedService{Replicas: &replicas}},
		EndpointSpec: &dockerswarm.EndpointSpec{Ports: []dockerswarm.PortConfig{
			{TargetPort: 8080, PublishedPort: 30080, Protocol: "tcp", PublishMode: "ingress"},
		}},
		UpdateConfig: &dockerswarm.UpdateConfig{Parallelism: 2, Delay: time.Second, Monitor: 5 * time.Second},
	}

	got := serviceSpecFromDocker(spec)
	if got.Name != "api" || got.Replicas != 3 || got.Image != "nginx:1.27" || got.Restart.Delay != restartDelay {
		t.Fatalf("runtime fields were lost: %#v", got)
	}
	if !reflect.DeepEqual(got.EnvVars, []string{"A=1", "B=2"}) {
		t.Fatalf("environment was not normalized: %v", got.EnvVars)
	}
	if len(got.Volumes) != 1 || got.Volumes[0].Source != "/srv/moduleos/api" {
		t.Fatalf("bind mounts were not filtered correctly: %#v", got.Volumes)
	}
	if len(got.Networks) != 2 || got.Networks[0].Network != "network-a" {
		t.Fatalf("networks were not normalized: %#v", got.Networks)
	}
	if !got.UnsupportedTaskTemplate {
		t.Fatal("unsupported named volume was not preserved as task-template drift")
	}
}

func TestServiceSpecFromDockerFlagsUnsupportedTaskTemplateFields(t *testing.T) {
	one := uint64(1)
	tests := map[string]func(*dockerswarm.TaskSpec){
		"command": func(spec *dockerswarm.TaskSpec) {
			spec.ContainerSpec.Command = []string{"sh", "-c", "sleep 60"}
		},
		"resources": func(spec *dockerswarm.TaskSpec) {
			spec.Resources = &dockerswarm.ResourceRequirements{Limits: &dockerswarm.Limit{MemoryBytes: 1024}}
		},
		"placement": func(spec *dockerswarm.TaskSpec) {
			spec.Placement = &dockerswarm.Placement{Constraints: []string{"node.role==manager"}}
		},
		"healthcheck": func(spec *dockerswarm.TaskSpec) {
			spec.ContainerSpec.Healthcheck = &container.HealthConfig{Test: []string{"CMD", "true"}}
		},
		"dns config": func(spec *dockerswarm.TaskSpec) {
			spec.ContainerSpec.DNSConfig = &dockerswarm.DNSConfig{Search: []string{"internal.example"}}
		},
		"stop grace period": func(spec *dockerswarm.TaskSpec) {
			grace := 30 * time.Second
			spec.ContainerSpec.StopGracePeriod = &grace
		},
		"network driver options": func(spec *dockerswarm.TaskSpec) {
			spec.Networks[0].DriverOpts = map[string]string{"encrypted": "true"}
		},
		"restart attempts": func(spec *dockerswarm.TaskSpec) {
			spec.RestartPolicy.MaxAttempts = &one
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			desired := canonicalServiceSpec()
			runtimeSpec := buildSwarmSpec(desired)
			mutate(&runtimeSpec.TaskTemplate)
			observed := serviceSpecFromDocker(runtimeSpec)
			if !observed.UnsupportedTaskTemplate {
				t.Fatal("unsupported task-template field was discarded")
			}
			if got := DiffServiceSpec(desired, observed); !reflect.DeepEqual(got, []string{"task_template"}) {
				t.Fatalf("unsupported field diff = %v, want task_template", got)
			}
		})
	}
}

func TestServiceSpecFromDockerAcceptsSwarmTaskDefaults(t *testing.T) {
	desired := canonicalServiceSpec()
	runtimeSpec := buildSwarmSpec(desired)
	grace := 10 * time.Second
	runtimeSpec.TaskTemplate.Resources = &dockerswarm.ResourceRequirements{}
	runtimeSpec.TaskTemplate.Placement = &dockerswarm.Placement{}
	runtimeSpec.TaskTemplate.ContainerSpec.StopGracePeriod = &grace
	runtimeSpec.TaskTemplate.ContainerSpec.DNSConfig = &dockerswarm.DNSConfig{}

	observed := serviceSpecFromDocker(runtimeSpec)
	if observed.UnsupportedTaskTemplate {
		t.Fatal("Swarm task defaults were treated as unsupported")
	}
	if got := DiffServiceSpec(desired, observed); len(got) != 0 {
		t.Fatalf("Swarm task defaults produced drift: %v", got)
	}
}

func TestServiceSpecFromDockerAcceptsRegistryResolvedPlatforms(t *testing.T) {
	desired := canonicalServiceSpec()
	runtimeSpec := buildSwarmSpec(desired)
	runtimeSpec.TaskTemplate.Placement = &dockerswarm.Placement{Platforms: []dockerswarm.Platform{
		{Architecture: "amd64", OS: "linux"},
		{Architecture: "arm64", OS: "linux"},
	}}

	observed := serviceSpecFromDocker(runtimeSpec)
	if observed.UnsupportedTaskTemplate {
		t.Fatal("registry-resolved platforms were treated as unsupported")
	}
	if got := DiffServiceSpec(desired, observed); len(got) != 0 {
		t.Fatalf("registry-resolved platforms produced drift: %v", got)
	}
}

func TestServiceSpecFromDockerAcceptsCanonicalZeroRestartDefaults(t *testing.T) {
	desired := canonicalServiceSpec()
	runtimeSpec := buildSwarmSpec(desired)
	zeroAttempts := uint64(0)
	zeroWindow := time.Duration(0)
	runtimeSpec.TaskTemplate.RestartPolicy.MaxAttempts = &zeroAttempts
	runtimeSpec.TaskTemplate.RestartPolicy.Window = &zeroWindow

	observed := serviceSpecFromDocker(runtimeSpec)
	if observed.UnsupportedTaskTemplate {
		t.Fatal("canonical zero restart defaults were treated as unsupported")
	}
	if got := DiffServiceSpec(desired, observed); len(got) != 0 {
		t.Fatalf("canonical zero restart defaults produced drift: %v", got)
	}
}

func TestServiceSpecFromDockerAcceptsCanonicalDefaultIsolation(t *testing.T) {
	desired := canonicalServiceSpec()
	runtimeSpec := buildSwarmSpec(desired)
	runtimeSpec.TaskTemplate.ContainerSpec.Isolation = container.IsolationDefault

	observed := serviceSpecFromDocker(runtimeSpec)
	if observed.UnsupportedTaskTemplate {
		t.Fatal("canonical default isolation was treated as unsupported")
	}
	if got := DiffServiceSpec(desired, observed); len(got) != 0 {
		t.Fatalf("canonical default isolation produced drift: %v", got)
	}
}

func TestServiceSpecFromDockerIgnoresEphemeralForceUpdateCounter(t *testing.T) {
	desired := canonicalServiceSpec()
	runtimeSpec := buildSwarmSpec(desired)
	runtimeSpec.TaskTemplate.ForceUpdate = 7

	observed := serviceSpecFromDocker(runtimeSpec)
	if observed.UnsupportedTaskTemplate {
		t.Fatal("force-update counter was treated as persistent task-template drift")
	}
	if got := DiffServiceSpec(desired, observed); len(got) != 0 {
		t.Fatalf("force-update counter produced drift: %v", got)
	}
}

func TestServiceSpecFromDockerPreservesInvalidObservedMountForDriftDetection(t *testing.T) {
	desired := canonicalServiceSpec()
	desired.Volumes = nil
	runtimeSpec := buildSwarmSpec(desired)
	runtimeSpec.TaskTemplate.ContainerSpec.Mounts = append(
		runtimeSpec.TaskTemplate.ContainerSpec.Mounts,
		mount.Mount{Type: mount.TypeBind, Source: "/etc", Target: "/host-etc"},
	)

	observed := serviceSpecFromDocker(runtimeSpec)
	if len(observed.Volumes) != 1 || observed.Volumes[0].Source != "/etc" {
		t.Fatalf("invalid observed mount was discarded: %#v", observed.Volumes)
	}
	if got := DiffServiceSpec(desired, observed); !reflect.DeepEqual(got, []string{"mounts"}) {
		t.Fatalf("invalid observed mount did not produce drift: %v", got)
	}
}

func TestToServiceInfoProjectsDockerStatus(t *testing.T) {
	replicas := uint64(2)
	spec := buildSwarmSpec(canonicalServiceSpec())
	spec.Mode.Replicated.Replicas = &replicas
	spec.UpdateConfig.MaxFailureRatio = 0.25
	spec.RollbackConfig.MaxFailureRatio = 0.5
	running := uint64(1)
	svc := dockerswarm.Service{
		ID:            "service-id",
		Spec:          spec,
		ServiceStatus: &dockerswarm.ServiceStatus{RunningTasks: running},
	}

	got := toServiceInfo(svc)
	if got.ID != "service-id" || got.Name != "moduleos_api" || got.Replicas != 2 || got.Running != 1 {
		t.Fatalf("service status converted incorrectly: %#v", got)
	}
	if got.Spec.Update.MaxFailureRatio != 0.25 || got.Spec.Rollback.MaxFailureRatio != 0.5 {
		t.Fatalf("failure ratios were not projected: update=%v rollback=%v", got.Spec.Update.MaxFailureRatio, got.Spec.Rollback.MaxFailureRatio)
	}

	svc.ServiceStatus = nil
	svc.Spec.Mode = dockerswarm.ServiceMode{}
	got = toServiceInfo(svc)
	if got.Replicas != 0 || got.Running != 0 {
		t.Fatalf("missing status should produce zero counts: %#v", got)
	}
}

func TestDockerClientConstructionAndAccessors(t *testing.T) {
	clientWithPath, err := NewDockerClientWithEndpoint("/var/run/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	if clientWithPath.Client() == nil {
		t.Fatal("underlying Docker client is nil")
	}
	if err := clientWithPath.Close(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	clientFromEnv, err := NewDockerClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := clientFromEnv.Close(); err != nil {
		t.Fatal(err)
	}

	if got := ServiceName("worker"); got != "moduleos_worker" {
		t.Fatalf("ServiceName() = %q", got)
	}
}
