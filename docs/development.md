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
configuration and must be enabled in every clone. GitHub Actions is disabled
for this private repository to avoid paid runner usage, so the local scan and
manual review must run before each push.
Hooks can be bypassed, and scanners do not reliably identify customer data.
Review every diff and keep real configuration outside this checkout.

## Validation without paid CI

GitHub Actions was disabled on 2026-10-19 at the user's request. Keep it
disabled unless the user explicitly authorizes re-enabling it. Do not provision
paid runners or test infrastructure. Run Go tests, vet, build checks and secret
scans on the workstation. Protocol acceptance requires an existing authorized
Linux environment with Docker and the required kernel features; record the
checks actually run and any validation still pending. Existing workflow files
remain as references, but their presence does not mean hosted CI is active.

## Public contribution

The `upstream` remote refers to the original public project and has pushing
disabled locally. `origin` is the private development repository. To contribute
later, start a branch from the original public project's history, transfer only
reviewed patches, and scan the candidate branch before publishing. Do not merge
the independent private history into the public project.
