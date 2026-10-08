package swarm

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

func DiffServiceSpec(desired, observed ServiceSpec) []string {
	var diff []string
	if !imageEquivalent(desired.Image, observed.Image) {
		diff = append(diff, "image")
	}
	if desired.ServiceMode != observed.ServiceMode {
		diff = append(diff, "service_mode")
	}
	if desired.EndpointMode != observed.EndpointMode {
		diff = append(diff, "endpoint_mode")
	}
	if desired.Replicas != observed.Replicas {
		diff = append(diff, "replicas")
	}
	if !reflect.DeepEqual(sortedStrings(desired.EnvVars), sortedStrings(observed.EnvVars)) {
		diff = append(diff, "environment")
	}
	if !reflect.DeepEqual(desiredRuntimePorts(desired.Ports), observed.Ports) {
		diff = append(diff, "ports")
	}
	if !reflect.DeepEqual(desired.Volumes, observed.Volumes) {
		diff = append(diff, "mounts")
	}
	if !networkSpecsEqual(desired.Networks, observed.Networks) {
		diff = append(diff, "networks")
	}
	if desired.TaskTemplateHash != observed.TaskTemplateHash || observed.UnsupportedTaskTemplate {
		diff = append(diff, "task_template")
	}
	if !ownedLabelsEqual(desired.Labels, observed.Labels) {
		diff = append(diff, "labels")
	}
	if desired.Update != observed.Update || desired.Rollback != observed.Rollback || desired.Restart != observed.Restart {
		diff = append(diff, "policy")
	}
	return diff
}

func desiredRuntimePorts(ports []PortConfig) []PortConfig {
	var published []PortConfig
	for _, port := range ports {
		if port.PublishedPort != 0 {
			published = append(published, port)
		}
	}
	return published
}

func imageEquivalent(desired, observed string) bool {
	return ImageReferenceMatches(desired, observed)
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func ownedLabelsEqual(desired, observed map[string]string) bool {
	for key, desiredValue := range desired {
		if strings.HasPrefix(key, OwnedLabelPrefix) || strings.HasPrefix(key, "traefik.") {
			if observed[key] != desiredValue {
				return false
			}
		}
	}
	for key := range observed {
		if (strings.HasPrefix(key, OwnedLabelPrefix) || strings.HasPrefix(key, "traefik.")) && desired[key] == "" {
			return false
		}
	}
	return true
}

func networkSpecsEqual(desired, observed []NetworkAttachment) bool {
	desiredMap := networkAliasMap(desired)
	observedMap := networkAliasMap(observed)
	if len(desiredMap) != len(observedMap) {
		return false
	}
	for network, desiredAliases := range desiredMap {
		observedAliases, exists := observedMap[network]
		if !exists {
			return false
		}
		for alias := range desiredAliases {
			if _, exists := observedAliases[alias]; !exists {
				return false
			}
		}
	}
	return true
}

func networkAliasMap(networks []NetworkAttachment) map[string]map[string]struct{} {
	result := make(map[string]map[string]struct{}, len(networks))
	for _, network := range networks {
		aliases := result[network.Network]
		if aliases == nil {
			aliases = make(map[string]struct{})
			result[network.Network] = aliases
		}
		for _, alias := range network.Aliases {
			aliases[alias] = struct{}{}
		}
	}
	return result
}

func FormatDiff(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	return fmt.Sprintf("managed fields differ: %s", strings.Join(fields, ","))
}
