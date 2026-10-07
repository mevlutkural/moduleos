package watcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

type safeRecordingQueue struct {
	mu     sync.Mutex
	names  []string
	queued chan struct{}
}

func newSafeRecordingQueue() *safeRecordingQueue {
	return &safeRecordingQueue{queued: make(chan struct{}, 16)}
}

func (q *safeRecordingQueue) Enqueue(name string) {
	q.mu.Lock()
	q.names = append(q.names, name)
	q.mu.Unlock()
	select {
	case q.queued <- struct{}{}:
	default:
	}
}

func (q *safeRecordingQueue) snapshot() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.names...)
}

type reconnectingEventClient struct {
	swarm.Client
	calls atomic.Int32
}

func (c *reconnectingEventClient) WatchEvents(ctx context.Context) (<-chan swarm.SwarmEvent, <-chan error) {
	call := c.calls.Add(1)
	events := make(chan swarm.SwarmEvent)
	errs := make(chan error)
	go func() {
		if call == 1 {
			events <- swarm.SwarmEvent{Type: "container", Action: "die", Target: "moduleos_first"}
			close(events)
			return
		}
		events <- swarm.SwarmEvent{Type: "service", Action: "update", Target: "moduleos_second"}
		<-ctx.Done()
		close(events)
		close(errs)
	}()
	return events, errs
}

type blockingEventClient struct {
	swarm.Client
	calls  atomic.Int32
	called chan struct{}
}

func (c *blockingEventClient) WatchEvents(ctx context.Context) (<-chan swarm.SwarmEvent, <-chan error) {
	c.calls.Add(1)
	select {
	case c.called <- struct{}{}:
	default:
	}
	events := make(chan swarm.SwarmEvent)
	errs := make(chan error)
	go func() {
		<-ctx.Done()
		close(events)
		close(errs)
	}()
	return events, errs
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestWatcherConfigurationDefaultsAndValidation(t *testing.T) {
	watch := New(nil, nil, nil)
	if watch.log == nil || watch.initialBackoff != time.Second || watch.maxBackoff != 30*time.Second {
		t.Fatalf("unexpected watcher defaults: %#v", watch)
	}
	watch.WithBackoff(5*time.Millisecond, 20*time.Millisecond)
	if watch.initialBackoff != 5*time.Millisecond || watch.maxBackoff != 20*time.Millisecond {
		t.Fatalf("valid backoff was not applied: %#v", watch)
	}
	watch.WithBackoff(0, time.Second).WithBackoff(time.Second, time.Millisecond)
	if watch.initialBackoff != 5*time.Millisecond || watch.maxBackoff != 20*time.Millisecond {
		t.Fatal("invalid backoff changed watcher configuration")
	}

	done := make(chan struct{})
	go func() {
		watch.Run(t.Context())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("misconfigured watcher did not return")
	}
}

func TestProcessEventsReturnsWhenEventStreamCloses(t *testing.T) {
	watch := New(nil, newSafeRecordingQueue(), discardLogger())
	events := make(chan swarm.SwarmEvent)
	errs := make(chan error)
	close(events)

	done := make(chan bool, 1)
	go func() { done <- watch.processEvents(t.Context(), events, errs) }()
	select {
	case received := <-done:
		if received {
			t.Fatal("closed empty stream reported activity")
		}
	case <-time.After(time.Second):
		t.Fatal("watcher blocked on an error channel after the event stream closed")
	}
}

func TestProcessEventsReturnsWhenErrorStreamCloses(t *testing.T) {
	watch := New(nil, newSafeRecordingQueue(), discardLogger())
	errs := make(chan error)
	close(errs)
	if received := watch.processEvents(t.Context(), nil, errs); received {
		t.Fatal("closed empty error stream reported event activity")
	}
}

func TestProcessEventsStopsOnErrorAndCancellation(t *testing.T) {
	watch := New(nil, newSafeRecordingQueue(), discardLogger())
	events := make(chan swarm.SwarmEvent)
	errs := make(chan error, 1)
	errs <- errors.New("stream failed")
	if received := watch.processEvents(t.Context(), events, errs); received {
		t.Fatal("error-only stream reported event activity")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if received := watch.processEvents(ctx, make(chan swarm.SwarmEvent), make(chan error)); received {
		t.Fatal("cancelled stream reported event activity")
	}
}

func TestRunReconnectsAfterEventChannelCloses(t *testing.T) {
	client := &reconnectingEventClient{}
	queue := newSafeRecordingQueue()
	watch := New(client, queue, discardLogger()).WithBackoff(time.Millisecond, 4*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	done := make(chan struct{})
	go func() {
		watch.Run(ctx)
		close(done)
	}()

	for len(queue.snapshot()) < 2 {
		select {
		case <-queue.queued:
		case <-ctx.Done():
			t.Fatalf("watcher did not reconnect: calls=%d names=%#v", client.calls.Load(), queue.snapshot())
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop after cancellation")
	}
	if got := queue.snapshot(); len(got) < 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("queued names = %#v", got)
	}
	if client.calls.Load() < 2 {
		t.Fatalf("watch calls = %d, want at least two", client.calls.Load())
	}
}

func TestRunRejectsConcurrentInvocation(t *testing.T) {
	client := &blockingEventClient{called: make(chan struct{}, 1)}
	watch := New(client, newSafeRecordingQueue(), discardLogger()).WithBackoff(time.Millisecond, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watch.Run(ctx)
		close(done)
	}()
	select {
	case <-client.called:
	case <-time.After(time.Second):
		t.Fatal("first watcher run did not subscribe")
	}

	secondDone := make(chan struct{})
	go func() {
		watch.Run(ctx)
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("concurrent watcher run did not return")
	}
	if client.calls.Load() != 1 {
		t.Fatalf("concurrent run opened %d streams", client.calls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("primary watcher run did not stop")
	}
}
