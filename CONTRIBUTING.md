# Contributing to ModuleOS

Thank you for considering a contribution to ModuleOS. Contributions should keep
the project understandable, operable, and secure rather than adding capability
at the expense of clear boundaries.

## Before you start

- Search existing issues and discussions before opening a new one.
- Use a private security report for vulnerabilities; do not open a public issue.
- Open an issue before a broad feature, architectural change, or compatibility
  break so the scope can be agreed on first.
- Architecture decisions with lasting consequences require an ADR. See
  [Architecture Decision Records](docs/adr/README.md).

## Contribution standards

- Keep each pull request focused on one coherent change.
- Add or update tests whenever behavior changes.
- Update documentation and public contracts when applicable.
- Preserve backwards compatibility unless a breaking change is explicitly
  approved and documented.
- Do not commit secrets, credentials, personal data, generated build output, or
  environment-specific configuration.
- Do not describe unfinished behavior as supported.
- Review new dependencies for necessity, maintenance quality, license, and
  security impact.

## Repository conventions

Follow [Repository Structure](docs/REPOSITORY_STRUCTURE.md) for component names
and ownership boundaries. Each application or package must document its own
development and verification commands when it is introduced.

Commit messages should be short, imperative, and preferably follow Conventional
Commits, for example:

```text
feat(control-plane): add application reconciliation
fix(cli): preserve installer configuration
docs: clarify release support policy
```

## Pull requests

A pull request should explain:

1. The problem being solved.
2. The chosen approach and meaningful alternatives.
3. Risk, compatibility, security, and operational impact.
4. How the change was verified.

Maintainers may request that an oversized pull request be split before review.
Approval does not replace passing required checks.

## Community expectations

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md). For help
choosing the correct channel, see [Support](SUPPORT.md).
