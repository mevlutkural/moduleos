package store

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func commandTestStore(t *testing.T) (*SQLiteStore, *Application) {
	t.Helper()
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	app := canonicalTestApp("command-app")
	if err := st.CreateApplication(t.Context(), app); err != nil {
		t.Fatal(err)
	}
	return st, app
}

func commandFixture(app *Application, key string, now time.Time) DeploymentCommand {
	return DeploymentCommand{
		AppName: app.Name, ExpectedAppID: app.ID, ExpectedGeneration: app.DesiredGeneration,
		Operation: DeploymentOperationDeploy, Image: "nginx:2.0", NewDeploymentID: uuid.NewString(),
		KeyDigest: sha256.Sum256([]byte(key)), Fingerprint: sha256.Sum256([]byte("fingerprint:" + key)),
		Now: now, ConvergenceDeadline: now.Add(5 * time.Minute),
	}
}

func TestDeploymentCommandAcceptanceReplayAndExpiry(t *testing.T) {
	st, application := commandTestStore(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	command := commandFixture(application, "first", now)
	accepted, err := st.AcceptDeploymentCommand(t.Context(), command)
	if err != nil || accepted.Replayed || accepted.TargetGeneration != application.DesiredGeneration+1 {
		t.Fatalf("first acceptance = %#v, %v", accepted, err)
	}
	current, err := st.GetApplication(t.Context(), application.Name)
	if err != nil || current.DesiredGeneration != accepted.TargetGeneration || current.Image != command.Image {
		t.Fatalf("persisted application = %#v, %v", current, err)
	}
	command.NewDeploymentID = uuid.NewString()
	command.Now = now.Add(23 * time.Hour)
	command.ConvergenceDeadline = command.Now.Add(5 * time.Minute)
	replay, err := st.AcceptDeploymentCommand(t.Context(), command)
	if err != nil || !replay.Replayed || replay.DeploymentID != accepted.DeploymentID || replay.TargetGeneration != accepted.TargetGeneration {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	page, err := st.ListDeploymentPage(t.Context(), application.ID, 0, 10)
	if err != nil || len(page) != 1 {
		t.Fatalf("one intent after replay = %#v, %v", page, err)
	}
	command.Fingerprint = sha256.Sum256([]byte("different"))
	if _, err := st.AcceptDeploymentCommand(t.Context(), command); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same key with different fingerprint = %v", err)
	}
	command.Fingerprint = sha256.Sum256([]byte("fingerprint:first"))
	command.ExpectedGeneration = accepted.TargetGeneration
	command.Now = now.Add(24 * time.Hour)
	command.ConvergenceDeadline = command.Now.Add(5 * time.Minute)
	expired, err := st.AcceptDeploymentCommand(t.Context(), command)
	if err != nil || expired.Replayed || expired.DeploymentID == accepted.DeploymentID {
		t.Fatalf("exact expiry boundary = %#v, %v", expired, err)
	}
}

func TestDeploymentCommandPreconditionAndAtomicOrder(t *testing.T) {
	st, application := commandTestStore(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	command := commandFixture(application, "atomic", now)
	command.ExpectedAppID = uuid.NewString()
	if _, err := st.AcceptDeploymentCommand(t.Context(), command); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("wrong incarnation = %v", err)
	}
	command.ExpectedAppID = application.ID
	command.NewDeploymentID = "not-a-uuid"
	if _, err := st.AcceptDeploymentCommand(t.Context(), command); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("invalid ID = %v", err)
	}
	command.NewDeploymentID = uuid.NewString()
	if _, err := st.db.ExecContext(t.Context(), `CREATE TRIGGER reject_order BEFORE INSERT ON deployment_order BEGIN SELECT RAISE(ABORT, 'injected order failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptDeploymentCommand(t.Context(), command); err == nil {
		t.Fatal("injected order failure unexpectedly accepted")
	}
	current, err := st.GetApplication(t.Context(), application.Name)
	if err != nil || current.DesiredGeneration != application.DesiredGeneration || current.Image != application.Image {
		t.Fatalf("failed command partially updated application: %#v, %v", current, err)
	}
	for _, table := range []string{"deployments", "deployment_order", "deployment_idempotency"} {
		var count int
		if err := st.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count after rollback = %d, %v", table, count, err)
		}
	}
}

func TestDeploymentCommandRollbackRechecksTarget(t *testing.T) {
	st, application := commandTestStore(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	target := &Deployment{
		ID: uuid.NewString(), AppID: application.ID, SourceType: SourceTypeImage,
		Image: "nginx:1.0", ResolvedImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64),
		Status: DeploymentStatusSucceeded, TriggeredBy: TriggeredByAPI,
		CreatedAt: now.Add(-time.Hour), TargetGeneration: application.DesiredGeneration,
	}
	if err := st.CreateDeployment(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	command := commandFixture(application, "rollback", now)
	command.Operation = DeploymentOperationRollback
	command.RollbackTargetID = target.ID
	command.Image = target.ResolvedImage
	if _, err := st.AcceptDeploymentCommand(t.Context(), command); err != nil {
		t.Fatalf("valid rollback = %v", err)
	}
	current, err := st.GetApplication(t.Context(), application.Name)
	if err != nil || current.Image != target.ResolvedImage {
		t.Fatalf("rollback image = %#v, %v", current, err)
	}
	command = commandFixture(current, "changed-target", now.Add(time.Second))
	command.Operation = DeploymentOperationRollback
	command.RollbackTargetID = target.ID
	command.Image = target.ResolvedImage
	target.ResolvedImage = "docker.io/library/nginx@sha256:" + strings.Repeat("b", 64)
	if err := st.UpdateDeployment(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AcceptDeploymentCommand(t.Context(), command); !errors.Is(err, ErrConflict) {
		t.Fatalf("modified rollback target = %v", err)
	}
}

func TestDeploymentOrderSameTimestampAndOwnership(t *testing.T) {
	st, application := commandTestStore(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := st.CreateDeployment(t.Context(), &Deployment{ID: uuid.NewString(), AppID: application.ID,
			SourceType: SourceTypeImage, Image: "nginx:1.0", Status: DeploymentStatusPending,
			TriggeredBy: TriggeredByAPI, CreatedAt: now, TargetGeneration: application.DesiredGeneration}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := st.ListDeploymentPage(t.Context(), application.ID, 0, 2)
	if err != nil || len(first) != 3 || first[0].Sequence <= first[1].Sequence {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	second, err := st.ListDeploymentPage(t.Context(), application.ID, first[1].Sequence, 2)
	if err != nil || len(second) != 1 || second[0].Deployment.ID == first[0].Deployment.ID {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	if _, err := st.GetDeploymentForApp(t.Context(), uuid.NewString(), first[0].Deployment.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-app detail = %v", err)
	}
}

func TestDeploymentCommandCapacityAndCorruptReplay(t *testing.T) {
	st, application := commandTestStore(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	var first DeploymentCommand
	var firstID string
	for i := 0; i < 1024; i++ {
		command := commandFixture(application, fmt.Sprintf("key-%04d", i), now.Add(time.Duration(i)*time.Second))
		command.ExpectedGeneration = application.DesiredGeneration + int64(i)
		accepted, err := st.AcceptDeploymentCommand(t.Context(), command)
		if err != nil {
			t.Fatalf("accept key %d: %v", i, err)
		}
		if i == 0 {
			first, firstID = command, accepted.DeploymentID
		}
	}
	blocked := commandFixture(application, "overflow", now.Add(1024*time.Second))
	blocked.ExpectedGeneration = application.DesiredGeneration + 1024
	limit, err := st.AcceptDeploymentCommand(t.Context(), blocked)
	if !errors.Is(err, ErrIdempotencyCapacity) || limit == nil || !limit.EarliestExpiryUTC.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("capacity = %#v, %v", limit, err)
	}
	first.Now = now.Add(23 * time.Hour)
	first.ConvergenceDeadline = first.Now.Add(5 * time.Minute)
	replay, err := st.AcceptDeploymentCommand(t.Context(), first)
	if err != nil || !replay.Replayed || replay.DeploymentID != firstID {
		t.Fatalf("replay at capacity = %#v, %v", replay, err)
	}
	if _, err := st.db.ExecContext(t.Context(), `DELETE FROM deployments WHERE id = ?`, firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LookupDeploymentCommand(t.Context(), application.ID, first.KeyDigest, first.Fingerprint, first.Now); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("missing ledger target = %v", err)
	}
}

func TestDeploymentPageLargeHistoryUsesIndex(t *testing.T) {
	st, application := commandTestStore(t)
	tx, err := st.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for i := 1; i <= 10000; i++ {
		id := fmt.Sprintf("30000000-0000-0000-0000-%012d", i)
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO deployments
			(id, app_id, source_type, image, status, triggered_by, created_at, target_generation)
			VALUES (?, ?, 'image', 'nginx:1.0', 'pending', 'api', ?, 1)`,
			id, application.ID, "2026-10-14 12:00:00"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := st.db.QueryContext(t.Context(), `EXPLAIN QUERY PLAN SELECT d.id FROM deployment_order o
		JOIN deployments d ON d.id = o.deployment_id AND d.app_id = o.app_id
		WHERE o.app_id = ? AND o.sequence < ? ORDER BY o.sequence DESC LIMIT ?`, application.ID, 9000, 101)
	if err != nil {
		t.Fatal(err)
	}
	var queryPlan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		queryPlan += detail + "\n"
	}
	if err := rows.Err(); err != nil || !strings.Contains(queryPlan, "idx_deployment_order_app_sequence") {
		t.Fatalf("page query is not index-backed: %s (%v)", queryPlan, err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := st.ListDeploymentPage(t.Context(), application.ID, 9000, 100)
	if err != nil || len(page) != 101 || page[0].Sequence != 8999 {
		t.Fatalf("bounded page = %d rows, %v", len(page), err)
	}
	started := time.Now()
	history, err := st.ListDeployments(t.Context(), application.ID)
	if err != nil || len(history) != 10000 {
		t.Fatalf("legacy reconciler history = %d rows, %v", len(history), err)
	}
	fullReadDuration := time.Since(started)
	var pageCount, pageSize int64
	if err := st.db.QueryRowContext(t.Context(), `PRAGMA page_count`).Scan(&pageCount); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRowContext(t.Context(), `PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	t.Logf("10,000 deployment fixture: legacy full read=%s, database pages=%d, page bytes=%d, total bytes=%d",
		fullReadDuration, pageCount, pageSize, pageCount*pageSize)
}

func TestDeploymentCommandReplayAfterReopenAndRecreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployment.db")
	st, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	application := canonicalTestApp("reopen-command")
	if err := st.CreateApplication(t.Context(), application); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	command := commandFixture(application, "persistent-key", now)
	first, err := st.AcceptDeploymentCommand(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	replay, err := reopened.LookupDeploymentCommand(t.Context(), application.ID, command.KeyDigest, command.Fingerprint, now.Add(time.Hour))
	if err != nil || replay == nil || replay.DeploymentID != first.DeploymentID {
		t.Fatalf("replay after restart = %#v, %v", replay, err)
	}
	if err := reopened.DeleteApplication(t.Context(), application.Name); err != nil {
		t.Fatal(err)
	}
	recreated := canonicalTestApp(application.Name)
	if err := reopened.CreateApplication(t.Context(), recreated); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.AcceptDeploymentCommand(t.Context(), command); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("old-incarnation command accepted after recreation: %v", err)
	}
	var count int
	if err := reopened.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM deployment_idempotency WHERE app_id = ?`, application.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old ledger survived app deletion: %d, %v", count, err)
	}
}

func TestDeploymentCommandFailureAtEveryWriteRollsBack(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
	}{
		{"application", `CREATE TRIGGER fail_command BEFORE UPDATE ON applications BEGIN SELECT RAISE(ABORT, 'injected app failure'); END`},
		{"terminalization", `CREATE TRIGGER fail_command BEFORE UPDATE ON deployments BEGIN SELECT RAISE(ABORT, 'injected terminalization failure'); END`},
		{"deployment", `CREATE TRIGGER fail_command BEFORE INSERT ON deployments BEGIN SELECT RAISE(ABORT, 'injected deployment failure'); END`},
		{"order", `CREATE TRIGGER fail_command BEFORE INSERT ON deployment_order BEGIN SELECT RAISE(ABORT, 'injected order failure'); END`},
		{"ledger", `CREATE TRIGGER fail_command BEFORE INSERT ON deployment_idempotency BEGIN SELECT RAISE(ABORT, 'injected ledger failure'); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, application := commandTestStore(t)
			now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
			pending := &Deployment{
				ID: uuid.NewString(), AppID: application.ID, SourceType: SourceTypeImage,
				Image: application.Image, Status: DeploymentStatusPending,
				TriggeredBy: TriggeredByAPI, CreatedAt: now, TargetGeneration: application.DesiredGeneration,
			}
			if err := st.CreateDeployment(t.Context(), pending); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.ExecContext(t.Context(), test.trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := st.AcceptDeploymentCommand(t.Context(), commandFixture(application, "failure", now)); err == nil {
				t.Fatal("injected write failure was accepted")
			}
			current, err := st.GetApplication(t.Context(), application.Name)
			if err != nil || current.Image != application.Image || current.DesiredGeneration != application.DesiredGeneration {
				t.Fatalf("application changed on rollback: %#v, %v", current, err)
			}
			original, err := st.GetDeployment(t.Context(), pending.ID)
			if err != nil || original.Status != DeploymentStatusPending {
				t.Fatalf("old intent terminalized on rollback: %#v, %v", original, err)
			}
			for table, want := range map[string]int{"deployments": 1, "deployment_order": 1, "deployment_idempotency": 0} {
				var count int
				if err := st.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != want {
					t.Fatalf("%s count = %d, want %d (%v)", table, count, want, err)
				}
			}
		})
	}
}

func TestDeploymentCommandAmbiguousCommitIsResolvedByLedger(t *testing.T) {
	st, application := commandTestStore(t)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	st.deploymentCommit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("injected lost commit acknowledgment")
	}
	command := commandFixture(application, "lost-ack", now)
	accepted, err := st.AcceptDeploymentCommand(t.Context(), command)
	if err != nil || accepted == nil || accepted.Replayed {
		t.Fatalf("committed request was lost after ambiguous acknowledgment: %#v, %v", accepted, err)
	}
	stored, err := st.LookupDeploymentCommand(t.Context(), application.ID, command.KeyDigest, command.Fingerprint, now)
	if err != nil || stored == nil || stored.DeploymentID != accepted.DeploymentID {
		t.Fatalf("ledger mismatch after lost acknowledgment: %#v, %v", stored, err)
	}
	st.deploymentCommit = func(*sql.Tx) error { return errors.New("injected pre-commit failure") }
	next := commandFixture(application, "no-commit", now.Add(time.Second))
	next.ExpectedGeneration = accepted.TargetGeneration
	if _, err := st.AcceptDeploymentCommand(t.Context(), next); !errors.Is(err, ErrCommitOutcomeUnknown) {
		t.Fatalf("unconfirmed commit must fail closed: %v", err)
	}
	if replay, err := st.LookupDeploymentCommand(t.Context(), application.ID, next.KeyDigest, next.Fingerprint, next.Now); err != nil || replay != nil {
		t.Fatalf("failed commit wrote ledger: %#v, %v", replay, err)
	}
}
