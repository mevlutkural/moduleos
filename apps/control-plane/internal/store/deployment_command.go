package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/distribution/reference"
	"github.com/google/uuid"
)

var ErrIdempotencyConflict = errors.New("idempotency key conflict")
var ErrIdempotencyCapacity = errors.New("idempotency capacity exceeded")
var ErrCommitOutcomeUnknown = errors.New("deployment commit outcome unknown")

type DeploymentOperation string

const (
	DeploymentOperationDeploy   DeploymentOperation = "deploy"
	DeploymentOperationRollback DeploymentOperation = "rollback"
)

type DeploymentCommand struct {
	AppName             string
	ExpectedAppID       string
	ExpectedGeneration  int64
	Operation           DeploymentOperation
	Image               string
	RollbackTargetID    string
	NewDeploymentID     string
	KeyDigest           [32]byte
	Fingerprint         [32]byte
	Now                 time.Time
	ConvergenceDeadline time.Time
}

type DeploymentAcceptance struct {
	AppID             string
	DeploymentID      string
	TargetGeneration  int64
	Replayed          bool
	EarliestExpiryUTC time.Time
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *SQLiteStore) LookupDeploymentCommand(ctx context.Context, appID string, keyDigest, fingerprint [32]byte, now time.Time) (*DeploymentAcceptance, error) {
	if !canonicalCommandUUID(appID) || !validCommandTime(now) {
		return nil, fmt.Errorf("%w: invalid lookup input", ErrInvalidData)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return lookupDeploymentCommand(ctx, tx, appID, keyDigest, fingerprint, now.UnixMicro())
}

func lookupDeploymentCommand(ctx context.Context, query rowQuerier, appID string, keyDigest, fingerprint [32]byte, nowMicros int64) (*DeploymentAcceptance, error) {
	var storedFingerprint []byte
	var deploymentID string
	var generation, expiry int64
	err := query.QueryRowContext(ctx, `SELECT fingerprint, deployment_id, accepted_generation, expires_at
		FROM deployment_idempotency WHERE app_id = ? AND key_digest = ?`, appID, keyDigest[:]).
		Scan(&storedFingerprint, &deploymentID, &generation, &expiry)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && expiry <= nowMicros) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(storedFingerprint) != 32 || !canonicalCommandUUID(deploymentID) || generation <= 0 || expiry <= 0 {
		return nil, fmt.Errorf("%w: corrupt deployment ledger", ErrInvalidData)
	}
	var linkedAppID string
	var linkedGeneration int64
	err = query.QueryRowContext(ctx, `SELECT app_id, target_generation FROM deployments WHERE id = ?`, deploymentID).
		Scan(&linkedAppID, &linkedGeneration)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (linkedAppID != appID || linkedGeneration != generation)) {
		return nil, fmt.Errorf("%w: deployment ledger target mismatch", ErrInvalidData)
	}
	if err != nil {
		return nil, err
	}
	if !equalDigest(storedFingerprint, fingerprint[:]) {
		return nil, ErrIdempotencyConflict
	}
	return &DeploymentAcceptance{AppID: appID, DeploymentID: deploymentID, TargetGeneration: generation, Replayed: true}, nil
}

func equalDigest(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for i := range left {
		difference |= left[i] ^ right[i]
	}
	return difference == 0
}

func canonicalCommandUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func validCommandTime(now time.Time) bool {
	return !now.IsZero() && now.Year() >= 1970 && now.Year() <= 9999 && now.Location() == time.UTC
}

func (s *SQLiteStore) AcceptDeploymentCommand(ctx context.Context, command DeploymentCommand) (*DeploymentAcceptance, error) {
	if command.AppName == "" || !canonicalCommandUUID(command.ExpectedAppID) ||
		!canonicalCommandUUID(command.NewDeploymentID) || command.ExpectedGeneration <= 0 ||
		!validCommandTime(command.Now) ||
		!command.ConvergenceDeadline.After(command.Now) ||
		(command.Operation != DeploymentOperationDeploy && command.Operation != DeploymentOperationRollback) {
		return nil, fmt.Errorf("%w: invalid deployment command", ErrInvalidData)
	}
	if command.Operation == DeploymentOperationDeploy && (command.Image == "" || command.RollbackTargetID != "") {
		return nil, fmt.Errorf("%w: invalid deploy command", ErrInvalidData)
	}
	if command.Operation == DeploymentOperationRollback && !canonicalCommandUUID(command.RollbackTargetID) {
		return nil, fmt.Errorf("%w: invalid rollback command", ErrInvalidData)
	}
	command.Now = command.Now.Truncate(time.Microsecond)
	expiry := command.Now.Add(24 * time.Hour)
	if expiry.Year() > 9999 {
		return nil, fmt.Errorf("%w: invalid deployment expiry", ErrInvalidData)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	application, err := scanApplication(tx.QueryRowContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE name = ?`, command.AppName))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if application.ID != command.ExpectedAppID {
		return nil, ErrGenerationConflict
	}
	replayed, err := lookupDeploymentCommand(ctx, tx, application.ID, command.KeyDigest, command.Fingerprint, command.Now.UnixMicro())
	if err != nil || replayed != nil {
		return replayed, err
	}
	if application.DesiredGeneration != command.ExpectedGeneration {
		return nil, ErrGenerationConflict
	}
	if application.DesiredGeneration == math.MaxInt64 {
		return nil, fmt.Errorf("%w: generation overflow", ErrInvalidData)
	}
	if application.DeletionTimestamp != nil {
		return nil, fmt.Errorf("%w: application is deleting", ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM deployment_idempotency WHERE app_id = ? AND expires_at <= ?`, application.ID, command.Now.UnixMicro()); err != nil {
		return nil, err
	}
	var liveCount int
	var earliest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), MIN(expires_at) FROM deployment_idempotency WHERE app_id = ?`, application.ID).
		Scan(&liveCount, &earliest); err != nil {
		return nil, err
	}
	if liveCount >= 1024 {
		return &DeploymentAcceptance{EarliestExpiryUTC: time.UnixMicro(earliest.Int64).UTC()}, ErrIdempotencyCapacity
	}
	image := command.Image
	var rollbackSource *string
	if command.Operation == DeploymentOperationRollback {
		target, err := scanDeployment(tx.QueryRowContext(ctx,
			`SELECT `+deploymentColumns+` FROM deployments WHERE id = ? AND app_id = ?`,
			command.RollbackTargetID, application.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if (target.Status != DeploymentStatusSucceeded && target.Status != DeploymentStatusSuccess) || !immutableCommandImage(target.ResolvedImage) {
			return nil, fmt.Errorf("%w: invalid rollback target", ErrConflict)
		}
		image = target.ResolvedImage
		rollbackSource = &command.RollbackTargetID
		if command.Image != "" && command.Image != image {
			return nil, fmt.Errorf("%w: rollback target changed", ErrConflict)
		}
	}
	application.DesiredGeneration++
	application.Image = image
	application.Status = AppStatusUpdating
	application.ObservedState = ObservedStatePending
	application.UpdatedAt = command.Now
	application.LastTransitionAt = &command.Now
	if err := updateApplicationTx(ctx, tx, application); err != nil {
		return nil, err
	}
	if err := terminalizeOvertakenDeploymentsTx(ctx, tx, application.ID, application.DesiredGeneration, application.ObservedGeneration, command.Now); err != nil {
		return nil, err
	}
	deadline := command.ConvergenceDeadline
	deployment := &Deployment{
		ID: command.NewDeploymentID, AppID: application.ID, SourceType: SourceTypeImage,
		Image: image, Status: DeploymentStatusPending, TriggeredBy: TriggeredByAPI,
		CreatedAt: command.Now, TargetGeneration: application.DesiredGeneration,
		PreviousObservedImage: application.ObservedImage, ConvergenceDeadline: &deadline,
		RollbackSourceDeploymentID: rollbackSource,
	}
	if err := insertDeploymentTx(ctx, tx, deployment); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_idempotency
		(app_id, key_digest, fingerprint, deployment_id, accepted_generation, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, application.ID, command.KeyDigest[:], command.Fingerprint[:],
		deployment.ID, deployment.TargetGeneration, command.Now.UnixMicro(), expiry.UnixMicro()); err != nil {
		return nil, err
	}
	accepted := &DeploymentAcceptance{AppID: application.ID, DeploymentID: deployment.ID, TargetGeneration: deployment.TargetGeneration}
	commit := tx.Commit
	if s.deploymentCommit != nil {
		commit = func() error { return s.deploymentCommit(tx) }
	}
	if err := commit(); err != nil {
		// Commit may have succeeded despite a lost acknowledgment. Never issue a
		// fresh intent until the durable key has been checked independently.
		_ = tx.Rollback()
		verifyCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		verified, lookupErr := s.LookupDeploymentCommand(verifyCtx, application.ID, command.KeyDigest, command.Fingerprint, command.Now)
		if lookupErr == nil && verified != nil {
			verified.Replayed = false
			return verified, nil
		}
		return nil, fmt.Errorf("%w: %v", ErrCommitOutcomeUnknown, err)
	}
	return accepted, nil
}

func immutableCommandImage(image string) bool {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return false
	}
	_, ok := named.(reference.Digested)
	return ok
}
