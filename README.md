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

## Project documents

- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Support](SUPPORT.md)
- [Governance](GOVERNANCE.md)
- [Code of Conduct](CODE_OF_CONDUCT.md)
- [Architecture decisions](docs/adr/README.md)

## License

ModuleOS is licensed under the [MIT License](LICENSE).
