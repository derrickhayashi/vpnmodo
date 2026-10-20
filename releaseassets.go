// Package vpnmodo contains the provisioning inputs shipped with the CLI.
package vpnmodo

import (
	_ "embed"
	"encoding/base64"
)

//go:embed examples/lima/vpnmodo.yaml
var LimaYAML []byte

//go:embed examples/lima/registry-release.json
var RegistryRelease []byte

//go:embed scripts/release/install-vm.py
var VMInstaller []byte

// Set by the release builder after publishing images from the same source revision.
var registryReleaseBase64 string

func init() {
	if registryReleaseBase64 == "" {
		return
	}
	data, err := base64.StdEncoding.DecodeString(registryReleaseBase64)
	if err != nil {
		panic("invalid linked registry manifest")
	}
	RegistryRelease = data
}
