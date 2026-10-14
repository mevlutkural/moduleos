package handler

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

type DeployApplicationRequest struct {
	Image string `json:"image"`
}

type DeploymentAcceptedResponse struct {
	DeploymentID     string `json:"deployment_id"`
	Application      string `json:"application"`
	TargetGeneration int64  `json:"target_generation"`
	Status           string `json:"status"`
}

type DeploymentResponse struct {
	ID                         string                 `json:"id"`
	SourceType                 store.SourceType       `json:"source_type"`
	Image                      string                 `json:"image,omitempty"`
	ResolvedImage              string                 `json:"resolved_image,omitempty"`
	Status                     store.DeploymentStatus `json:"status"`
	TriggeredBy                store.TriggeredBy      `json:"triggered_by"`
	TargetGeneration           int64                  `json:"target_generation"`
	CreatedAt                  time.Time              `json:"created_at"`
	StartedAt                  *time.Time             `json:"started_at,omitempty"`
	FinishedAt                 *time.Time             `json:"finished_at,omitempty"`
	RollbackSourceDeploymentID *string                `json:"rollback_source_deployment_id,omitempty"`
	FailureCode                string                 `json:"failure_code,omitempty"`
}

type DeploymentCollectionResponse struct {
	Deployments []DeploymentResponse `json:"deployments"`
	Limit       int                  `json:"limit"`
	NextCursor  *string              `json:"next_cursor"`
}

var publicDeploymentFailureCodes = map[string]struct{}{
	"image_not_found":         {},
	"invalid_image":           {},
	"published_port_conflict": {},
	"convergence_timeout":     {},
	"rollout_paused":          {},
	"task_rejected":           {},
}

func mapDeploymentResponse(deployment *store.Deployment) (DeploymentResponse, error) {
	if deployment == nil || !canonicalUUID(deployment.ID) || !canonicalUUID(deployment.AppID) ||
		(deployment.SourceType != store.SourceTypeImage && deployment.SourceType != store.SourceTypeTar) ||
		!knownDeploymentStatus(deployment.Status) || !knownDeploymentTrigger(deployment.TriggeredBy) ||
		deployment.TargetGeneration < 0 || deployment.CreatedAt.IsZero() ||
		!validOptionalTime(deployment.StartedAt) || !validOptionalTime(deployment.FinishedAt) ||
		(deployment.StartedAt != nil && deployment.StartedAt.Before(deployment.CreatedAt)) ||
		(deployment.FinishedAt != nil && deployment.FinishedAt.Before(deployment.CreatedAt)) ||
		(deployment.StartedAt != nil && deployment.FinishedAt != nil && deployment.FinishedAt.Before(*deployment.StartedAt)) ||
		(deployment.RollbackSourceDeploymentID != nil && !canonicalUUID(*deployment.RollbackSourceDeploymentID)) ||
		(deployment.ResolvedImage != "" && (!validPersistedImageReference(deployment.ResolvedImage) ||
			!swarm.IsImmutableImageReference(deployment.ResolvedImage))) {
		return DeploymentResponse{}, errInvalidPublicState
	}
	if deployment.SourceType == store.SourceTypeImage && deployment.Image != "" && !validPersistedImageReference(deployment.Image) {
		return DeploymentResponse{}, errInvalidPublicState
	}
	response := DeploymentResponse{
		ID: deployment.ID, SourceType: deployment.SourceType, Status: deployment.Status,
		TriggeredBy: deployment.TriggeredBy, TargetGeneration: deployment.TargetGeneration,
		CreatedAt:                  deployment.CreatedAt.UTC(),
		StartedAt:                  utcTimePointer(deployment.StartedAt),
		FinishedAt:                 utcTimePointer(deployment.FinishedAt),
		RollbackSourceDeploymentID: deployment.RollbackSourceDeploymentID,
		ResolvedImage:              deployment.ResolvedImage,
	}
	if deployment.SourceType == store.SourceTypeImage {
		response.Image = deployment.Image
	}
	if deployment.Status == store.DeploymentStatusFailed && deployment.ErrorCode != "" {
		if _, ok := publicDeploymentFailureCodes[deployment.ErrorCode]; ok {
			response.FailureCode = deployment.ErrorCode
		} else {
			response.FailureCode = "deployment_failed"
		}
	}
	return response, nil
}

func mapDeploymentPage(deployments []*store.Deployment, limit int, nextCursor string) (DeploymentCollectionResponse, error) {
	if limit < 1 || limit > 100 || len(deployments) > limit {
		return DeploymentCollectionResponse{}, errInvalidPublicState
	}
	response := DeploymentCollectionResponse{Deployments: make([]DeploymentResponse, 0, len(deployments)), Limit: limit}
	appID := ""
	for _, deployment := range deployments {
		mapped, err := mapDeploymentResponse(deployment)
		if err != nil {
			return DeploymentCollectionResponse{}, err
		}
		if appID == "" {
			appID = deployment.AppID
		} else if deployment.AppID != appID {
			return DeploymentCollectionResponse{}, errInvalidPublicState
		}
		response.Deployments = append(response.Deployments, mapped)
	}
	if nextCursor != "" {
		if appID == "" || len(nextCursor) > 256 {
			return DeploymentCollectionResponse{}, errInvalidPublicState
		}
		payload, err := base64.RawURLEncoding.DecodeString(nextCursor)
		if err != nil || base64.RawURLEncoding.EncodeToString(payload) != nextCursor {
			return DeploymentCollectionResponse{}, errInvalidPublicState
		}
		parts := strings.Split(string(payload), ":")
		if len(parts) != 3 || parts[0] != "v1" || parts[1] != appID {
			return DeploymentCollectionResponse{}, errInvalidPublicState
		}
		sequence, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || sequence <= 0 || strconv.FormatInt(sequence, 10) != parts[2] {
			return DeploymentCollectionResponse{}, errInvalidPublicState
		}
		response.NextCursor = &nextCursor
	}
	return response, nil
}

func knownDeploymentStatus(status store.DeploymentStatus) bool {
	switch status {
	case store.DeploymentStatusPending, store.DeploymentStatusBuilding,
		store.DeploymentStatusInProgress, store.DeploymentStatusSuccess,
		store.DeploymentStatusFailed, store.DeploymentStatusSucceeded,
		store.DeploymentStatusApplying, store.DeploymentStatusSuperseded:
		return true
	default:
		return false
	}
}

func knownDeploymentTrigger(trigger store.TriggeredBy) bool {
	return trigger == store.TriggeredByAPI || trigger == store.TriggeredByReconciler || trigger == store.TriggeredByEvent
}

func canonicalDeploymentID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}
