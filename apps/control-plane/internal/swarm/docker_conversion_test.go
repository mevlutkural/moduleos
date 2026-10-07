package swarm

import (
	"reflect"
	"testing"
	"time"

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
}

func TestToServiceInfoProjectsDockerStatus(t *testing.T) {
	replicas := uint64(2)
	spec := buildSwarmSpec(canonicalServiceSpec())
	spec.Mode.Replicated.Replicas = &replicas
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
