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
  without an explicit request. A future public PR must use reviewed patches on
  a branch based on the original repository; do not merge the independent history.
- GitHub Actions is enabled only for Go checks and Secret scan. Keep other
  workflows disabled unless the user authorizes them. Do not provision paid
  test infrastructure or expand CI beyond the authorized checks.
- Build release container images locally on an existing native Linux Docker
  builder and publish them to GHCR. Keep protocol acceptance local on an
  authorized Linux environment; no GitLab build pipeline is planned. Do not
  treat missing hosted CI or protocol results as passing tests.
- New product/CLI name: `vpnmodo`; controller executable/image/service:
  `vpnmodo-controller`. Keep existing legacy launcher commands operational.
- Workstation and VM each belong to one user. The control layer uses Go
  and JSON-RPC over SSH; existing backend behavior remains the migration base.

See `docs/development.md` for local build and test commands.
