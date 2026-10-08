package reconciler_test

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

func TestQualificationSoak(t *testing.T) {
	rawDuration := os.Getenv("MODULEOS_SOAK_DURATION")
	if rawDuration == "" {
		t.Skip("set MODULEOS_SOAK_DURATION (release qualification: 24h)")
	}
	duration, err := time.ParseDuration(rawDuration)
	if err != nil || duration <= 0 {
		t.Fatalf("invalid MODULEOS_SOAK_DURATION %q", rawDuration)
	}
	interval := 100 * time.Millisecond
	if rawInterval := os.Getenv("MODULEOS_SOAK_INTERVAL"); rawInterval != "" {
		interval, err = time.ParseDuration(rawInterval)
		if err != nil || interval < 0 {
			t.Fatalf("invalid MODULEOS_SOAK_INTERVAL %q", rawInterval)
		}
	}
	rec, st, mock := setup(t)
	svc := appSvcFromStore(t, st, mock)
	created, err := svc.CreateApp(t.Context(), app.CreateAppRequest{Name: "soak", Image: "nginx:1.27"})
	if err != nil {
		t.Fatal(err)
	}
	rec.Reconcile(t.Context())
	baseline := runtime.NumGoroutine()
	baselineFDs := openFileDescriptors()
	started := time.Now()
	deadline := time.Now().Add(duration)
	iterations := 0
	for time.Now().Before(deadline) {
		service := mock.Services[swarm.ServiceName(created.Name)]
		service.Spec.Image = "redis:7"
		service.Image = "redis:7"
		rec.Reconcile(t.Context())
		iterations++
		if iterations%100 == 0 {
			runtime.GC()
		}
		if interval > 0 {
			time.Sleep(interval)
		}
	}
	runtime.GC()
	if growth := runtime.NumGoroutine() - baseline; growth > 5 {
		t.Fatalf("goroutine growth after %d iterations: %d", iterations, growth)
	}
	if iterations == 0 {
		t.Fatal("soak completed without an iteration")
	}
	if finalFDs := openFileDescriptors(); baselineFDs >= 0 && finalFDs-baselineFDs > 3 {
		t.Fatalf("file descriptor growth after %d iterations: baseline=%d final=%d", iterations, baselineFDs, finalFDs)
	}
	t.Logf("soak completed: duration=%s iterations=%d goroutines=%d->%d file_descriptors=%d->%d", time.Since(started), iterations, baseline, runtime.NumGoroutine(), baselineFDs, openFileDescriptors())
}

func openFileDescriptors() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}
