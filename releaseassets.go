// Package vpnmodo contains the provisioning inputs shipped with the CLI.
package vpnmodo

import _ "embed"

//go:embed examples/lima/vpnmodo.yaml
var LimaYAML []byte

//go:embed examples/lima/registry-release.json
var RegistryRelease []byte

//go:embed scripts/release/install-vm.py
var VMInstaller []byte
