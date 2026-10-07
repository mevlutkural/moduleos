package swarm

import (
	"reflect"
	"testing"
	"time"
)

func canonicalServiceSpec() ServiceSpec {
	return ServiceSpec{
		Name:     "api",
		Image:    "registry.example/api:1.0",
		Replicas: 2,
		EnvVars:  []string{"B=2", "A=1"},
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
		Networks: []NetworkAttachment{{Network: "moduleos-root-net", Aliases: []string{"root.api", "api"}}},
		Update:   UpdatePolicy{Parallelism: 1, Delay: 2 * time.Second, Monitor: 10 * time.Second},
		Restart:  RestartPolicy{Delay: 5 * time.Second},
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

func TestDiffServiceSpecReportsManagedFieldsInStableOrder(t *testing.T) {
	desired := canonicalServiceSpec()
	observed := canonicalServiceSpec()
	observed.Image = "registry.example/api:2.0"
	observed.Replicas = 3
	observed.EnvVars = []string{"A=changed"}
	observed.Ports = nil
	observed.Volumes = nil
	observed.Networks = []NetworkAttachment{{Network: "other"}}
	delete(observed.Labels, LabelGeneration)
	observed.Update.Delay = time.Second

	want := []string{"image", "replicas", "environment", "ports", "mounts", "networks", "labels", "policy"}
	if got := DiffServiceSpec(desired, observed); !reflect.DeepEqual(got, want) {
		t.Fatalf("DiffServiceSpec() = %v, want %v", got, want)
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
