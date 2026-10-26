//go:build integration

package integration_test

import (
	"fmt"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	moduleosswarm "github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

func TestApplicationLifecycleAndDeploymentConvergeAgainstSwarm(t *testing.T) {
	f := newIntegrationFixture(t)
	project := f.createProject("Lifecycle", f.unique("lifecycle-project"))
	if _, err := f.docker.GetNetwork(f.ctx, project.Network); err == nil || !errdefs.IsNotFound(err) {
		t.Fatalf("project service method created runtime state before reconciliation: %v", err)
	}
	f.reconcileProject(project)

	application := f.createApplication(project, f.unique("lifecycle-app"), qualificationImage, 1)
	converged := f.reconcileApplicationUntilConverged(application.Name)
	runtimeService := f.registerApplicationService(application)
	assertApplicationRuntimeConverged(t, f, converged, runtimeService)

	stopped, err := f.service.SetRunStateIfMatch(f.ctx, application.Name, store.DesiredRunStateStopped, application.ID, converged.DesiredGeneration)
	if err != nil {
		t.Fatalf("persist stop intent: %v", err)
	}
	if stopped.DesiredGeneration != converged.DesiredGeneration+1 {
		t.Fatalf("stop generation = %d, want %d", stopped.DesiredGeneration, converged.DesiredGeneration+1)
	}
	converged = f.reconcileApplicationUntilConverged(application.Name)
	if converged.ObservedState != store.ObservedStateStopped {
		t.Fatalf("stopped observed state = %q", converged.ObservedState)
	}

	started, err := f.service.SetRunStateIfMatch(f.ctx, application.Name, store.DesiredRunStateRunning, application.ID, converged.DesiredGeneration)
	if err != nil {
		t.Fatalf("persist start intent: %v", err)
	}
	if started.DesiredGeneration != converged.DesiredGeneration+1 {
		t.Fatalf("start generation = %d, want %d", started.DesiredGeneration, converged.DesiredGeneration+1)
	}
	converged = f.reconcileApplicationUntilConverged(application.Name)
	if converged.ObservedState != store.ObservedStateRunning {
		t.Fatalf("running observed state = %q", converged.ObservedState)
	}

	updated, err := f.service.UpdateApp(f.ctx, app.UpdateAppRequest{
		AppName:            application.Name,
		ExpectedID:         application.ID,
		ExpectedGeneration: converged.DesiredGeneration,
		EnvVars:            map[string]string{"MODULEOS_INTEGRATION": "updated"},
	})
	if err != nil {
		t.Fatalf("persist environment update: %v", err)
	}
	if updated.DesiredGeneration != converged.DesiredGeneration+1 {
		t.Fatalf("update generation = %d, want %d", updated.DesiredGeneration, converged.DesiredGeneration+1)
	}
	converged = f.reconcileApplicationUntilConverged(application.Name)
	runtimeService, err = f.docker.GetService(f.ctx, runtimeService.ID)
	if err != nil {
		t.Fatalf("inspect updated service: %v", err)
	}
	assertApplicationRuntimeConverged(t, f, converged, runtimeService)

	accepted, err := f.service.AcceptDeployment(f.ctx, app.DeploymentMutationRequest{
		AppName:            application.Name,
		ExpectedAppID:      application.ID,
		ExpectedGeneration: converged.DesiredGeneration,
		IdempotencyKey:     "phase8a-" + integrationSuite.runID,
		Operation:          store.DeploymentOperationDeploy,
		Image:              updateImage,
	})
	if err != nil {
		t.Fatalf("accept deployment: %v", err)
	}
	if accepted.Replayed || accepted.TargetGeneration != converged.DesiredGeneration+1 {
		t.Fatalf("unexpected deployment acceptance: %#v", accepted)
	}
	converged = f.reconcileApplicationUntilConverged(application.Name)
	deployment, err := f.store.GetDeployment(f.ctx, accepted.DeploymentID)
	if err != nil {
		t.Fatalf("read converged deployment: %v", err)
	}
	if deployment.Status != store.DeploymentStatusSucceeded {
		t.Fatalf("deployment status = %q, error_code=%q message=%q", deployment.Status, deployment.ErrorCode, deployment.ErrorMessage)
	}
	if !moduleosswarm.IsImmutableImageReference(deployment.ResolvedImage) || deployment.ResolvedImage != updateImage {
		t.Fatalf("deployment resolved image is not exact immutable target: %q", deployment.ResolvedImage)
	}
	if converged.Image != updateImage || converged.ObservedImage != updateImage {
		t.Fatalf("application images did not converge: desired=%q observed=%q", converged.Image, converged.ObservedImage)
	}
	runtimeService, err = f.docker.GetService(f.ctx, runtimeService.ID)
	if err != nil {
		t.Fatalf("inspect deployed service: %v", err)
	}
	assertApplicationRuntimeConverged(t, f, converged, runtimeService)
}

func assertApplicationRuntimeConverged(t *testing.T, f *integrationFixture, application *store.Application, runtimeService *moduleosswarm.ServiceInfo) {
	t.Helper()
	if application.DesiredGeneration != application.ObservedGeneration {
		t.Fatalf("generation did not converge: desired=%d observed=%d", application.DesiredGeneration, application.ObservedGeneration)
	}
	desired, err := f.service.BuildDesiredServiceSpec(f.ctx, application)
	if err != nil {
		t.Fatalf("build desired service spec: %v", err)
	}
	deployments, err := f.store.ListDeployments(f.ctx, application.ID)
	if err != nil {
		t.Fatalf("list application deployments: %v", err)
	}
	for _, deployment := range deployments {
		if deployment.TargetGeneration != application.DesiredGeneration {
			continue
		}
		desired, err = moduleosswarm.WithRolloutIdentity(desired, deployment.ID)
		if err != nil {
			t.Fatalf("apply deployment rollout identity: %v", err)
		}
		break
	}
	if fields := moduleosswarm.DiffServiceSpec(desired, runtimeService.Spec); len(fields) != 0 {
		t.Fatalf("runtime spec drifted: %v", fields)
	}
	if runtimeService.Labels[moduleosswarm.LabelAppID] != application.ID {
		t.Fatalf("runtime application identity = %q, want %q", runtimeService.Labels[moduleosswarm.LabelAppID], application.ID)
	}
	if runtimeService.Labels[moduleosswarm.LabelGeneration] != fmt.Sprint(application.DesiredGeneration) {
		t.Fatalf("runtime generation label = %q", runtimeService.Labels[moduleosswarm.LabelGeneration])
	}
}
