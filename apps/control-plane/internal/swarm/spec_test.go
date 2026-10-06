package swarm

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/moby/moby/api/types/mount"
)

func validDesiredInput() DesiredServiceInput {
	return DesiredServiceInput{
		ApplicationID:  "11111111-1111-1111-1111-111111111111",
		ProjectID:      "22222222-2222-2222-2222-222222222222",
		ProjectSlug:    "payments",
		Name:           "api",
		Image:          "registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		DesiredRunning: true,
		Replicas:       2,
		Generation:     7,
		Environment:    []string{"SECRET=line1\nline2=a:b", "A=1"},
		ProjectNetwork: NetworkAttachment{
			Network: "moduleos-payments-net",
			Aliases: []string{"payments.api", "api"},
		},
		BaseDomain:     "moduleos.example",
		IngressNetwork: "moduleos-ingress",
	}
}

func TestBuildDesiredServiceSpecDeterministic(t *testing.T) {
	first := validDesiredInput()
	first.LinkedNetworks = []NetworkAttachment{
		{Network: "moduleos-source-net", Aliases: []string{"db", "db"}},
		{Network: "moduleos-payments-net", Aliases: []string{"extra"}},
	}
	second := first
	second.Environment = []string{"A=1", "SECRET=line1\nline2=a:b"}
	second.LinkedNetworks = []NetworkAttachment{
		{Network: "moduleos-payments-net", Aliases: []string{"extra"}},
		{Network: "moduleos-source-net", Aliases: []string{"db"}},
	}

	left, err := BuildDesiredServiceSpec(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := BuildDesiredServiceSpec(second)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("same semantic input produced drift\nleft=%#v\nright=%#v", left, right)
	}
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	if string(leftJSON) != string(rightJSON) {
		t.Fatalf("non-deterministic encoding:\n%s\n%s", leftJSON, rightJSON)
	}
}

func TestBuildDesiredServiceSpecAllFields(t *testing.T) {
	input := validDesiredInput()
	input.Expose = true
	input.IngressPort = 8080
	input.Ports = []PortConfig{
		{ContainerPort: 8080, PublishedPort: 30080, Protocol: "tcp", PublishMode: "ingress"},
		{ContainerPort: 5353, PublishedPort: 30553, Protocol: "udp", PublishMode: "host"},
		{ContainerPort: 9090},
	}
	input.Volumes = []VolumeConfig{
		{Source: "/srv/moduleos/api/cache", Target: "/cache", ReadOnly: false},
		{Source: "/srv/moduleos/api/config", Target: "/etc/api", ReadOnly: true},
	}
	input.LinkedNetworks = []NetworkAttachment{{Network: "moduleos-data-net", Aliases: []string{"database"}}}
	input.PreservedLabels = map[string]string{
		"example.owner":  "platform",
		LabelGeneration:  "stale",
		"traefik.enable": "false",
	}

	spec, err := BuildDesiredServiceSpec(input)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Labels[LabelAppID] != input.ApplicationID || spec.Labels[LabelProjectID] != input.ProjectID || spec.Labels[LabelGeneration] != "7" {
		t.Fatalf("ownership labels incomplete: %#v", spec.Labels)
	}
	if spec.Labels["example.owner"] != "platform" || spec.Labels["traefik.enable"] != "true" {
		t.Fatalf("external/owned label policy failed: %#v", spec.Labels)
	}
	if spec.Labels["traefik.swarm.network"] != input.IngressNetwork {
		t.Fatalf("Traefik Swarm network label = %q", spec.Labels["traefik.swarm.network"])
	}
	if len(spec.Networks) != 3 {
		t.Fatalf("network count = %d, want project+link+ingress", len(spec.Networks))
	}

	dockerSpec := buildSwarmSpec(spec)
	if len(dockerSpec.EndpointSpec.Ports) != 2 {
		t.Fatalf("published Docker ports = %#v", dockerSpec.EndpointSpec.Ports)
	}
	if len(dockerSpec.TaskTemplate.ContainerSpec.Mounts) != 2 || dockerSpec.TaskTemplate.ContainerSpec.Mounts[1].Type != mount.TypeBind {
		t.Fatalf("mounts missing: %#v", dockerSpec.TaskTemplate.ContainerSpec.Mounts)
	}
	readOnlyFound := false
	for _, mounted := range dockerSpec.TaskTemplate.ContainerSpec.Mounts {
		if mounted.Target == "/etc/api" && mounted.ReadOnly {
			readOnlyFound = true
		}
	}
	if !readOnlyFound {
		t.Fatal("read-only mount was lost")
	}
	if dockerSpec.UpdateConfig == nil || dockerSpec.TaskTemplate.RestartPolicy == nil || dockerSpec.RollbackConfig == nil {
		t.Fatal("deployment/restart policies must be explicit")
	}
}

func TestBuildDesiredServiceSpecStopped(t *testing.T) {
	input := validDesiredInput()
	input.DesiredRunning = false
	spec, err := BuildDesiredServiceSpec(input)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Replicas != 0 {
		t.Fatalf("stopped replicas = %d", spec.Replicas)
	}
}

func TestBuildDesiredServiceSpecRejectsInvalidInput(t *testing.T) {
	tests := map[string]func(*DesiredServiceInput){
		"empty image":                  func(in *DesiredServiceInput) { in.Image = "" },
		"expose without explicit port": func(in *DesiredServiceInput) { in.Expose = true },
		"duplicate mount target": func(in *DesiredServiceInput) {
			in.Volumes = []VolumeConfig{{Source: "/srv/a", Target: "/data"}, {Source: "/srv/b", Target: "/data"}}
		},
		"relative mount": func(in *DesiredServiceInput) {
			in.Volumes = []VolumeConfig{{Source: "relative", Target: "/data"}}
		},
		"duplicate published port": func(in *DesiredServiceInput) {
			in.Ports = []PortConfig{{ContainerPort: 80, PublishedPort: 8080}, {ContainerPort: 81, PublishedPort: 8080}}
		},
		"invalid protocol": func(in *DesiredServiceInput) {
			in.Ports = []PortConfig{{ContainerPort: 53, PublishedPort: 53, Protocol: "sctp"}}
		},
		"duplicate env": func(in *DesiredServiceInput) { in.Environment = []string{"A=1", "A=2"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := validDesiredInput()
			mutate(&input)
			spec, err := BuildDesiredServiceSpec(input)
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("error = %v", err)
			}
			if !reflect.DeepEqual(spec, ServiceSpec{}) {
				t.Fatalf("invalid input returned partial spec: %#v", spec)
			}
		})
	}
}

func TestMergeServiceLabelsPreservesOnlyExternalLabels(t *testing.T) {
	merged := mergeServiceLabels(map[string]string{
		"external.owner": "team-a",
		LabelGeneration:  "1",
		"traefik.enable": "false",
	}, map[string]string{
		LabelGeneration:  "2",
		"traefik.enable": "true",
	})
	if merged["external.owner"] != "team-a" || merged[LabelGeneration] != "2" || merged["traefik.enable"] != "true" {
		t.Fatalf("label merge = %#v", merged)
	}
}
