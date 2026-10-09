package handler

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/distribution/reference"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

const (
	maximumApplicationEnvironmentEntries = 200
	maximumApplicationPorts              = 20
	maximumApplicationVolumes            = 20
	maximumApplicationPortNumber         = 65535
	maximumDesiredImageReferenceLength   = 255
	maximumPersistedImageReferenceLength = maximumDesiredImageReferenceLength + len("@sha256:") + 64
)

type optionalValue[T any] struct {
	Value   T
	Present bool
	Null    bool
}

func (value *optionalValue[T]) UnmarshalJSON(data []byte) error {
	value.Present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		value.Null = true
		return nil
	}
	return json.Unmarshal(data, &value.Value)
}

type ApplicationPortInput struct {
	ContainerPort uint32                `json:"container_port"`
	PublishedPort optionalValue[uint32] `json:"published_port,omitempty"`
	Protocol      optionalValue[string] `json:"protocol,omitempty"`
	PublishMode   optionalValue[string] `json:"publish_mode,omitempty"`
}

type ApplicationVolumeInput struct {
	Source   string              `json:"source"`
	Target   string              `json:"target"`
	ReadOnly optionalValue[bool] `json:"read_only,omitempty"`
}

type applicationEnvironmentInput map[string]optionalValue[string]

type CreateApplicationRequest struct {
	Name                 string                                     `json:"name"`
	ProjectSlug          optionalValue[string]                      `json:"project_slug,omitempty"`
	Image                string                                     `json:"image"`
	Replicas             optionalValue[int]                         `json:"replicas,omitempty"`
	EnvVars              optionalValue[applicationEnvironmentInput] `json:"env_vars,omitempty"`
	Ports                optionalValue[[]ApplicationPortInput]      `json:"ports,omitempty"`
	Volumes              optionalValue[[]ApplicationVolumeInput]    `json:"volumes,omitempty"`
	Expose               optionalValue[bool]                        `json:"expose,omitempty"`
	IngressContainerPort optionalValue[uint32]                      `json:"ingress_container_port,omitempty"`
}

type UpdateApplicationRequest struct {
	EnvVars              optionalValue[applicationEnvironmentInput] `json:"env_vars,omitempty"`
	Ports                optionalValue[[]ApplicationPortInput]      `json:"ports,omitempty"`
	Volumes              optionalValue[[]ApplicationVolumeInput]    `json:"volumes,omitempty"`
	Expose               optionalValue[bool]                        `json:"expose,omitempty"`
	IngressContainerPort optionalValue[uint32]                      `json:"ingress_container_port,omitempty"`
}

type ScaleApplicationRequest struct {
	Replicas optionalValue[int] `json:"replicas"`
}

type RedactedValue struct {
	Redacted bool `json:"redacted"`
}

type ApplicationPortResponse struct {
	ContainerPort uint32 `json:"container_port"`
	PublishedPort uint32 `json:"published_port"`
	Protocol      string `json:"protocol"`
	PublishMode   string `json:"publish_mode"`
}

type ApplicationVolumeResponse struct {
	Target         string `json:"target"`
	ReadOnly       bool   `json:"read_only"`
	SourceRedacted bool   `json:"source_redacted"`
}

type ApplicationResponse struct {
	ID                   string                      `json:"id"`
	ProjectID            string                      `json:"project_id"`
	Name                 string                      `json:"name"`
	SourceType           store.SourceType            `json:"source_type"`
	Image                string                      `json:"image"`
	ObservedImage        string                      `json:"observed_image,omitempty"`
	Status               store.AppStatus             `json:"status"`
	Replicas             int                         `json:"replicas"`
	DesiredRunState      store.DesiredRunState       `json:"desired_run_state"`
	DesiredGeneration    int64                       `json:"desired_generation"`
	ObservedGeneration   int64                       `json:"observed_generation"`
	ObservedState        store.ObservedState         `json:"observed_state"`
	ReconcileErrorCode   string                      `json:"reconcile_error_code,omitempty"`
	ReconcileRetryable   bool                        `json:"reconcile_retryable"`
	LastTransitionAt     *time.Time                  `json:"last_transition_at,omitempty"`
	LastReconciledAt     *time.Time                  `json:"last_reconciled_at,omitempty"`
	DeletionRequestedAt  *time.Time                  `json:"deletion_requested_at,omitempty"`
	EnvVars              map[string]RedactedValue    `json:"env_vars"`
	Ports                []ApplicationPortResponse   `json:"ports"`
	Volumes              []ApplicationVolumeResponse `json:"volumes"`
	Expose               bool                        `json:"expose"`
	IngressContainerPort uint32                      `json:"ingress_container_port"`
	CreatedAt            time.Time                   `json:"created_at"`
	UpdatedAt            time.Time                   `json:"updated_at"`
}

type ApplicationCollectionResponse struct {
	Applications []ApplicationResponse `json:"applications"`
	Total        int                   `json:"total"`
}

type ApplicationOperationResponse struct {
	Operation  string `json:"operation"`
	Resource   string `json:"resource"`
	Status     string `json:"status"`
	Generation int64  `json:"generation"`
}

func mapApplicationResponse(application *store.Application) (ApplicationResponse, error) {
	if application == nil || !canonicalUUID(application.ID) || !canonicalUUID(application.ProjectID) ||
		!publicApplicationName.MatchString(application.Name) || application.SourceType != store.SourceTypeImage ||
		!validPersistedImageReference(application.Image) || !validOptionalPersistedImageReference(application.ObservedImage) ||
		!knownApplicationStatus(application.Status) || !knownDesiredRunState(application.DesiredRunState) ||
		!knownObservedState(application.ObservedState) || application.Replicas < 0 ||
		application.IngressContainerPort > maximumApplicationPortNumber ||
		application.DesiredGeneration < 1 || application.ObservedGeneration < 0 ||
		application.ObservedGeneration > application.DesiredGeneration || application.ReconcileAttempt < 0 ||
		application.CreatedAt.IsZero() || application.UpdatedAt.IsZero() || application.UpdatedAt.Before(application.CreatedAt) {
		return ApplicationResponse{}, errInvalidPublicState
	}
	if application.ReconcileErrorCode != "" && !publicErrorCode.MatchString(application.ReconcileErrorCode) {
		return ApplicationResponse{}, errInvalidPublicState
	}
	if !validOptionalTime(application.LastTransitionAt) || !validOptionalTime(application.LastReconciledAt) || !validOptionalTime(application.DeletionTimestamp) {
		return ApplicationResponse{}, errInvalidPublicState
	}

	environment, err := decodeCanonicalEnvironment(application.EnvVars)
	if err != nil {
		return ApplicationResponse{}, err
	}
	ports, err := decodeCanonicalPorts(application.Ports)
	if err != nil {
		return ApplicationResponse{}, err
	}
	volumes, err := decodeCanonicalVolumes(application.Volumes)
	if err != nil {
		return ApplicationResponse{}, err
	}
	if application.Expose && application.IngressContainerPort == 0 {
		return ApplicationResponse{}, errInvalidPublicState
	}

	return ApplicationResponse{
		ID:                   application.ID,
		ProjectID:            application.ProjectID,
		Name:                 application.Name,
		SourceType:           application.SourceType,
		Image:                application.Image,
		ObservedImage:        application.ObservedImage,
		Status:               application.Status,
		Replicas:             application.Replicas,
		DesiredRunState:      application.DesiredRunState,
		DesiredGeneration:    application.DesiredGeneration,
		ObservedGeneration:   application.ObservedGeneration,
		ObservedState:        application.ObservedState,
		ReconcileErrorCode:   application.ReconcileErrorCode,
		ReconcileRetryable:   application.ReconcileRetryable,
		LastTransitionAt:     utcTimePointer(application.LastTransitionAt),
		LastReconciledAt:     utcTimePointer(application.LastReconciledAt),
		DeletionRequestedAt:  utcTimePointer(application.DeletionTimestamp),
		EnvVars:              environment,
		Ports:                ports,
		Volumes:              volumes,
		Expose:               application.Expose,
		IngressContainerPort: application.IngressContainerPort,
		CreatedAt:            application.CreatedAt.UTC(),
		UpdatedAt:            application.UpdatedAt.UTC(),
	}, nil
}

func mapApplicationCollection(applications []*store.Application) (ApplicationCollectionResponse, error) {
	if len(applications) > maximumPublicApplications {
		return ApplicationCollectionResponse{}, errInvalidPublicState
	}
	response := ApplicationCollectionResponse{Applications: make([]ApplicationResponse, 0, len(applications))}
	for _, application := range applications {
		mapped, err := mapApplicationResponse(application)
		if err != nil {
			return ApplicationCollectionResponse{}, err
		}
		response.Applications = append(response.Applications, mapped)
	}
	response.Total = len(response.Applications)
	return response, nil
}

func validImageReference(value string) bool {
	return validImageReferenceWithin(value, maximumDesiredImageReferenceLength)
}

func validImageReferenceWithin(value string, maximumLength int) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || value == "" || len(value) > maximumLength || longLowerHexIdentifier(value) {
		return false
	}
	if separator := strings.LastIndexByte(value, '@'); separator >= 0 {
		digest := value[separator+1:]
		if len(digest) != len("sha256:")+64 || !strings.HasPrefix(digest, "sha256:") || !lowerHex(digest[len("sha256:"):]) {
			return false
		}
	}
	_, err := reference.ParseNormalizedNamed(value)
	return err == nil
}

func longLowerHexIdentifier(value string) bool {
	return len(value) >= 64 && lowerHex(value)
}

func lowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validPersistedImageReference(value string) bool {
	return validImageReferenceWithin(value, maximumPersistedImageReferenceLength)
}

func validOptionalPersistedImageReference(value string) bool {
	return value == "" || validPersistedImageReference(value)
}

func validOptionalTime(value *time.Time) bool {
	return value == nil || !value.IsZero()
}

func decodeCanonicalEnvironment(raw string) (map[string]RedactedValue, error) {
	values := make(map[string]string)
	if err := decodeCanonicalJSON(raw, &values); err != nil || values == nil || len(values) > maximumApplicationEnvironmentEntries {
		return nil, errInvalidPublicState
	}
	result := make(map[string]RedactedValue, len(values))
	for key, value := range values {
		if !validEnvironmentKey(key) || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return nil, errInvalidPublicState
		}
		result[key] = RedactedValue{Redacted: true}
	}
	return result, nil
}

func decodeCanonicalPorts(raw string) ([]ApplicationPortResponse, error) {
	var values []swarm.PortConfig
	if err := decodeCanonicalJSON(raw, &values); err != nil || values == nil || len(values) > maximumApplicationPorts {
		return nil, errInvalidPublicState
	}
	result := make([]ApplicationPortResponse, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.ContainerPort == 0 || value.ContainerPort > maximumApplicationPortNumber || value.PublishedPort > maximumApplicationPortNumber {
			return nil, errInvalidPublicState
		}
		protocol := value.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		mode := value.PublishMode
		if mode == "" {
			mode = "ingress"
		}
		if protocol != "tcp" && protocol != "udp" || mode != "ingress" && mode != "host" {
			return nil, errInvalidPublicState
		}
		if value.PublishedPort != 0 {
			key := strconv.FormatUint(uint64(value.PublishedPort), 10) + "/" + protocol + "/" + mode
			if _, exists := seen[key]; exists {
				return nil, errInvalidPublicState
			}
			seen[key] = struct{}{}
		}
		result = append(result, ApplicationPortResponse{ContainerPort: value.ContainerPort, PublishedPort: value.PublishedPort, Protocol: protocol, PublishMode: mode})
	}
	return result, nil
}

func decodeCanonicalVolumes(raw string) ([]ApplicationVolumeResponse, error) {
	var values []swarm.VolumeConfig
	if err := decodeCanonicalJSON(raw, &values); err != nil || values == nil || len(values) > maximumApplicationVolumes {
		return nil, errInvalidPublicState
	}
	result := make([]ApplicationVolumeResponse, 0, len(values))
	seenTargets := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !utf8.ValidString(value.Source) || !utf8.ValidString(value.Target) ||
			strings.ContainsRune(value.Source, '\x00') || strings.ContainsRune(value.Target, '\x00') ||
			!filepath.IsAbs(value.Source) || !filepath.IsAbs(value.Target) ||
			filepath.Clean(value.Source) != value.Source || filepath.Clean(value.Target) != value.Target ||
			value.Target == string(filepath.Separator) {
			return nil, errInvalidPublicState
		}
		if _, exists := seenTargets[value.Target]; exists {
			return nil, errInvalidPublicState
		}
		seenTargets[value.Target] = struct{}{}
		result = append(result, ApplicationVolumeResponse{Target: value.Target, ReadOnly: value.ReadOnly, SourceRedacted: true})
	}
	return result, nil
}

func decodeCanonicalJSON(raw string, destination any) error {
	if raw == "" || !utf8.ValidString(raw) {
		return errInvalidPublicState
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errInvalidPublicState
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return errInvalidPublicState
	}
	canonical, err := json.Marshal(destination)
	if err != nil || string(canonical) != raw {
		return errInvalidPublicState
	}
	return nil
}

func validEnvironmentKey(key string) bool {
	for index, character := range key {
		if index == 0 {
			if character != '_' && !unicode.IsLetter(character) {
				return false
			}
			continue
		}
		if character != '_' && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return false
		}
	}
	return key != ""
}

func applicationPorts(values []ApplicationPortInput) []swarm.PortConfig {
	result := make([]swarm.PortConfig, len(values))
	for index, value := range values {
		result[index] = swarm.PortConfig{ContainerPort: value.ContainerPort, PublishedPort: value.PublishedPort.Value, Protocol: value.Protocol.Value, PublishMode: value.PublishMode.Value}
	}
	return result
}

func applicationVolumes(values []ApplicationVolumeInput) []swarm.VolumeConfig {
	result := make([]swarm.VolumeConfig, len(values))
	for index, value := range values {
		result[index] = swarm.VolumeConfig{Source: value.Source, Target: value.Target, ReadOnly: value.ReadOnly.Value}
	}
	return result
}

func validApplicationPortInputs(values []ApplicationPortInput) bool {
	seenPublished := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.ContainerPort == 0 || value.ContainerPort > maximumApplicationPortNumber ||
			value.PublishedPort.Null || value.PublishedPort.Value > maximumApplicationPortNumber ||
			value.Protocol.Null || value.PublishMode.Null {
			return false
		}
		protocol := "tcp"
		if value.Protocol.Present {
			protocol = value.Protocol.Value
		}
		mode := "ingress"
		if value.PublishMode.Present {
			mode = value.PublishMode.Value
		}
		if protocol != "tcp" && protocol != "udp" || mode != "ingress" && mode != "host" {
			return false
		}
		if value.PublishedPort.Value == 0 {
			continue
		}
		key := strconv.FormatUint(uint64(value.PublishedPort.Value), 10) + "/" + protocol + "/" + mode
		if _, exists := seenPublished[key]; exists {
			return false
		}
		seenPublished[key] = struct{}{}
	}
	return true
}

func validApplicationEnvironmentInputs(values applicationEnvironmentInput) bool {
	for key, value := range values {
		if !validEnvironmentKey(key) || !value.Present || value.Null || !utf8.ValidString(value.Value) || strings.ContainsRune(value.Value, '\x00') {
			return false
		}
	}
	return true
}

func applicationEnvironment(values applicationEnvironmentInput) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value.Value
	}
	return result
}

func validApplicationVolumeInputs(values []ApplicationVolumeInput) bool {
	seenTargets := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.ReadOnly.Null || !canonicalAbsolutePath(value.Source) || !canonicalAbsolutePath(value.Target) || value.Target == string(filepath.Separator) {
			return false
		}
		if _, exists := seenTargets[value.Target]; exists {
			return false
		}
		seenTargets[value.Target] = struct{}{}
	}
	return true
}

func canonicalAbsolutePath(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') && filepath.IsAbs(value) && filepath.Clean(value) == value
}
