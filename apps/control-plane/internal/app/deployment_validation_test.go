package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

type deploymentCancellationBarrier struct{ entered chan struct{} }

func (barrier *deploymentCancellationBarrier) Lock(ctx context.Context) error {
	close(barrier.entered)
	<-ctx.Done()
	return ctx.Err()
}

func (*deploymentCancellationBarrier) Unlock() { panic("cancelled lock must not be released") }

func TestDeploymentImageValidationMatchesApplicationBoundary(t *testing.T) {
	for _, test := range []struct {
		image string
		valid bool
	}{
		{"nginx:1.0", true},
		{"docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), true},
		{"", false},
		{" nginx:1.0", false},
		{"nginx:1.0 ", false},
		{"nginx:\x00bad", false},
		{"\xff", false},
		{"docker.io/library/nginx@sha512:" + strings.Repeat("a", 64), false},
		{"docker.io/library/nginx@sha256:" + strings.Repeat("A", 64), false},
		{"docker.io/library/nginx@sha256:" + strings.Repeat("a", 63), false},
		{strings.Repeat("a", 64), false},
		{"nginx:" + strings.Repeat("a", 251), false},
	} {
		if got := validDeploymentImageReference(test.image); got != test.valid {
			t.Errorf("image %q accepted=%t want=%t", test.image, got, test.valid)
		}
	}
}

func TestDeploymentCursorCanonicalValidation(t *testing.T) {
	const appID = "20000000-0000-0000-0000-000000000001"
	valid := encodeDeploymentCursor(appID, 5)
	if got, err := decodeDeploymentCursor(valid, appID); err != nil || got != 5 {
		t.Fatalf("valid cursor = %d, %v", got, err)
	}
	for _, cursor := range []string{
		"@@", strings.Repeat("A", 257),
		encodeDeploymentCursor(appID, 0),
		encodeDeploymentCursor(appID, -1),
		encodeDeploymentCursor(appID, 5) + "=",
		encodeDeploymentCursor("20000000-0000-0000-0000-000000000002", 5),
		encodeDeploymentCursor(appID, 5)[:len(valid)-1],
	} {
		if _, err := decodeDeploymentCursor(cursor, appID); err == nil {
			t.Errorf("invalid cursor %q accepted", cursor)
		}
	}
	if _, err := decodeDeploymentCursor(valid, "not-an-app-id"); err == nil {
		t.Fatal("invalid application ID accepted")
	}
}

func TestIdempotencyTokenValidation(t *testing.T) {
	for _, test := range []struct {
		key   string
		valid bool
	}{
		{strings.Repeat("a", 16), true},
		{strings.Repeat("a", 128), true},
		{"0123456789._:-AB", true},
		{strings.Repeat("a", 15), false},
		{strings.Repeat("a", 129), false},
		{"0123456789abcdef ", false},
		{"0123456789abcdef\n", false},
		{"0123456789abcdeé", false},
	} {
		if got := validIdempotencyKey(test.key); got != test.valid {
			t.Errorf("key %q accepted=%t want=%t", test.key, got, test.valid)
		}
	}
}

func TestDeploymentCommandCancellationWhileWaitingForMutationLock(t *testing.T) {
	st, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := NewService(st, nil, "moduleos.local", nil)
	barrier := &deploymentCancellationBarrier{entered: make(chan struct{})}
	svc.resourceMu = barrier
	ctx, cancel := context.WithCancel(t.Context())
	results := make(chan error, 1)
	go func() {
		_, err := svc.AcceptDeployment(ctx, DeploymentMutationRequest{
			AppName: "blocked-command", ExpectedAppID: uuid.NewString(),
			ExpectedGeneration: 1,
			IdempotencyKey:     "01234567-89ab-cdef-0123-456789abcdef",
			Operation:          store.DeploymentOperationDeploy, Image: "nginx:2.0",
		})
		results <- err
	}()
	<-barrier.entered
	cancel()
	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting command cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled command did not release its waiter")
	}
}
