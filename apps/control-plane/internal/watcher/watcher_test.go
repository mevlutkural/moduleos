package watcher

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

type recordingQueue struct{ names []string }

func (q *recordingQueue) Enqueue(name string) { q.names = append(q.names, name) }

func TestAppNameFromTarget(t *testing.T) {
	cases := []struct {
		target string
		want   string
	}{
		{"moduleos_my-api", "my-api"},
		{"moduleos_redis", "redis"},
		{"other_service", ""},
		{"moduleos_", ""},
		{"random", ""},
	}

	for _, tc := range cases {
		got := appNameFromTarget(tc.target)
		if got != tc.want {
			t.Errorf("appNameFromTarget(%q): got %q, want %q", tc.target, got, tc.want)
		}
	}
}

func TestProcessEventsOnlyEnqueuesManagedResources(t *testing.T) {
	queue := &recordingQueue{}
	watch := &Watcher{queue: queue, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	events := make(chan swarm.SwarmEvent, 2)
	errs := make(chan error)
	events <- swarm.SwarmEvent{Type: "container", Action: "die", Target: "moduleos_api"}
	events <- swarm.SwarmEvent{Type: "service", Action: "update", Target: "external"}
	close(events)
	close(errs)
	if received := watch.processEvents(context.Background(), events, errs); !received {
		t.Fatal("expected event activity")
	}
	if len(queue.names) != 1 || queue.names[0] != "api" {
		t.Fatalf("queued names = %#v", queue.names)
	}
}
