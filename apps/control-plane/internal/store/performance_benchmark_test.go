package store_test

import (
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

func BenchmarkSQLiteConcurrentIntentWrites(b *testing.B) {
	st, err := store.NewSQLiteStore(filepath.Join(b.TempDir(), "performance.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	application := testApp("sqlite-write-benchmark")
	if err := st.CreateApplication(b.Context(), application); err != nil {
		b.Fatal(err)
	}

	var sequence atomic.Uint64
	var failures atomic.Uint64
	b.ResetTimer()
	b.RunParallel(func(worker *testing.PB) {
		for worker.Next() {
			replicas := int(sequence.Add(1)%2) + 1
			if _, updateErr := st.UpdateApplicationIntent(b.Context(), application.Name, -1, store.ApplicationMutation{Replicas: &replicas}); updateErr != nil {
				failures.Add(1)
			}
		}
	})
	b.StopTimer()
	b.ReportMetric(float64(failures.Load()), "write-errors")
	if failures.Load() != 0 {
		b.Fatalf("concurrent SQLite writes failed: %d", failures.Load())
	}
}
