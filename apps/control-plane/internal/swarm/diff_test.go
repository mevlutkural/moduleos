package swarm

import (
	"reflect"
	"testing"
	"time"
)

func canonicalServiceSpec() ServiceSpec {
	return ServiceSpec{
		Name:         "api",
		Image:        "registry.example/api:1.0",
		ServiceMode:  "replicated",
		EndpointMode: "vip",
		Replicas:     2,
		EnvVars:      []string{"B=2", "A=1"},
		Ports: []PortConfig{{
			ContainerPort: 8080,
			PublishedPort: 30080,
			Protocol:      "tcp",
			PublishMode:   "ingress",
		}},
		Volumes: []VolumeConfig{{Source: "/srv/moduleos/api", Target: "/data", ReadOnly: true}},
		Labels: map[string]string{
			LabelAppID:       "app-id",
			LabelGeneration:  "7",
			"traefik.enable": "true",
			"external.owner": "platform",
		},
		Networks:         []NetworkAttachment{{Network: "moduleos-root-net", Aliases: []string{"root.api", "api"}}},
		TaskTemplateHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Update:           UpdatePolicy{Parallelism: 1, Delay: 2 * time.Second, Monitor: 10 * time.Second, FailureAction: "pause", Order: "stop-first"},
		Rollback:         UpdatePolicy{Parallelism: 1, Delay: 2 * time.Second, Monitor: 10 * time.Second, FailureAction: "pause", Order: "stop-first"},
		Restart:          RestartPolicy{Condition: "any", Delay: 5 * time.Second},
	}
}

func TestDiffServiceSpecEquivalentRuntimeRepresentation(t *testing.T) {
	desired := canonicalServiceSpec()
	observed := canonicalServiceSpec()
	observed.Image = desired.Image + "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	observed.EnvVars = []string{"A=1", "B=2"}
	observed.Networks[0].Aliases = []string{"api", "root.api", "docker-generated-alias"}
	observed.Labels["external.owner"] = "runtime-owner"
	observed.Labels["external.annotation"] = "ignored"

	if diff := DiffServiceSpec(desired, observed); len(diff) != 0 {
		t.Fatalf("equivalent specs differ: %v", diff)
	}
}

func TestDiffServiceSpecIgnoresUnpublishedPortsMissingFromRuntime(t *testing.T) {
	desired := canonicalServiceSpec()
	desired.Ports = append(desired.Ports, PortConfig{
		ContainerPort: 9090,
		Protocol:      "tcp",
		PublishMode:   "ingress",
	})
	observed := canonicalServiceSpec()

	if diff := DiffServiceSpec(desired, observed); len(diff) != 0 {
		t.Fatalf("unpublished desired port caused runtime drift: %v", diff)
	}

	observed.Ports = append(observed.Ports, PortConfig{
		ContainerPort: 9191,
		PublishedPort: 30191,
		Protocol:      "tcp",
		PublishMode:   "ingress",
	})
	if got := DiffServiceSpec(desired, observed); !reflect.DeepEqual(got, []string{"ports"}) {
		t.Fatalf("unexpected published port diff = %v", got)
	}

	observed = canonicalServiceSpec()
	observed.Ports = append(observed.Ports, PortConfig{
		ContainerPort: 9191,
		Protocol:      "tcp",
		PublishMode:   "host",
	})
	if got := DiffServiceSpec(desired, observed); !reflect.DeepEqual(got, []string{"ports"}) {
		t.Fatalf("dynamic observed port was not detected as drift: %v", got)
	}
}

func TestDiffServiceSpecReportsManagedFieldsInStableOrder(t *testing.T) {
	desired := canonicalServiceSpec()
	observed := canonicalServiceSpec()
	observed.Image = "registry.example/api:2.0"
	observed.ServiceMode = "global"
	observed.EndpointMode = "dnsrr"
	observed.Replicas = 3
	observed.EnvVars = []string{"A=changed"}
	observed.Ports = nil
	observed.Volumes = nil
	observed.Networks = []NetworkAttachment{{Network: "other"}}
	delete(observed.Labels, LabelGeneration)
	observed.Update.Delay = time.Second

	observed.TaskTemplateHash = "different"
	want := []string{"image", "service_mode", "endpoint_mode", "replicas", "environment", "ports", "mounts", "networks", "task_template", "labels", "policy"}
	if got := DiffServiceSpec(desired, observed); !reflect.DeepEqual(got, want) {
		t.Fatalf("DiffServiceSpec() = %v, want %v", got, want)
	}
}

func TestDiffServiceSpecDetectsFixedDockerPolicyDrift(t *testing.T) {
	tests := map[string]func(*ServiceSpec){
		"endpoint mode":           func(spec *ServiceSpec) { spec.EndpointMode = "dnsrr" },
		"update failure ratio":    func(spec *ServiceSpec) { spec.Update.MaxFailureRatio = 0.25 },
		"update failure action":   func(spec *ServiceSpec) { spec.Update.FailureAction = "continue" },
		"update order":            func(spec *ServiceSpec) { spec.Update.Order = "start-first" },
		"rollback failure action": func(spec *ServiceSpec) { spec.Rollback.FailureAction = "continue" },
		"rollback failure ratio":  func(spec *ServiceSpec) { spec.Rollback.MaxFailureRatio = 0.25 },
		"rollback order":          func(spec *ServiceSpec) { spec.Rollback.Order = "start-first" },
		"restart condition":       func(spec *ServiceSpec) { spec.Restart.Condition = "on-failure" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			desired := canonicalServiceSpec()
			observed := canonicalServiceSpec()
			mutate(&observed)
			if got := DiffServiceSpec(desired, observed); len(got) != 1 {
				t.Fatalf("fixed Docker field drift was not detected: %v", got)
			}
		})
	}
}

func TestDiffServiceSpecDetectsObservedManagedExtras(t *testing.T) {
	desired := canonicalServiceSpec()
	observed := canonicalServiceSpec()
	observed.Labels["moduleos.stale"] = "true"
	observed.Labels["traefik.http.routers.stale.rule"] = "Host(`stale.example`)"

	if got := DiffServiceSpec(desired, observed); !reflect.DeepEqual(got, []string{"labels"}) {
		t.Fatalf("DiffServiceSpec() = %v, want labels drift", got)
	}
}

func TestNetworkSpecsEqualNormalizesOrderAndDuplicateAttachments(t *testing.T) {
	left := []NetworkAttachment{
		{Network: "one", Aliases: []string{"b", "a"}},
		{Network: "two", Aliases: []string{"api"}},
		{Network: "one", Aliases: []string{"a"}},
	}
	right := []NetworkAttachment{
		{Network: "two", Aliases: []string{"api", "runtime-extra"}},
		{Network: "one", Aliases: []string{"a", "b"}},
	}
	if !networkSpecsEqual(left, right) {
		t.Fatal("equivalent network sets were treated as drift")
	}
	right[1].Aliases = []string{"a"}
	if networkSpecsEqual(left, right) {
		t.Fatal("a missing desired alias was not treated as drift")
	}
}

func TestFormatDiff(t *testing.T) {
	if got := FormatDiff(nil); got != "" {
		t.Fatalf("FormatDiff(nil) = %q", got)
	}
	if got := FormatDiff([]string{"image", "policy"}); got != "managed fields differ: image,policy" {
		t.Fatalf("FormatDiff(fields) = %q", got)
	}
}
