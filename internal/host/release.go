package host

import (
	"errors"
	"os"
	"regexp"

	"github.com/derrickhayashi/vpnmodo/internal/jsonstrict"
)

var localImageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var errRelease = errors.New("invalid installed release file; use the verified VM loader output")

type InstalledRelease struct {
	SchemaVersion   int               `json:"schema_version"`
	OK              bool              `json:"ok"`
	Version         string            `json:"version"`
	ControllerImage string            `json:"controller_image"`
	BackendImages   map[string]string `json:"backend_images"`
	Activated       bool              `json:"activated"`
}

func validBackendImages(images map[string]string) bool {
	if images == nil {
		return true
	}
	if len(images) != 3 && len(images) != 4 {
		return false
	}
	for _, role := range []string{"l2tp", "xauth", "application"} {
		if !localImageID.MatchString(images[role]) {
			return false
		}
	}
	if len(images) == 4 && !localImageID.MatchString(images["ikev2"]) {
		return false
	}
	return true
}

func ReadInstalledRelease(path string) (InstalledRelease, error) {
	var result InstalledRelease
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return result, errRelease
	}
	data, err := os.ReadFile(path)
	if err != nil || jsonstrict.Decode(data, &result, 4096) != nil || result.SchemaVersion != 1 || !result.OK || result.Activated || result.Version == "" || !localImageID.MatchString(result.ControllerImage) || result.BackendImages == nil || !validBackendImages(result.BackendImages) {
		return InstalledRelease{}, errRelease
	}
	return result, nil
}
