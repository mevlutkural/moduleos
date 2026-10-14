package app_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

func TestAcceptDeploymentReplayConflictAndCursor(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()
	application := mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "deployment-api", Image: "nginx:1.0"})
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	svc.WithDeploymentClock(func() time.Time { return now })
	request := app.DeploymentMutationRequest{
		AppName: application.Name, ExpectedAppID: application.ID,
		ExpectedGeneration: application.DesiredGeneration, IdempotencyKey: "01234567-89ab-cdef-0123-456789abcdef",
		Operation: store.DeploymentOperationDeploy, Image: "nginx:2.0",
	}
	first, err := svc.AcceptDeployment(ctx, request)
	if err != nil || first.Replayed {
		t.Fatalf("first acceptance = %#v, %v", first, err)
	}
	replay, err := svc.AcceptDeployment(ctx, request)
	if err != nil || !replay.Replayed || replay.DeploymentID != first.DeploymentID {
		t.Fatalf("same-key replay = %#v, %v", replay, err)
	}
	request.Image = "nginx:3.0"
	if _, err := svc.AcceptDeployment(ctx, request); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("changed image with same key = %v", err)
	}
	request.Image = "nginx:2.0"
	request.IdempotencyKey = "abcdef01-2345-6789-abcd-ef0123456789"
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.AcceptDeployment(cancelled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acceptance = %v", err)
	}
	if _, err := svc.AcceptDeployment(ctx, request); !errors.Is(err, store.ErrGenerationConflict) {
		t.Fatalf("stale generation with new key = %v", err)
	}
	page, err := svc.ListDeploymentPage(ctx, application.Name, 1, "")
	if err != nil || len(page.Deployments) != 1 || page.Deployments[0].ID != first.DeploymentID || page.NextCursor != "" {
		t.Fatalf("page = %#v, %v", page, err)
	}
	if _, err := svc.GetDeploymentForApp(ctx, application.Name, first.DeploymentID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetDeploymentForApp(ctx, application.Name, "not-a-uuid"); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("invalid detail ID = %v", err)
	}
	current, err := st.GetApplication(ctx, application.Name)
	if err != nil || current.DesiredGeneration != first.TargetGeneration {
		t.Fatalf("unexpected generation = %#v, %v", current, err)
	}
}

func TestAcceptDeploymentConcurrentSameKey(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()
	application := mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "concurrent-api", Image: "nginx:1.0"})
	request := app.DeploymentMutationRequest{
		AppName: application.Name, ExpectedAppID: application.ID,
		ExpectedGeneration: application.DesiredGeneration, IdempotencyKey: "01234567-89ab-cdef-0123-456789abcdef",
		Operation: store.DeploymentOperationDeploy, Image: "nginx:2.0",
	}
	start := make(chan struct{})
	results := make(chan *store.DeploymentAcceptance, 2)
	errorsCh := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := svc.AcceptDeployment(ctx, request)
			results <- result
			errorsCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original string
	for result := range results {
		if original != "" && result.DeploymentID != original {
			t.Fatalf("concurrent retry created a second deployment: %s vs %s", original, result.DeploymentID)
		}
		original = result.DeploymentID
	}
	page, err := st.ListDeploymentPage(ctx, application.ID, 0, 10)
	if err != nil || len(page) != 1 {
		t.Fatalf("persisted deployments = %#v, %v", page, err)
	}
}

func TestAcceptDeploymentConcurrentDifferentKeysHaveOneWinner(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()
	application := mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "concurrent-different", Image: "nginx:1.0"})
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, key := range []string{
		"01234567-89ab-cdef-0123-456789abcdef",
		"11234567-89ab-cdef-0123-456789abcdef",
	} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			<-start
			_, err := svc.AcceptDeployment(ctx, app.DeploymentMutationRequest{
				AppName: application.Name, ExpectedAppID: application.ID,
				ExpectedGeneration: application.DesiredGeneration,
				IdempotencyKey:     key, Operation: store.DeploymentOperationDeploy, Image: "nginx:2.0",
			})
			results <- err
		}(key)
	}
	close(start)
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, store.ErrGenerationConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent outcomes: success=%d stale=%d", successes, conflicts)
	}
	page, err := st.ListDeploymentPage(ctx, application.ID, 0, 10)
	if err != nil || len(page) != 1 {
		t.Fatalf("multiple intents committed: %#v, %v", page, err)
	}
}

func TestDeploymentHistoryCursorSurvivesNewInsertAndRejectsWrongApp(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	application := mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "history-api", Image: "nginx:1.0"})
	other := mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "other-history", Image: "nginx:1.0"})
	for i, key := range []string{"01234567-89ab-cdef-0123-456789abcdef", "11234567-89ab-cdef-0123-456789abcdef", "21234567-89ab-cdef-0123-456789abcdef"} {
		request := app.DeploymentMutationRequest{
			AppName: application.Name, ExpectedAppID: application.ID,
			ExpectedGeneration: application.DesiredGeneration + int64(i),
			IdempotencyKey:     key, Operation: store.DeploymentOperationDeploy, Image: "nginx:2.0",
		}
		if _, err := svc.AcceptDeployment(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	first, err := svc.ListDeploymentPage(ctx, application.Name, 1, "")
	if err != nil || len(first.Deployments) != 1 || first.NextCursor == "" {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	if _, err := svc.ListDeploymentPage(ctx, other.Name, 1, first.NextCursor); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("foreign cursor = %v", err)
	}
	if _, err := svc.ListDeploymentPage(ctx, application.Name, 1, "@@"); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("malformed cursor = %v", err)
	}
	newer, err := svc.AcceptDeployment(ctx, app.DeploymentMutationRequest{
		AppName: application.Name, ExpectedAppID: application.ID,
		ExpectedGeneration: application.DesiredGeneration + 3,
		IdempotencyKey:     "31234567-89ab-cdef-0123-456789abcdef",
		Operation:          store.DeploymentOperationDeploy, Image: "nginx:3.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.ListDeploymentPage(ctx, application.Name, 1, first.NextCursor)
	if err != nil || len(second.Deployments) != 1 || second.Deployments[0].ID == first.Deployments[0].ID || second.Deployments[0].ID == newer.DeploymentID {
		t.Fatalf("second page was displaced by new insert = %#v, %v", second, err)
	}
}

func TestAcceptDeploymentRollbackAndInvalidRequests(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()
	application := mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "rollback-api", Image: "nginx:1.0"})
	other := mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "rollback-other", Image: "nginx:1.0"})
	image := "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64)
	target := &store.Deployment{
		ID: uuid.NewString(), AppID: application.ID, SourceType: store.SourceTypeImage,
		Image: "nginx:1.0", ResolvedImage: image,
		Status: store.DeploymentStatusSucceeded, TriggeredBy: store.TriggeredByAPI,
		CreatedAt: time.Now().UTC(), TargetGeneration: application.DesiredGeneration,
	}
	if err := st.CreateDeployment(ctx, target); err != nil {
		t.Fatal(err)
	}
	request := app.DeploymentMutationRequest{
		AppName: application.Name, ExpectedAppID: application.ID,
		ExpectedGeneration: application.DesiredGeneration,
		IdempotencyKey:     "01234567-89ab-cdef-0123-456789abcdef",
		Operation:          store.DeploymentOperationRollback, RollbackTargetID: target.ID,
	}
	if _, err := svc.GetDeploymentForApp(ctx, other.Name, target.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-app target leaked: %v", err)
	}
	accepted, err := svc.AcceptDeployment(ctx, request)
	if err != nil || accepted.Replayed {
		t.Fatalf("rollback acceptance = %#v, %v", accepted, err)
	}
	created, err := st.GetDeployment(ctx, accepted.DeploymentID)
	if err != nil || created.Image != image || created.RollbackSourceDeploymentID == nil || *created.RollbackSourceDeploymentID != target.ID {
		t.Fatalf("rollback intent = %#v, %v", created, err)
	}
	if err := st.UpdateDeployment(ctx, &store.Deployment{
		ID: target.ID, Image: target.Image, ResolvedImage: "", Status: store.DeploymentStatusFailed,
	}); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.AcceptDeployment(ctx, request)
	if err != nil || !replay.Replayed || replay.DeploymentID != accepted.DeploymentID {
		t.Fatalf("rollback replay after target changes = %#v, %v", replay, err)
	}
	request.IdempotencyKey = "11234567-89ab-cdef-0123-456789abcdef"
	request.ExpectedGeneration = accepted.TargetGeneration
	if _, err := svc.AcceptDeployment(ctx, request); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("new rollback of failed target = %v", err)
	}
	request.RollbackTargetID = uuid.NewString()
	if _, err := svc.AcceptDeployment(ctx, request); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing rollback target = %v", err)
	}
	request.Operation = store.DeploymentOperationDeploy
	request.RollbackTargetID = ""
	request.Image = "bad image"
	if _, err := svc.AcceptDeployment(ctx, request); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("invalid image = %v", err)
	}
	request.Image = "nginx:2.0"
	request.IdempotencyKey = "short"
	if _, err := svc.AcceptDeployment(ctx, request); !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("invalid key = %v", err)
	}
	request.IdempotencyKey = "11234567-89ab-cdef-0123-456789abcdef"
	request.ExpectedAppID = other.ID
	if _, err := svc.AcceptDeployment(ctx, request); !errors.Is(err, store.ErrGenerationConflict) {
		t.Fatalf("wrong app incarnation = %v", err)
	}
}

func TestAcceptDeploymentGenerationOverflowFailsWithoutIntent(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()
	application := mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "generation-limit", Image: "nginx:1.0"})
	application.DesiredGeneration = math.MaxInt64
	if err := st.UpdateApplication(ctx, application); err != nil {
		t.Fatal(err)
	}
	_, err := svc.AcceptDeployment(ctx, app.DeploymentMutationRequest{
		AppName: application.Name, ExpectedAppID: application.ID,
		ExpectedGeneration: math.MaxInt64,
		IdempotencyKey:     "01234567-89ab-cdef-0123-456789abcdef",
		Operation:          store.DeploymentOperationDeploy, Image: "nginx:2.0",
	})
	if !errors.Is(err, store.ErrInvalidData) {
		t.Fatalf("generation overflow = %v", err)
	}
	page, err := st.ListDeploymentPage(ctx, application.ID, 0, 1)
	if err != nil || len(page) != 0 {
		t.Fatalf("overflow created an intent: %#v, %v", page, err)
	}
}

func TestAcceptDeploymentReplayWhileApplicationIsDeleting(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()
	application := mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "deleting-replay", Image: "nginx:1.0"})
	request := app.DeploymentMutationRequest{
		AppName: application.Name, ExpectedAppID: application.ID,
		ExpectedGeneration: application.DesiredGeneration,
		IdempotencyKey:     "01234567-89ab-cdef-0123-456789abcdef",
		Operation:          store.DeploymentOperationDeploy, Image: "nginx:2.0",
	}
	first, err := svc.AcceptDeployment(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDeletionIntent(ctx, application.Name, first.TargetGeneration); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.AcceptDeployment(ctx, request)
	if err != nil || !replay.Replayed || replay.DeploymentID != first.DeploymentID {
		t.Fatalf("live key must replay during deletion: %#v, %v", replay, err)
	}
	request.IdempotencyKey = "11234567-89ab-cdef-0123-456789abcdef"
	request.ExpectedGeneration = first.TargetGeneration + 1
	if _, err := svc.AcceptDeployment(ctx, request); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("new command during deletion = %v", err)
	}
}

func TestAcceptDeploymentCanRollbackSuccessfulRollback(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()
	application := mustCreateTestApp(t, svc, ctx, app.CreateAppRequest{Name: "nested-rollback", Image: "nginx:1.0"})
	image := "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64)
	target := &store.Deployment{
		ID: uuid.NewString(), AppID: application.ID, SourceType: store.SourceTypeImage,
		Image: "nginx:1.0", ResolvedImage: image,
		Status: store.DeploymentStatusSuccess, TriggeredBy: store.TriggeredByAPI,
		CreatedAt: time.Now().UTC(), TargetGeneration: application.DesiredGeneration,
	}
	if err := st.CreateDeployment(ctx, target); err != nil {
		t.Fatal(err)
	}
	first, err := svc.AcceptDeployment(ctx, app.DeploymentMutationRequest{
		AppName: application.Name, ExpectedAppID: application.ID,
		ExpectedGeneration: application.DesiredGeneration,
		IdempotencyKey:     "01234567-89ab-cdef-0123-456789abcdef",
		Operation:          store.DeploymentOperationRollback, RollbackTargetID: target.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstDeployment, err := st.GetDeployment(ctx, first.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	firstDeployment.Status = store.DeploymentStatusSucceeded
	firstDeployment.ResolvedImage = image
	if err := st.UpdateDeployment(ctx, firstDeployment); err != nil {
		t.Fatal(err)
	}
	second, err := svc.AcceptDeployment(ctx, app.DeploymentMutationRequest{
		AppName: application.Name, ExpectedAppID: application.ID,
		ExpectedGeneration: first.TargetGeneration,
		IdempotencyKey:     "11234567-89ab-cdef-0123-456789abcdef",
		Operation:          store.DeploymentOperationRollback, RollbackTargetID: first.DeploymentID,
	})
	if err != nil {
		t.Fatalf("rollback of successful rollback: %v", err)
	}
	secondDeployment, err := st.GetDeployment(ctx, second.DeploymentID)
	if err != nil || secondDeployment.RollbackSourceDeploymentID == nil || *secondDeployment.RollbackSourceDeploymentID != first.DeploymentID || secondDeployment.Image != image {
		t.Fatalf("nested rollback intent = %#v, %v", secondDeployment, err)
	}
}
