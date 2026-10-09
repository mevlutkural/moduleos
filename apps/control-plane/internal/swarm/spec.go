package swarm

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/distribution/reference"
)

var ErrInvalidSpec = errors.New("invalid desired service spec")
var ErrOwnershipConflict = errors.New("resource ownership conflict")
var ErrImageResolution = errors.New("image resolution failed")

const maximumPortNumber uint32 = 65535

func IsImmutableImageReference(image string) bool {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return false
	}
	_, immutable := named.(reference.Digested)
	return immutable
}

func ImageReferencesEqual(left, right string) bool {
	leftNamed, err := reference.ParseNormalizedNamed(left)
	if err != nil {
		return false
	}
	rightNamed, err := reference.ParseNormalizedNamed(right)
	if err != nil {
		return false
	}
	leftNormalized := reference.FamiliarString(reference.TagNameOnly(leftNamed))
	rightNormalized := reference.FamiliarString(reference.TagNameOnly(rightNamed))
	return leftNormalized == rightNormalized
}

func ImageReferenceMatches(source, runtime string) bool {
	if ImageReferencesEqual(source, runtime) {
		return true
	}
	sourceNamed, err := reference.ParseNormalizedNamed(source)
	if err != nil {
		return false
	}
	runtimeNamed, err := reference.ParseNormalizedNamed(runtime)
	if err != nil {
		return false
	}
	if _, sourceIsDigest := sourceNamed.(reference.Digested); sourceIsDigest {
		return false
	}
	if _, runtimeIsDigest := runtimeNamed.(reference.Digested); !runtimeIsDigest {
		return false
	}
	sourceTagged := reference.TagNameOnly(sourceNamed)
	sourceTag, ok := sourceTagged.(reference.NamedTagged)
	if !ok {
		return false
	}
	if runtimeTag, ok := runtimeNamed.(reference.NamedTagged); ok {
		runtimeTagged, err := reference.WithTag(reference.TrimNamed(runtimeNamed), runtimeTag.Tag())
		return err == nil && ImageReferencesEqual(reference.FamiliarString(sourceTagged), reference.FamiliarString(runtimeTagged))
	}
	return sourceTag.Tag() == "latest" && reference.FamiliarName(sourceNamed) == reference.FamiliarName(runtimeNamed)
}

const (
	LabelAppID        = "moduleos.app.id"
	LabelProjectID    = "moduleos.project.id"
	LabelProject      = "moduleos.project.slug"
	LabelGeneration   = "moduleos.generation"
	LabelTaskTemplate = "moduleos.task-template"
	LabelSchema       = "moduleos.schema"
	LabelResourceKind = "moduleos.resource.kind"
	OwnedLabelPrefix  = "moduleos."
	SchemaVersion     = "v0"
)

type DesiredServiceInput struct {
	ApplicationID   string
	ProjectID       string
	ProjectSlug     string
	Name            string
	Image           string
	DesiredRunning  bool
	Replicas        int
	Generation      int64
	Environment     []string
	Ports           []PortConfig
	Volumes         []VolumeConfig
	ProjectNetwork  NetworkAttachment
	LinkedNetworks  []NetworkAttachment
	Expose          bool
	IngressPort     uint32
	IngressNetwork  string
	BaseDomain      string
	RolloutIdentity string
	PreservedLabels map[string]string
}

func BuildDesiredServiceSpec(input DesiredServiceInput) (ServiceSpec, error) {
	if input.ApplicationID == "" || input.ProjectID == "" || input.ProjectSlug == "" {
		return ServiceSpec{}, fmt.Errorf("%w: immutable ownership identity is required", ErrInvalidSpec)
	}
	if input.Name == "" || len(input.Name) > 63 {
		return ServiceSpec{}, fmt.Errorf("%w: application name must contain 1..63 characters", ErrInvalidSpec)
	}
	if input.Image == "" {
		return ServiceSpec{}, fmt.Errorf("%w: image is required", ErrInvalidSpec)
	}
	namedImage, err := reference.ParseNormalizedNamed(input.Image)
	if err != nil {
		return ServiceSpec{}, fmt.Errorf("%w: invalid image reference", ErrInvalidSpec)
	}
	input.Image = reference.FamiliarString(reference.TagNameOnly(namedImage))
	if input.Replicas < 0 || input.Generation < 0 {
		return ServiceSpec{}, fmt.Errorf("%w: replicas and generation cannot be negative", ErrInvalidSpec)
	}
	if input.IngressPort > maximumPortNumber {
		return ServiceSpec{}, fmt.Errorf("%w: ingress port outside 0..65535", ErrInvalidSpec)
	}
	if input.ProjectNetwork.Network == "" {
		return ServiceSpec{}, fmt.Errorf("%w: project network is required", ErrInvalidSpec)
	}
	if input.Expose && (input.IngressPort == 0 || input.IngressNetwork == "" || input.BaseDomain == "") {
		return ServiceSpec{}, fmt.Errorf("%w: exposed applications require ingress port, network, and base domain", ErrInvalidSpec)
	}

	environment := append([]string(nil), input.Environment...)
	sort.Strings(environment)
	if err := validateEnvironment(environment); err != nil {
		return ServiceSpec{}, err
	}
	ports, err := normalizePorts(input.Ports)
	if err != nil {
		return ServiceSpec{}, err
	}
	volumes, err := normalizeVolumes(input.Volumes)
	if err != nil {
		return ServiceSpec{}, err
	}
	networks := append([]NetworkAttachment{input.ProjectNetwork}, input.LinkedNetworks...)
	if input.Expose {
		networks = append(networks, NetworkAttachment{Network: input.IngressNetwork})
	}
	networks, err = normalizeNetworks(networks)
	if err != nil {
		return ServiceSpec{}, err
	}

	labels := make(map[string]string)
	for key, value := range input.PreservedLabels {
		if !strings.HasPrefix(key, OwnedLabelPrefix) && !strings.HasPrefix(key, "traefik.") {
			labels[key] = value
		}
	}
	for key, value := range ownershipLabels(input) {
		labels[key] = value
	}

	replicas := uint64(input.Replicas)
	if !input.DesiredRunning {
		replicas = 0
	}
	result := ServiceSpec{
		Name:         input.Name,
		Image:        input.Image,
		ServiceMode:  "replicated",
		EndpointMode: "vip",
		Replicas:     replicas,
		EnvVars:      environment,
		Ports:        ports,
		Volumes:      volumes,
		Labels:       labels,
		Networks:     networks,
		Update: UpdatePolicy{
			Parallelism:   1,
			Delay:         2 * time.Second,
			Monitor:       10 * time.Second,
			FailureAction: "pause",
			Order:         "stop-first",
		},
		Rollback: UpdatePolicy{
			Parallelism:   1,
			Delay:         2 * time.Second,
			Monitor:       10 * time.Second,
			FailureAction: "pause",
			Order:         "stop-first",
		},
		Restart: RestartPolicy{Condition: "any", Delay: 5 * time.Second},
	}
	return WithRolloutIdentity(result, input.RolloutIdentity)
}

func WithRolloutIdentity(spec ServiceSpec, identity string) (ServiceSpec, error) {
	templateHash, err := taskTemplateHash(spec, identity)
	if err != nil {
		return ServiceSpec{}, fmt.Errorf("%w: hash task template: %v", ErrInvalidSpec, err)
	}
	spec.TaskTemplateHash = templateHash
	return spec, nil
}

func taskTemplateHash(spec ServiceSpec, rolloutIdentity string) (string, error) {
	payload, err := json.Marshal(struct {
		Image           string
		EnvVars         []string
		Volumes         []VolumeConfig
		Networks        []NetworkAttachment
		Restart         RestartPolicy
		RolloutIdentity string
	}{
		Image:           spec.Image,
		EnvVars:         spec.EnvVars,
		Volumes:         spec.Volumes,
		Networks:        spec.Networks,
		Restart:         spec.Restart,
		RolloutIdentity: rolloutIdentity,
	})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	return fmt.Sprintf("%x", hash), nil
}

func ownershipLabels(input DesiredServiceInput) map[string]string {
	labels := map[string]string{
		LabelManagedBy:   "true",
		LabelAppName:     input.Name,
		LabelAppID:       input.ApplicationID,
		LabelProjectID:   input.ProjectID,
		LabelProject:     input.ProjectSlug,
		LabelGeneration:  strconv.FormatInt(input.Generation, 10),
		LabelSchema:      SchemaVersion,
		"traefik.enable": strconv.FormatBool(input.Expose),
	}
	if !input.Expose {
		return labels
	}
	resourceName := "moduleos-" + strings.ReplaceAll(input.ApplicationID, "-", "")
	if len(resourceName) > 25 {
		resourceName = resourceName[:25]
	}
	host := AutoDomain(input.Name, input.BaseDomain)
	labels[fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port", resourceName)] = strconv.Itoa(int(input.IngressPort))
	labels[fmt.Sprintf("traefik.http.routers.%s.rule", resourceName)] = fmt.Sprintf("Host(`%s`)", host)
	labels[fmt.Sprintf("traefik.http.routers.%s.service", resourceName)] = resourceName
	labels[fmt.Sprintf("traefik.http.routers.%s.entrypoints", resourceName)] = "web"
	labels["traefik.swarm.network"] = input.IngressNetwork
	return labels
}

func validateEnvironment(environment []string) error {
	seen := make(map[string]struct{}, len(environment))
	for _, item := range environment {
		if strings.ContainsRune(item, '\x00') {
			return fmt.Errorf("%w: environment entry contains NUL", ErrInvalidSpec)
		}
		key, _, ok := strings.Cut(item, "=")
		if !ok || !validEnvironmentKey(key) {
			return fmt.Errorf("%w: invalid environment entry", ErrInvalidSpec)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: duplicate environment key %q", ErrInvalidSpec, key)
		}
		seen[key] = struct{}{}
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

func normalizePorts(ports []PortConfig) ([]PortConfig, error) {
	result := append([]PortConfig(nil), ports...)
	seen := make(map[string]struct{}, len(result))
	for i := range result {
		if result[i].ContainerPort == 0 || result[i].ContainerPort > maximumPortNumber || result[i].PublishedPort > maximumPortNumber {
			return nil, fmt.Errorf("%w: port outside 1..65535", ErrInvalidSpec)
		}
		if result[i].Protocol == "" {
			result[i].Protocol = "tcp"
		}
		if result[i].Protocol != "tcp" && result[i].Protocol != "udp" {
			return nil, fmt.Errorf("%w: protocol must be tcp or udp", ErrInvalidSpec)
		}
		if result[i].PublishMode == "" {
			result[i].PublishMode = "ingress"
		}
		if result[i].PublishMode != "ingress" && result[i].PublishMode != "host" {
			return nil, fmt.Errorf("%w: publish mode must be ingress or host", ErrInvalidSpec)
		}
		if result[i].PublishedPort == 0 {
			continue
		}
		key := fmt.Sprintf("%d/%s/%s", result[i].PublishedPort, result[i].Protocol, result[i].PublishMode)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%w: duplicate published port %s", ErrInvalidSpec, key)
		}
		seen[key] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool {
		left := fmt.Sprintf("%05d/%05d/%s/%s", result[i].PublishedPort, result[i].ContainerPort, result[i].Protocol, result[i].PublishMode)
		right := fmt.Sprintf("%05d/%05d/%s/%s", result[j].PublishedPort, result[j].ContainerPort, result[j].Protocol, result[j].PublishMode)
		return left < right
	})
	return result, nil
}

func normalizeVolumes(volumes []VolumeConfig) ([]VolumeConfig, error) {
	result := append([]VolumeConfig(nil), volumes...)
	seenTargets := make(map[string]struct{}, len(result))
	for _, volume := range result {
		if strings.ContainsRune(volume.Source, '\x00') || strings.ContainsRune(volume.Target, '\x00') {
			return nil, fmt.Errorf("%w: mount paths cannot contain NUL", ErrInvalidSpec)
		}
		if !filepath.IsAbs(volume.Source) || !filepath.IsAbs(volume.Target) {
			return nil, fmt.Errorf("%w: mount source and target must be absolute", ErrInvalidSpec)
		}
		if filepath.Clean(volume.Target) == string(filepath.Separator) {
			return nil, fmt.Errorf("%w: mount target cannot be root", ErrInvalidSpec)
		}
		source := filepath.Clean(volume.Source)
		if source == "/" || source == "/var/run/docker.sock" || source == "/proc" || strings.HasPrefix(source, "/proc/") || source == "/sys" || strings.HasPrefix(source, "/sys/") || source == "/dev" || strings.HasPrefix(source, "/dev/") || source == "/etc" || strings.HasPrefix(source, "/etc/") {
			return nil, fmt.Errorf("%w: sensitive mount source is denied", ErrInvalidSpec)
		}
		if _, exists := seenTargets[volume.Target]; exists {
			return nil, fmt.Errorf("%w: duplicate mount target %q", ErrInvalidSpec, volume.Target)
		}
		seenTargets[volume.Target] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Target == result[j].Target {
			return result[i].Source < result[j].Source
		}
		return result[i].Target < result[j].Target
	})
	return result, nil
}

func normalizeNetworks(networks []NetworkAttachment) ([]NetworkAttachment, error) {
	byNetwork := make(map[string]map[string]struct{})
	for _, network := range networks {
		if network.Network == "" {
			return nil, fmt.Errorf("%w: network name cannot be empty", ErrInvalidSpec)
		}
		aliases := byNetwork[network.Network]
		if aliases == nil {
			aliases = make(map[string]struct{})
			byNetwork[network.Network] = aliases
		}
		for _, alias := range network.Aliases {
			if alias != "" {
				aliases[alias] = struct{}{}
			}
		}
	}
	result := make([]NetworkAttachment, 0, len(byNetwork))
	for network, aliasSet := range byNetwork {
		aliases := make([]string, 0, len(aliasSet))
		for alias := range aliasSet {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		result = append(result, NetworkAttachment{Network: network, Aliases: aliases})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Network < result[j].Network })
	return result, nil
}
