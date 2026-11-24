//go:build operations

package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func (j *journey) setupFixtures() {
	label := "io.moduleos.operations.run=" + j.run
	j.fixture("network", j.ingress, "network", "create", "--driver", "overlay", "--attachable", "--label", label, j.ingress)
	j.rememberCanaries()
	registry := "ops-" + j.run + "-registry"
	registryPort := j.freePort()
	j.dockerLong("pull", registryImage)
	j.fixture("container", registry, "run", "-d", "--name", registry, "--label", label, "--publish", fmt.Sprintf("127.0.0.1:%d:5000", registryPort), registryImage)
	j.wait(30*time.Second, func(ctx context.Context) (bool, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/v2/", registryPort), nil)
		r, err := j.api.Do(req)
		if err != nil {
			return false, nil
		}
		defer r.Body.Close()
		return r.StatusCode == 200, nil
	})
	for _, variant := range []string{"a", "b"} {
		tag := fmt.Sprintf("127.0.0.1:%d/ops-%s-%s:fixture", registryPort, j.run, variant)
		j.record(Resource{Kind: "image", Name: tag, OwnerKey: "io.moduleos.operations.run", Owner: j.run})
		j.dockerLong("buildx", "build", "--platform", "linux/amd64", "--load", "--label", label, "--build-arg", "VARIANT="+variant, "-t", tag, "fixture")
		id := string(j.docker("image", "inspect", "--format", "{{.Id}}", tag))
		j.imageIDs[variant] = id
		j.ledger.Resources[len(j.ledger.Resources)-1].ID = id
		j.require(SaveLedger(j.ledgerFile, j.ledger) == nil, "ledger_write")
		j.dockerLong("push", tag)
		var digests []string
		j.require(json.Unmarshal(j.docker("image", "inspect", "--format", "{{json .RepoDigests}}", tag), &digests) == nil, "fixture_digest")
		prefix := strings.Split(tag, ":fixture")[0] + "@sha256:"
		for _, digest := range digests {
			if strings.HasPrefix(digest, prefix) {
				j.images[variant] = digest
			}
		}
		j.require(j.images[variant] != "", "fixture_digest")
		// Independent registry observation proves which bytes that digest identifies.
		digest := strings.Split(j.images[variant], "@sha256:")[1]
		req, _ := http.NewRequestWithContext(j.ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/v2/ops-%s-%s/manifests/sha256:%s", registryPort, j.run, variant, digest), nil)
		req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json")
		response, err := j.api.Do(req)
		j.require(err == nil, "registry_fixture")
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		sum := sha256.Sum256(body)
		j.require(err == nil && response.StatusCode == 200 && hex.EncodeToString(sum[:]) == digest, "registry_integrity")
	}
	j.dockerLong("pull", traefikImage)
	proxy := "ops-" + j.run + "-traefik"
	j.fixture("service", proxy, "service", "create", "--name", proxy, "--label", label, "--container-label", label, "--constraint", "node.role==manager", "--network", j.ingress, "--publish", fmt.Sprintf("published=%d,target=80,mode=host", j.proxyPort), "--mount", "type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock,readonly", traefikImage, "--providers.swarm=true", "--providers.swarm.endpoint=unix:///var/run/docker.sock", "--providers.swarm.exposedbydefault=false", "--providers.swarm.refreshseconds=1s", "--entrypoints.web.address=:80")
	canary := "ops-" + j.run + "-route"
	j.fixture("service", canary, "service", "create", "--name", canary, "--label", label, "--container-label", label, "--network", j.ingress, "--env", "OPS_MARKER=canary"+j.run, "--label", "traefik.enable=true", "--label", "traefik.swarm.network="+j.ingress, "--label", "traefik.http.routers.opscanary.rule=Host(`canary"+j.run+".moduleos.example`)", "--label", "traefik.http.routers.opscanary.entrypoints=web", "--label", "traefik.http.routers.opscanary.service=opscanary", "--label", "traefik.http.services.opscanary.loadbalancer.server.port=8080", j.images["a"])
	j.canaryRoute()
	env, err := os.ReadFile(j.envFile)
	j.require(err == nil, "environment_fixture")
	env = append(env, []byte("MODULEOS_INGRESS_NETWORK="+j.ingress+"\n")...)
	j.require(os.WriteFile(j.envFile, env, 0600) == nil, "environment_fixture")
}
func (j *journey) canaryRoute() {
	j.wait(90*time.Second, func(ctx context.Context) (bool, error) {
		s, b, err := j.backend(ctx, j.proxyPort, "canary"+j.run+".moduleos.example")
		return err == nil && s == 200 && b == "ops-canary"+j.run+"-a\n", nil
	})
}
