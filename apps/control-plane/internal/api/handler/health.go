package handler

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/buildinfo"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

type HealthStore interface {
	Ping(ctx context.Context) error
	ListApplications(ctx context.Context) ([]*store.Application, error)
}

type HealthRuntime interface {
	Health(ctx context.Context) error
	GetNetwork(ctx context.Context, networkNameOrID string) (*swarm.NetworkInfo, error)
}

type ComponentStatus string

const (
	ComponentNotChecked     ComponentStatus = "not_checked"
	ComponentNotRequired    ComponentStatus = "not_required"
	ComponentReady          ComponentStatus = "ready"
	ComponentUnavailable    ComponentStatus = "unavailable"
	MaximumReadinessTimeout                 = 5 * time.Second
)

var (
	ErrMissingHealthStore   = errors.New("health store is required")
	ErrMissingHealthRuntime = errors.New("health runtime is required")
	ErrInvalidHealthOptions = errors.New("health timeout and ingress network must be configured")
)

type ReadinessComponents struct {
	Database ComponentStatus `json:"database"`
	Docker   ComponentStatus `json:"docker"`
	Swarm    ComponentStatus `json:"swarm"`
	Ingress  ComponentStatus `json:"ingress"`
}

type LivenessResponse struct {
	Status string `json:"status"`
}

type ReadinessResponse struct {
	Status     string              `json:"status"`
	Code       string              `json:"code,omitempty"`
	Components ReadinessComponents `json:"components"`
}

type VersionResponse struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}

type HealthHandler struct {
	store          HealthStore
	runtime        HealthRuntime
	ingressNetwork string
	timeout        time.Duration
	stateMu        sync.RWMutex
	draining       bool
}

func NewHealthHandler(store HealthStore, runtime HealthRuntime, ingressNetwork string, timeout time.Duration) (*HealthHandler, error) {
	if isNilInterface(store) {
		return nil, ErrMissingHealthStore
	}
	if isNilInterface(runtime) {
		return nil, ErrMissingHealthRuntime
	}
	ingressNetwork = strings.TrimSpace(ingressNetwork)
	if ingressNetwork == "" || timeout <= 0 || timeout > MaximumReadinessTimeout {
		return nil, ErrInvalidHealthOptions
	}
	return &HealthHandler{store: store, runtime: runtime, ingressNetwork: ingressNetwork, timeout: timeout}, nil
}

func (h *HealthHandler) SetDraining() {
	h.stateMu.Lock()
	h.draining = true
	h.stateMu.Unlock()
}

func (h *HealthHandler) Live(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(LivenessResponse{Status: "live"})
}

func (h *HealthHandler) Ready(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	components := uncheckedComponents()
	if h.isDraining() {
		return writeReadiness(c, fiber.StatusServiceUnavailable, "shutting_down", components)
	}

	ctx, cancel := context.WithTimeout(c.RequestCtx(), h.timeout)
	defer cancel()
	if err := h.store.Ping(ctx); err != nil {
		components.Database = ComponentUnavailable
		return writeReadiness(c, fiber.StatusServiceUnavailable, "database_unavailable", components)
	}
	components.Database = ComponentReady

	if err := h.runtime.Health(ctx); err != nil {
		if errors.Is(err, swarm.ErrSwarmInactive) || errors.Is(err, swarm.ErrSwarmManagerRequired) {
			components.Docker = ComponentReady
			components.Swarm = ComponentUnavailable
			return writeReadiness(c, fiber.StatusServiceUnavailable, "swarm_unavailable", components)
		}
		components.Docker = ComponentUnavailable
		return writeReadiness(c, fiber.StatusServiceUnavailable, "docker_unavailable", components)
	}
	components.Docker = ComponentReady
	components.Swarm = ComponentReady

	applications, err := h.store.ListApplications(ctx)
	if err != nil {
		components.Database = ComponentUnavailable
		return writeReadiness(c, fiber.StatusServiceUnavailable, "database_unavailable", components)
	}
	components.Ingress = ComponentNotRequired
	for _, application := range applications {
		if application != nil && application.Expose {
			components.Ingress = ComponentReady
			network, err := h.runtime.GetNetwork(ctx, h.ingressNetwork)
			if err != nil || network == nil || network.Driver != "overlay" || !network.Attachable {
				components.Ingress = ComponentUnavailable
				return writeReadiness(c, fiber.StatusServiceUnavailable, "ingress_network_unavailable", components)
			}
			break
		}
	}

	return h.writeReady(c, components)
}

func (h *HealthHandler) Version(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	info := buildinfo.Current()
	return c.JSON(VersionResponse{Version: info.Version, Commit: info.Commit, BuildDate: info.BuildDate})
}

func uncheckedComponents() ReadinessComponents {
	return ReadinessComponents{
		Database: ComponentNotChecked,
		Docker:   ComponentNotChecked,
		Swarm:    ComponentNotChecked,
		Ingress:  ComponentNotChecked,
	}
}

func writeReadiness(c fiber.Ctx, status int, code string, components ReadinessComponents) error {
	return c.Status(status).JSON(ReadinessResponse{Status: "not_ready", Code: code, Components: components})
}

func (h *HealthHandler) isDraining() bool {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	return h.draining
}

func (h *HealthHandler) writeReady(c fiber.Ctx, components ReadinessComponents) error {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	if h.draining {
		return writeReadiness(c, fiber.StatusServiceUnavailable, "shutting_down", components)
	}
	return c.JSON(ReadinessResponse{Status: "ready", Components: components})
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
