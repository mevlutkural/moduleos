package reconciler_test

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/reconciler"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/testkit/swarmfake"
)

func benchmarkSetup(b *testing.B) (*reconciler.Reconciler, *store.SQLiteStore, *swarmfake.Client) {
	b.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(b.TempDir(), "reconciler-performance.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	mock := swarmfake.New()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.NewService(st, mock, "moduleos.local", log)
	return reconciler.New(mock, svc, log).WithStabilizationWindow(0), st, mock
}

func BenchmarkFullReconcile100Applications(b *testing.B) {
	rec, st, mock := benchmarkSetup(b)
	svc := appSvcFromStore(b, st, mock)
	for index := range 100 {
		name := fmt.Sprintf("bench-%03d", index)
		if _, err := svc.CreateApp(b.Context(), app.CreateAppRequest{Name: name, Image: "nginx:1.27"}); err != nil {
			b.Fatal(err)
		}
	}
	rec.Reconcile(b.Context())
	b.ResetTimer()
	for b.Loop() {
		rec.Reconcile(b.Context())
	}
}

func BenchmarkNoOpApplicationReconcile(b *testing.B) {
	rec, st, mock := benchmarkSetup(b)
	svc := appSvcFromStore(b, st, mock)
	created, err := svc.CreateApp(b.Context(), app.CreateAppRequest{Name: "benchmark", Image: "nginx:1.27"})
	if err != nil {
		b.Fatal(err)
	}
	if err := rec.ReconcileApplication(b.Context(), created.Name); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if err := rec.ReconcileApplication(b.Context(), created.Name); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDriftApplicationReconcile(b *testing.B) {
	rec, st, mock := benchmarkSetup(b)
	svc := appSvcFromStore(b, st, mock)
	created, err := svc.CreateApp(b.Context(), app.CreateAppRequest{Name: "drift-benchmark", Image: "nginx:1.27", Replicas: 1})
	if err != nil {
		b.Fatal(err)
	}
	if err := rec.ReconcileApplication(b.Context(), created.Name); err != nil {
		b.Fatal(err)
	}
	serviceName := swarm.ServiceName(created.Name)
	b.ResetTimer()
	for b.Loop() {
		// Manual replica drift is injected before every control-loop pass. The
		// measured operation includes detection, canonical diff, update and the
		// observed-state CAS write.
		mock.Services[serviceName].Replicas = 2
		mock.Services[serviceName].Running = 2
		mock.Services[serviceName].Spec.Replicas = 2
		if err := rec.ReconcileApplication(b.Context(), created.Name); err != nil {
			b.Fatal(err)
		}
	}
}
