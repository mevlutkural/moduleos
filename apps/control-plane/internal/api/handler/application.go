package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

var (
	ErrMissingApplicationService = errors.New("application service is required")
	ErrMissingApplicationLogger  = errors.New("application logger is required")
)

type ApplicationService interface {
	CreateApp(context.Context, app.CreateAppRequest) (*store.Application, error)
	ListApps(context.Context) ([]*store.Application, error)
	GetApp(context.Context, string) (*store.Application, error)
	UpdateApp(context.Context, app.UpdateAppRequest) (*store.Application, error)
	DeleteAppIntent(context.Context, string, int64) (*store.Application, error)
	SetRunState(context.Context, string, store.DesiredRunState, int64) (*store.Application, error)
	ScaleAppIntent(context.Context, string, int, int64) (*store.Application, error)
}

type ApplicationHandler struct {
	service ApplicationService
	log     *slog.Logger
}

func NewApplicationHandler(service ApplicationService, log *slog.Logger) (*ApplicationHandler, error) {
	if isNilInterface(service) {
		return nil, ErrMissingApplicationService
	}
	if log == nil {
		return nil, ErrMissingApplicationLogger
	}
	return &ApplicationHandler{service: service, log: log}, nil
}

func (h *ApplicationHandler) Create(c fiber.Ctx) error {
	var request CreateApplicationRequest
	if err := decodeStrictJSON(c, &request); err != nil {
		return writeJSONError(c, err)
	}
	if fields := validateCreateApplicationRequest(request); len(fields) > 0 {
		return apiresponse.WriteError(c, fiber.StatusUnprocessableEntity, "validation_failed", "request validation failed", apiresponse.ErrorOptions{Fields: fields})
	}

	replicas := 1
	if request.Replicas.Present {
		replicas = request.Replicas.Value
	}
	created, err := h.service.CreateApp(c.RequestCtx(), app.CreateAppRequest{
		Name:                 request.Name,
		ProjectSlug:          request.ProjectSlug.Value,
		Image:                request.Image,
		Replicas:             replicas,
		EnvVars:              applicationEnvironment(request.EnvVars.Value),
		Ports:                applicationPorts(request.Ports.Value),
		Volumes:              applicationVolumes(request.Volumes.Value),
		Expose:               request.Expose.Value,
		IngressContainerPort: request.IngressContainerPort.Value,
		SourceType:           store.SourceTypeImage,
	})
	if err != nil {
		return h.writeServiceError(c, "create_application", err)
	}
	if created == nil || created.Name != request.Name {
		return h.writeServiceError(c, "create_application_response", errInvalidPublicState)
	}
	response, err := mapApplicationResponse(created)
	if err != nil {
		return h.writeServiceError(c, "create_application_response", err)
	}
	c.Location("/api/v1/apps/" + response.Name)
	setApplicationETag(c, response.DesiredGeneration)
	return c.Status(fiber.StatusCreated).JSON(response)
}

func (h *ApplicationHandler) List(c fiber.Ctx) error {
	applications, err := h.service.ListApps(c.RequestCtx())
	if err != nil {
		return h.writeServiceError(c, "list_applications", err)
	}
	response, err := mapApplicationCollection(applications)
	if err != nil {
		return h.writeServiceError(c, "list_applications_response", err)
	}
	return c.JSON(response)
}

func (h *ApplicationHandler) Get(c fiber.Ctx) error {
	name, ok := validApplicationName(c.Params("name"))
	if !ok {
		return writeInvalidPath(c, "name")
	}
	application, err := h.service.GetApp(c.RequestCtx(), name)
	if err != nil {
		return h.writeServiceError(c, "get_application", err)
	}
	if application == nil || application.Name != name {
		return h.writeServiceError(c, "get_application_response", errInvalidPublicState)
	}
	response, err := mapApplicationResponse(application)
	if err != nil {
		return h.writeServiceError(c, "get_application_response", err)
	}
	setApplicationETag(c, response.DesiredGeneration)
	return c.JSON(response)
}

func (h *ApplicationHandler) Update(c fiber.Ctx) error {
	name, ok := validApplicationName(c.Params("name"))
	if !ok {
		return writeInvalidPath(c, "name")
	}
	expected, err := readExpectedGeneration(c)
	if err != nil {
		return writeExpectedGenerationError(c, err)
	}
	var request UpdateApplicationRequest
	if err := decodeStrictJSON(c, &request); err != nil {
		return writeJSONError(c, err)
	}
	if fields := validateUpdateApplicationRequest(request); len(fields) > 0 {
		return apiresponse.WriteError(c, fiber.StatusUnprocessableEntity, "validation_failed", "request validation failed", apiresponse.ErrorOptions{Fields: fields})
	}

	serviceRequest := app.UpdateAppRequest{AppName: name, ExpectedGeneration: expected}
	if request.EnvVars.Present {
		serviceRequest.EnvVars = applicationEnvironment(request.EnvVars.Value)
	}
	if request.Ports.Present {
		ports := applicationPorts(request.Ports.Value)
		serviceRequest.Ports = &ports
	}
	if request.Volumes.Present {
		volumes := applicationVolumes(request.Volumes.Value)
		serviceRequest.Volumes = &volumes
	}
	if request.Expose.Present {
		serviceRequest.Expose = &request.Expose.Value
	}
	if request.IngressContainerPort.Present {
		serviceRequest.IngressContainerPort = &request.IngressContainerPort.Value
	}

	updated, err := h.service.UpdateApp(c.RequestCtx(), serviceRequest)
	if err != nil {
		return h.writeServiceError(c, "update_application", err)
	}
	if updated == nil || updated.Name != name {
		return h.writeServiceError(c, "update_application_response", errInvalidPublicState)
	}
	response, err := mapApplicationResponse(updated)
	if err != nil {
		return h.writeServiceError(c, "update_application_response", err)
	}
	setApplicationETag(c, response.DesiredGeneration)
	return c.Status(fiber.StatusAccepted).JSON(response)
}

func (h *ApplicationHandler) Delete(c fiber.Ctx) error {
	return h.mutateWithoutBody(c, "delete", func(ctx context.Context, name string, generation int64) (*store.Application, error) {
		return h.service.DeleteAppIntent(ctx, name, generation)
	})
}

func (h *ApplicationHandler) Start(c fiber.Ctx) error {
	return h.mutateWithoutBody(c, "start", func(ctx context.Context, name string, generation int64) (*store.Application, error) {
		return h.service.SetRunState(ctx, name, store.DesiredRunStateRunning, generation)
	})
}

func (h *ApplicationHandler) Stop(c fiber.Ctx) error {
	return h.mutateWithoutBody(c, "stop", func(ctx context.Context, name string, generation int64) (*store.Application, error) {
		return h.service.SetRunState(ctx, name, store.DesiredRunStateStopped, generation)
	})
}

func (h *ApplicationHandler) Scale(c fiber.Ctx) error {
	name, ok := validApplicationName(c.Params("name"))
	if !ok {
		return writeInvalidPath(c, "name")
	}
	expected, err := readExpectedGeneration(c)
	if err != nil {
		return writeExpectedGenerationError(c, err)
	}
	var request ScaleApplicationRequest
	if err := decodeStrictJSON(c, &request); err != nil {
		return writeJSONError(c, err)
	}
	if !request.Replicas.Present || request.Replicas.Null {
		return apiresponse.WriteError(c, fiber.StatusUnprocessableEntity, "validation_failed", "request validation failed", apiresponse.ErrorOptions{Fields: map[string]string{"replicas": "is required"}})
	}
	if request.Replicas.Value < 0 {
		return apiresponse.WriteError(c, fiber.StatusUnprocessableEntity, "validation_failed", "request validation failed", apiresponse.ErrorOptions{Fields: map[string]string{"replicas": "must be non-negative"}})
	}
	application, err := h.service.ScaleAppIntent(c.RequestCtx(), name, request.Replicas.Value, expected)
	if err != nil {
		return h.writeServiceError(c, "scale_application", err)
	}
	return writeApplicationOperation(c, "scale", name, application)
}

func (h *ApplicationHandler) mutateWithoutBody(c fiber.Ctx, operation string, mutation func(context.Context, string, int64) (*store.Application, error)) error {
	name, ok := validApplicationName(c.Params("name"))
	if !ok {
		return writeInvalidPath(c, "name")
	}
	if len(c.Body()) != 0 {
		return apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "request body must be empty", apiresponse.ErrorOptions{})
	}
	expected, err := readExpectedGeneration(c)
	if err != nil {
		return writeExpectedGenerationError(c, err)
	}
	application, err := mutation(c.RequestCtx(), name, expected)
	if err != nil {
		return h.writeServiceError(c, operation+"_application", err)
	}
	return writeApplicationOperation(c, operation, name, application)
}

func writeApplicationOperation(c fiber.Ctx, operation, expectedName string, application *store.Application) error {
	if application == nil || application.Name != expectedName || !publicApplicationName.MatchString(application.Name) || application.DesiredGeneration < 1 {
		return fmt.Errorf("%w: invalid application operation result", errInvalidPublicState)
	}
	setApplicationETag(c, application.DesiredGeneration)
	return c.Status(fiber.StatusAccepted).JSON(ApplicationOperationResponse{
		Operation: operation, Resource: application.Name, Status: "accepted", Generation: application.DesiredGeneration,
	})
}

func readExpectedGeneration(c fiber.Ctx) (int64, error) {
	return expectedGeneration(c)
}

func writeExpectedGenerationError(c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, errMissingIfMatch):
		return apiresponse.WriteError(c, fiber.StatusPreconditionRequired, "precondition_required", "If-Match header is required", apiresponse.ErrorOptions{})
	default:
		return apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "If-Match header is malformed", apiresponse.ErrorOptions{Fields: map[string]string{"If-Match": "must contain one strong application generation ETag"}})
	}
}

func setApplicationETag(c fiber.Ctx, generation int64) {
	c.Set(fiber.HeaderETag, generationETag(generation))
}

func validateCreateApplicationRequest(request CreateApplicationRequest) map[string]string {
	fields := make(map[string]string)
	if _, ok := validApplicationName(request.Name); !ok {
		fields["name"] = "must be a canonical application name"
	}
	if request.ProjectSlug.Null {
		fields["project_slug"] = "must be a string"
	} else if request.ProjectSlug.Present {
		if _, ok := validProjectSlug(request.ProjectSlug.Value); !ok {
			fields["project_slug"] = "must be a canonical project slug"
		}
	}
	if !validImageReference(request.Image) {
		fields["image"] = "must be a valid container image reference"
	}
	if request.Replicas.Null || request.Replicas.Present && request.Replicas.Value < 1 {
		fields["replicas"] = "must be a positive integer when provided"
	}
	if request.EnvVars.Null {
		fields["env_vars"] = "must be an object"
	}
	if request.Ports.Null {
		fields["ports"] = "must be an array"
	}
	if request.Volumes.Null {
		fields["volumes"] = "must be an array"
	}
	if request.Expose.Null {
		fields["expose"] = "must be a boolean"
	}
	if request.IngressContainerPort.Null {
		fields["ingress_container_port"] = "must be an integer"
	} else if request.IngressContainerPort.Present && request.IngressContainerPort.Value > maximumApplicationPortNumber {
		fields["ingress_container_port"] = "must be between 0 and 65535"
	}
	if len(request.EnvVars.Value) > maximumApplicationEnvironmentEntries {
		fields["env_vars"] = "must contain at most 200 entries"
	} else if request.EnvVars.Present && !request.EnvVars.Null && !validApplicationEnvironmentInputs(request.EnvVars.Value) {
		fields["env_vars"] = "must contain valid keys and NUL-free values"
	}
	if len(request.Ports.Value) > maximumApplicationPorts {
		fields["ports"] = "must contain at most 20 entries"
	} else if request.Ports.Present && !request.Ports.Null && !validApplicationPortInputs(request.Ports.Value) {
		fields["ports"] = "must contain valid non-null port definitions"
	}
	if len(request.Volumes.Value) > maximumApplicationVolumes {
		fields["volumes"] = "must contain at most 20 entries"
	} else if request.Volumes.Present && !request.Volumes.Null && !validApplicationVolumeInputs(request.Volumes.Value) {
		fields["volumes"] = "must contain unique canonical absolute source and target paths"
	}
	return fields
}

func validateUpdateApplicationRequest(request UpdateApplicationRequest) map[string]string {
	fields := make(map[string]string)
	if !request.EnvVars.Present && !request.Ports.Present && !request.Volumes.Present && !request.Expose.Present && !request.IngressContainerPort.Present {
		fields["body"] = "must contain at least one mutable field"
	}
	if request.EnvVars.Null {
		fields["env_vars"] = "must be an object"
	}
	if request.Ports.Null {
		fields["ports"] = "must be an array"
	}
	if request.Volumes.Null {
		fields["volumes"] = "must be an array"
	}
	if request.Expose.Null {
		fields["expose"] = "must be a boolean"
	}
	if request.IngressContainerPort.Null {
		fields["ingress_container_port"] = "must be an integer"
	} else if request.IngressContainerPort.Present && request.IngressContainerPort.Value > maximumApplicationPortNumber {
		fields["ingress_container_port"] = "must be between 0 and 65535"
	}
	if len(request.EnvVars.Value) > maximumApplicationEnvironmentEntries {
		fields["env_vars"] = "must contain at most 200 entries"
	} else if request.EnvVars.Present && !request.EnvVars.Null && !validApplicationEnvironmentInputs(request.EnvVars.Value) {
		fields["env_vars"] = "must contain valid keys and NUL-free values"
	}
	if len(request.Ports.Value) > maximumApplicationPorts {
		fields["ports"] = "must contain at most 20 entries"
	} else if request.Ports.Present && !request.Ports.Null && !validApplicationPortInputs(request.Ports.Value) {
		fields["ports"] = "must contain valid non-null port definitions"
	}
	if len(request.Volumes.Value) > maximumApplicationVolumes {
		fields["volumes"] = "must contain at most 20 entries"
	} else if request.Volumes.Present && !request.Volumes.Null && !validApplicationVolumeInputs(request.Volumes.Value) {
		fields["volumes"] = "must contain unique canonical absolute source and target paths"
	}
	return fields
}

func validApplicationName(name string) (string, bool) {
	return name, publicApplicationName.MatchString(name)
}

func (h *ApplicationHandler) writeServiceError(c fiber.Ctx, operation string, err error) error {
	status := fiber.StatusInternalServerError
	code := "internal_error"
	message := "internal server error"
	options := apiresponse.ErrorOptions{}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status = fiber.StatusServiceUnavailable
		code = "dependency_unavailable"
		message = "service is temporarily unavailable"
		options.Retryable = true
	case errors.Is(err, store.ErrNotFound):
		status = fiber.StatusNotFound
		code = "not_found"
		message = "resource not found"
	case errors.Is(err, store.ErrGenerationConflict):
		status = fiber.StatusPreconditionFailed
		code = "precondition_failed"
		message = "If-Match does not match the current application generation"
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrInvalidTransition):
		status = fiber.StatusConflict
		code = "conflict"
		message = "request conflicts with current application state"
	case (errors.Is(err, store.ErrInvalidData) || errors.Is(err, app.ErrValidation)) && applicationInputOperation(operation):
		status = fiber.StatusUnprocessableEntity
		code = "validation_failed"
		message = "request validation failed"
	}
	if status >= fiber.StatusInternalServerError {
		h.log.Error("application API operation failed",
			"request_id", apiresponse.RequestID(c),
			"operation", operation,
			"status", status,
			"error_type", fmt.Sprintf("%T", err),
		)
	}
	return apiresponse.WriteError(c, status, code, message, options)
}

func applicationInputOperation(operation string) bool {
	return operation == "create_application" || operation == "update_application" || operation == "scale_application"
}
