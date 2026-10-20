# Build from source and run tests

For normal installation, use the [prebuilt CLI](../README.md#installation).
Building from source requires Go 1.27.1. Python tests use `uv` to provide their
Ansible, Jinja2 and YAML dependencies. Gitleaks is used for secret scanning.

## Build the CLI

From the repository root:

```sh
go build -trimpath -o /tmp/vpnmodo ./cmd/vpnmodo
/tmp/vpnmodo version
/tmp/vpnmodo help
```

For container builds and controller prerequisites, see the
[controller guide](../containers/controller/README.md). For packaged binaries,
source archives and image bundles, see [release artifacts](releases.md).

## Run local checks

```sh
go test -race ./...
go vet ./...
uv run --no-project --with pyyaml --with jinja2 --with ansible-core python -m unittest discover -s tests -v
scripts/check-secrets
```

Compose checks require Docker Compose. Protocol acceptance runs separately on
Linux; it cannot be inferred from the unit-test results.

`scripts/check-secrets` requires Gitleaks on your PATH. To run it automatically
before pushes from this checkout, enable the supplied Git hook:

```sh
git config core.hooksPath .githooks
```

The scanner checks reachable Git history and the working tree with redacted
output. It does not reliably detect customer names or deployment information.
Use synthetic test data and keep VPN credential files, inventories and runtime
captures outside the checkout.

GitHub Actions runs Go checks and Secret scan. Release container images are built
on a native Linux Docker builder and published to GHCR; protocol tests run in the
local Linux lab.

## Run protocol tests on Linux

The [isolated Ubuntu lab](../tests/local-lab/README.md) provides commands to build,
transfer and run the acceptance bundle on an Ubuntu 24.04 VM. Its disposable
container has a separate Docker daemon and uses synthetic gateways, credentials
and service endpoints. The VM still supplies the kernel and must support
IPsec/XFRM and PPP.

The lab covers controller operations, L2TP, XAuth, packet enforcement, service
forwarding and PMTU behavior. See [release validation](release-validation.md)
for recorded results and [known issues](known-issues.md) for unresolved failures.
