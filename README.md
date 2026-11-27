# ModuleOS

ModuleOS is an open-source, self-hosted platform control plane for managing application workloads. Its goal is to combine a simple platform experience with explicit state, predictable reconciliation, and operational boundaries that remain understandable as the system grows.

The project is built around a few principles:

- The control plane and the workloads it manages are separate concerns.
- Desired state is persisted; observed state is discovered from the runtime.
- Events provide fast feedback, while reconciliation provides correctness.
- Ownership, failure behavior, and security boundaries must be explicit.
- Public interfaces are versioned contracts, not accidental implementation details.
- A capability is documented as supported only after it is implemented and verified.

## Repository model

ModuleOS is maintained as a monorepo. Deployable components live under `apps/`, reusable contracts and clients under `packages/`, and operational assets under dedicated top-level directories.

Directories are added when their implementation is introduced; the repository does not use empty placeholders for future components. The intended boundaries and naming rules are documented in [Repository Structure](docs/REPOSITORY_STRUCTURE.md).

## Current control-plane scope

The public pre-release source manages image-based application workloads through
a versioned HTTP API, persistent desired state, reconciliation, projects and
network links, deployment history/rollback, and bounded runtime logs/SSE.
The official deployment uses one control-plane replica on a dedicated rootful
Linux single-node Swarm manager, local Docker Unix socket and local SQLite WAL
volume. Workload replica scaling does not imply a replicated control plane.

Genesis does not include a dashboard, CLI, installer, source-build pipeline,
registry service, multi-node placement, federation, HA control plane, user/RBAC
management or automatic DNS/TLS. External Traefik, management TLS and host
network restrictions are operator responsibilities. This describes implemented
source, not a claim that Genesis has been released or passed its separate
mandatory 24-hour release qualification.

- [Deploy on a dedicated Swarm manager](deploy/swarm/README.md)
- [Operate: verify, rotate credentials, back up and recover](docs/control-plane/operations.md)
- [Configuration reference](docs/control-plane/configuration.md)
- [Architecture and exact qualification boundaries](docs/control-plane/architecture.md)
- [Public API contract](packages/api-contract/README.md)
- [Control-plane development and image](apps/control-plane/README.md)

## Project documents

- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Support](SUPPORT.md)
- [Governance](GOVERNANCE.md)
- [Code of Conduct](CODE_OF_CONDUCT.md)
- [Architecture decisions](docs/adr/README.md)

## License

ModuleOS is licensed under the [MIT License](LICENSE).
