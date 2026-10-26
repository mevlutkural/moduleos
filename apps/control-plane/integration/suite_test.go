//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	moduleosswarm "github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
)

const (
	integrationAcknowledgement = "MODULEOS_INTEGRATION_DISPOSABLE"
	integrationEndpointEnv     = "MODULEOS_INTEGRATION_DOCKER_ENDPOINT"
	integrationRunIDEnv        = "MODULEOS_INTEGRATION_RUN_ID"
	integrationDockerEndpoint  = "unix:///var/run/docker.sock"
	integrationRunLabel        = "io.moduleos.integration.run"

	qualificationImage = "nginx:1.27-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10"
	updateImage        = "nginx:1.28-alpine@sha256:a8b39bd9cf0f83869a2162827a0caf6137ddf759d50a171451b335cecc87d236"
)

var integrationSuite *suiteState

type runtimeSnapshot struct {
	services map[string]string
	networks map[string]string
}

type suiteState struct {
	runID    string
	endpoint string
	client   *moduleosswarm.DockerClient
	raw      *client.Client
	baseline runtimeSnapshot
}

func TestMain(m *testing.M) {
	if os.Getenv(integrationAcknowledgement) != "1" {
		fmt.Fprintf(os.Stderr, "integration prerequisite failed: %s=1 is required\n", integrationAcknowledgement)
		os.Exit(2)
	}
	if endpoint := os.Getenv(integrationEndpointEnv); endpoint != "" && endpoint != integrationDockerEndpoint {
		fmt.Fprintf(os.Stderr, "integration prerequisite failed: %s must be %q\n", integrationEndpointEnv, integrationDockerEndpoint)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	runID, err := integrationRunID()
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration setup failed: generate run identity: %v\n", err)
		os.Exit(2)
	}
	dockerClient, err := moduleosswarm.NewDockerClientWithEndpoint(integrationDockerEndpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration prerequisite failed: construct Docker client: %v\n", err)
		os.Exit(2)
	}
	raw := dockerClient.Client()
	if err := dockerClient.Health(ctx); err != nil {
		_ = dockerClient.Close()
		fmt.Fprintf(os.Stderr, "integration prerequisite failed: Docker/Swarm health: %v\n", err)
		os.Exit(2)
	}
	if err := requireSingleNodeManager(ctx, raw); err != nil {
		_ = dockerClient.Close()
		fmt.Fprintf(os.Stderr, "integration prerequisite failed: %v\n", err)
		os.Exit(2)
	}
	baseline, err := captureRuntimeSnapshot(ctx, raw)
	if err != nil {
		_ = dockerClient.Close()
		fmt.Fprintf(os.Stderr, "integration setup failed: capture baseline: %v\n", err)
		os.Exit(2)
	}
	if err := rejectUnsafeBaseline(ctx, raw, runID); err != nil {
		_ = dockerClient.Close()
		fmt.Fprintf(os.Stderr, "integration safety check failed: %v\n", err)
		os.Exit(2)
	}
	for _, image := range []string{qualificationImage, updateImage} {
		if err := pullImage(ctx, raw, image); err != nil {
			_ = dockerClient.Close()
			fmt.Fprintf(os.Stderr, "integration setup failed: prepare qualification image: %v\n", err)
			os.Exit(2)
		}
	}

	integrationSuite = &suiteState{
		runID:    runID,
		endpoint: integrationDockerEndpoint,
		client:   dockerClient,
		raw:      raw,
		baseline: baseline,
	}
	testCode := m.Run()

	auditCtx, auditCancel := context.WithTimeout(context.Background(), 45*time.Second)
	auditErr := auditRuntimeSnapshot(auditCtx, raw, baseline, runID)
	auditCancel()
	closeErr := dockerClient.Close()
	if auditErr != nil {
		fmt.Fprintf(os.Stderr, "integration leak audit failed: %v\n", auditErr)
		testCode = 1
	}
	if closeErr != nil {
		fmt.Fprintf(os.Stderr, "integration cleanup failed: close Docker client: %v\n", closeErr)
		testCode = 1
	}
	os.Exit(testCode)
}

func newRunID() (string, error) {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func integrationRunID() (string, error) {
	value := os.Getenv(integrationRunIDEnv)
	if value == "" {
		return newRunID()
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 6 {
		return "", fmt.Errorf("%s must contain exactly 12 lowercase hexadecimal characters", integrationRunIDEnv)
	}
	if value != strings.ToLower(value) {
		return "", fmt.Errorf("%s must contain exactly 12 lowercase hexadecimal characters", integrationRunIDEnv)
	}
	return value, nil
}

func requireSingleNodeManager(ctx context.Context, dockerClient *client.Client) error {
	infoResult, err := dockerClient.Info(ctx, client.InfoOptions{})
	if err != nil {
		return fmt.Errorf("read Docker info: %w", err)
	}
	if infoResult.Info.OSType != "linux" {
		return fmt.Errorf("Linux Docker Engine is required, got %q", infoResult.Info.OSType)
	}
	if infoResult.Info.Swarm.LocalNodeState != swarm.LocalNodeStateActive || !infoResult.Info.Swarm.ControlAvailable {
		return fmt.Errorf("an active Swarm manager is required")
	}
	nodes, err := dockerClient.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return fmt.Errorf("list Swarm nodes: %w", err)
	}
	if len(nodes.Items) != 1 {
		return fmt.Errorf("a disposable single-node Swarm is required, found %d nodes", len(nodes.Items))
	}
	return nil
}

func pullImage(ctx context.Context, dockerClient *client.Client, image string) error {
	reader, err := dockerClient.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull %q: %w", safeImageName(image), err)
	}
	waitErr := reader.Wait(ctx)
	closeErr := reader.Close()
	if waitErr != nil {
		return fmt.Errorf("consume pull response for %q: %w", safeImageName(image), waitErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close pull response for %q: %w", safeImageName(image), closeErr)
	}
	return nil
}

func safeImageName(image string) string {
	if index := strings.IndexByte(image, '@'); index >= 0 {
		return image[:index] + "@sha256:<redacted>"
	}
	return image
}

func captureRuntimeSnapshot(ctx context.Context, dockerClient *client.Client) (runtimeSnapshot, error) {
	services, err := dockerClient.ServiceList(ctx, client.ServiceListOptions{})
	if err != nil {
		return runtimeSnapshot{}, fmt.Errorf("list services: %w", err)
	}
	networks, err := dockerClient.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return runtimeSnapshot{}, fmt.Errorf("list networks: %w", err)
	}
	snapshot := runtimeSnapshot{services: make(map[string]string), networks: make(map[string]string)}
	for _, service := range services.Items {
		snapshot.services[service.ID] = service.Spec.Annotations.Name
	}
	for _, network := range networks.Items {
		snapshot.networks[network.ID] = network.Name
	}
	return snapshot, nil
}

func rejectUnsafeBaseline(ctx context.Context, dockerClient *client.Client, runID string) error {
	services, err := dockerClient.ServiceList(ctx, client.ServiceListOptions{})
	if err != nil {
		return fmt.Errorf("list baseline services: %w", err)
	}
	if len(services.Items) != 0 {
		names := make([]string, 0, len(services.Items))
		for _, service := range services.Items {
			names = append(names, service.Spec.Annotations.Name)
		}
		sort.Strings(names)
		return fmt.Errorf("disposable Swarm must contain no services, found %s", strings.Join(names, ", "))
	}
	networks, err := dockerClient.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return fmt.Errorf("list baseline networks: %w", err)
	}
	for _, network := range networks.Items {
		if !isSystemNetwork(network) && !isOwnedRootNetwork(network, runID) {
			return fmt.Errorf("disposable Docker Engine contains user-created network %q", network.Name)
		}
	}
	return nil
}

func isOwnedRootNetwork(summary network.Summary, runID string) bool {
	return summary.Name == "moduleos-root-net" &&
		summary.Driver == "overlay" && summary.Scope == "swarm" && summary.Attachable && !summary.Ingress &&
		summary.Labels[integrationRunLabel] == runID &&
		summary.Labels[moduleosswarm.LabelManagedBy] == "true" &&
		summary.Labels[moduleosswarm.LabelResourceKind] == "project-network" &&
		summary.Labels[moduleosswarm.LabelProjectID] == "00000000-0000-0000-0000-000000000001" &&
		summary.Labels[moduleosswarm.LabelProject] == "root" &&
		summary.Labels[moduleosswarm.LabelSchema] == moduleosswarm.SchemaVersion
}

func isSystemNetwork(summary network.Summary) bool {
	switch summary.Name {
	case "bridge":
		return summary.Driver == "bridge" && summary.Scope == "local" && !summary.Ingress
	case "host":
		return summary.Driver == "host" && summary.Scope == "local" && !summary.Ingress
	case "none":
		return summary.Driver == "null" && summary.Scope == "local" && !summary.Ingress
	case "docker_gwbridge":
		return summary.Driver == "bridge" && summary.Scope == "local" && !summary.Ingress
	case "ingress":
		return summary.Driver == "overlay" && summary.Scope == "swarm" && summary.Ingress
	default:
		return false
	}
}

func auditRuntimeSnapshot(ctx context.Context, dockerClient *client.Client, baseline runtimeSnapshot, runID string) error {
	current, err := captureRuntimeSnapshot(ctx, dockerClient)
	if err != nil {
		return err
	}
	differences := snapshotDifferences(baseline, current)
	if len(differences) == 0 {
		return nil
	}
	sort.Strings(differences)
	return fmt.Errorf("runtime snapshot changed for run %s: %s", runID, strings.Join(differences, "; "))
}

func snapshotDifferences(before, after runtimeSnapshot) []string {
	var differences []string
	for id, name := range before.services {
		if after.services[id] != name {
			differences = append(differences, fmt.Sprintf("missing baseline service %s(%s)", name, shortID(id)))
		}
	}
	for id, name := range after.services {
		if before.services[id] != name {
			differences = append(differences, fmt.Sprintf("added service %s(%s)", name, shortID(id)))
		}
	}
	for id, name := range before.networks {
		if after.networks[id] != name {
			differences = append(differences, fmt.Sprintf("missing baseline network %s(%s)", name, shortID(id)))
		}
	}
	for id, name := range after.networks {
		if before.networks[id] != name {
			differences = append(differences, fmt.Sprintf("added network %s(%s)", name, shortID(id)))
		}
	}
	return differences
}

func shortID(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
