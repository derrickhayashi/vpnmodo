package host

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"

	assets "github.com/derrickhayashi/vpnmodo"
	"github.com/derrickhayashi/vpnmodo/internal/jsonstrict"
)

//go:embed upgrade.py
var upgradeScript string

type UpgradeParams struct {
	Action  string          `json:"action"`
	Token   string          `json:"token"`
	Runtime json.RawMessage `json:"runtime,omitempty"`
}
type UpgradeResult struct {
	OK    bool   `json:"ok"`
	Phase string `json:"phase,omitempty"`
	Error string `json:"error,omitempty"`
}

func upgradeProgram() string {
	// Independent namespaces prevent embedded modules' globals/main from colliding.
	b, _ := json.Marshal(bootstrap)
	i, _ := json.Marshal(string(assets.VMInstaller))
	return "B={'__name__':'bootstrap'}\nexec(" + string(b) + ", B)\nI={'__name__':'installer'}\nexec(" + string(i) + ", I)\nglobals().update({k:v for k,v in B.items() if not k.startswith('__')})\n" + upgradeScript
}

func Upgrade(ctx context.Context, h Host, p UpgradeParams) (UpgradeResult, error) {
	script := upgradeProgram()
	args, err := SSHArgs(h, "python3 -c '"+strings.ReplaceAll(script, "'", "'\"'\"'")+"'")
	if err != nil {
		return UpgradeResult{}, err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return UpgradeResult{}, err
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stderr = io.Discard
	var out limitedBuffer
	cmd.Stdout = &out
	if cmd.Run() != nil {
		return UpgradeResult{}, errors.New("upgrade transport interrupted; use upgrade --resume or --rollback")
	}
	var r UpgradeResult
	if jsonstrict.Decode(out.Bytes(), &r, 4096) != nil || !r.OK {
		return r, errors.New("VM upgrade failed; use upgrade --rollback to recover, then check registry access and active operations")
	}
	return r, nil
}
