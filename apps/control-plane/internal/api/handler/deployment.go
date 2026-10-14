package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

var (
	ErrMissingDeploymentService = errors.New("deployment service is required")
	ErrMissingDeploymentLogger  = errors.New("deployment logger is required")
)

type DeploymentService interface {
	AcceptDeployment(context.Context, app.DeploymentMutationRequest) (*store.DeploymentAcceptance, error)
	ListDeploymentPage(context.Context, string, int, string) (*app.DeploymentPage, error)
	GetDeploymentForApp(context.Context, string, string) (*store.Deployment, error)
}

type DeploymentHandler struct {
	service DeploymentService
	log     *slog.Logger
	clock   func() time.Time
}

func NewDeploymentHandler(service DeploymentService, log *slog.Logger, clock func() time.Time) (*DeploymentHandler, error) {
	if isNilInterface(service) {
		return nil, ErrMissingDeploymentService
	}
	if log == nil {
		return nil, ErrMissingDeploymentLogger
	}
	if clock == nil {
		return nil, errors.New("deployment clock is required")
	}
	return &DeploymentHandler{service: service, log: log, clock: clock}, nil
}

func (h *DeploymentHandler) Deploy(c fiber.Ctx) error {
	name, expectedID, generation, key, ok, err := deploymentMutationInputs(c)
	if !ok {
		return err
	}
	var body DeployApplicationRequest
	if err := decodeStrictJSON(c, &body); err != nil {
		return writeJSONError(c, err)
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(c.Body(), &rawFields); err != nil {
		return writeJSONError(c, err)
	}
	for field := range rawFields {
		if field != "image" {
			return apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "request body contains an unsupported field", apiresponse.ErrorOptions{})
		}
	}
	if !validImageReference(body.Image) {
		return apiresponse.WriteError(c, fiber.StatusUnprocessableEntity, "validation_failed", "request validation failed",
			apiresponse.ErrorOptions{Fields: map[string]string{"image": "must be a valid container image reference"}})
	}
	accepted, err := h.service.AcceptDeployment(c.RequestCtx(), app.DeploymentMutationRequest{
		AppName: name, ExpectedAppID: expectedID, ExpectedGeneration: generation,
		IdempotencyKey: key, Operation: store.DeploymentOperationDeploy, Image: body.Image,
	})
	if err != nil {
		return h.writeServiceError(c, "deploy_application", accepted, err)
	}
	return writeDeploymentAcceptance(c, name, expectedID, accepted)
}

func (h *DeploymentHandler) Rollback(c fiber.Ctx) error {
	name, expectedID, generation, key, ok, err := deploymentMutationInputs(c)
	if !ok {
		return err
	}
	targetID := c.Params("id")
	if !canonicalDeploymentID(targetID) {
		return writeInvalidPath(c, "id")
	}
	if len(c.Body()) != 0 {
		return apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "request body must be empty", apiresponse.ErrorOptions{})
	}
	accepted, err := h.service.AcceptDeployment(c.RequestCtx(), app.DeploymentMutationRequest{
		AppName: name, ExpectedAppID: expectedID, ExpectedGeneration: generation,
		IdempotencyKey: key, Operation: store.DeploymentOperationRollback, RollbackTargetID: targetID,
	})
	if err != nil {
		return h.writeServiceError(c, "rollback_deployment", accepted, err)
	}
	return writeDeploymentAcceptance(c, name, expectedID, accepted)
}

func (h *DeploymentHandler) List(c fiber.Ctx) error {
	name, ok := validApplicationName(c.Params("name"))
	if !ok {
		return writeInvalidPath(c, "name")
	}
	if len(c.Body()) != 0 {
		return apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "request body must be empty", apiresponse.ErrorOptions{})
	}
	limit, cursor, err := deploymentPageQuery(c)
	if err != nil {
		return apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "query parameter is malformed", apiresponse.ErrorOptions{})
	}
	page, err := h.service.ListDeploymentPage(c.RequestCtx(), name, limit, cursor)
	if err != nil {
		return h.writeServiceError(c, "list_deployments", nil, err)
	}
	if page == nil {
		return h.writeServiceError(c, "list_deployments_response", nil, errInvalidPublicState)
	}
	response, err := mapDeploymentPage(page.Deployments, limit, page.NextCursor)
	if err != nil {
		return h.writeServiceError(c, "list_deployments_response", nil, err)
	}
	return c.JSON(response)
}

func (h *DeploymentHandler) Get(c fiber.Ctx) error {
	name, ok := validApplicationName(c.Params("name"))
	if !ok {
		return writeInvalidPath(c, "name")
	}
	id := c.Params("id")
	if !canonicalDeploymentID(id) {
		return writeInvalidPath(c, "id")
	}
	if !emptyDeploymentRead(c) {
		return apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "request body or query must be empty", apiresponse.ErrorOptions{})
	}
	deployment, err := h.service.GetDeploymentForApp(c.RequestCtx(), name, id)
	if err != nil {
		return h.writeServiceError(c, "get_deployment", nil, err)
	}
	if deployment == nil || deployment.ID != id {
		return h.writeServiceError(c, "get_deployment_response", nil, errInvalidPublicState)
	}
	response, err := mapDeploymentResponse(deployment)
	if err != nil {
		return h.writeServiceError(c, "get_deployment_response", nil, err)
	}
	return c.JSON(response)
}

func deploymentMutationInputs(c fiber.Ctx) (name, id string, generation int64, key string, ok bool, err error) {
	name, ok = validApplicationName(c.Params("name"))
	if !ok {
		return "", "", 0, "", false, writeInvalidPath(c, "name")
	}
	if len(c.Request().URI().QueryString()) != 0 {
		return "", "", 0, "", false, apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "query parameters are not supported", apiresponse.ErrorOptions{})
	}
	id, generation, err = expectedApplicationETag(c)
	if err != nil {
		return "", "", 0, "", false, writeExpectedGenerationError(c, err)
	}
	values := c.Request().Header.PeekAll("Idempotency-Key")
	if len(values) != 1 || !validDeploymentKey(string(values[0])) {
		return "", "", 0, "", false, apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "Idempotency-Key header is malformed",
			apiresponse.ErrorOptions{Fields: map[string]string{"Idempotency-Key": "must be one 16 to 128 character token"}})
	}
	return name, id, generation, string(values[0]), true, nil
}

func validDeploymentKey(key string) bool {
	if len(key) < 16 || len(key) > 128 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

func deploymentPageQuery(c fiber.Ctx) (int, string, error) {
	raw := string(c.Request().URI().QueryString())
	values, err := url.ParseQuery(raw)
	if err != nil {
		return 0, "", err
	}
	for name, items := range values {
		if (name != "limit" && name != "cursor") || len(items) != 1 || items[0] == "" {
			return 0, "", errors.New("invalid deployment query")
		}
	}
	limit := 25
	if items, ok := values["limit"]; ok {
		for _, character := range items[0] {
			if character < '0' || character > '9' {
				return 0, "", errors.New("invalid page limit")
			}
		}
		limit, err = strconv.Atoi(items[0])
		if err != nil || limit < 1 || limit > 100 {
			return 0, "", errors.New("invalid page limit")
		}
	}
	cursor := ""
	if items, ok := values["cursor"]; ok {
		cursor = items[0]
		if len(cursor) > 256 {
			return 0, "", errors.New("invalid cursor")
		}
	}
	return limit, cursor, nil
}

func emptyDeploymentRead(c fiber.Ctx) bool {
	return len(c.Body()) == 0 && len(c.Request().URI().QueryString()) == 0
}

func writeDeploymentAcceptance(c fiber.Ctx, name, expectedID string, accepted *store.DeploymentAcceptance) error {
	if accepted == nil || accepted.AppID != expectedID || !canonicalDeploymentID(accepted.AppID) ||
		!canonicalDeploymentID(accepted.DeploymentID) || accepted.TargetGeneration <= 0 {
		return fmt.Errorf("%w: invalid deployment acceptance", errInvalidPublicState)
	}
	c.Location("/api/v1/apps/" + name + "/deployments/" + accepted.DeploymentID)
	setApplicationETag(c, accepted.AppID, accepted.TargetGeneration)
	return c.Status(fiber.StatusAccepted).JSON(DeploymentAcceptedResponse{
		DeploymentID: accepted.DeploymentID, Application: name,
		TargetGeneration: accepted.TargetGeneration, Status: "accepted",
	})
}

func (h *DeploymentHandler) writeServiceError(c fiber.Ctx, operation string, accepted *store.DeploymentAcceptance, err error) error {
	status, code, message := fiber.StatusInternalServerError, "internal_error", "internal server error"
	options := apiresponse.ErrorOptions{}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), errors.Is(err, store.ErrCommitOutcomeUnknown):
		status, code, message = fiber.StatusServiceUnavailable, "dependency_unavailable", "service is temporarily unavailable"
		options.Retryable = true
	case errors.Is(err, store.ErrNotFound):
		status, code, message = fiber.StatusNotFound, "not_found", "resource not found"
	case errors.Is(err, app.ErrInvalidDeploymentCursor) && operation == "list_deployments":
		status, code, message = fiber.StatusBadRequest, "invalid_request", "query parameter is malformed"
	case errors.Is(err, store.ErrIdempotencyConflict), errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrInvalidTransition):
		status, code, message = fiber.StatusConflict, "conflict", "request conflicts with current resource state"
	case errors.Is(err, store.ErrGenerationConflict):
		status, code, message = fiber.StatusPreconditionFailed, "precondition_failed", "If-Match does not match the current application generation"
	case errors.Is(err, store.ErrIdempotencyCapacity):
		status, code, message = fiber.StatusTooManyRequests, "rate_limited", "idempotency capacity reached"
		options.Retryable = true
		seconds := 1
		if accepted != nil {
			seconds = deploymentRetryAfter(h.clock(), accepted.EarliestExpiryUTC)
		}
		c.Set(fiber.HeaderRetryAfter, strconv.Itoa(seconds))
	}
	if status >= fiber.StatusInternalServerError {
		h.log.Error("deployment API operation failed",
			"request_id", apiresponse.RequestID(c), "operation", operation,
			"status", status, "error_type", fmt.Sprintf("%T", err))
	}
	return apiresponse.WriteError(c, status, code, message, options)
}

func deploymentRetryAfter(now, expiry time.Time) int {
	if expiry.IsZero() {
		return 1
	}
	seconds := math.Ceil(expiry.Sub(now).Seconds())
	if seconds < 1 {
		return 1
	}
	if seconds > 86400 {
		return 86400
	}
	return int(seconds)
}
