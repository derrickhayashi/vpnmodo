# Private development

This independent private repository starts with a reviewed source snapshot from
`https://github.com/derrickhayashi/vpn-sidecars` at commit
`c47c2f0`, plus the existing uncommitted generic architecture documentation and
its README link. Optional DNS examples use documentation-only addresses.
Original Git history, ignored files and customer deployments were not imported.

## Local checks

Install Gitleaks (on macOS, `brew install gitleaks`), then enable the local hook:

```sh
git config core.hooksPath .githooks
scripts/check-secrets
```

The hook rejects pushes if Gitleaks is missing or finds secrets. It checks all
local Git history and the working tree with redacted output. Git hooks are local
configuration and must be enabled in every clone. GitHub Actions also runs
Secret scan, but the local scan and manual review must run before each push.
Hooks can be bypassed, and scanners do not reliably identify customer data.
Review every diff and keep real configuration outside this checkout.

## CI and local builds

GitHub Actions is enabled for **Go checks** and **Secret scan** only. Other
workflows, including Controller SSH acceptance and the older Go contracts
entry, remain disabled. Do not expand hosted CI or provision paid test
infrastructure without user authorization.

Release container images are built locally on an existing native Linux Docker
builder and pushed to GHCR. No GitLab build pipeline is planned. Users pull the
prepared images during setup; they do not build them. See the
[release guide](releases.md) for build and publishing commands.

Go checks and secret scans can also run on the workstation. Protocol acceptance
requires an existing authorized Linux environment with Docker and the required
kernel features; record the checks actually run and any validation still pending.
Passing Go checks and Secret scan does not establish protocol acceptance.

For a shared Ubuntu VM, use the [isolated local lab](../tests/local-lab/README.md).
It runs the existing acceptance suites in a disposable container with its own
Docker daemon, without mounting the VM's Docker socket or host networking.
Prepare only committed sources and synthetic fixtures; keep SSH identities,
host addresses and runtime state outside the checkout.

## Public contribution

The `upstream` remote refers to the original public project and has pushing
disabled locally. `origin` is the private development repository. To contribute
later, start a branch from the original public project's history, transfer only
reviewed patches, and scan the candidate branch before publishing. Do not merge
the independent private history into the public project.
