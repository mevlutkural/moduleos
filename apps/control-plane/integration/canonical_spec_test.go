//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	moduleosswarm "github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

func TestCanonicalServiceSpecRoundTripAgainstSwarm(t *testing.T) {
	f := newIntegrationFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 90*time.Second)
	defer cancel()

	networkName := f.unique("canonical-net")
	projectID := "project-" + integrationSuite.runID
	applicationID := "application-" + integrationSuite.runID
	if err := f.docker.EnsureProjectNetwork(ctx, networkName, projectID, "canonical"); err != nil {
		t.Fatalf("ensure project network: %v", err)
	}
	f.register(ownedResource{kind: networkResource, name: networkName, labels: map[string]string{
		moduleosswarm.LabelManagedBy: "true",
		moduleosswarm.LabelProjectID: projectID,
	}})
	network, err := f.docker.GetNetwork(ctx, networkName)
	if err != nil {
		t.Fatalf("inspect project network: %v", err)
	}
	f.registerManagedNetwork(network, projectID)
	var listed *moduleosswarm.NetworkInfo
	networkCtx, networkCancel := context.WithTimeout(ctx, 10*time.Second)
	err = pollUntil(networkCtx, func() (bool, error) {
		networks, listErr := f.docker.ListNetworks(networkCtx)
		if listErr != nil {
			return false, listErr
		}
		for index := range networks {
			if networks[index].ID == network.ID {
				candidate := networks[index]
				listed = &candidate
				return candidate.Driver == "overlay" && candidate.Attachable, nil
			}
		}
		return false, nil
	})
	networkCancel()
	if err != nil {
		t.Fatalf("project network did not become an attachable overlay: inspected=%#v listed=%#v: %v", network, listed, err)
	}

	appName := f.unique("canonical")
	desired, err := moduleosswarm.BuildDesiredServiceSpec(moduleosswarm.DesiredServiceInput{
		ApplicationID:  applicationID,
		ProjectID:      projectID,
		ProjectSlug:    "canonical",
		Name:           appName,
		Image:          qualificationImage,
		DesiredRunning: false,
		Replicas:       1,
		Generation:     7,
		Environment:    []string{"B=2", "A=1"},
		ProjectNetwork: moduleosswarm.NetworkAttachment{Network: networkName, Aliases: []string{"canonical.app", "canonical"}},
	})
	if err != nil {
		t.Fatalf("build desired spec: %v", err)
	}
	if err := f.docker.CreateService(ctx, desired); err != nil {
		t.Fatalf("create service: %v", err)
	}
	f.register(ownedResource{kind: serviceResource, name: moduleosswarm.ServiceName(appName), labels: map[string]string{
		moduleosswarm.LabelManagedBy: "true",
		moduleosswarm.LabelAppID:     applicationID,
	}})
	observed, err := f.docker.GetService(ctx, moduleosswarm.ServiceName(appName))
	if err != nil {
		t.Fatalf("inspect created service: %v", err)
	}
	f.registerManagedService(observed, applicationID)
	if fields := moduleosswarm.DiffServiceSpec(desired, observed.Spec); len(fields) != 0 {
		t.Fatalf("canonical create drifted after Docker normalization: %v", fields)
	}
	if observed.Labels[moduleosswarm.LabelAppID] != applicationID || observed.Labels[moduleosswarm.LabelGeneration] != "7" {
		t.Fatalf("immutable ownership/generation labels changed: %#v", observed.Labels)
	}

	desired.Image = updateImage
	desired.Labels[moduleosswarm.LabelGeneration] = "8"
	desired.EnvVars = []string{"A=changed", "B=2"}
	desired, err = moduleosswarm.WithRolloutIdentity(desired, "")
	if err != nil {
		t.Fatalf("recompute canonical task template identity: %v", err)
	}
	if err := updateExclusivelyOwnedCanonicalService(ctx, f, observed.ID, applicationID, "7", desired); err != nil {
		t.Fatalf("update service: %v", err)
	}
	updated, err := f.docker.GetService(ctx, observed.ID)
	if err != nil {
		t.Fatalf("inspect updated service: %v", err)
	}
	if fields := moduleosswarm.DiffServiceSpec(desired, updated.Spec); len(fields) != 0 {
		t.Fatalf("canonical update drifted after Docker normalization: %v", fields)
	}

	if fields := moduleosswarm.DiffServiceSpec(desired, updated.Spec); len(fields) != 0 {
		t.Fatalf("repeated desired comparison produced false drift: %v", fields)
	}
	if got := len(updated.Spec.Networks); got != 1 {
		t.Fatalf("network attachment count = %d, want 1", got)
	}
	aliases := updated.Spec.Networks[0].Aliases
	if fmt.Sprint(aliases) != fmt.Sprint([]string{"canonical", "canonical.app"}) {
		t.Fatalf("canonical aliases = %v", aliases)
	}
}

func updateExclusivelyOwnedCanonicalService(
	ctx context.Context,
	f *integrationFixture,
	serviceID, applicationID, previousGeneration string,
	desired moduleosswarm.ServiceSpec,
) error {
	targetGeneration := desired.Labels[moduleosswarm.LabelGeneration]
	return pollUntil(ctx, func() (bool, error) {
		current, err := f.docker.InspectService(ctx, serviceID)
		if err != nil {
			return true, err
		}
		if current.Labels[moduleosswarm.LabelAppID] != applicationID {
			return true, fmt.Errorf("canonical service ownership changed before update")
		}
		generation := current.Labels[moduleosswarm.LabelGeneration]
		if generation == targetGeneration {
			observed, err := f.docker.GetService(ctx, serviceID)
			if err != nil {
				return true, err
			}
			if fields := moduleosswarm.DiffServiceSpec(desired, observed.Spec); len(fields) != 0 {
				return true, fmt.Errorf("target generation has unexpected drift: %v", fields)
			}
			return true, nil
		}
		if generation != previousGeneration {
			return true, fmt.Errorf("refusing canonical update from unexpected generation %q", generation)
		}
		if err := f.docker.UpdateService(ctx, serviceID, desired); err != nil {
			if isDockerVersionConflict(err) {
				return false, err
			}
			return true, err
		}
		return false, nil
	})
}
