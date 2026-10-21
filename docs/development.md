# Build from source and run tests

For normal installation, use the [prebuilt CLI](../README.md#installation).
Building from source requires Go 1.27.1. Python regression tests use the
standard library; native Expect is required for the XAuth prompt tests.
Gitleaks is used for secret scanning.

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
python3 -m unittest discover -s tests -v
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

Go checks run when Go source, module/workspace files, embedded Python/Lima assets,
or the Go workflow changes. Documentation-only changes skip Go compilation and
tests. Secret scan runs on every push and pull request, including documentation
changes. Release container images are built on a native Linux Docker builder and
published to GHCR; protocol tests run in the local Linux lab.

## Run protocol tests on Linux

The [isolated Ubuntu lab](../tests/local-lab/README.md) provides commands to build,
transfer and run the acceptance bundle on an Ubuntu 24.04 VM. Its disposable
container has a separate Docker daemon and uses synthetic gateways, credentials
and service endpoints. The VM still supplies the kernel and must support
IPsec/XFRM and PPP.

The lab covers controller operations, L2TP, XAuth, packet enforcement, service
forwarding and PMTU behavior. See [known issues](known-issues.md) for failure
symptoms and [supported platforms](supported-platforms.md) for limitations.

For IKEv2, run `tests/ikev2-lab/run` inside the isolated lab after transferring
reviewed source. Set `VPNMODO_IKEV2_TEST_CONTROLLER_IMAGE` to a locally built
production controller image to include the controller integration check. This
check needs the explicitly authorized disposable daemon socket and native Linux
Docker volume storage. It covers profile import, application attachment,
controller recreation and teardown through JSON-RPC; it does not replace SSH
transport acceptance. Runtime keys and synthetic credentials are generated in
temporary resources and removed by the harness. Keep run results in PRs or
issues, and retain raw evidence outside the checkout.
