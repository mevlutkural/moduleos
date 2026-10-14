package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const qualifiedDeploymentColumns = `d.id, d.app_id, d.source_type, d.image,
	d.resolved_image, d.status, d.triggered_by, d.error_message, d.created_at,
	d.finished_at, d.target_generation, d.previous_observed_image,
	d.error_code, d.started_at, d.convergence_deadline,
	d.rollback_source_deployment_id`

func (s *SQLiteStore) ListDeploymentPage(ctx context.Context, appID string, beforeSequence int64, limit int) ([]DeploymentPageRow, error) {
	if appID == "" || beforeSequence < 0 || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: invalid deployment page", ErrInvalidData)
	}
	query := `SELECT ` + qualifiedDeploymentColumns + `, o.sequence
		FROM deployment_order o
		JOIN deployments d ON d.id = o.deployment_id AND d.app_id = o.app_id
		WHERE o.app_id = ?`
	arguments := []any{appID}
	if beforeSequence > 0 {
		query += ` AND o.sequence < ?`
		arguments = append(arguments, beforeSequence)
	}
	query += ` ORDER BY o.sequence DESC LIMIT ?`
	arguments = append(arguments, limit+1)
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]DeploymentPageRow, 0, limit+1)
	for rows.Next() {
		var item DeploymentPageRow
		item.Deployment = &Deployment{}
		d := item.Deployment
		if err := rows.Scan(&d.ID, &d.AppID, &d.SourceType, &d.Image,
			&d.ResolvedImage, &d.Status, &d.TriggeredBy, &d.ErrorMessage,
			&d.CreatedAt, &d.FinishedAt, &d.TargetGeneration,
			&d.PreviousObservedImage, &d.ErrorCode, &d.StartedAt,
			&d.ConvergenceDeadline, &d.RollbackSourceDeploymentID,
			&item.Sequence); err != nil {
			return nil, err
		}
		if item.Sequence <= 0 || d.AppID != appID {
			return nil, fmt.Errorf("%w: deployment order mismatch", ErrInvalidData)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *SQLiteStore) GetDeploymentForApp(ctx context.Context, appID, deploymentID string) (*Deployment, error) {
	if appID == "" || deploymentID == "" {
		return nil, fmt.Errorf("%w: deployment identity required", ErrInvalidData)
	}
	d, err := scanDeployment(s.db.QueryRowContext(ctx,
		`SELECT `+deploymentColumns+` FROM deployments WHERE app_id = ? AND id = ?`,
		appID, deploymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}
