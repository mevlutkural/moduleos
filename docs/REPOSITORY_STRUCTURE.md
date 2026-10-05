# Repository Structure

This document defines the intended boundaries and naming conventions of the
ModuleOS monorepo. It is a structural contract, not a claim that every component
already exists.

Empty directories are not committed. A component directory appears when its
implementation and its own development documentation are introduced.

## Target layout

```text
moduleos/
├── apps/
│   ├── control-plane/
│   ├── cli/
│   └── dashboard/
├── packages/
│   ├── api-contract/
│   ├── api-client-go/
│   └── api-client-ts/
├── deploy/
├── packaging/
├── docs/
├── scripts/
└── .github/
```

## Applications

### `apps/control-plane`

The long-running ModuleOS backend and authoritative control plane. It owns
application use cases, desired state, reconciliation, runtime adapters, and the
API transport layer. The HTTP API is an adapter within the control plane, not
the identity of the whole application.

The production daemon produced by this application is named `moduleosd`.

### `apps/cli`

The future operator-facing `moduleos` command. Lifecycle operations such as
`install`, `uninstall`, `upgrade`, `status`, `doctor`, `backup`, and `restore`
belong here as subcommands when they are implemented. Installer and uninstaller
logic must not become separate product applications merely because they are
distributed as scripts or bootstrap artifacts.

### `apps/dashboard`

The future web interface. It is independently built and deployed, communicates
through the versioned API contract, and must not bypass control-plane use cases
or persistence boundaries.

## Shared packages

### `packages/api-contract`

The canonical machine-readable public API contract, such as OpenAPI sources and
compatibility metadata.

### `packages/api-client-go` and `packages/api-client-ts`

Shared or generated clients derived from the canonical contract. Generated code
must be reproducible and its update process documented.

Shared packages must have an actual cross-application consumer. Application-only
code stays with its owning application rather than moving into a generic package.

## Operational directories

### `deploy`

Deployment definitions and operator-facing examples, such as Compose, systemd,
or environment templates. These describe running ModuleOS; they do not build its
release artifacts.

### `packaging`

Release packaging, archive layouts, bootstrap assets, and artifact assembly.

### `scripts`

Repository automation used by developers and CI. Scripts are thin entry points;
product behavior belongs in tested application code.

### `docs`

Architecture, operation, development, security, and release documentation.
Architecture Decision Records live under `docs/adr/`.

### `.github`

GitHub community health files, automation, dependency management, and workflow
definitions.

## Module and versioning policy

The repository starts with a single Go module when Go implementation is added.
A `go.work` file or additional modules should be introduced only when components
require genuinely independent dependency or release boundaries.

ModuleOS initially follows one product version across its release artifacts.
Component-specific versioning requires an explicit architecture and release
decision.

## Boundary rules

- Public contracts are defined explicitly and versioned deliberately.
- Applications do not import another application's internal implementation.
- Runtime-specific behavior sits behind an adapter boundary owned by the control
  plane.
- The dashboard and CLI use public contracts rather than database access.
- Operational scripts do not become a second, untested control plane.
- New top-level directories require a clear owner and documented purpose.
