package controller

import (
	"context"
	"os"
	"path/filepath"

	"github.com/derrickhayashi/vpn-sidecars-next/internal/operation"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/operationstore"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/profilestore"
	"github.com/derrickhayashi/vpn-sidecars-next/internal/protocol"
)

func upgradePending(root string) bool {
	_, err := os.Lstat(filepath.Join(filepath.Dir(root), "upgrade-pending"))
	return !os.IsNotExist(err)
}

// CheckUpgrade inspects only a private offline snapshot, never the live journal.
// No network/container actions are possible in this command.
func CheckUpgrade(root string) error {
	if _, err := os.Lstat(filepath.Join(root, "operations.db")); err != nil {
		return ErrService
	}
	store, err := operationstore.Open(root)
	if err != nil {
		return ErrService
	}
	defer store.Close()
	state, err := store.Load()
	if err != nil {
		return ErrService
	}
	for _, r := range state.Operations {
		if !r.State.Terminal() {
			return ErrService
		}
	}
	for _, c := range state.Connections {
		if c.ActiveOperation != "" || c.Observed == operation.Unknown {
			return ErrService
		}
	}
	profiles, err := profilestore.Open(root, nil)
	if err != nil {
		return ErrService
	}
	list, err := profiles.List(context.Background())
	if err != nil {
		return ErrService
	}
	for _, p := range list {
		if !p.CredentialsPresent {
			return ErrService
		}
	}
	return nil
}
func allowedDuringUpgrade(m protocol.Method) bool {
	switch m {
	case protocol.Hello, protocol.SystemStatus, protocol.BackendList, protocol.BackendDescribe, protocol.ProfileList, protocol.ProfileInspect, protocol.ConnectionStatus, protocol.OperationStatus:
		return true
	}
	return false
}
