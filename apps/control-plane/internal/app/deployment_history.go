package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

var ErrInvalidDeploymentCursor = errors.New("invalid deployment cursor")

type DeploymentPage struct {
	Deployments []*store.Deployment
	NextCursor  string
}

func (s *Service) ListDeploymentPage(ctx context.Context, appName string, limit int, cursor string) (*DeploymentPage, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("%w: page limit out of range", store.ErrInvalidData)
	}
	application, err := s.store.GetApplication(ctx, appName)
	if err != nil {
		return nil, err
	}
	before, err := decodeDeploymentCursor(cursor, application.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListDeploymentPage(ctx, application.ID, before, limit)
	if err != nil {
		return nil, err
	}
	current, err := s.store.GetApplication(ctx, appName)
	if err != nil {
		return nil, err
	}
	if current.ID != application.ID {
		return nil, store.ErrNotFound
	}
	page := &DeploymentPage{Deployments: make([]*store.Deployment, 0, limit)}
	for i := 0; i < len(rows) && i < limit; i++ {
		page.Deployments = append(page.Deployments, rows[i].Deployment)
	}
	if len(rows) > limit {
		page.NextCursor = encodeDeploymentCursor(application.ID, rows[limit-1].Sequence)
	}
	return page, nil
}

func (s *Service) GetDeploymentForApp(ctx context.Context, appName, deploymentID string) (*store.Deployment, error) {
	if !canonicalDeploymentUUID(deploymentID) {
		return nil, fmt.Errorf("%w: invalid deployment ID", store.ErrInvalidData)
	}
	application, err := s.store.GetApplication(ctx, appName)
	if err != nil {
		return nil, err
	}
	deployment, err := s.store.GetDeploymentForApp(ctx, application.ID, deploymentID)
	if err != nil {
		return nil, err
	}
	current, err := s.store.GetApplication(ctx, appName)
	if err != nil {
		return nil, err
	}
	if current.ID != application.ID {
		return nil, store.ErrNotFound
	}
	return deployment, nil
}

func canonicalDeploymentUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func encodeDeploymentCursor(appID string, sequence int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte("v1:" + appID + ":" + strconv.FormatInt(sequence, 10)))
}

func decodeDeploymentCursor(cursor, appID string) (int64, error) {
	if !canonicalDeploymentUUID(appID) {
		return 0, fmt.Errorf("%w: invalid application ID", store.ErrInvalidData)
	}
	if cursor == "" {
		return 0, nil
	}
	if len(cursor) > 256 {
		return 0, fmt.Errorf("%w: cursor too long", ErrInvalidDeploymentCursor)
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != cursor {
		return 0, fmt.Errorf("%w: malformed cursor", ErrInvalidDeploymentCursor)
	}
	prefix := "v1:" + appID + ":"
	if !strings.HasPrefix(string(payload), prefix) {
		return 0, fmt.Errorf("%w: cursor application or version mismatch", ErrInvalidDeploymentCursor)
	}
	value := strings.TrimPrefix(string(payload), prefix)
	sequence, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sequence <= 0 || strconv.FormatInt(sequence, 10) != value {
		return 0, fmt.Errorf("%w: invalid cursor sequence", ErrInvalidDeploymentCursor)
	}
	return sequence, nil
}
