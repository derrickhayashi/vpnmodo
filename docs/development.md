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
configuration and must be enabled in every clone. CI also scans pushes and PRs;
CI detects problems after upload and cannot prevent the initial exposure.
Hooks can be bypassed, and scanners do not reliably identify customer data.
Review every diff and keep real configuration outside this checkout.

## Public contribution

The `upstream` remote refers to the original public project and has pushing
disabled locally. `origin` is the private development repository. To contribute
later, start a branch from the original public project's history, transfer only
reviewed patches, and scan the candidate branch before publishing. Do not merge
the independent private history into the public project.
