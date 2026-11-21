# CI qualification

`check-container-image.sh` builds and executes the production image on the
Docker Engine's native architecture. CI supplies `MODULEOS_EXPECT_ARCH` to
reject a mismatched runner and `MODULEOS_SCAN_IMAGE=1` to scan that exact local
image before cleanup. Cross-compilation alone is not runtime qualification.
The scan/extraction helpers target the Linux CI runners and require GNU timeout
and setsid; the default standalone container suite remains usable on macOS.

The scanner receives a run-scoped image archive, not a Docker socket. Trivy
0.76.2 checks OS and Go vulnerabilities; HIGH and CRITICAL findings (including
unfixed findings), scanner errors and database download errors fail the job.
There are no accepted vulnerability exceptions. Go reachability scanning remains
a separate check. The vulnerability database is current, not pinned to an old
snapshot; the scanner prints its database identity for each run.

`install-ci-tools.sh` and `install-trivy.sh` extract binaries from official
digest-pinned images using `extract-ci-tool.sh`. The tool containers are never
started. Pins are Trivy 0.76.2, ShellCheck 0.11.0 and Hadolint 2.15.1; the
immutable multi-platform image references live in the extraction script.

`bash scripts/check-deploy-assets.sh` checks all tracked shell scripts (including
shebang scripts without an extension), ShellCheck, Hadolint and the complete
rendered Swarm configuration. It needs Bash, ShellCheck, Hadolint, Go and the
Docker CLI. It does not require a running daemon or modify Swarm. Hadolint's
single DL3002 exception documents the tested root-to-non-root startup helper.

The expected render is `deploy/swarm/testdata/stack.golden.yml`. To intentionally
regenerate it after reviewing a deployment-contract change, run from the root:

```sh
env -i PATH="$PATH" HOME="$HOME" \
  MODULEOS_IMAGE=example/moduleos:v1 MODULEOS_PORT=18888 \
  MODULEOS_BASE_DOMAIN=moduleos.example \
  MODULEOS_API_KEY_SECRET=moduleos_api_key_v1 \
  MODULEOS_DATA_VOLUME=moduleos-data MODULEOS_APP_DATA_ROOT=/srv/moduleos-apps \
  MODULEOS_INGRESS_NETWORK=moduleos-ingress MODULEOS_LOG_LEVEL=info \
  MODULEOS_CORS_ALLOWED_ORIGINS= \
  docker --host unix:///tmp/moduleos-no-daemon.sock stack config \
  -c deploy/swarm/stack.yml > deploy/swarm/testdata/stack.golden.yml
```

The check never rewrites the fixture. YAML map ordering and formatting are
ignored; every key, value and array entry is compared, including unknown keys.
An ordinary regression test verifies rejection of security-field and new-key drift.
The runtime base digest must also match its OCI metadata; the Docker Dependabot
ecosystem discovers both FROM inputs, and a base update must keep labels in sync.

## Proposed protected-main checks

After both native jobs have actually passed on GitHub, the maintainer can require:

- Quality
- Coverage
- Race
- Vulnerability
- Secrets
- Integration
- Container + image scan (amd64)
- Container + image scan (arm64)
- Deploy assets
- Stack smoke

The two combined container checks qualify and scan the same image. None is
path-filtered or intentionally skipped. These names are a rollout proposal,
not evidence that protection has already been changed. No registry publication,
release signatures, SBOM generation or 24-hour qualification is performed here.
