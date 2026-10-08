package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

type blockingProjectLookupStore struct {
	store.Store
	projectLookups atomic.Int32
	firstEntered   chan struct{}
	releaseFirst   chan struct{}
}

func (s *blockingProjectLookupStore) GetProject(ctx context.Context, slug string) (*store.Project, error) {
	if s.projectLookups.Add(1) == 1 {
		close(s.firstEntered)
		select {
		case <-s.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.Store.GetProject(ctx, slug)
}

type disappearingProjectLinkStore struct {
	store.Store
}

func (s *disappearingProjectLinkStore) DeleteProjectLink(context.Context, string) error {
	return store.ErrNotFound
}

type failingProjectLinkStore struct {
	store.Store
	listApplicationsErr error
	listLinksErr        error
	deleteLinkErr       error
}

func (s *failingProjectLinkStore) ListApplicationsByProject(ctx context.Context, projectID string) ([]*store.Application, error) {
	if s.listApplicationsErr != nil {
		return nil, s.listApplicationsErr
	}
	return s.Store.ListApplicationsByProject(ctx, projectID)
}

func (s *failingProjectLinkStore) ListProjectLinksByProject(ctx context.Context, projectID string) ([]*store.ProjectLink, error) {
	if s.listLinksErr != nil {
		return nil, s.listLinksErr
	}
	return s.Store.ListProjectLinksByProject(ctx, projectID)
}

func (s *failingProjectLinkStore) DeleteProjectLink(ctx context.Context, linkID string) error {
	if s.deleteLinkErr != nil {
		return s.deleteLinkErr
	}
	return s.Store.DeleteProjectLink(ctx, linkID)
}

func TestDeleteProjectLinkSerializesConcurrentRetries(t *testing.T) {
	baseService, st, mock := newTestService(t)
	target := mustCreateTestProject(t, baseService, t.Context(), app.CreateProjectRequest{Name: "Target", Slug: "target"})
	mustCreateTestApp(t, baseService, t.Context(), app.CreateAppRequest{Name: "target-app", ProjectSlug: target.Slug, Image: "nginx:latest"})
	source := mustCreateTestProject(t, baseService, t.Context(), app.CreateProjectRequest{Name: "Source", Slug: "source"})
	link := mustCreateTestLink(t, baseService, t.Context(), source.Slug, "target-app", "target.api")

	blockingStore := &blockingProjectLookupStore{
		Store: st, firstEntered: make(chan struct{}), releaseFirst: make(chan struct{}),
	}
	service := app.NewService(blockingStore, mock, "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	results := make(chan error, 2)
	go func() { results <- service.DeleteProjectLink(context.Background(), source.Slug, link.ID) }()
	select {
	case <-blockingStore.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first deletion did not enter project lookup")
	}
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		results <- service.DeleteProjectLink(context.Background(), source.Slug, link.ID)
	}()
	<-secondStarted

	select {
	case <-time.After(25 * time.Millisecond):
		if lookups := blockingStore.projectLookups.Load(); lookups != 1 {
			t.Fatalf("concurrent deletion crossed the service lock: lookups = %d", lookups)
		}
	case err := <-results:
		t.Fatalf("deletion completed before the first lookup was released: %v", err)
	}
	close(blockingStore.releaseFirst)

	var success, missing int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			success++
		case errors.Is(err, store.ErrNotFound):
			missing++
		default:
			t.Fatalf("unexpected deletion result: %v", err)
		}
	}
	if success != 1 || missing != 1 {
		t.Fatalf("success/missing = %d/%d", success, missing)
	}
}

func TestDeleteProjectLinkPreservesConcurrentCascadeNotFound(t *testing.T) {
	baseService, st, mock := newTestService(t)
	target := mustCreateTestProject(t, baseService, t.Context(), app.CreateProjectRequest{Name: "Target", Slug: "target"})
	mustCreateTestApp(t, baseService, t.Context(), app.CreateAppRequest{Name: "target-app", ProjectSlug: target.Slug, Image: "nginx:latest"})
	source := mustCreateTestProject(t, baseService, t.Context(), app.CreateProjectRequest{Name: "Source", Slug: "source"})
	link := mustCreateTestLink(t, baseService, t.Context(), source.Slug, "target-app", "target.api")

	service := app.NewService(&disappearingProjectLinkStore{Store: st}, mock, "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.DeleteProjectLink(t.Context(), source.Slug, link.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestDeleteProjectLinkLockReleasesAfterCancellation(t *testing.T) {
	baseService, st, mock := newTestService(t)
	target := mustCreateTestProject(t, baseService, t.Context(), app.CreateProjectRequest{Name: "Target", Slug: "target"})
	mustCreateTestApp(t, baseService, t.Context(), app.CreateAppRequest{Name: "target-app", ProjectSlug: target.Slug, Image: "nginx:latest"})
	source := mustCreateTestProject(t, baseService, t.Context(), app.CreateProjectRequest{Name: "Source", Slug: "source"})
	link := mustCreateTestLink(t, baseService, t.Context(), source.Slug, "target-app", "target.api")

	release := make(chan struct{})
	blockingStore := &blockingProjectLookupStore{Store: st, firstEntered: make(chan struct{}), releaseFirst: release}
	service := app.NewService(blockingStore, mock, "moduleos.local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.DeleteProjectLink(ctx, source.Slug, link.ID) }()
	<-blockingStore.firstEntered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}

	close(release)
	if err := service.DeleteProjectLink(t.Context(), source.Slug, link.ID); err != nil {
		t.Fatalf("lock remained held after cancellation: %v", err)
	}
}

func TestProjectLinkInternalFailuresPreserveOnlyCancellationClassification(t *testing.T) {
	baseService, st, mock := newTestService(t)
	target := mustCreateTestProject(t, baseService, t.Context(), app.CreateProjectRequest{Name: "Target", Slug: "target"})
	mustCreateTestApp(t, baseService, t.Context(), app.CreateAppRequest{Name: "target-app", ProjectSlug: target.Slug, Image: "nginx:latest"})
	source := mustCreateTestProject(t, baseService, t.Context(), app.CreateProjectRequest{Name: "Source", Slug: "source"})
	link := mustCreateTestLink(t, baseService, t.Context(), source.Slug, "target-app", "target.api")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name      string
		err       error
		configure func(*failingProjectLinkStore)
		call      func(*app.Service) error
	}{
		{
			name: "create cancellation",
			err:  context.Canceled,
			configure: func(st *failingProjectLinkStore) {
				st.listApplicationsErr = context.Canceled
			},
			call: func(service *app.Service) error {
				_, err := service.CreateProjectLink(t.Context(), source.Slug, "target-app", "other.api")
				return err
			},
		},
		{
			name: "list deadline",
			err:  context.DeadlineExceeded,
			configure: func(st *failingProjectLinkStore) {
				st.listLinksErr = context.DeadlineExceeded
			},
			call: func(service *app.Service) error {
				_, err := service.ListProjectLinks(t.Context(), source.Slug)
				return err
			},
		},
		{
			name: "delete cancellation",
			err:  context.Canceled,
			configure: func(st *failingProjectLinkStore) {
				st.deleteLinkErr = context.Canceled
			},
			call: func(service *app.Service) error {
				return service.DeleteProjectLink(t.Context(), source.Slug, link.ID)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failingStore := &failingProjectLinkStore{Store: st}
			test.configure(failingStore)
			err := test.call(app.NewService(failingStore, mock, "moduleos.local", log))
			if !errors.Is(err, app.ErrInternal) || !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want ErrInternal and %v", err, test.err)
			}
		})
	}

	orphaned := app.NewService(&failingProjectLinkStore{Store: st, listLinksErr: store.ErrNotFound}, mock, "moduleos.local", log)
	_, err := orphaned.ListProjectLinks(t.Context(), source.Slug)
	if !errors.Is(err, app.ErrInternal) || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("orphaned state error = %v, want internal classification only", err)
	}
}
