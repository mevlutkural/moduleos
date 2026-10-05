# Governance

ModuleOS uses a maintainer-led open-source governance model. The project lead and
current maintainer is Mevlüt Kural (`@mevlutkural`).

## Principles

Project decisions prioritize, in order:

1. Security and the safety of user workloads and data.
2. Correctness, recoverability, and operational clarity.
3. Compatibility and explicit migration paths.
4. Maintainability and a coherent architecture.
5. New capability and convenience.

## Decision making

Routine changes are decided through issue and pull request review. Maintainers
seek evidence and constructive consensus, but the project maintainer has final
responsibility for scope, architecture, releases, and security response.

Significant or difficult-to-reverse decisions require an Architecture Decision
Record. Examples include changes to state ownership, reconciliation semantics,
public APIs, persistence formats, trust boundaries, cluster membership, or
release compatibility. ADRs are reviewed through pull requests and follow the
process in [docs/adr/README.md](docs/adr/README.md).

## Roles

### Contributors

Anyone who participates through code, documentation, design, testing, issue
triage, or community support.

### Maintainers

Trusted contributors who may review and merge changes, triage issues, manage
releases, and uphold project policies. Maintainer access is granted based on a
sustained record of sound technical judgment, respectful collaboration, and
responsible handling of security-sensitive work.

## Releases

Maintainers decide release scope and timing. A release must have a documented
scope, verification evidence appropriate to its risk, and clear compatibility
and security expectations. Version numbers follow Semantic Versioning once a
public versioning policy is established.

## Changes to governance

Material governance changes are proposed and reviewed publicly through a pull
request. Security response and confidential conduct matters remain private where
necessary.
