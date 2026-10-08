package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/app"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

var (
	ErrMissingProjectService = errors.New("project service is required")
	ErrMissingProjectLogger  = errors.New("project logger is required")
	projectSlugPattern       = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

const maximumProjectNameLength = 120

type ProjectService interface {
	CreateProject(ctx context.Context, request app.CreateProjectRequest) (*store.Project, error)
	ListProjects(ctx context.Context) ([]*store.Project, error)
	GetProject(ctx context.Context, slug string) (*store.Project, error)
	DeleteProject(ctx context.Context, slug string) error
	ListProjectApps(ctx context.Context, slug string) ([]*store.Application, error)
	CreateProjectLink(ctx context.Context, projectSlug, targetAppName, alias string) (*app.ProjectLinkDetails, error)
	ListProjectLinks(ctx context.Context, projectSlug string) ([]*app.ProjectLinkDetails, error)
	DeleteProjectLink(ctx context.Context, projectSlug, linkID string) error
}

type ProjectHandler struct {
	service ProjectService
	log     *slog.Logger
}

func NewProjectHandler(service ProjectService, log *slog.Logger) (*ProjectHandler, error) {
	if isNilInterface(service) {
		return nil, ErrMissingProjectService
	}
	if log == nil {
		return nil, ErrMissingProjectLogger
	}
	return &ProjectHandler{service: service, log: log}, nil
}

func (h *ProjectHandler) Create(c fiber.Ctx) error {
	var request CreateProjectRequest
	if err := decodeStrictJSON(c, &request); err != nil {
		return writeJSONError(c, err)
	}
	trimmedName := strings.TrimSpace(request.Name)
	if trimmedName == "" {
		return apiresponse.WriteError(c, fiber.StatusUnprocessableEntity, "validation_failed", "request validation failed", apiresponse.ErrorOptions{Fields: map[string]string{"name": "is required"}})
	}
	if utf8.RuneCountInString(request.Name) > maximumProjectNameLength {
		return apiresponse.WriteError(c, fiber.StatusUnprocessableEntity, "validation_failed", "request validation failed", apiresponse.ErrorOptions{Fields: map[string]string{"name": "must contain at most 120 characters"}})
	}

	project, err := h.service.CreateProject(c.RequestCtx(), app.CreateProjectRequest{Name: request.Name, Slug: request.Slug})
	if err != nil {
		return h.writeServiceError(c, "create_project", err)
	}
	response, err := mapProjectResponse(project)
	if err != nil {
		return h.writeServiceError(c, "create_project_response", err)
	}
	c.Location("/api/v1/projects/" + response.Slug)
	return c.Status(fiber.StatusCreated).JSON(response)
}

func (h *ProjectHandler) List(c fiber.Ctx) error {
	projects, err := h.service.ListProjects(c.RequestCtx())
	if err != nil {
		return h.writeServiceError(c, "list_projects", err)
	}
	response, err := mapProjectCollection(projects)
	if err != nil {
		return h.writeServiceError(c, "list_projects_response", err)
	}
	return c.JSON(response)
}

func (h *ProjectHandler) Get(c fiber.Ctx) error {
	slug, ok := validProjectSlug(c.Params("slug"))
	if !ok {
		return writeInvalidPath(c, "slug")
	}
	project, err := h.service.GetProject(c.RequestCtx(), slug)
	if err != nil {
		return h.writeServiceError(c, "get_project", err)
	}
	response, err := mapProjectResponse(project)
	if err != nil {
		return h.writeServiceError(c, "get_project_response", err)
	}
	return c.JSON(response)
}

func (h *ProjectHandler) Delete(c fiber.Ctx) error {
	slug, ok := validProjectSlug(c.Params("slug"))
	if !ok {
		return writeInvalidPath(c, "slug")
	}
	if err := h.service.DeleteProject(c.RequestCtx(), slug); err != nil {
		return h.writeServiceError(c, "delete_project", err)
	}
	return c.Status(fiber.StatusAccepted).JSON(AcceptedOperationResponse{
		Operation: "delete_project", ResourceType: "project", Resource: slug, Status: "accepted",
	})
}

func (h *ProjectHandler) ListApplications(c fiber.Ctx) error {
	slug, ok := validProjectSlug(c.Params("slug"))
	if !ok {
		return writeInvalidPath(c, "slug")
	}
	applications, err := h.service.ListProjectApps(c.RequestCtx(), slug)
	if err != nil {
		return h.writeServiceError(c, "list_project_applications", err)
	}
	response, err := mapProjectApplications(applications)
	if err != nil {
		return h.writeServiceError(c, "list_project_applications_response", err)
	}
	return c.JSON(response)
}

func (h *ProjectHandler) CreateLink(c fiber.Ctx) error {
	slug, ok := validProjectSlug(c.Params("slug"))
	if !ok {
		return writeInvalidPath(c, "slug")
	}
	var request CreateProjectLinkRequest
	if err := decodeStrictJSON(c, &request); err != nil {
		return writeJSONError(c, err)
	}
	if strings.TrimSpace(request.TargetAppName) == "" {
		return apiresponse.WriteError(c, fiber.StatusUnprocessableEntity, "validation_failed", "request validation failed", apiresponse.ErrorOptions{Fields: map[string]string{"target_app_name": "is required"}})
	}
	if !publicApplicationName.MatchString(request.TargetAppName) {
		return apiresponse.WriteError(c, fiber.StatusUnprocessableEntity, "validation_failed", "request validation failed", apiresponse.ErrorOptions{Fields: map[string]string{"target_app_name": "must be a canonical application name"}})
	}

	link, err := h.service.CreateProjectLink(c.RequestCtx(), slug, request.TargetAppName, request.Alias)
	if err != nil {
		return h.writeServiceError(c, "create_project_link", err)
	}
	response, err := mapProjectLinkResponse(link)
	if err != nil {
		return h.writeServiceError(c, "create_project_link_response", err)
	}
	c.Location("/api/v1/projects/" + slug + "/links/" + response.ID)
	return c.Status(fiber.StatusCreated).JSON(response)
}

func (h *ProjectHandler) ListLinks(c fiber.Ctx) error {
	slug, ok := validProjectSlug(c.Params("slug"))
	if !ok {
		return writeInvalidPath(c, "slug")
	}
	links, err := h.service.ListProjectLinks(c.RequestCtx(), slug)
	if err != nil {
		return h.writeServiceError(c, "list_project_links", err)
	}
	response, err := mapProjectLinkCollection(links)
	if err != nil {
		return h.writeServiceError(c, "list_project_links_response", err)
	}
	return c.JSON(response)
}

func (h *ProjectHandler) DeleteLink(c fiber.Ctx) error {
	slug, ok := validProjectSlug(c.Params("slug"))
	if !ok {
		return writeInvalidPath(c, "slug")
	}
	linkID := c.Params("link_id")
	if !canonicalUUID(linkID) {
		return writeInvalidPath(c, "link_id")
	}
	if err := h.service.DeleteProjectLink(c.RequestCtx(), slug, linkID); err != nil {
		return h.writeServiceError(c, "delete_project_link", err)
	}
	return c.Status(fiber.StatusAccepted).JSON(AcceptedOperationResponse{
		Operation: "delete_project_link", ResourceType: "project_link", Resource: linkID, Status: "accepted",
	})
}

func writeJSONError(c fiber.Ctx, err error) error {
	if errors.Is(err, ErrUnsupportedMedia) {
		return apiresponse.WriteError(c, fiber.StatusUnsupportedMediaType, "unsupported_media_type", "request media type is unsupported", apiresponse.ErrorOptions{})
	}
	return apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "request body is malformed", apiresponse.ErrorOptions{})
}

func writeInvalidPath(c fiber.Ctx, field string) error {
	return apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "path parameter is malformed", apiresponse.ErrorOptions{Fields: map[string]string{field: "is invalid"}})
}

func (h *ProjectHandler) writeServiceError(c fiber.Ctx, operation string, err error) error {
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
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrGenerationConflict), errors.Is(err, store.ErrInvalidTransition):
		status = fiber.StatusConflict
		code = "conflict"
		message = "request conflicts with current resource state"
	case errors.Is(err, app.ErrValidation):
		status = fiber.StatusUnprocessableEntity
		code = "validation_failed"
		message = "request validation failed"
	}
	if status >= fiber.StatusInternalServerError {
		h.log.Error("project API operation failed",
			"request_id", apiresponse.RequestID(c),
			"operation", operation,
			"status", status,
			"error_type", fmt.Sprintf("%T", err),
		)
	}
	return apiresponse.WriteError(c, status, code, message, options)
}

func validProjectSlug(slug string) (string, bool) {
	return slug, len(slug) <= 48 && projectSlugPattern.MatchString(slug)
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}
