package host

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestEmbeddedUpgradeProgramTreatsInputAsData(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 not installed")
	}
	cmd := exec.Command(python, "-c", upgradeProgram())
	cmd.Stdin = strings.NewReader(`{"action":"$(exit 99)'\"","token":"synthetic"}`)
	data, err := cmd.Output()
	if err != nil {
		t.Fatal("embedded upgrade program failed", err)
	}
	var r UpgradeResult
	if json.Unmarshal(data, &r) != nil || r.OK || r.Error != "upgrade" {
		t.Fatal("unsafe or invalid error response")
	}
}
