# Development boundary

This repository contains the reusable VPN sidecar project. Keep source and
documentation suitable for public distribution, regardless of repository visibility.

- Keep real profiles, credentials, MFA codes, addresses, hostnames, customer
  names, service mappings, inventories, logs, packet captures and runtime state
  outside this repository. Use synthetic fixtures and documentation addresses.
- Do not copy consumer project directories or local ignored files into source.
- Never print secrets or include them in command arguments, reports or commits.
- Run `scripts/check-secrets` before pushing. Review diffs for customer data as
  well: secret scanners cannot reliably detect customer-specific information.
- Keep the original public repository read-only. Do not push or publish there
  without an explicit request. Contributions there must use reviewed patches on a
  branch based on its history. Publish this project only to derrickhayashi/vpnmodo.
- GitHub Actions is enabled only for Go checks and Secret scan. Keep other
  workflows disabled unless the user authorizes them. Do not provision paid
  test infrastructure or expand CI beyond the authorized checks.
- Build release container images locally on an existing native Linux Docker
  builder and publish them to GHCR. Keep protocol acceptance local on an
  authorized Linux environment; no GitLab build pipeline is planned. Do not
  treat missing hosted CI or protocol results as passing tests.
- New product/CLI name: `vpnmodo`; controller executable/image/service:
  `vpnmodo-controller`. User workflows use the Go CLI and controller; do not
  restore the retired standalone launchers or their compatibility paths.
- Workstation and VM each belong to one user. The control layer uses Go
  and JSON-RPC over SSH; existing backend behavior remains the migration base.

# Documentation

Keep documentation user-facing: explain how to use vpnmodo, how it works, and
its supported behavior and limitations. Put dated test runs, acceptance evidence,
implementation progress and agent work journals in PR descriptions or issue
updates, not product documentation. Developer guides may explain how to build
and run tests without becoming a record of individual runs.

See `docs/development.md` for local build and test commands.
