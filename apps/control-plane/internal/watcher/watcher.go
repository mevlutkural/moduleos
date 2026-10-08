package watcher

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

const servicePrefix = "moduleos_"

const (
	defaultInitialBackoff = time.Second
	defaultMaxBackoff     = 30 * time.Second
)

type Enqueuer interface {
	Enqueue(applicationName string)
}

type Watcher struct {
	swarm          swarm.Client
	queue          Enqueuer
	log            *slog.Logger
	initialBackoff time.Duration
	maxBackoff     time.Duration
	running        atomic.Bool
}

func New(sw swarm.Client, queue Enqueuer, log *slog.Logger) *Watcher {
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{
		swarm:          sw,
		queue:          queue,
		log:            log,
		initialBackoff: defaultInitialBackoff,
		maxBackoff:     defaultMaxBackoff,
	}
}

func (w *Watcher) WithBackoff(initial, maximum time.Duration) *Watcher {
	if initial > 0 && maximum >= initial {
		w.initialBackoff = initial
		w.maxBackoff = maximum
	}
	return w
}

func (w *Watcher) Run(ctx context.Context) {
	if !w.running.CompareAndSwap(false, true) {
		w.log.Warn("event watcher is already running")
		return
	}
	defer w.running.Store(false)
	if w.swarm == nil || w.queue == nil {
		w.log.Error("event watcher is not configured")
		return
	}
	w.log.Info("event watcher started")
	backoff := w.initialBackoff
	for ctx.Err() == nil {
		events, errs := w.swarm.WatchEvents(ctx)
		received := w.processEvents(ctx, events, errs)
		if ctx.Err() != nil {
			break
		}
		if received {
			backoff = w.initialBackoff
		}
		w.log.Warn("event stream disconnected", "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			w.log.Info("event watcher stopped")
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, w.maxBackoff)
	}
	w.log.Info("event watcher stopped")
}

func (w *Watcher) processEvents(ctx context.Context, events <-chan swarm.SwarmEvent, errs <-chan error) bool {
	received := false
	for events != nil || errs != nil {
		select {
		case <-ctx.Done():
			return received
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				w.log.Warn("event stream error", "error", err)
				return received
			}
		case event, ok := <-events:
			if !ok {
				return received
			}
			received = true
			if appName := appNameFromTarget(event.Target); appName != "" {
				w.queue.Enqueue(appName)
				w.log.Debug("event queued for reconciliation", "type", event.Type, "action", event.Action, "app", appName)
			}
		}
	}
	return received
}

func appNameFromTarget(target string) string {
	if !strings.HasPrefix(target, servicePrefix) {
		return ""
	}
	return strings.TrimPrefix(target, servicePrefix)
}
