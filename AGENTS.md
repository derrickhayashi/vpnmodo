# Development boundary

This is private development of the reusable VPN sidecar project. Private
visibility is an additional safeguard, not permission to store customer data.

- Keep real profiles, credentials, MFA codes, addresses, hostnames, customer
  names, service mappings, inventories, logs, packet captures and runtime state
  outside this repository. Use synthetic fixtures and documentation addresses.
- Do not copy consumer project directories or local ignored files into source.
- Never print secrets or include them in command arguments, reports or commits.
- Run `scripts/check-secrets` before pushing. Review diffs for customer data as
  well: secret scanners cannot reliably detect customer-specific information.
- Keep the original public repository read-only. Do not push or publish there
  without an explicit request. A future public PR must use reviewed patches on
  a branch based on the original repository; never merge this private history.
- Workstation and VM each belong to one user. The planned control layer uses Go
  and JSON-RPC over SSH; existing backend behavior remains the migration base.

See `docs/development.md` for provenance and local safeguards.
